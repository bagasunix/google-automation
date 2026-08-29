"""
browser/profiles.py
===================
Persistent browser profile directory manager for warming up sessions
and retaining cookies, local storage, and realistic browser cache.
"""

import os
import shutil
import logging
from typing import Optional

logger = logging.getLogger("worker.browser.profiles")

BASE_PROFILES_DIR = os.path.expanduser("~/Project/google-automation/data/profiles")
DEFAULT_POOL_SIZE = 10


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
        # Deterministically map IP to a profile slot (0..9)
        idx = abs(hash(proxy_ip)) % DEFAULT_POOL_SIZE
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
