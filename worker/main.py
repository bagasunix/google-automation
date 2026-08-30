"""
main.py
=======
gRPC server entry point for the Python search-automation worker.

SeleniumBase UC is synchronous — task execution runs in a ThreadPoolExecutor
so the async gRPC server stays non-blocking.
"""

from __future__ import annotations

import argparse
import asyncio
import logging
import os
import sys
import random
import time

import paths as _paths
from concurrent.futures import ThreadPoolExecutor

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

def _load_dotenv_file():
    for candidate in [".env", "../.env", _paths.ENV_PATH]:
        if os.path.isfile(candidate):
            try:
                with open(candidate, "r") as f:
                    for line in f:
                        line = line.strip()
                        if line and not line.startswith("#") and "=" in line:
                            k, v = line.split("=", 1)
                            k = k.strip()
                            v = v.strip().strip("'\"")
                            if k not in os.environ:
                                os.environ[k] = v
                break
            except Exception:
                pass

_load_dotenv_file()

try:
    from generated.task_pb2 import TaskRequest, TaskResponse
    from generated import task_pb2_grpc
    USE_REAL_GRPC = True
except Exception as _e:
    logging.warning("Could not import real protobuf stubs: %s — using fallback", _e)
    from generated.task_pb2 import TaskRequest, TaskResponse
    task_pb2_grpc = None
    USE_REAL_GRPC = False

from browser.session import create_session
from browser.humanizer import random_pause, human_scroll, random_mouse_jitter
from browser import bandwidth
from search.google import google_search_flow, google_click_target
from search.bing import bing_search_flow, bing_click_target
from search.serp import SerpSearchOutcome, SerpResult, detect_captcha
from engagement.dwell import simulate_reading
from engagement.click import simulate_internal_clicks
from engagement.exit import exit_article, post_exit_cooldown
from reporter import build_result, save_result_json, log_result, capture_screenshot

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
)
logger = logging.getLogger("worker.main")

DEFAULT_PORT = 50051
_ARGS = None


# ---------------------------------------------------------------------------
# Core task execution (synchronous — runs inside ThreadPoolExecutor)
# ---------------------------------------------------------------------------

