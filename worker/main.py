"""
main.py
=======
gRPC server entry point for the Python search-automation worker.

Responsibilities:
  - Listen on localhost:50051 for gRPC TaskRequest messages from the Go orchestrator
  - Execute the full humanized search flow:
      1. Create stealth browser session (with proxy injection)
      2. Pre-search with topical queries (NOT the article title)
      3. Target search (search for the exact article title)
      4. Find target domain in SERP, record position
      5. SERP click variation (50% direct / 30% scroll past / 20% competitor first)
      6. Post-click engagement (dwell, scroll, internal clicks, mouse movement)
      7. Exit strategy (close / back / homepage / navigate)
      8. Post-exit cooldown (30-120s)
  - Return TaskResponse with result metrics
  - Capture screenshot on error
  - Save JSON result to disk

gRPC mode:
  Uses the real protobuf-generated stubs (generated/task_pb2.py, task_pb2_grpc.py).
  Generated from task.proto via: python -m grpc_tools.protoc

HTTP fallback mode:
  If --http flag is used, runs a simple HTTP server that accepts POST /execute
  with JSON body and returns JSON response.

Usage:
    python3 main.py                    # gRPC on localhost:50051
    python3 main.py --port 50052       # custom port
    python3 main.py --http             # force HTTP fallback mode
    python3 main.py --headed           # run browser in headed mode (debugging)
    python3 main.py --no-cooldown      # skip post-exit cooldown (debugging)
"""

from __future__ import annotations

import argparse
import asyncio
import logging
import os
import sys
import json
import time
import random
from typing import Optional

# Ensure local imports work
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

# ---------------------------------------------------------------------------
# Import protobuf messages and gRPC stubs
# Try real protobuf first, fall back to manual JSON-based classes
# ---------------------------------------------------------------------------

try:
    from generated.task_pb2 import TaskRequest, TaskResponse
    from generated import task_pb2_grpc
    USE_REAL_GRPC = True
except Exception as _e:
    logging.warning("Could not import real protobuf stubs: %s — using fallback", _e)
    from generated.task_pb2 import TaskRequest, TaskResponse
    task_pb2_grpc = None
    USE_REAL_GRPC = False

from browser.session import StealthSession, create_session
from browser.humanizer import random_pause, human_scroll, random_mouse_jitter
from search.google import google_search_flow, google_click_target
from search.bing import bing_search_flow, bing_click_target
from search.serp import SerpSearchOutcome, SerpResult, detect_captcha
from engagement.dwell import simulate_reading
from engagement.click import simulate_internal_clicks
from engagement.exit import exit_article, post_exit_cooldown
from reporter import build_result, save_result_json, log_result, capture_screenshot

# ---------------------------------------------------------------------------
# Logging
# ---------------------------------------------------------------------------

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
)
logger = logging.getLogger("worker.main")

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------

BASE_DIR = os.path.expanduser("~/Project/google-automation")
DEFAULT_PORT = 50051

# Module-level args (set in main())
_ARGS = None


# ---------------------------------------------------------------------------
# Task executor — the core search automation flow
# ---------------------------------------------------------------------------

