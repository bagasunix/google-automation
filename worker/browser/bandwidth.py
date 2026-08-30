"""browser/bandwidth.py
====================
Bandwidth tracking + CDP-level resource blocking for proxy bandwidth conservation.

Webshare free plan = 1GB/month per API key.

This module provides:
  - BandwidthTracker: track bytes transferred per API key, persist to JSON
  - set_network_blocking: CDP Network.setBlockedURLs — block images/media/fonts
    on non-target pages (Google/Bing/competitors), keep full resources on
    target domains for realistic engagement
  - accumulate / get_total_kb: real per-task bandwidth measurement via the
    browser's own Resource Timing API (sb.execute_script), read right before
    each navigation since the performance timeline is scoped to the current
    document
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

import paths as _paths

logger = logging.getLogger("worker.browser.bandwidth")

BASE_DIR = _paths.BASE_DIR
BANDWIDTH_FILE = os.path.join(BASE_DIR, "data", "bandwidth.json")


def _load_bandwidth_config() -> dict:
    """Load bandwidth config from config.yaml."""
    try:
        import yaml
        config_path = _paths.CONFIG_PATH
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


def set_network_blocking(sb, target: bool) -> None:
    """
    CDP-level URL blocking for bandwidth conservation.

    Strategy:
      - target=True (on/about to load a configured target domain, e.g.
        bagasunix.com): only block fonts + media — keep images and CSS for
        realistic engagement (scroll depth, reading simulation).
      - target=False (Google/Bing SERP, competitor pages, distraction sites):
        also block images (+ CSS if configured) — these pages are only
        viewed for seconds and don't need full rendering.

    Applied via CDP Network.setBlockedURLs (glob patterns) through the
    classic Selenium driver underneath SeleniumBase's uc=True session.
    driver.execute_cdp_cmd() is a one-shot CDP call, unaffected by the
    execute_script()-under-CDP-mode quirks documented in humanizer.py.

    Call this BEFORE the navigation it should apply to — Chrome applies the
    block list to requests made after it's set, not retroactively. Reads
    config from config.yaml bandwidth: section.
    """
    cfg = _load_bandwidth_config()
    block_fonts = cfg.get("block_fonts", True)
    block_media = cfg.get("block_media", True)
    block_stylesheets = cfg.get("block_stylesheets", False)

    exts: list[str] = []
    if block_fonts:
        exts += ["woff", "woff2", "ttf", "otf", "eot"]
    if block_media:
        # Deliberately NOT mp3/wav/ogg — reCAPTCHA's audio challenge is
        # served from a query-string endpoint (recaptcha/api2/payload?...)
        # with no matching extension, so generic audio patterns would add
        # blocking risk there for zero benefit.
        exts += ["mp4", "webm", "ogv", "mov", "avi", "m3u8", "mpd"]
    if not target:
        exts += ["jpg", "jpeg", "png", "gif", "webp", "svg", "ico", "bmp", "avif"]
        if block_stylesheets:
            exts += ["css"]

    # Network.setBlockedURLs' `urls` (simple wildcard-string) parameter is
    # DEPRECATED and — confirmed live against this project's actual Chrome
    # build — a silent no-op; `urlPatterns` (URLPattern-spec syntax, e.g.
    # "*://*:*/*.jpg") is the parameter that's actually still enforced.
    patterns = [{"urlPattern": f"*://*:*/*.{ext}", "block": True} for ext in exts]

    if not target:
        # Extension-matching alone misses a lot of real Google/Bing SERP
        # image traffic — their own thumbnail/favicon CDNs serve images from
        # dynamic query-string endpoints with no file extension in the URL
        # at all (e.g. encrypted-tbn0.gstatic.com/images?q=tbn:...). Block
        # those by host+path instead, since (unlike a generic image
        # extension) these hosts serve nothing BUT images/favicons — safe to
        # block wholesale without risking the page's own scripts/CSS/fonts,
        # which live on other paths/hosts (e.g. www.google.com, www.gstatic.com).
        patterns += [
            {"urlPattern": "*://encrypted-tbn*.gstatic.com:*/*", "block": True},  # Google Images/SERP thumbnails
            {"urlPattern": "*://t*.gstatic.com:*/faviconV2*", "block": True},     # Google's favicon CDN
            {"urlPattern": "*://www.google.com:*/s2/favicons*", "block": True},  # Google's older favicon proxy
            {"urlPattern": "*://tse*.mm.bing.net:*/*", "block": True},           # Bing image thumbnails
            {"urlPattern": "*://th.bing.com:*/*", "block": True},                # Bing image thumbnails (alt host)
        ]

    try:
        driver = getattr(sb, "driver", sb)
        driver.execute_cdp_cmd("Network.enable", {})
        driver.execute_cdp_cmd("Network.setBlockedURLs", {"urlPatterns": patterns})
        logger.debug("Network blocking set (target=%s): %d patterns", target, len(patterns))
    except Exception as e:
        logger.debug("set_network_blocking failed: %s", e)


def navigate(sb, url: str, target: bool, timeout: int = 15) -> None:
    """
    Navigate to `url` while keeping CDP network blocking in effect.

    sb.open() under SeleniumBase's uc=True mode disconnects the classic
    Selenium/chromedriver CDP session and re-navigates through a separate
    async CDP connection (sb.cdp) instead — see base_case.py's open():
    for a UC+CDP session it calls self.disconnect() then self.cdp.open().
    That silently discards whatever Network.setBlockedURLs state was set via
    driver.execute_cdp_cmd() on the old (now-disconnected) session, so a
    block applied right before sb.open() never actually reaches the new
    page's requests — confirmed live: a same-page click-triggered navigation
    (human_click_element on an <a> tag) preserves the block correctly, but
    sb.open() to the same URL does not.

    Work around it by driving the navigation itself through the same raw CDP
    session the block was set on (Page.navigate), instead of sb.open().
    Falls back to sb.open() if the CDP navigate call itself fails for any
    reason — better a page loads unblocked than not at all.
    """
    accumulate(sb)  # capture whatever page we're leaving before this nav
    set_network_blocking(sb, target)
    driver = getattr(sb, "driver", sb)
    try:
        driver.execute_cdp_cmd("Page.navigate", {"url": url})
    except Exception as e:
        logger.debug("Page.navigate failed, falling back to sb.open(): %s", e)
        sb.open(url)
        return
    try:
        sb.wait_for_ready_state_complete(timeout=timeout)
    except Exception:
        pass


# Deliberately a single line starting with "return ": SeleniumBase's
# execute_script() takes one of two totally different paths depending on
# driver state (see shared_utils.is_cdp_swap_needed) —
#   - classic Selenium (driver connected, the common case): runs this text
#     as a function body verbatim, so it MUST have a literal top-level
#     "return" or nothing comes back to Python (confirmed live — an IIFE
#     with no leading "return" silently returns None here, not an error).
#   - CDP mode (driver disconnected — sb.cdp.evaluate()): strips "return "
#     from the *last* line only if it starts with "return " (see the CDP
#     execute_script gotcha noted elsewhere in this codebase), then evaluates
#     the rest as a bare expression whose completion value comes back
#     automatically. A leading "return " on a *multi-line* script would
#     survive un-stripped there and throw "Illegal return statement".
# One line satisfies both: classic path keeps "return " and needs it;
# CDP path strips it from the (only) line and doesn't need it.
# Resource Timing entries persist (and keep accumulating) until the page
# navigates away OR performance.clearResourceTimings() is called — accumulate()
# is called from many places (some pages, like the target article, get
# multiple calls before their eventual navigation), so entries are cleared
# after every read and the single per-page navigation entry is only ever
# counted once (via a window-scoped flag that's naturally gone after the
# next navigation) — otherwise a second call on the same page would double
# (or triple-, ...) count bytes already reported.
_TRANSFER_BYTES_JS = (
    "return (function() { var total = 0; try { "
    "if (!window.__bwNavCounted) { "
    "var nav = performance.getEntriesByType('navigation'); "
    "if (nav.length > 0) total += nav[0].transferSize || 0; "
    "window.__bwNavCounted = true; "
    "} "
    "var res = performance.getEntriesByType('resource'); "
    "for (var i = 0; i < res.length; i++) { total += res[i].transferSize || 0; } "
    "performance.clearResourceTimings(); "
    "} catch (e) {} return total; })();"
)


def accumulate(sb) -> None:
    """
    Read real network bytes transferred on the currently-loaded page — via
    the browser's own Resource Timing API, the same transferSize numbers
    Chrome DevTools' Network tab shows — and add them to this session's
    running total (stashed on the sb object itself, so callers scattered
    across modules don't need to thread an accumulator through).

    Call this right before any navigation away from the current page
    (sb.open, sb.go_back, a click on a link) — the performance timeline is
    scoped to the current document and is discarded on navigation, so
    reading it any later loses the page's data entirely.
    """
    try:
        n = sb.execute_script(_TRANSFER_BYTES_JS)
        kb = float(n or 0) / 1024.0
        sb._bw_total_kb = getattr(sb, "_bw_total_kb", 0.0) + kb
    except Exception as e:
        logger.debug("bandwidth accumulate failed: %s", e)


def get_total_kb(sb) -> float:
    """Total measured bandwidth (KB) accumulated on this session so far."""
    return getattr(sb, "_bw_total_kb", 0.0)