def execute_task_sync(request) -> object:
    task_id          = request.task_id
    article_title    = request.article_title
    article_url      = request.article_url
    domain           = request.domain
    proxy_ip         = request.proxy_ip
    proxy_port       = request.proxy_port
    engine           = request.engine or "google"
    pre_search_queries = list(request.pre_search_queries) if request.pre_search_queries else []

    proxy_username   = getattr(request, "proxy_username", "")
    proxy_password   = getattr(request, "proxy_password", "")
    proxy_country    = getattr(request, "proxy_country", "")
    proxy_timezone   = getattr(request, "proxy_timezone", "")

    pre_search_enabled      = getattr(request, "pre_search_enabled", True)
    pre_search_2_chance     = getattr(request, "pre_search_2_chance", 0.0)
    serp_casual_click_chance = getattr(request, "serp_casual_click_chance", 0.0)
    competitor_click_chance  = getattr(request, "competitor_click_chance", 0.0)
    distraction_exit_chance  = getattr(request, "distraction_exit_chance", 0.0)

    proxy_str = f"{proxy_ip}:{proxy_port}" if proxy_ip else "direct"
    if proxy_username:
        proxy_str += " (auth)"

    logger.info("=" * 60)
    logger.info("TASK START: %s", task_id)
    logger.info("  article: %s", article_title[:80])
    logger.info("  domain:  %s", domain)
    logger.info("  engine:  %s", engine)
    logger.info("  proxy:   %s", proxy_str)
    logger.info("=" * 60)

    serp_position  = 0
    dwell_time     = 0
    scroll_depth   = 0
    internal_clicks = 0
    captcha_hit    = False
    error_msg      = ""
    success        = False
    session        = None
    bandwidth_used_kb = 0

    try:
        # Step 1: create stealth session
        logger.info("[Step 1] Creating SeleniumBase UC session")
        session = create_session(
            proxy_ip=proxy_ip,
            proxy_port=proxy_port,
            proxy_username=proxy_username,
            proxy_password=proxy_password,
            proxy_country=proxy_country,
            proxy_timezone=proxy_timezone,
            headless=not _ARGS.headed if _ARGS else True,
        )
        sb = session.sb

        # Direct/social traffic lands straight on the target domain (no
        # Google/Bing detour), so it should get full resources from the
        # first navigation; every other engine starts on a search engine.
        bandwidth.set_network_blocking(sb, target=(engine in ("direct", "social")))

        # Direct & Social Referral Traffic Flows
        if engine in ("direct", "social"):
            logger.info("Executing %s traffic flow for %s", engine.upper(), article_url)
            if engine == "social":
                social_referrers = [
                    "https://t.co/",
                    "https://www.reddit.com/r/technology/",
                    "https://www.linkedin.com/feed/",
                    "https://news.ycombinator.com/",
                ]
                ref = random.choice(social_referrers)
                logger.info("Simulating social referrer arrival: %s", ref)
                bandwidth.navigate(sb, article_url, target=True)
            else:
                # Direct traffic: 40% visit homepage first, 60% direct article bookmark
                if random.random() < 0.40:
                    bandwidth.navigate(sb, f"https://{domain}", target=True)
                    time.sleep(random.uniform(3, 6))
                    bandwidth.navigate(sb, article_url, target=True)
                else:
                    bandwidth.navigate(sb, article_url, target=True)

            sb.wait_for_ready_state_complete(timeout=15)
            time.sleep(random.uniform(2, 4))
            current_url = sb.get_current_url()

            if domain in current_url:
                logger.info("Landed on target article via %s traffic", engine)
                dwell_time, scroll_depth = simulate_reading(sb, domain)
                internal_clicks = simulate_internal_clicks(sb, domain)
                success = True
                bandwidth.accumulate(sb)  # capture the article before exit navigates away
                exit_article(sb, domain, distraction_exit_chance)
            else:
                error_msg = f"Failed to land on {domain}. Current URL: {current_url}"
                logger.warning(error_msg)
            return TaskResponse(
                task_id=task_id,
                success=success,
                engine=engine,
                proxy_used=f"{proxy_ip}:{proxy_port}",
                serp_position=1,
                dwell_time_seconds=dwell_time,
                scroll_depth_percent=scroll_depth,
                internal_clicks=internal_clicks,
                captcha_hit=False,
                error=error_msg,
                bandwidth_used_kb=int(round(bandwidth.get_total_kb(sb))),
            )

        # Step 2: pre-search #1
        from search.query_expander import expand_queries_ai
        if not pre_search_queries:
            pre_search_queries = expand_queries_ai(article_title, domain=domain)

        if pre_search_enabled and pre_search_queries:
            pre_query_1 = pre_search_queries[0]
            logger.info("[Step 2] Pre-search #1: %s", pre_query_1)
            outcome = _do_search(sb, pre_query_1, domain, engine)

            if outcome.captcha_hit:
                captcha_hit = True
                error_msg = "CAPTCHA during pre-search #1: " + outcome.error
                capture_screenshot(sb, task_id, "captcha_presearch1")
                raise RuntimeError(error_msg)

            _browse_serp_casually(sb, engine, domain, serp_casual_click_chance)
            random_pause(3, 8)

            # Step 3: pre-search #2
            if len(pre_search_queries) > 1 and random.random() < pre_search_2_chance:
                pre_query_2 = pre_search_queries[1]
                logger.info("[Step 3] Pre-search #2: %s", pre_query_2)
                outcome2 = _do_search(sb, pre_query_2, domain, engine)

                if outcome2.captcha_hit:
                    captcha_hit = True
                    error_msg = "CAPTCHA during pre-search #2: " + outcome2.error
                    capture_screenshot(sb, task_id, "captcha_presearch2")
                    raise RuntimeError(error_msg)

                _browse_serp_casually(sb, engine, domain, serp_casual_click_chance)
                random_pause(3, 8)
            else:
                logger.info("[Step 3] Pre-search #2 skipped (chance=%.0f%%)", pre_search_2_chance * 100)
        else:
            logger.info("[Step 2-3] Pre-search skipped (enabled=%s)", pre_search_enabled)

        # Step 4: target search
        logger.info("[Step 4] Target search: %s", article_title[:80])
        target_outcome = _do_search(sb, article_title, domain, engine)

        if target_outcome.captcha_hit:
            captcha_hit = True
            error_msg = "CAPTCHA during target search: " + target_outcome.error
            capture_screenshot(sb, task_id, "captcha_target")
            raise RuntimeError(error_msg)

        if not target_outcome.found:
            error_msg = f"Target domain '{domain}' not found in SERP"
            logger.warning(error_msg)
            capture_screenshot(sb, task_id, "target_not_found")
        else:
            serp_position = target_outcome.position
            logger.info("[Step 4] Target found at position %d", serp_position)

            # Step 5: SERP click variation
            logger.info("[Step 5] Clicking target")
            target_result = next(
                (r for r in target_outcome.results if r.position == serp_position), None
            )
            if target_result and target_result.element_ref:
                if engine == "google":
                    google_click_target(sb, target_result, competitor_click_chance)
                else:
                    bing_click_target(sb, target_result, competitor_click_chance)
            else:
                logger.warning("No element ref — opening target URL directly")
                bandwidth.navigate(sb, article_url, target=True)

            # Step 6: verify landing
            time.sleep(random.uniform(1, 3))
            current_url = sb.get_current_url()
            logger.info("Landed on: %s", current_url)

            if domain in current_url:
                logger.info("Successfully landed on target article")

                # Step 7: engagement
                logger.info("[Step 7] Post-click engagement")
                dwell_time, scroll_depth = simulate_reading(sb, domain)
                logger.info("Dwell: %ds, Scroll: %d%%", dwell_time, scroll_depth)

                internal_clicks = simulate_internal_clicks(sb, domain)
                logger.info("Internal clicks: %d", internal_clicks)

                success = True

                # Step 8: exit strategy
                logger.info("[Step 8] Exit strategy")
                bandwidth.accumulate(sb)  # capture the article before exit navigates away
                exit_article(sb, domain, distraction_exit_chance)
            else:
                error_msg = f"Did not land on target article. URL: {current_url}"
                logger.warning(error_msg)
                capture_screenshot(sb, task_id, "wrong_landing")

    except RuntimeError as e:
        error_msg = str(e)
        logger.error("Task failed: %s", error_msg)
        if "CAPTCHA" in error_msg:
            captcha_hit = True
    except Exception as e:
        error_msg = f"Unexpected error: {type(e).__name__}: {e}"
        logger.error("Task failed: %s", error_msg, exc_info=True)
        if session and session._sb:
            try:
                capture_screenshot(session._sb, task_id, "exception")
            except Exception:
                pass
    finally:
        if session and session._sb:
            try:
                bandwidth_used_kb = bandwidth.get_total_kb(session._sb)
            except Exception:
                pass
        if session:
            session.close()

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

    # Post-exit cooldown is managed centrally by the Go orchestrator
    # with context cancellation and dynamic jitter.

    return TaskResponse(
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
        bandwidth_used_kb=int(round(bandwidth_used_kb)),
    )


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _do_search(sb, query: str, target_domain: str, engine: str) -> SerpSearchOutcome:
    if engine == "google":
        return google_search_flow(sb, query, target_domain)
    return bing_search_flow(sb, query, target_domain)


