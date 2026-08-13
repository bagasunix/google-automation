"""
reporter.py
===========
Result reporter for search automation tasks.

Responsibilities:
  - Build a TaskResponse-compatible result dict from task execution data
  - Write JSON result files to disk (for debugging / analytics)
  - Capture screenshots on error
  - Log task outcomes

The reporter is engine-agnostic — it accepts the raw outcome fields from
the task execution and packages them into a structured response.
"""

from __future__ import annotations

import json
import logging
import os
import time
from datetime import datetime, timezone
from typing import Optional

logger = logging.getLogger("worker.reporter")

# Base directory for output (screenshots, JSON results)
BASE_DIR = os.path.expanduser("~/Project/google-automation")
SCREENSHOTS_DIR = os.path.join(BASE_DIR, "screenshots")
RESULTS_DIR = os.path.join(BASE_DIR, "results")


def ensure_dirs() -> None:
    """Create output directories if they don't exist."""
    os.makedirs(SCREENSHOTS_DIR, exist_ok=True)
    os.makedirs(RESULTS_DIR, exist_ok=True)


async def capture_screenshot(page, task_id: str, label: str = "error") -> Optional[str]:
    """
    Capture a screenshot of the current page state.

    Used when a task fails (CAPTCHA, timeout, page load error, etc.)
    Screenshots are saved to ~/Project/google-automation/screenshots/.

    Returns the file path of the screenshot, or None on failure.
    """
    ensure_dirs()
    timestamp = datetime.now(timezone.utc).strftime("%Y%m%d_%H%M%S")
    filename = f"{task_id}_{label}_{timestamp}.png"
    filepath = os.path.join(SCREENSHOTS_DIR, filename)

    try:
        await page.screenshot(path=filepath, full_page=True)
        logger.info("Screenshot saved: %s", filepath)
        return filepath
    except Exception as e:
        logger.error("Failed to capture screenshot: %s", e)
        return None


def build_result(
    task_id: str,
    success: bool,
    engine: str,
    proxy_used: str,
    serp_position: int,
    dwell_time_seconds: int,
    scroll_depth_percent: int,
    internal_clicks: int,
    captcha_hit: bool,
    error: str,
) -> dict:
    """
    Build a structured result dictionary.

    This mirrors the TaskResponse proto message fields.
    """
    return {
        "task_id": task_id,
        "success": success,
        "engine": engine,
        "proxy_used": proxy_used,
        "serp_position": serp_position,
        "dwell_time_seconds": dwell_time_seconds,
        "scroll_depth_percent": scroll_depth_percent,
        "internal_clicks": internal_clicks,
        "captcha_hit": captcha_hit,
        "error": error,
        "timestamp": datetime.now(timezone.utc).isoformat(),
    }


def save_result_json(result: dict) -> Optional[str]:
    """
    Save the result dict as a JSON file to the results directory.

    Returns the file path, or None on failure.
    """
    ensure_dirs()
    task_id = result.get("task_id", "unknown")
    timestamp = datetime.now(timezone.utc).strftime("%Y%m%d_%H%M%S")
    filename = f"{task_id}_{timestamp}.json"
    filepath = os.path.join(RESULTS_DIR, filename)

    try:
        with open(filepath, "w", encoding="utf-8") as f:
            json.dump(result, f, indent=2, ensure_ascii=False)
        logger.info("Result JSON saved: %s", filepath)
        return filepath
    except Exception as e:
        logger.error("Failed to save result JSON: %s", e)
        return None


def log_result(result: dict) -> None:
    """Log the result in a human-readable format."""
    status = "SUCCESS" if result.get("success") else "FAIL"
    logger.info(
        "=== Task Result [%s] ===\n"
        "  task_id:          %s\n"
        "  engine:           %s\n"
        "  proxy_used:       %s\n"
        "  serp_position:    %d\n"
        "  dwell_time:       %ds\n"
        "  scroll_depth:     %d%%\n"
        "  internal_clicks: %d\n"
        "  captcha_hit:     %s\n"
        "  error:            %s",
        status,
        result.get("task_id"),
        result.get("engine"),
        result.get("proxy_used"),
        result.get("serp_position", 0),
        result.get("dwell_time_seconds", 0),
        result.get("scroll_depth_percent", 0),
        result.get("internal_clicks", 0),
        result.get("captcha_hit", False),
        result.get("error", ""),
    )