async def execute_task(request) -> object:
    """
    Execute the full humanized search flow for a single task.

    Accepts a TaskRequest (protobuf or fallback) and returns a TaskResponse.

    Flow:
      1. Create stealth browser session with proxy
      2. Pre-search #1 (topical query from pre_search_queries)
      3. Pre-search #2 (optional, 50% chance — another topical query)
      4. Target search (exact article title)
      5. Find target domain in SERP
      6. SERP click variation
      7. Post-click engagement (dwell, scroll, internal clicks)
      8. Exit strategy + cooldown
    """
    # Extract fields from the protobuf request (works for both real and fallback)
    task_id = request.task_id
    article_title = request.article_title
    article_url = request.article_url
    domain = request.domain
    proxy_ip = request.proxy_ip
    proxy_port = request.proxy_port
    engine = request.engine or "google"
    # Handle repeated string field (protobuf returns RepeatedScalarContainer)
    pre_search_queries = list(request.pre_search_queries) if request.pre_search_queries else []
    # Proxy auth credentials (for Webshare authenticated proxies)
    proxy_username = getattr(request, "proxy_username", "")
    proxy_password = getattr(request, "proxy_password", "")
    proxy_country = getattr(request, "proxy_country", "")
    proxy_timezone = getattr(request, "proxy_timezone", "")

    # Bandwidth-saving behavior controls
    pre_search_enabled = getattr(request, "pre_search_enabled", True)
    pre_search_2_chance = getattr(request, "pre_search_2_chance", 0.0)
    serp_casual_click_chance = getattr(request, "serp_casual_click_chance", 0.0)
    competitor_click_chance = getattr(request, "competitor_click_chance", 0.0)
    distraction_exit_chance = getattr(request, "distraction_exit_chance", 0.0)
    serp_dwell_min = getattr(request, "serp_dwell_seconds_min", 2)
    serp_dwell_max = getattr(request, "serp_dwell_seconds_max", 5)

    proxy_str = f"{proxy_ip}:{proxy_port}" if proxy_ip else "direct"
    if proxy_username:
        proxy_str += " (auth)"

    logger.info("=" * 60)
    logger.info("TASK START: %s", task_id)
    logger.info("  article:  %s", article_title[:80])
    logger.info("  domain:   %s", domain)
    logger.info("  engine:   %s", engine)
    logger.info("  proxy:    %s", proxy_str)
    logger.info("  pre_search_queries: %s", pre_search_queries)
    logger.info("=" * 60)

    # Initialise response fields with defaults
    serp_position = 0
    dwell_time = 0
    scroll_depth = 0
    internal_clicks = 0
    captcha_hit = False
    error_msg = ""
    success = False

    session = None

    try:
        # === Step 1: Create stealth browser session ===
        logger.info("[Step 1] Creating stealth browser session")
        session = await create_session(
            proxy_ip=proxy_ip,
            proxy_port=proxy_port,
            proxy_username=proxy_username,
            proxy_password=proxy_password,
            proxy_country=proxy_country,
            proxy_timezone=proxy_timezone,
            headless=not _ARGS.headed if _ARGS else True,
        )
        page = session.page

        # === Step 2: Pre-search #1 (topical query, NOT the article title) ===
        if pre_search_enabled and pre_search_queries:
            pre_query_1 = pre_search_queries[0]
            logger.info("[Step 2] Pre-search #1: %s", pre_query_1)

            outcome = await _do_search(page, pre_query_1, domain, engine)

            if outcome.captcha_hit:
                captcha_hit = True
                error_msg = "CAPTCHA during pre-search #1: " + outcome.error
                logger.error(error_msg)
                await capture_screenshot(page, task_id, "captcha_presearch1")
                raise RuntimeError(error_msg)

            # Browse the SERP (read snippets, scroll, maybe click a random result)
            await _browse_serp_casually(page, engine, domain, serp_casual_click_chance)

            # Small pause between searches
            await random_pause(3, 8)

            # === Step 3: Pre-search #2 (configurable chance) ===
            if len(pre_search_queries) > 1 and random.random() < pre_search_2_chance:
                pre_query_2 = pre_search_queries[1]
                logger.info("[Step 3] Pre-search #2: %s", pre_query_2)

                outcome2 = await _do_search(page, pre_query_2, domain, engine)

                if outcome2.captcha_hit:
                    captcha_hit = True
                    error_msg = "CAPTCHA during pre-search #2: " + outcome2.error
                    logger.error(error_msg)
                    await capture_screenshot(page, task_id, "captcha_presearch2")
                    raise RuntimeError(error_msg)

                await _browse_serp_casually(page, engine, domain, serp_casual_click_chance)
                await random_pause(3, 8)
            else:
                logger.info("[Step 3] Pre-search #2 skipped (chance=%.0f%%)", pre_search_2_chance * 100)
        else:
            logger.info("[Step 2-3] Pre-search skipped (enabled=%s)", pre_search_enabled)

        # === Step 4: Target search (exact article title) ===
        logger.info("[Step 4] Target search: %s", article_title[:80])
        target_outcome = await _do_search(page, article_title, domain, engine)

        if target_outcome.captcha_hit:
            captcha_hit = True
            error_msg = "CAPTCHA during target search: " + target_outcome.error
            logger.error(error_msg)
            await capture_screenshot(page, task_id, "captcha_target")
            raise RuntimeError(error_msg)

        if not target_outcome.found:
            error_msg = f"Target domain '{domain}' not found in SERP for query '{article_title}'"
            logger.warning(error_msg)
            success = False
            await capture_screenshot(page, task_id, "target_not_found")
        else:
            serp_position = target_outcome.position
            logger.info("[Step 4] Target found at SERP position %d", serp_position)

            # === Step 5: SERP click variation ===
            logger.info("[Step 5] Clicking target with variation strategy")
            target_result = None
            for r in target_outcome.results:
                if r.position == serp_position:
                    target_result = r
                    break

            if target_result and target_result.element_ref:
                if engine == "google":
                    await google_click_target(page, target_result, competitor_click_chance)
                else:
                    await bing_click_target(page, target_result, competitor_click_chance)
            else:
                logger.warning("No element ref — trying to click target by URL")
                await _click_target_by_url(page, article_url, engine)

            # === Step 6: Verify we landed on the target article ===
            await asyncio.sleep(random.uniform(1, 3))
            current_url = page.url
            logger.info("Landed on: %s", current_url)

            if domain in current_url:
                logger.info("Successfully landed on target article")

                # === Step 7: Post-click engagement ===
                logger.info("[Step 7] Post-click engagement: reading simulation")

                dwell_time, scroll_depth = await simulate_reading(page, domain)
                logger.info("Dwell: %ds, Scroll depth: %d%%", dwell_time, scroll_depth)

                internal_clicks = await simulate_internal_clicks(page, domain)
                logger.info("Internal clicks: %d", internal_clicks)

                success = True

                # === Step 8: Exit strategy ===
                logger.info("[Step 8] Exit strategy")
                await exit_article(page, domain, session.context, distraction_exit_chance)
            else:
                logger.warning("Did not land on target domain — URL: %s", current_url)
                error_msg = f"Did not land on target article. URL: {current_url}"
                success = False
                await capture_screenshot(page, task_id, "wrong_landing")

    except RuntimeError as e:
        error_msg = str(e)
        logger.error("Task failed (RuntimeError): %s", error_msg)
        if "CAPTCHA" in error_msg:
            captcha_hit = True
    except Exception as e:
        err_name = type(e).__name__
        msg = str(e)
        if err_name == "TargetClosedError" or "has been closed" in msg:
            error_msg = "Task interrupted: browser closed mid-task"
            logger.warning("Task %s interrupted: %s", task_id, err_name)
        else:
            error_msg = f"Unexpected error: {err_name}: {e}"
            logger.error("Task failed: %s", error_msg, exc_info=True)
            if session and session._page:
                try:
                    await capture_screenshot(session._page, task_id, "exception")
                except Exception:
                    pass
    finally:
        if session:
            await session.close()

    # Build and save result
    result = build_result(
        task_id=task_id,
        success=success,
        engine=engine,
        proxy_used=proxy_str,
        serp_position=serp_position,
        dwell_time_seconds=dwell_time,
        scroll_depth_percent=scroll_depth,
        internal_clicks=internal_clicks,
        captcha_hit=captcha_hit,
        error=error_msg,
    )
    save_result_json(result)
    log_result(result)

    # === Step 9: Post-exit cooldown (30-120s) ===
    if success and _ARGS and not _ARGS.no_cooldown:
        logger.info("[Step 9] Post-exit cooldown")
        await post_exit_cooldown(30, 120)

    # Build TaskResponse (real protobuf or fallback)
    bw_used = session.bytes_received_kb if session else 0
    if bw_used > 0:
        logger.info("Bandwidth used: %d KB (%.1f MB)", bw_used, bw_used / 1024)

    response = TaskResponse(
        task_id=task_id,
        success=success,
        engine=engine,
        proxy_used=proxy_str,
        serp_position=serp_position,
        dwell_time_seconds=dwell_time,
        scroll_depth_percent=scroll_depth,
        internal_clicks=internal_clicks,
        captcha_hit=captcha_hit,
        error=error_msg,
        bandwidth_used_kb=bw_used,
    )

    logger.info("TASK END: %s (success=%s)", task_id, success)
    return response


