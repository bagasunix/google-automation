"""
browser/profiles.py
===================
Persistent browser profile directory manager for warming up sessions
and retaining cookies, local storage, and realistic browser cache.
"""

import os
import shutil
import logging
import zlib
from typing import Optional

import paths as _paths

logger = logging.getLogger("worker.browser.profiles")

BASE_PROFILES_DIR = _paths.PROFILES_DIR
# Comfortably above the real proxy count (12 as of 2026-08-30, growing) to
# keep IP->slot collisions rare — a shared profile means two different
# proxies' Google cookies get mixed into the same browsing history, which
# reads as impossible-travel/account-abuse to Google's own fraud detection.
DEFAULT_POOL_SIZE = 50


def get_profile_dir(profile_id: Optional[int] = None, proxy_ip: str = "") -> str:
    """
    Get or create a persistent user-data-dir for the browser session.
    If profile_id is None, derive an index deterministically from the proxy IP
    or default to profile 0.
    """
    os.makedirs(BASE_PROFILES_DIR, exist_ok=True)

    if profile_id is not None:
        idx = profile_id % DEFAULT_POOL_SIZE
    elif proxy_ip:
        # Deterministically map IP to a profile slot (0..N-1). Must use a
        # hash that's stable ACROSS PROCESS RUNS, not Python's builtin
        # hash() — that one is randomized per-process by default (PEP 456 /
        # PYTHONHASHSEED) specifically to prevent it being relied on this
        # way. Confirmed live: hash('31.58.9.4') % 10 returned 2, 7, then 5
        # across three separate interpreter runs. That meant every worker
        # restart silently reshuffled which proxy used which warm profile —
        # cookies for one proxy's IP could end up reused moments later by a
        # completely different proxy in a different country, defeating the
        # entire point of a per-proxy warm profile (see the module
        # docstring) and actively creating the IP/cookie-mismatch signal
        # this system exists to avoid. zlib.crc32 is stable across runs.
        idx = zlib.crc32(proxy_ip.encode()) % DEFAULT_POOL_SIZE
    else:
        idx = 0

    profile_path = os.path.join(BASE_PROFILES_DIR, f"profile_{idx}")
    os.makedirs(profile_path, exist_ok=True)
    return profile_path


def cleanup_profile(profile_path: str) -> None:
    """Safely clean up temporary lock files inside a profile directory."""
    if not os.path.isdir(profile_path):
        return
    for fname in ["SingletonLock", "SingletonSocket", "SingletonCookie"]:
        fpath = os.path.join(profile_path, fname)
        if os.path.exists(fpath):
            try:
                os.remove(fpath)
            except Exception:
                pass


def reset_all_profiles() -> None:
    """Remove and re-create all warm profiles (for clean slate)."""
    if os.path.isdir(BASE_PROFILES_DIR):
        try:
            shutil.rmtree(BASE_PROFILES_DIR, ignore_errors=True)
            os.makedirs(BASE_PROFILES_DIR, exist_ok=True)
            logger.info("All browser profiles reset successfully")
        except Exception as e:
            logger.warning("Error resetting profiles: %s", e)
