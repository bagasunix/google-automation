"""
search/google.py
================
Google search flow with humanized behavior.

Responsibilities:
  - Navigate to Google.com
  - Type the search query with 80-200ms per-char random variance
  - Handle Google's search interactions (search box focus, enter, wait for results)
  - Check for CAPTCHA / "unusual traffic"
  - Return parsed SERP results + target domain position

Flow:
  1. Navigate to google.com
  2. Wait for page load
  3. Locate search input (multiple selector fallbacks)
  4. Type query character by character (humanized)
  5. Press Enter or click search button
  6. Wait for SERP to load
  7. Detect CAPTCHA
  8. Return the page reference (SERP parsing done by serp.py)
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
)
from search.serp import (
    SerpResult,
    SerpSearchOutcome,
    find_target_in_serp,
    detect_captcha,
    click_target_with_variation,
)

logger = logging.getLogger("worker.search.google")

GOOGLE_URL = "https://www.google.com"
GOOGLE_SEARCH_INPUT_SELECTORS = [
    'textarea[name="q"]',
    'input[name="q"]',
    'textarea[role="combobox"]',
    "#APjFqb",
    "#searchform input[type='text']",
    "textarea.gLFyf",
    'input[aria-label="Search"]',
    'textarea[aria-label="Search"]',
]


async def navigate_to_google(page) -> None:
    """Navigate to Google homepage with realistic timing."""
    logger.info("Navigating to %s", GOOGLE_URL)

    await page.goto(GOOGLE_URL, wait_until="domcontentloaded", timeout=30000)
    await asyncio.sleep(random.uniform(0.5, 1.5))

    # Wait for the search box to appear
    search_found = False
    for selector in GOOGLE_SEARCH_INPUT_SELECTORS:
        try:
            el = await page.wait_for_selector(selector, timeout=5000)
            if el:
                search_found = True
                logger.debug("Found Google search input: %s", selector)
                break
        except Exception:
            continue

    if not search_found:
        logger.warning("Could not find Google search input — page may be blocked")


async def perform_google_search(page, query: str) -> bool:
    """
    Type a query into Google's search box and submit.

    Uses humanized typing (80-200ms per char with variance, occasional typos).
    Returns True if search was submitted successfully.
    """
    logger.info("Performing Google search: %s", query[:80])

    # Move mouse to search area first
    await random_mouse_jitter(page, duration_s=1.0)

    # Find the search input
    search_input = None
    for selector in GOOGLE_SEARCH_INPUT_SELECTORS:
        try:
            search_input = await page.query_selector(selector)
            if search_input:
                break
        except Exception:
            continue

    if not search_input:
        logger.error("Google search input not found")
        return False

    # Click the search box (human-like)
    box = await search_input.bounding_box()
    if box:
        from browser.humanizer import mouse_bezier
        await mouse_bezier(page, box["x"] + box["width"] / 2, box["y"] + box["height"] / 2)
        await asyncio.sleep(random.uniform(0.2, 0.5))

    # Type the query humanized
    # We need to use a valid selector — find which one matched
    matched_selector = None
    for selector in GOOGLE_SEARCH_INPUT_SELECTORS:
        if await page.query_selector(selector):
            matched_selector = selector
            break

    if matched_selector:
        await type_humanized(page, matched_selector, query)
    else:
        # Fallback: type into the focused element
        await page.keyboard.type(query, delay=random.randint(80, 200))

    # Small pause before submitting (human reads what they typed)
    await asyncio.sleep(random.uniform(0.5, 1.5))

    # Submit: press Enter
    await press_enter_humanized(page)

    # Wait for results to load
    try:
        await page.wait_for_load_state("domcontentloaded", timeout=15000)
        await asyncio.sleep(random.uniform(1.0, 3.0))  # let results render
    except Exception as e:
        logger.warning("Google results page load timeout: %s", e)

    return True


async def google_search_flow(
    page,
    query: str,
    target_domain: str,
) -> SerpSearchOutcome:
    """
    Full Google search flow: navigate → search → parse SERP → find target.

    Returns SerpSearchOutcome with:
      - found: whether the target domain was found
      - position: SERP position (0 = not found)
      - captcha_hit: whether a CAPTCHA was detected
      - error: error message if something went wrong
    """
    try:
        # Navigate to Google
        await navigate_to_google(page)

        # Check for CAPTCHA on the homepage (rare but possible)
        if await detect_captcha(page, "google"):
            return SerpSearchOutcome(captcha_hit=True, error="CAPTCHA on Google homepage")

        # Perform the search
        success = await perform_google_search(page, query)
        if not success:
            return SerpSearchOutcome(error="Failed to submit Google search")

        # Check for CAPTCHA after search
        if await detect_captcha(page, "google"):
            return SerpSearchOutcome(captcha_hit=True, error="CAPTCHA after search")

        # Browse the SERP (scroll a bit, read snippets)
        await human_scroll(page, random.randint(300, 600))
        await random_pause(2, 5)

        # Find the target domain in the results
        outcome = await find_target_in_serp(page, target_domain, engine="google")

        return outcome

    except Exception as e:
        logger.error("Google search flow error: %s", e, exc_info=True)
        return SerpSearchOutcome(error=str(e))


async def google_click_target(
    page,
    target_result: SerpResult,
) -> None:
    """
    Click the target result on the Google SERP with variation strategy.

    Delegates to serp.click_target_with_variation().
    """
    await click_target_with_variation(page, target_result, engine="google")