# ---------------------------------------------------------------------------
# Helper functions
# ---------------------------------------------------------------------------

async def _do_search(page, query: str, target_domain: str, engine: str) -> SerpSearchOutcome:
    """Execute a search on the specified engine and return the outcome."""
    if engine == "google":
        return await google_search_flow(page, query, target_domain)
    else:
        return await bing_search_flow(page, query, target_domain)


async def _browse_serp_casually(page, engine: str, target_domain: str, casual_click_chance: float = 0.0) -> None:
    """
    Browse the SERP casually during pre-search:
      - Scroll through results (200-600px)
      - Read snippets (2-5s)
      - casual_click_chance probability: click a random non-target result, dwell, go back
    """
    logger.info("Casually browsing SERP (pre-search, click_chance=%.0f%%)", casual_click_chance * 100)

    await human_scroll(page, random.randint(200, 600))
    await random_pause(2, 5)

    await human_scroll(page, random.randint(200, 400))
    await random_pause(3, 10)

    if casual_click_chance <= 0:
        return

    if random.random() < casual_click_chance:
        logger.info("Clicking a random result during pre-search browsing")
        try:
            if engine == "google":
                links = await page.query_selector_all("div.g h3 a, div.g a[href]")
            else:
                links = await page.query_selector_all("li.b_algo h2 a")

            non_target_links = []
            for link in links:
                try:
                    href = await link.get_attribute("href") or ""
                    if target_domain not in href and href.startswith("http"):
                        non_target_links.append(link)
                except Exception:
                    continue

            if non_target_links:
                random_link = random.choice(non_target_links)
                from browser.humanizer import human_click_element
                clicked = await human_click_element(page, random_link)

                if clicked:
                    try:
                        await page.wait_for_load_state("domcontentloaded", timeout=15000)
                        await asyncio.sleep(random.uniform(1.5, 3.0))
                    except Exception:
                        pass

                    # === Proper dwell — like a real human reading the page ===
                    # Initial scan
                    await random_pause(5, 12)
                    await random_mouse_jitter(page, duration_s=random.uniform(1, 3))

                    # Scroll down in chunks — reading the article
                    scroll_steps = random.randint(3, 6)
                    for _ in range(scroll_steps):
                        await human_scroll(page, random.randint(200, 400))
                        await random_pause(3, 8)

                    # Scroll back up a bit (re-reading)
                    if random.random() < 0.4:
                        await human_scroll(page, -random.randint(150, 300))
                        await random_pause(2, 5)

                    # Final pause before leaving
                    await random_pause(3, 8)
                    await random_mouse_jitter(page, duration_s=random.uniform(1, 2))

                    logger.info("Done reading casual result — going back to SERP")
                    await page.go_back()
                    try:
                        await page.wait_for_load_state("domcontentloaded", timeout=15000)
                        await asyncio.sleep(random.uniform(1, 2))
                    except Exception:
                        pass
                    await random_pause(1, 3)

        except Exception as e:
            logger.warning("Error during casual SERP browsing: %s", e)

    await random_mouse_jitter(page, duration_s=random.uniform(1, 3))


