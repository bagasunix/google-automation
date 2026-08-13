"""
search/bing.py
==============
Bing search flow with humanized behavior.

Responsibilities:
  - Navigate to Bing.com
  - Type the search query with 80-200ms per-char random variance
  - Handle Bing's search interactions (search box, search button, wait for results)
  - Check for CAPTCHA
  - Return parsed SERP results + target domain position

Flow is analogous to google.py but with Bing-specific selectors and URLs.
"""

from __future__ import annotations

import asyncio
import logging
from typing import Optional

from browser.humanizer import (
    type_humanized,
    press_enter_humanized,
    random_pause,
    random_mouse_jitter,
    human_scroll,
    mouse_bezier,
)
from search.serp import (
    SerpResult,
    SerpSearchOutcome,
    find_target_in_serp,
    detect_captcha,
    click_target_with_variation,
)

logger = logging.getLogger("worker.search.bing")

BING_URL = "https://www.bing.com"
BING_SEARCH_INPUT_SELECTORS = [
    'textarea[name="q"]',
    'input[name="q"]',
    "#sb_form_q",
    'textarea[role="combobox"]',
    "#searchbox",
    'input[type="search"]',
    'textarea[type="search"]',
    'input[aria-label="Search"]',
    'textarea[aria-label="Search"]',
]


async def navigate_to_bing(page) -> None:
    """Navigate to Bing homepage with realistic timing."""
    logger.info("Navigating to %s", BING_URL)

    await page.goto(BING_URL, wait_until="domcontentloaded", timeout=30000)
    await asyncio.sleep(random.uniform(0.5, 1.5))

    # Wait for the search box
    search_found = False
    for selector in BING_SEARCH_INPUT_SELECTORS:
        try:
            el = await page.wait_for_selector(selector, timeout=5000)
            if el:
                search_found = True
                logger.debug("Found Bing search input: %s", selector)
                break
        except Exception:
            continue

    if not search_found:
        logger.warning("Could not find Bing search input")


async def perform_bing_search(page, query: str) -> bool:
    """
    Type a query into Bing's search box and submit.

    Uses humanized typing (80-200ms per char with variance).
    Returns True if search was submitted successfully.
    """
    logger.info("Performing Bing search: %s", query[:80])

    # Move mouse around first
    await random_mouse_jitter(page, duration_s=1.0)

    # Find the search input
    search_input = None
    matched_selector = None
    for selector in BING_SEARCH_INPUT_SELECTORS:
        try:
            search_input = await page.query_selector(selector)
            if search_input:
                matched_selector = selector
                break
        except Exception:
            continue

    if not search_input:
        logger.error("Bing search input not found")
        return False

    # Click the search box (human-like)
    box = await search_input.bounding_box()
    if box:
        await mouse_bezier(page, box["x"] + box["width"] / 2, box["y"] + box["height"] / 2)
        await asyncio.sleep(random.uniform(0.2, 0.5))

    # Type the query humanized
    if matched_selector:
        await type_humanized(page, matched_selector, query)
    else:
        await page.keyboard.type(query, delay=random.randint(80, 200))

    # Small pause before submitting
    await asyncio.sleep(random.uniform(0.5, 1.5))

    # Submit: press Enter
    await press_enter_humanized(page)

    # Wait for results to load
    try:
        await page.wait_for_load_state("domcontentloaded", timeout=15000)
        await asyncio.sleep(random.uniform(1.0, 3.0))
    except Exception as e:
        logger.warning("Bing results page load timeout: %s", e)

    return True


async def bing_search_flow(
    page,
    query: str,
    target_domain: str,
) -> SerpSearchOutcome:
    """
    Full Bing search flow: navigate → search → parse SERP → find target.

    Returns SerpSearchOutcome with found/position/captcha/error.
    """
    try:
        # Navigate to Bing
        await navigate_to_bing(page)

        # Check for CAPTCHA on homepage
        if await detect_captcha(page, "bing"):
            return SerpSearchOutcome(captcha_hit=True, error="CAPTCHA on Bing homepage")

        # Perform the search
        success = await perform_bing_search(page, query)
        if not success:
            return SerpSearchOutcome(error="Failed to submit Bing search")

        # Check for CAPTCHA after search
        if await detect_captcha(page, "bing"):
            return SerpSearchOutcome(captcha_hit=True, error="CAPTCHA after search")

        # Browse the SERP
        await human_scroll(page, random.randint(300, 600))
        await random_pause(2, 5)

        # Find the target domain
        outcome = await find_target_in_serp(page, target_domain, engine="bing")

        return outcome

    except Exception as e:
        logger.error("Bing search flow error: %s", e, exc_info=True)
        return SerpSearchOutcome(error=str(e))


async def bing_click_target(
    page,
    target_result: SerpResult,
) -> None:
    """
    Click the target result on the Bing SERP with variation strategy.

    Delegates to serp.click_target_with_variation().
    """
    await click_target_with_variation(page, target_result, engine="bing")
