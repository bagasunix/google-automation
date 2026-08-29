"""
browser/session.py
==================
SeleniumBase UC stealth browser session.

uc=True uses undetected-chromedriver which patches Chrome at driver level.
headless=True uses Chrome's built-in new headless mode (--headless=new),
which is significantly less detectable than the old --headless flag.
"""

from __future__ import annotations

import logging
import os
import random
import time
from dataclasses import dataclass
from typing import Optional

from browser.stealth import StealthProfile, apply_stealth_to_sb
from browser.ip_health import check_proxy_health

logger = logging.getLogger("worker.browser.session")


@dataclass
class SBSession:
    proxy_ip: str = ""
    proxy_port: int = 0
    proxy_scheme: str = "http"
    proxy_username: str = ""
    proxy_password: str = ""
    proxy_country: str = ""
    proxy_timezone: str = ""
    headless: bool = True
    profile: Optional[StealthProfile] = None
    user_data_dir: Optional[str] = None
    use_warm_profile: bool = True

    _sb = None
    _sb_ctx = None
    _bytes_received: int = 0

    def __enter__(self) -> "SBSession":
        self.start()
        return self

    def __exit__(self, *_):
        self.close()

    def start(self, skip_health_check: bool = False) -> None:
        from seleniumbase import SB

        if not skip_health_check:
            health = check_proxy_health(
                proxy_ip=self.proxy_ip,
                proxy_port=self.proxy_port,
                proxy_username=self.proxy_username,
                proxy_password=self.proxy_password,
            )
            if not health.ok:
                raise RuntimeError(
                    f"Proxy IP health check failed: {health.flag_reason} "
                    f"(ip={health.ip} org={health.org!r})"
                )
            logger.info("IP health OK: %s", health)

        # Pre-import pyautogui with the current DISPLAY so Python caches it.
        # SeleniumBase UC on Linux changes os.environ['DISPLAY'] before importing
        # pyautogui, causing mouseinfo.__init__ to fail on the new non-existent
        # display. Caching it here prevents the re-init inside SB.
        if not os.environ.get("DISPLAY"):
            os.environ["DISPLAY"] = ":0"
        try:
            import pyautogui as _  # noqa: F401
        except Exception:
            pass

        if self.profile is None:
            self.profile = StealthProfile.for_proxy(
                country=self.proxy_country,
                timezone=self.proxy_timezone,
            )

        if self.profile.timezone:
            os.environ["TZ"] = self.profile.timezone

        # Use new headless mode — far less detectable than old --headless
        chrome_args = (
            "--disable-dev-shm-usage "
            "--no-sandbox "
            "--disable-gpu "
            "--force-webrtc-ip-handling-policy=disable_non_proxied_udp "
            "--window-size=1920,1080 "
        )
        if self.headless:
            chrome_args += "--headless=new "

        kwargs = dict(
            uc=True,
            xvfb=False,
            # headed=True tells SB to skip its Linux virtual-display spawner.
            # Chrome is still headless via --headless=new in chromium_arg.
            headed=True,
            agent=self.profile.user_agent,
            chromium_arg=chrome_args.strip(),
        )

        if self.use_warm_profile:
            from browser.profiles import get_profile_dir, cleanup_profile
            if not self.user_data_dir:
                self.user_data_dir = get_profile_dir(proxy_ip=self.proxy_ip)
            cleanup_profile(self.user_data_dir)
            kwargs["user_data_dir"] = self.user_data_dir
            logger.info("Using warm profile: %s", self.user_data_dir)

        if self.proxy_ip and self.proxy_port:
            if self.proxy_username and self.proxy_password:
                # SB expects "user:pass@host:port" (no scheme prefix).
                # Passing "http://user:pass@host:port" breaks SB's @ split,
                # making proxy_user = "http://user" → Chrome auth fails.
                proxy_str = (
                    f"{self.proxy_username}:{self.proxy_password}"
                    f"@{self.proxy_ip}:{self.proxy_port}"
                )
            else:
                proxy_str = f"{self.proxy_ip}:{self.proxy_port}"
            kwargs["proxy"] = proxy_str
            logger.info("Proxy configured: %s:%d", self.proxy_ip, self.proxy_port)
        else:
            logger.info("No proxy configured (direct connection)")

        self._sb_ctx = SB(**kwargs)
        self._sb = self._sb_ctx.__enter__()

        # Inject CDP stealth script (Canvas noise, WebGL, navigator overrides)
        apply_stealth_to_sb(self._sb, self.profile)

        vp = self.profile.viewport
        self._sb.set_window_size(vp["width"], vp["height"])

        logger.info(
            "SB session ready. UA=%s, tz=%s, headless=%s",
            self.profile.user_agent[:50],
            self.profile.timezone,
            self.headless,
        )

    def apply_stealth(self) -> None:
        """Re-apply CDP stealth script to the active browser session."""
        if self._sb and self.profile:
            apply_stealth_to_sb(self._sb, self.profile)

    def close(self) -> None:
        logger.info("Closing SB session")
        try:
            if self._sb_ctx:
                self._sb_ctx.__exit__(None, None, None)
        except Exception as e:
            logger.warning("Error closing SB session: %s", e)
        self._sb = None
        self._sb_ctx = None

    @property
    def sb(self):
        if self._sb is None:
            raise RuntimeError("Session not started.")
        return self._sb

    @property
    def bytes_received_kb(self) -> int:
        return self._bytes_received // 1024


def create_session(
    proxy_ip: str = "",
    proxy_port: int = 0,
    proxy_scheme: str = "http",
    proxy_username: str = "",
    proxy_password: str = "",
    proxy_country: str = "",
    proxy_timezone: str = "",
    headless: bool = True,
    profile: Optional[StealthProfile] = None,
    skip_health_check: bool = False,
) -> SBSession:
    session = SBSession(
        proxy_ip=proxy_ip,
        proxy_port=proxy_port,
        proxy_scheme=proxy_scheme,
        proxy_username=proxy_username,
        proxy_password=proxy_password,
        proxy_country=proxy_country,
        proxy_timezone=proxy_timezone,
        headless=headless,
        profile=profile,
    )
    session.start(skip_health_check=skip_health_check)
    return session
