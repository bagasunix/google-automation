"""browser/bandwidth.py
====================
Bandwidth tracking + aggressive resource blocking for proxy bandwidth conservation.

Webshare free plan = 1GB/month per API key.
Each search task uses ~2-4MB depending on resource blocking.

This module provides:
  - BandwidthTracker: track bytes transferred per API key, persist to JSON
  - aggressive_resource_blocker: block images/media/video/fonts > threshold KB
  - get_bandwidth_status: check remaining quota before starting a task
  - find_available_api_key: find first key with remaining bandwidth

Config (config.yaml bandwidth: section):
  monthly_limit_mb: 1024
  block_images: false
  block_media: true
  block_fonts: true
  block_stylesheets: false
  warn_threshold_percent: 80
  pause_threshold_percent: 95
"""

from __future__ import annotations

import json
import logging
import os
import time
from dataclasses import dataclass, field
from typing import Optional

logger = logging.getLogger("worker.browser.bandwidth")

BASE_DIR = os.path.expanduser("~/Project/google-automation")
BANDWIDTH_FILE = os.path.join(BASE_DIR, "data", "bandwidth.json")


def _load_bandwidth_config() -> dict:
    """Load bandwidth config from config.yaml."""
    try:
        import yaml
        config_path = os.path.expanduser("~/Project/google-automation/config/config.yaml")
        with open(config_path, "r") as f:
            cfg = yaml.safe_load(f)
        return cfg.get("bandwidth", {})
    except Exception:
        return {}


# Webshare free plan = 1GB = 1024MB = 1048576KB per month
DEFAULT_MONTHLY_LIMIT_KB = 1024 * 1024  # fallback, overridden by config


def _get_monthly_limit_kb() -> float:
    """Get monthly bandwidth limit from config.yaml."""
    cfg = _load_bandwidth_config()
    limit_mb = cfg.get("monthly_limit_mb", 1024)
    return limit_mb * 1024  # MB to KB

# Resource blocking thresholds
IMAGE_BLOCK_THRESHOLD_KB = 200  # block images larger than 200KB
MEDIA_BLOCK_TYPES = {"media", "video", "audio", "font", "stylesheet"}


@dataclass
class BandwidthTracker:
    """Track bandwidth usage per Webshare API key, persisted to JSON."""
    api_key_index: int = 0
    used_kb: float = 0.0
    month_key: str = ""
    tasks_count: int = 0
    last_updated: float = 0.0

    def __post_init__(self):
        if not self.month_key:
            self.month_key = _current_month_key()

    @property
    def remaining_kb(self) -> float:
        return max(0, _get_monthly_limit_kb() - self.used_kb)

    @property
    def remaining_mb(self) -> float:
        return self.remaining_kb / 1024

    @property
    def is_exhausted(self) -> bool:
        cfg = _load_bandwidth_config()
        pause_pct = cfg.get("pause_threshold_percent", 95)
        limit_kb = _get_monthly_limit_kb()
        pause_kb = limit_kb * (pause_pct / 100)
        return self.used_kb >= pause_kb

    @property
    def usage_percent(self) -> float:
        return min(100, (self.used_kb / _get_monthly_limit_kb()) * 100)


def _current_month_key() -> str:
    """Return current month key like '2026-08'."""
    import calendar
    t = time.gmtime()
    return f"{t.tm_year}-{t.tm_mon:02d}"


def _load_all_tracking() -> dict:
    """Load all bandwidth tracking data from JSON file."""
    if not os.path.exists(BANDWIDTH_FILE):
        return {}
    try:
        with open(BANDWIDTH_FILE, "r") as f:
            return json.load(f)
    except (json.JSONDecodeError, IOError):
        return {}


def _save_all_tracking(data: dict) -> None:
    """Save all bandwidth tracking data to JSON file."""
    os.makedirs(os.path.dirname(BANDWIDTH_FILE), exist_ok=True)
    try:
        with open(BANDWIDTH_FILE, "w") as f:
            json.dump(data, f, indent=2)
    except IOError as e:
        logger.warning("Failed to save bandwidth data: %s", e)


def get_bandwidth_status(api_key_index: int = 0) -> BandwidthTracker:
    """
    Get bandwidth status for a specific API key.

    Returns BandwidthTracker with used_kb, remaining_kb, is_exhausted.
    Resets automatically when month changes.
    """
    all_data = _load_all_tracking()
    month_key = _current_month_key()

    # Build storage key: "key0_2026-08"
    storage_key = f"key{api_key_index}_{month_key}"

    if storage_key in all_data:
        d = all_data[storage_key]
        return BandwidthTracker(
            api_key_index=api_key_index,
            used_kb=d.get("used_kb", 0.0),
            month_key=month_key,
            tasks_count=d.get("tasks_count", 0),
            last_updated=d.get("last_updated", 0.0),
        )

    return BandwidthTracker(api_key_index=api_key_index, month_key=month_key)