async def _click_target_by_url(page, target_url: str, engine: str) -> None:
    """Fallback: click a SERP result by matching its href to the target URL."""
    try:
        if engine == "google":
            links = await page.query_selector_all("div.g a[href], div.g h3 a")
        else:
            links = await page.query_selector_all("li.b_algo h2 a, li.b_algo a")

        for link in links:
            href = await link.get_attribute("href") or ""
            if target_url in href or href in target_url:
                from browser.humanizer import human_click_element
                await human_click_element(page, link)
                return

        logger.warning("Could not find target URL in SERP results to click")
    except Exception as e:
        logger.warning("Error in _click_target_by_url: %s", e)


# ---------------------------------------------------------------------------
# gRPC server (using grpcio + generated stubs)
# ---------------------------------------------------------------------------

class WorkerServiceServicer(task_pb2_grpc.WorkerServiceServicer):
    """gRPC service implementation for WorkerService.ExecuteTask."""

    async def ExecuteTask(self, request, context):
        """Execute a search automation task."""
        logger.info("Received gRPC ExecuteTask request: task_id=%s", request.task_id)
        try:
            response = await execute_task(request)
            return response
        except Exception as e:
            logger.error("ExecuteTask error: %s", e, exc_info=True)
            # Return error response
            return TaskResponse(
                task_id=request.task_id,
                success=False,
                engine=request.engine,
                error=f"Worker error: {type(e).__name__}: {e}",
            )


async def run_grpc_server(port: int) -> None:
    """
    Run the gRPC server on localhost:50051 using grpc.aio.

    Uses the real protobuf-generated stubs from task_pb2_grpc.py.
    """
    import grpc
    from grpc import aio as grpc_aio

    server = grpc_aio.server(
        interceptors=[],
        options=[
            ("grpc.max_send_message_length", 50 * 1024 * 1024),
            ("grpc.max_receive_message_length", 50 * 1024 * 1024),
            ("grpc.so_reuseport", 0),
        ],
    )

    task_pb2_grpc.add_WorkerServiceServicer_to_server(
        WorkerServiceServicer(),
        server,
    )

    server.add_insecure_port(f"127.0.0.1:{port}")

    await server.start()
    logger.info("gRPC server started on 127.0.0.1:%d", port)
    logger.info("  Service: searchautomation.WorkerService")
    logger.info("  Method:  ExecuteTask(TaskRequest) -> (TaskResponse)")

    await server.wait_for_termination()


