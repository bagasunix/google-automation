"""
browser/session.py
==================
Stealth browser session builder with proxy injection.

Responsibilities:
  - Launch Playwright with headless Chromium (or headed if configured)
  - Configure proxy (HTTP/SOCKS) from TaskRequest proxy_ip:proxy_port
  - Apply stealth profile (UA, viewport, timezone, WebGL, canvas, WebRTC block)
  - Fresh cookie jar per session (new browser context each time)
  - Apply playwright-stealth if available (additional evasion patches)
  - Provide a context manager for clean setup/teardown

Usage:
    async with StealthSession(proxy_ip="1.2.3.4", proxy_port=8080) as session:
        page = await session.new_page()
        await page.goto("https://google.com")
        ...

The session creates a fresh BrowserContext with:
  - A randomised StealthProfile
  - Proxy injected at context creation (Playwright supports per-context proxy)
  - All stealth init scripts injected
  - playwright-stealth applied if installed
"""

from __future__ import annotations

import logging
from dataclasses import dataclass
from typing import Optional

from playwright.async_api import async_playwright, Browser, BrowserContext, Page

from browser.stealth import StealthProfile, apply_stealth

logger = logging.getLogger("worker.browser.session")


@dataclass
class StealthSession:
    """
    A stealth browser session wrapping Playwright.

    Attributes:
        proxy_ip:     Proxy server IP (empty string = no proxy / direct)
        proxy_port:   Proxy server port
        proxy_scheme: "http" or "socks5"
        headless:     Run browser headless (default True for server use)
        profile:      StealthProfile to use (random if None)
        slow_mo:      Slow down Playwright actions by N ms (debugging)
    """

    proxy_ip: str = ""
    proxy_port: int = 0
    proxy_scheme: str = "http"
    proxy_username: str = ""
    proxy_password: str = ""
    headless: bool = True
    profile: Optional[StealthProfile] = None
    slow_mo: int = 0

    # Internal state
    _playwright = None
    _browser: Optional[Browser] = None
    _context: Optional[BrowserContext] = None
    _page: Optional[Page] = None

    async def __aenter__(self) -> "StealthSession":
        await self.start()
        return self

    async def __aexit__(self, exc_type, exc_val, exc_tb):
        await self.close()

    async def start(self) -> None:
        """Launch the browser and create a stealthed context."""
        logger.info(
            "Starting stealth session: proxy=%s:%d (scheme=%s), headless=%s",
            self.proxy_ip, self.proxy_port, self.proxy_scheme, self.headless,
        )

        self._playwright = await async_playwright().start()

        # Generate a random stealth profile if none provided
        if self.profile is None:
            self.profile = StealthProfile.random()

        # Browser launch options
        launch_args = [
            "--disable-blink-features=AutomationControlled",
            "--disable-features=IsolateOrigins,site-per-process",
            "--disable-infobars",
            "--disable-dev-shm-usage",
            "--no-first-run",
            "--no-default-browser-check",
            "--disable-extensions",
            "--disable-component-extensions-with-background-pages",
            "--disable-background-networking",
            "--disable-sync",
            "--metrics-recording-only",
            "--disable-default-apps",
            "--disable-translate",
            "--mute-audio",
            "--no-sandbox",
            "--disable-setuid-sandbox",
            # WebRTC: disable to prevent IP leak
            "--enforce-webrtc-ip-permission-check",
            "--disable-webrtc-multiple-routes",
            "--disable-webrtc-hw-encoding",
            "--disable-webrtc-hw-decoding",
        ]

        self._browser = await self._playwright.chromium.launch(
            headless=self.headless,
            args=launch_args,
            slow_mo=self.slow_mo,
        )

        # Context options — proxy is set per-context
        context_options = {
            "user_agent": self.profile.user_agent,
            "viewport": self.profile.viewport,
            "timezone_id": self.profile.timezone,
            "locale": self.profile.locale,
            "device_scale_factor": self.profile.device_pixel_ratio,
            "color_scheme": "light",
            "java_script_enabled": True,
            "ignore_https_errors": True,
            # Note: resource blocking is handled via route() below (playwright API)
        }

        # Inject proxy if provided
        if self.proxy_ip and self.proxy_port:
            proxy_url = f"{self.proxy_scheme}://{self.proxy_ip}:{self.proxy_port}"
            context_options["proxy"] = {
                "server": proxy_url,
            }
            # Add auth credentials for Webshare proxies
            if self.proxy_username and self.proxy_password:
                context_options["proxy"]["username"] = self.proxy_username
                context_options["proxy"]["password"] = self.proxy_password
                logger.info("Proxy configured: %s (authenticated)", proxy_url)
            else:
                logger.info("Proxy configured: %s", proxy_url)
        else:
            logger.info("No proxy configured (direct connection)")

        self._context = await self._browser.new_context(**context_options)

        # Block font resources for speed (but keep images for engagement realism)
        async def _block_fonts(route):
            if route.request.resource_type == "font":
                await route.abort()
            else:
                await route.continue_()

        await self._context.route("**/*", _block_fonts)

        # Apply our custom stealth init scripts
        apply_stealth(self._context, self.profile)

        # Apply playwright-stealth if available (additional evasion layer)
        await self._apply_playwright_stealth(self._context)

        # Create default page
        self._page = await self._context.new_page()

        logger.info("Stealth session ready. Profile: UA=%s, tz=%s",
                     self.profile.user_agent[:50], self.profile.timezone)

    async def _apply_playwright_stealth(self, context: BrowserContext) -> None:
        """Try to apply playwright-stealth if the package is installed."""
        try:
            from playwright_stealth import stealth_async, StealthConfig

            # playwright-stealth v1.x: apply per-context
            # Some versions use stealth_sync/stealth_async on pages,
            # others use StealthConfig. We try the async page approach.
            try:
                # v2.x API: StealthConfig applied to context
                config = StealthConfig()
                # Apply to all pages via init script
                await context.add_init_script(
                    config.get_script() if hasattr(config, 'get_script') else ""
                )
                logger.info("playwright-stealth applied (context-level, v2 API)")
            except Exception:
                # v1.x API: apply per-page
                for page in context.pages:
                    await stealth_async(page)
                logger.info("playwright-stealth applied (page-level, v1 API)")
        except ImportError:
            logger.warning(
                "playwright-stealth not installed — relying on custom stealth scripts only"
            )
        except Exception as e:
            logger.warning("playwright-stealth application failed: %s — continuing with custom stealth", e)

    @property
    def page(self) -> Page:
        """Get the default page."""
        if self._page is None:
            raise RuntimeError("Session not started. Use 'async with StealthSession(...)' or call start().")
        return self._page

    @property
    def context(self) -> BrowserContext:
        """Get the browser context."""
        if self._context is None:
            raise RuntimeError("Session not started.")
        return self._context

    async def new_page(self) -> Page:
        """Create a new page (tab) in the stealthed context."""
        if self._context is None:
            raise RuntimeError("Session not started.")
        page = await self._context.new_page()
        return page

    async def close(self) -> None:
        """Close the browser session cleanly."""
        logger.info("Closing stealth session")
        try:
            if self._context:
                await self._context.close()
        except Exception as e:
            logger.warning("Error closing context: %s", e)
        try:
            if self._browser:
                await self._browser.close()
        except Exception as e:
            logger.warning("Error closing browser: %s", e)
        try:
            if self._playwright:
                await self._playwright.stop()
        except Exception as e:
            logger.warning("Error stopping playwright: %s", e)

        self._playwright = None
        self._browser = None
        self._context = None
        self._page = None


async def create_session(
    proxy_ip: str = "",
    proxy_port: int = 0,
    proxy_scheme: str = "http",
    proxy_username: str = "",
    proxy_password: str = "",
    headless: bool = True,
    profile: Optional[StealthProfile] = None,
) -> StealthSession:
    """
    Convenience function to create and start a stealth session.
    Supports authenticated proxies (Webshare) via proxy_username/password.

    Usage:
        session = await create_session("1.2.3.4", 8080)
        page = await session.new_page()
        ...
        await session.close()
    """
    session = StealthSession(
        proxy_ip=proxy_ip,
        proxy_port=proxy_port,
        proxy_scheme=proxy_scheme,
        proxy_username=proxy_username,
        proxy_password=proxy_password,
        headless=headless,
        profile=profile,
    )
    await session.start()
    return session
