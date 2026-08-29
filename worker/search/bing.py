"""
search/bing.py
==============
Bing search flow (SeleniumBase UC sync).
"""

from __future__ import annotations

import logging
import random
import time

from browser.humanizer import (
    type_humanized,
    press_enter_humanized,
    random_pause,
    random_mouse_jitter,
    human_scroll,
    human_click_element,
)
from search.serp import (
    SerpResult,
    SerpSearchOutcome,
    find_target_in_serp,
    detect_captcha,
    click_target_with_variation,
)
from captcha.solver import solve_captcha

CAPTCHA_MAX_ATTEMPTS = 3

logger = logging.getLogger("worker.search.bing")

BING_URL = "https://www.bing.com"
BING_SEARCH_INPUT_SELECTORS = [
    'textarea[name="q"]',
    'input[name="q"]',
    "#sb_form_q",
    'textarea[role="combobox"]',
    "#searchbox",
    'input[type="search"]',
    'input[aria-label="Search"]',
]


def navigate_to_bing(sb) -> None:
    logger.info("Navigating to %s", BING_URL)
    sb.open(BING_URL)
    time.sleep(random.uniform(0.5, 1.5))

    for sel in BING_SEARCH_INPUT_SELECTORS:
        try:
            sb.wait_for_element(sel, timeout=5)
            logger.debug("Found Bing search input: %s", sel)
            return
        except Exception:
            continue

    logger.warning("Could not find Bing search input")


def perform_bing_search(sb, query: str) -> bool:
    logger.info("Performing Bing search: %s", query[:80])
    random_mouse_jitter(sb, duration_s=1.0)

    matched_selector = None
    for sel in BING_SEARCH_INPUT_SELECTORS:
        try:
            if sb.is_element_visible(sel):
                matched_selector = sel
                break
        except Exception:
            continue

    if not matched_selector:
        logger.error("Bing search input not found")
        return False

    type_humanized(sb, matched_selector, query)
    time.sleep(random.uniform(0.5, 1.5))
    press_enter_humanized(sb, matched_selector)

    try:
        sb.wait_for_element("ol#b_results, #b_results", timeout=15)
        time.sleep(random.uniform(1.0, 3.0))
    except Exception as e:
        logger.warning("Bing results page load timeout: %s", e)

    return True


def bing_search_flow(sb, query: str, target_domain: str) -> SerpSearchOutcome:
    try:
        navigate_to_bing(sb)

        if detect_captcha(sb, "bing"):
            logger.warning("CAPTCHA on Bing homepage — attempting UC solve")
            solved = solve_captcha(sb, max_attempts=CAPTCHA_MAX_ATTEMPTS)
            if not solved:
                return SerpSearchOutcome(captcha_hit=True, error="CAPTCHA on homepage — solve failed")
            navigate_to_bing(sb)

        site_query = f"site:{target_domain} {query}"
        logger.debug("Bing query with site filter: %s", site_query[:120])
        success = perform_bing_search(sb, site_query)
        if not success:
            return SerpSearchOutcome(error="Failed to submit Bing search")

        if detect_captcha(sb, "bing"):
            logger.warning("CAPTCHA after search — attempting UC solve")
            solved = solve_captcha(sb, max_attempts=CAPTCHA_MAX_ATTEMPTS)
            if not solved:
                return SerpSearchOutcome(captcha_hit=True, error="CAPTCHA after search — solve failed")
            random_pause(2, 4)

        human_scroll(sb, random.randint(300, 600))
        random_pause(2, 5)

        return find_target_in_serp(sb, target_domain, engine="bing")

    except Exception as e:
        logger.error("Bing search flow error: %s", e, exc_info=True)
        return SerpSearchOutcome(error=str(e))


def bing_click_target(sb, target_result: SerpResult, competitor_click_chance: float = 0.0) -> None:
    click_target_with_variation(sb, target_result, engine="bing",
                                competitor_click_chance=competitor_click_chance)