def _browse_serp_casually(sb, engine: str, target_domain: str,
                           casual_click_chance: float = 0.0) -> None:
    logger.info("Casually browsing SERP (click_chance=%.0f%%)", casual_click_chance * 100)
    human_scroll(sb, random.randint(200, 600))
    random_pause(2, 5)
    human_scroll(sb, random.randint(200, 400))
    random_pause(3, 10)

    if casual_click_chance <= 0 or random.random() >= casual_click_chance:
        return

    try:
        sel = "div.g h3 a, div.g a[href]" if engine == "google" else "li.b_algo h2 a"
        links = sb.find_elements(sel)
        non_target = [
            l for l in links
            if target_domain not in (l.get_attribute("href") or "")
            and (l.get_attribute("href") or "").startswith("http")
        ]
        if not non_target:
            return

        link = random.choice(non_target)
        from browser.humanizer import human_click_element
        bandwidth.accumulate(sb)  # capture SERP page before leaving it
        human_click_element(sb, link)

        try:
            sb.wait_for_ready_state_complete(timeout=15)
            time.sleep(random.uniform(1.5, 3.0))
        except Exception:
            pass

        random_pause(5, 12)
        random_mouse_jitter(sb, duration_s=random.uniform(1, 3))

        for _ in range(random.randint(3, 6)):
            human_scroll(sb, random.randint(200, 400))
            random_pause(3, 8)

        if random.random() < 0.4:
            human_scroll(sb, -random.randint(150, 300))
            random_pause(2, 5)

        random_pause(3, 8)
        bandwidth.accumulate(sb)  # capture the distraction page before leaving it
        sb.go_back()
        try:
            sb.wait_for_ready_state_complete(timeout=15)
        except Exception:
            pass
        random_pause(1, 3)
    except Exception as e:
        logger.warning("Casual SERP browsing error: %s", e)

    random_mouse_jitter(sb, duration_s=random.uniform(1, 3))


# ---------------------------------------------------------------------------
# gRPC server
# ---------------------------------------------------------------------------

