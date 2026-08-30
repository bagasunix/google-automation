"""
reporter.py
===========
Result reporter for search automation tasks.
"""

from __future__ import annotations

import json
import logging
import os
from datetime import datetime, timezone
from typing import Optional

import paths as _paths

logger = logging.getLogger("worker.reporter")

BASE_DIR = _paths.BASE_DIR
SCREENSHOTS_DIR = os.path.join(BASE_DIR, "screenshots")
RESULTS_DIR = os.path.join(BASE_DIR, "results")


def ensure_dirs() -> None:
    os.makedirs(SCREENSHOTS_DIR, exist_ok=True)
    os.makedirs(RESULTS_DIR, exist_ok=True)


def capture_screenshot(sb, task_id: str, label: str = "error") -> Optional[str]:
    ensure_dirs()
    timestamp = datetime.now(timezone.utc).strftime("%Y%m%d_%H%M%S")
    filepath = os.path.join(SCREENSHOTS_DIR, f"{task_id}_{label}_{timestamp}.png")
    try:
        sb.save_screenshot(filepath)
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
    ensure_dirs()
    task_id = result.get("task_id", "unknown")
    timestamp = datetime.now(timezone.utc).strftime("%Y%m%d_%H%M%S")
    filepath = os.path.join(RESULTS_DIR, f"{task_id}_{timestamp}.json")
    try:
        with open(filepath, "w", encoding="utf-8") as f:
            json.dump(result, f, indent=2, ensure_ascii=False)
        logger.info("Result JSON saved: %s", filepath)
        return filepath
    except Exception as e:
        logger.error("Failed to save result JSON: %s", e)
        return None


def log_result(result: dict) -> None:
    status = "SUCCESS" if result.get("success") else "FAIL"
    logger.info(
        "=== Task Result [%s] ===\n"
        "  task_id:         %s\n"
        "  engine:          %s\n"
        "  proxy_used:      %s\n"
        "  serp_position:   %d\n"
        "  dwell_time:      %ds\n"
        "  scroll_depth:    %d%%\n"
        "  internal_clicks: %d\n"
        "  captcha_hit:     %s\n"
        "  error:           %s",
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