# ---------------------------------------------------------------------------
# HTTP fallback server (for when grpcio is not available)
# ---------------------------------------------------------------------------

async def run_http_server(port: int) -> None:
    """
    Simple HTTP server fallback.

    Accepts POST /execute with JSON body (TaskRequest fields) and returns
    JSON response (TaskResponse fields).
    """
    from aiohttp import web

    async def handle_execute(request: web.Request) -> web.Response:
        try:
            body = await request.json()
            # Build a TaskRequest from JSON dict
            task_req = TaskRequest(
                task_id=body.get("task_id", ""),
                article_title=body.get("article_title", ""),
                article_url=body.get("article_url", ""),
                domain=body.get("domain", ""),
                proxy_ip=body.get("proxy_ip", ""),
                proxy_port=body.get("proxy_port", 0),
                engine=body.get("engine", "google"),
                pre_search_queries=body.get("pre_search_queries", []),
            )
            logger.info("Received HTTP ExecuteTask request: %s", task_req.task_id)
            response = await execute_task(task_req)

            # Convert response to dict for JSON
            if hasattr(response, "to_dict"):
                resp_dict = response.to_dict()
            else:
                resp_dict = {
                    "task_id": response.task_id,
                    "success": response.success,
                    "engine": response.engine,
                    "proxy_used": response.proxy_used,
                    "serp_position": response.serp_position,
                    "dwell_time_seconds": response.dwell_time_seconds,
                    "scroll_depth_percent": response.scroll_depth_percent,
                    "internal_clicks": response.internal_clicks,
                    "captcha_hit": response.captcha_hit,
                    "error": response.error,
                    "bandwidth_used_kb": response.bandwidth_used_kb,
                }
            return web.json_response(resp_dict)
        except Exception as e:
            logger.error("HTTP handler error: %s", e, exc_info=True)
            return web.json_response({"error": str(e)}, status=500)

    async def handle_health(request: web.Request) -> web.Response:
        return web.json_response({"status": "ok", "service": "search-automation-worker"})

    app = web.Application()
    app.router.add_post("/execute", handle_execute)
    app.router.add_get("/health", handle_health)

    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, "localhost", port)
    await site.start()

    logger.info("HTTP fallback server started on localhost:%d", port)
    logger.info("  POST /execute  — execute a task (JSON body)")
    logger.info("  GET  /health   — health check")

    while True:
        await asyncio.sleep(3600)


# ---------------------------------------------------------------------------
# Main entry point
# ---------------------------------------------------------------------------

async def main():
    global _ARGS

    parser = argparse.ArgumentParser(description="Search Automation Python Worker")
    parser.add_argument("--port", type=int, default=DEFAULT_PORT, help="Port to listen on")
    parser.add_argument("--http", action="store_true", help="Force HTTP fallback mode")
    parser.add_argument("--headed", action="store_true", help="Run browser in headed mode (debugging)")
    parser.add_argument("--no-cooldown", action="store_true", help="Skip post-exit cooldown (debugging)")
    parser.add_argument("--debug", action="store_true", help="Enable debug logging")
    args = parser.parse_args()

    _ARGS = args

    if args.debug:
        logging.getLogger().setLevel(logging.DEBUG)
        logger.debug("Debug logging enabled")

    logger.info("=" * 60)
    logger.info("Search Automation Python Worker")
    logger.info("  Port: %d", args.port)
    logger.info("  Headed: %s", args.headed)
    logger.info("  Cooldown: %s", "disabled" if args.no_cooldown else "enabled")
    logger.info("  gRPC stubs: %s", "real protobuf" if USE_REAL_GRPC else "fallback")
    logger.info("  Mode: %s", "HTTP" if args.http else "gRPC")
    logger.info("=" * 60)

    # Ensure output directories exist
    from reporter import ensure_dirs
    ensure_dirs()

    # Validate CAPTCHA audio backend before accepting any tasks
    from captcha.audio import validate_backend
    if not validate_backend():
        logger.error("CAPTCHA backend validation failed — fix config and restart")
        raise SystemExit(1)

    if args.http or not USE_REAL_GRPC:
        if not USE_REAL_GRPC and not args.http:
            logger.warning("gRPC stubs not available — falling back to HTTP server")
        await run_http_server(args.port)
    else:
        await run_grpc_server(args.port)


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        logger.info("Worker shutting down (Ctrl+C)")
    except Exception as e:
        logger.error("Fatal error: %s", e, exc_info=True)
        sys.exit(1)