def _resolve_max_workers(cli_value: int | None) -> int:
    """Decide the thread pool size: --max-workers flag > WORKER_MAX_CONCURRENT
    env var > config.yaml's scheduler.concurrency (kept in sync with the Go
    orchestrator's own worker-slot count) > default of 4.

    Without this, the Go side could be configured for e.g. 8 concurrent
    worker slots while this pool silently capped real parallelism at 4 —
    slots 5-8 would just queue for a free thread, no extra throughput, no
    indication anything was wrong.
    """
    if cli_value is not None:
        return max(1, min(cli_value, 10))

    env_value = os.environ.get("WORKER_MAX_CONCURRENT")
    if env_value:
        try:
            return max(1, min(int(env_value), 10))
        except ValueError:
            logger.warning("WORKER_MAX_CONCURRENT=%r is not an integer, ignoring", env_value)

    for candidate in ["config/config.yaml", "../config/config.yaml"]:
        if os.path.isfile(candidate):
            try:
                import yaml
                with open(candidate, "r") as f:
                    cfg = yaml.safe_load(f) or {}
                concurrency = cfg.get("scheduler", {}).get("concurrency")
                if isinstance(concurrency, int) and concurrency > 0:
                    return max(1, min(concurrency, 10))
            except Exception as e:
                logger.warning("Could not read concurrency from %s: %s", candidate, e)
            break

    return 4


_executor: ThreadPoolExecutor | None = None


class WorkerServiceServicer(task_pb2_grpc.WorkerServiceServicer):
    async def ExecuteTask(self, request, context):
        logger.info("Received gRPC ExecuteTask: task_id=%s", request.task_id)
        loop = asyncio.get_running_loop()
        try:
            response = await loop.run_in_executor(_executor, execute_task_sync, request)
            return response
        except Exception as e:
            logger.error("ExecuteTask error: %s", e, exc_info=True)
            return TaskResponse(
                task_id=request.task_id,
                success=False,
                engine=request.engine,
                error=f"Worker error: {type(e).__name__}: {e}",
            )


async def run_grpc_server(port: int) -> None:
    import grpc
    from grpc import aio as grpc_aio

    server = grpc_aio.server()
    task_pb2_grpc.add_WorkerServiceServicer_to_server(WorkerServiceServicer(), server)
    addr = f"[::]:{port}"
    server.add_insecure_port(addr)
    await server.start()
    logger.info("gRPC server started on %s", addr)
    await server.wait_for_termination()


# ---------------------------------------------------------------------------
# HTTP fallback
# ---------------------------------------------------------------------------

async def run_http_server(port: int) -> None:
    from aiohttp import web

    async def handle_execute(req):
        body = await req.json()
        task_req = TaskRequest(**body)
        loop = asyncio.get_running_loop()
        response = await loop.run_in_executor(_executor, execute_task_sync, task_req)
        return web.json_response({
            "task_id": response.task_id,
            "success": response.success,
            "serp_position": response.serp_position,
            "dwell_time_seconds": response.dwell_time_seconds,
            "scroll_depth_percent": response.scroll_depth_percent,
            "internal_clicks": response.internal_clicks,
            "captcha_hit": response.captcha_hit,
            "error": response.error,
        })

    app = web.Application()
    app.router.add_post("/execute", handle_execute)
    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, "0.0.0.0", port)
    await site.start()
    logger.info("HTTP server started on port %d", port)
    await asyncio.Event().wait()


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

def main():
    global _ARGS, _executor

    parser = argparse.ArgumentParser(description="Search automation worker")
    parser.add_argument("--port", type=int, default=DEFAULT_PORT)
    parser.add_argument("--http", action="store_true", help="Run HTTP fallback server")
    parser.add_argument("--headed", action="store_true", help="Run browser headed (debug)")
    parser.add_argument("--no-cooldown", action="store_true", help="Skip post-exit cooldown")
    parser.add_argument("--max-workers", type=int, default=None,
                         help="Max concurrent browser sessions (default: config.yaml's "
                              "scheduler.concurrency, kept in sync with the Go orchestrator)")
    _ARGS = parser.parse_args()

    max_workers = _resolve_max_workers(_ARGS.max_workers)
    _executor = ThreadPoolExecutor(max_workers=max_workers)
    logger.info("Task executor: max %d concurrent browser sessions", max_workers)

    if _ARGS.http:
        logger.info("Starting HTTP fallback server on port %d", _ARGS.port)
        asyncio.run(run_http_server(_ARGS.port))
    elif USE_REAL_GRPC:
        logger.info("Starting gRPC server on port %d", _ARGS.port)
        asyncio.run(run_grpc_server(_ARGS.port))
    else:
        logger.error("gRPC stubs not available and --http not specified. Exiting.")
        sys.exit(1)


if __name__ == "__main__":
    main()
