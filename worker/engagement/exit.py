"""
engagement/exit.py
==================
Exit strategy after engagement is complete.

Responsibilities:
  - Varied exit methods (close tab / back to SERP / navigate to homepage)
  - Post-exit cooldown (30-120s) before next task

Exit strategies (randomised):
  30% chance: Close the tab (simulates user closing the article)
  40% chance: Go back to the search engine (SERP) — simulates returning to results
  20% chance: Navigate to the site's homepage — simulates exploring the site
  10% chance: Navigate to a different site entirely — simulates distraction

The post-exit cooldown is critical: it prevents the pattern of immediately
looping to the next task, which is a strong automation signal.
"""

from __future__ import annotations

import asyncio
import random
import logging

from browser.humanizer import random_pause, random_mouse_jitter

logger = logging.getLogger("worker.engagement.exit")


# Possible "distraction" sites (navigating away from the article domain)
DISTRACTION_SITES = [
    "https://www.youtube.com",
    "https://www.reddit.com",
    "https://twitter.com",
    "https://news.ycombinator.com",
    "https://github.com",
    "https://www.wikipedia.org",
]


async def exit_article(
    page,
    target_domain: str = "",
    context=None,
    distraction_exit_chance: float = 0.0,
) -> str:
    """
    Execute an exit strategy after reading the article.

    Args:
        page:             The Playwright Page currently on the article
        domain:           The article's domain (for homepage navigation)
        context:          The BrowserContext (for closing tabs)
        distraction_exit_chance: 0.0-1.0 probability of navigating to a
                                  distraction site (default 0 = disabled, saves bandwidth)

    Returns:
        The exit method used: "close", "back", "homepage", "navigate"
    """
    strategy = random.random()

    # Adjust thresholds based on distraction_exit_chance.
    # close 30%, back 40%, homepage 30%-X, navigate X
    navigate_thresh = 1.0 - distraction_exit_chance
    homepage_thresh = navigate_thresh - (navigate_thresh * 0.3 / (0.3 + 0.4))
    # Effectively: close 30%, back 40%, homepage (30%-X)%, navigate X%
    # If distraction=0: close 30%, back 40%, homepage 30%

    exit_method = ""

    if strategy < 0.30:
        # --- 30%: Close the tab ---
        exit_method = "close"
        logger.info("Exit strategy: close tab")

        # Mouse movement before closing (natural hesitation)
        await random_mouse_jitter(page, duration_s=random.uniform(1, 3))

        try:
            if context:
                # Close the page (tab) but keep the context alive
                await page.close()
            else:
                # Navigate to about:blank as a fallback
                await page.goto("about:blank", timeout=10000)
        except Exception as e:
            logger.warning("Error closing tab: %s", e)
            # Fallback: navigate to blank
            try:
                await page.goto("about:blank", timeout=10000)
            except Exception:
                pass

    elif strategy < 0.70:
        # --- 40%: Go back to the search engine (SERP) ---
        exit_method = "back"
        logger.info("Exit strategy: back to SERP")

        # Mouse movement first
        await random_mouse_jitter(page, duration_s=random.uniform(1, 2))

        try:
            await page.go_back(timeout=15000)
            await asyncio.sleep(random.uniform(1, 3))

            # Maybe scroll the SERP a bit (browsing other results)
            if random.random() < 0.40:
                await page.mouse.wheel(0, random.randint(100, 400))
                await asyncio.sleep(random.uniform(2, 5))
        except Exception as e:
            logger.warning("Error going back: %s", e)

    elif strategy < homepage_thresh:
        # --- Homepage: navigate to the site's homepage ---
        exit_method = "homepage"
        logger.info("Exit strategy: navigate to homepage")

        await random_mouse_jitter(page, duration_s=random.uniform(1, 2))

        if target_domain:
            homepage_url = f"https://{target_domain}"
            try:
                await page.goto(homepage_url, wait_until="domcontentloaded", timeout=20000)
                await asyncio.sleep(random.uniform(2, 5))

                # Browse the homepage a bit
                from browser.humanizer import human_scroll
                await human_scroll(page, random.randint(200, 500))
                await asyncio.sleep(random.uniform(3, 8))
            except Exception as e:
                logger.warning("Error navigating to homepage: %s", e)
        else:
            # No domain provided — fallback to back
            await page.go_back(timeout=15000)

    else:
        # --- 10%: Navigate to a different site (distraction) ---
        exit_method = "navigate"
        logger.info("Exit strategy: navigate to distraction site")

        distraction_url = random.choice(DISTRACTION_SITES)
        try:
            await page.goto(distraction_url, wait_until="domcontentloaded", timeout=20000)
            await asyncio.sleep(random.uniform(3, 8))

            # Browse the distraction site briefly
            from browser.humanizer import human_scroll
            await human_scroll(page, random.randint(200, 600))
            await asyncio.sleep(random.uniform(5, 15))
        except Exception as e:
            logger.warning("Error navigating to distraction site: %s", e)

    logger.info("Exit complete: method=%s", exit_method)
    return exit_method


async def post_exit_cooldown(min_s: int = 30, max_s: int = 120) -> int:
    """
    Execute a post-exit cooldown.

    This simulates a human shifting attention after reading an article:
    they might check their phone, open another tab, etc.

    The cooldown happens BEFORE the next task is picked, preventing the
    pattern of immediate task-to-task transitions.

    Returns the actual cooldown duration in seconds.
    """
    cooldown = random.randint(min_s, max_s)
    logger.info("Post-exit cooldown: %ds", cooldown)

    # We sleep in 5-second chunks so the server can still respond to shutdown signals
    elapsed = 0
    while elapsed < cooldown:
        chunk = min(5, cooldown - elapsed)
        await asyncio.sleep(chunk)
        elapsed += chunk

    return cooldown