def record_bandwidth_usage(api_key_index: int, used_kb: float) -> BandwidthTracker:
    """
    Record bandwidth usage for a task.

    Args:
        api_key_index: Which Webshare API key was used (0, 1, etc.)
        used_kb: Kilobytes transferred during the task

    Returns updated BandwidthTracker.
    """
    all_data = _load_all_tracking()
    month_key = _current_month_key()
    storage_key = f"key{api_key_index}_{month_key}"

    if storage_key in all_data:
        d = all_data[storage_key]
        d["used_kb"] = d.get("used_kb", 0.0) + used_kb
        d["tasks_count"] = d.get("tasks_count", 0) + 1
    else:
        d = {
            "used_kb": used_kb,
            "tasks_count": 1,
        }

    d["last_updated"] = time.time()
    all_data[storage_key] = d
    _save_all_tracking(all_data)

    tracker = BandwidthTracker(
        api_key_index=api_key_index,
        used_kb=d["used_kb"],
        month_key=month_key,
        tasks_count=d["tasks_count"],
        last_updated=d["last_updated"],
    )

    logger.info(
        "Bandwidth: key#%d used %.1f KB (%.1f MB / 1024 MB, %.1f%%, %d tasks this month)",
        api_key_index,
        used_kb,
        tracker.used_kb / 1024,
        tracker.usage_percent,
        tracker.tasks_count,
    )

    if tracker.is_exhausted:
        logger.warning(
            "Bandwidth EXHAUSTED for key#%d (%.1f MB used). Switch to next key or wait for monthly reset.",
            api_key_index,
            tracker.used_kb / 1024,
        )

    return tracker


def find_available_api_key(num_keys: int) -> Optional[int]:
    """
    Find the first API key index that still has bandwidth available.

    Returns None if all keys are exhausted.
    """
    for i in range(num_keys):
        tracker = get_bandwidth_status(i)
        if not tracker.is_exhausted:
            logger.info(
                "API key #%d available: %.1f MB remaining (%.1f%% used)",
                i, tracker.remaining_mb, tracker.usage_percent,
            )
            return i

    logger.error("All %d API keys exhausted bandwidth for this month", num_keys)
    return None


def _load_domains() -> list[str]:
    """Load target domains from config.yaml — these get full resource access."""
    try:
        import yaml
        config_path = os.path.expanduser("~/Project/google-automation/config/config.yaml")
        with open(config_path, "r") as f:
            cfg = yaml.safe_load(f)
        return [d.lower().lstrip("www.") for d in cfg.get("domains", [])]
    except Exception:
        return []


def _is_target_url(url: str, target_domains: list[str]) -> bool:
    """Check if a URL belongs to one of the target domains."""
    if not url or not target_domains:
        return False
    try:
        from urllib.parse import urlparse
        host = urlparse(url).netloc.lower().lstrip("www.")
        return any(d in host or host in d for d in target_domains)
    except Exception:
        return False


async def aggressive_resource_blocker(context, block_images: bool = None) -> None:
    """
    URL-aware resource blocking for bandwidth conservation.

    Strategy:
      - On TARGET domains (bagasunix.com): allow ALL resources (full realism
        for engagement — images, CSS, fonts, everything loads).
      - On NON-target sites (Google, Bing, competitor, distraction): block
        aggressively (fonts, media, video, audio, images, stylesheets).

    This saves ~60-70% bandwidth because Google/Bing SERP pages and
    competitor articles are loaded briefly and don't need full rendering.
    The target article is where we spend bandwidth for realistic engagement.

    Reads config from config.yaml bandwidth: + domains sections.
    """
    cfg = _load_bandwidth_config()
    target_domains = _load_domains()

    if block_images is None:
        block_images = cfg.get("block_images", False)
    block_media = cfg.get("block_media", True)
    block_fonts = cfg.get("block_fonts", True)
    block_stylesheets = cfg.get("block_stylesheets", False)

    # Non-target blocked types: block everything configurable
    non_target_blocked = set()
    if block_fonts:
        non_target_blocked.add("font")
    if block_media:
        non_target_blocked.update({"media", "video", "audio"})
    if block_stylesheets:
        non_target_blocked.add("stylesheet")
    # Images: on non-target sites, always block (SERP thumbnails don't matter)
    non_target_blocked.add("image")

    # Target blocked types: only block what's explicitly configured
    # Default: only fonts + media (keep images + CSS for realism)
    target_blocked = set()
    if block_fonts:
        target_blocked.add("font")
    if block_media:
        target_blocked.update({"media", "video", "audio"})
    # Don't block images or CSS on target — engagement realism

    logger.info(
        "URL-aware blocking: target=%s | non-target blocks=%s | target blocks=%s",
        target_domains, non_target_blocked, target_blocked,
    )

    async def _block_resources(route):
        url = route.request.url
        rt = route.request.resource_type

        # Never block reCAPTCHA resources — audio challenge download must go through
        if "recaptcha" in url or "/sorry/" in url:
            await route.continue_()
            return

        if _is_target_url(url, target_domains):
            if rt in target_blocked:
                await route.abort()
            else:
                await route.continue_()
        else:
            if rt in non_target_blocked:
                await route.abort()
            else:
                await route.continue_()

    await context.route("**/*", _block_resources)


async def measure_task_bandwidth(context, callback=None) -> float:
    """
    Measure bandwidth used during a browser session.

    Attaches a response handler to count bytes received.
    Returns total KB when session ends.
    """
    total_bytes = 0

    async def on_response(response):
        nonlocal total_bytes
        try:
            headers = response.headers
            content_length = headers.get("content-length", "0")
            if content_length and content_length.isdigit():
                total_bytes += int(content_length)
            elif response.ok:
                body = await response.body()
                total_bytes += len(body)
        except Exception:
            pass

    context.on("response", on_response)

    if callback:
        await callback()

    return total_bytes / 1024  # KB
