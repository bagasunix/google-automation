"""
search/google.py
================
Google search flow (SeleniumBase UC sync).
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
from captcha.solver import solve_sorry_captcha, detect_recaptcha_version

CAPTCHA_MAX_ATTEMPTS = 3

logger = logging.getLogger("worker.search.google")

GOOGLE_URL = "https://www.google.com"
GOOGLE_SEARCH_INPUT_SELECTORS = [
    'textarea[name="q"]',
    'input[name="q"]',
    'textarea[role="combobox"]',
    "#APjFqb",
    "textarea.gLFyf",
    'input[aria-label="Search"]',
    'textarea[aria-label="Search"]',
]


def navigate_to_google(sb) -> None:
    logger.info("Navigating to %s", GOOGLE_URL)
    sb.open(GOOGLE_URL)
    time.sleep(random.uniform(0.5, 1.5))

    # Dismiss consent/cookie banner
    consent_selectors = [
        "button#L2AGLb",
        "button#W0wltc",
        "[aria-label='Accept all']",
        "[aria-label='Agree']",
        "form[action*='consent'] button",
    ]
    for sel in consent_selectors:
        try:
            if sb.is_element_visible(sel):
                el = sb.find_element(sel)
                human_click_element(sb, el)
                logger.info("Dismissed consent banner: %s", sel)
                time.sleep(random.uniform(0.8, 1.5))
                break
        except Exception:
            continue

    # Wait for search box
    for sel in GOOGLE_SEARCH_INPUT_SELECTORS:
        try:
            sb.wait_for_element(sel, timeout=5)
            logger.debug("Found Google search input: %s", sel)
            return
        except Exception:
            continue

    try:
        logger.warning(
            "Could not find Google search input. URL=%s title=%s",
            sb.get_current_url(),
            sb.get_title(),
        )
        src = sb.get_page_source()
        logger.warning("Page source snippet: %s", src[:2000])
    except Exception:
        pass
    logger.warning("Could not find Google search input")


def perform_google_search(sb, query: str) -> bool | str:
    logger.info("Performing Google search: %s", query[:80])
    random_mouse_jitter(sb, duration_s=1.0)

    matched_selector = None
    for sel in GOOGLE_SEARCH_INPUT_SELECTORS:
        try:
            if sb.is_element_visible(sel):
                matched_selector = sel
                break
        except Exception:
            continue

    if not matched_selector:
        logger.error("Google search input not found")
        return False

    type_humanized(sb, matched_selector, query)
    time.sleep(random.uniform(0.5, 1.5))

    # Try clicking the Google Search button first — more reliable than Enter in UC mode
    submitted = False
    for btn_sel in ['input[name="btnK"]', 'input[aria-label="Google Search"]',
                    'button[aria-label="Google Search"]', '[jsaction*="sf.chk"]']:
        try:
            if sb.is_element_visible(btn_sel):
                el = sb.find_element(btn_sel)
                human_click_element(sb, el)
                submitted = True
                logger.debug("Submitted via button: %s", btn_sel)
                break
        except Exception:
            continue

    if not submitted:
        press_enter_humanized(sb, matched_selector)

    # If URL didn't change after submit, fall back to direct search URL navigation
    time.sleep(1.5)
    try:
        post_enter_url = sb.get_current_url()
        if "google.com/search" not in post_enter_url:
            from urllib.parse import quote_plus
            fallback_url = f"https://www.google.com/search?q={quote_plus(query)}"
            logger.warning("Submit did not navigate (URL=%s) — navigating: %s", post_enter_url, fallback_url)
            sb.open(fallback_url)
            time.sleep(2)
    except Exception as e:
        logger.warning("Post-submit URL check failed: %s", e)

    # Give the page a moment to start loading, then check for sorry/CAPTCHA
    # before waiting for results — solveSimpleChallenge can auto-redirect fast.
    time.sleep(2)
    current_url = ""
    try:
        current_url = sb.get_current_url()
    except Exception:
        pass
    if "/sorry/" in current_url or "ipv4.google.com/sorry" in current_url:
        logger.warning("Google sorry/CAPTCHA page detected after search. URL=%s", current_url)
        return "captcha"

    try:
        sb.wait_for_element("div#search, div#rso", timeout=20)
        time.sleep(random.uniform(1.0, 3.0))
    except Exception as e:
        logger.warning("Google results page load timeout: %s", e)
        # Check again after timeout — maybe auto-redirected from sorry
        try:
            current_url = sb.get_current_url()
            if "/sorry/" in current_url:
                logger.warning("CAPTCHA page still present after wait. URL=%s", current_url)
                return "captcha"
            src_snippet = sb.get_page_source()[:500]
            logger.warning("Page after timeout — URL=%s src=%s", current_url, src_snippet)
        except Exception:
            pass

    return True


def google_search_flow(sb, query: str, target_domain: str) -> SerpSearchOutcome:
    try:
        navigate_to_google(sb)

        if detect_captcha(sb, "google"):
            v = detect_recaptcha_version(sb)
            logger.warning("CAPTCHA on Google homepage — version=%s", v)
            if v == "v3":
                return SerpSearchOutcome(captcha_hit=True, error="CAPTCHA on homepage — reCAPTCHA v3 (score-based, unsolvable; rotate proxy)")
            solved = solve_sorry_captcha(sb, max_attempts=CAPTCHA_MAX_ATTEMPTS)
            if not solved:
                return SerpSearchOutcome(captcha_hit=True, error="CAPTCHA on homepage — token solve failed")
            navigate_to_google(sb)

        search_result = perform_google_search(sb, query)
        if search_result == "captcha":
            v = detect_recaptcha_version(sb)
            logger.warning("Google /sorry/ page detected — version=%s", v)
            if v == "v3":
                return SerpSearchOutcome(captcha_hit=True, error="CAPTCHA (/sorry/) — reCAPTCHA v3 (score-based, unsolvable; rotate proxy)")
            solved = solve_sorry_captcha(sb, max_attempts=CAPTCHA_MAX_ATTEMPTS)
            if not solved:
                return SerpSearchOutcome(captcha_hit=True, error="CAPTCHA (/sorry/) — token solve failed")
            random_pause(2, 4)
        elif not search_result:
            return SerpSearchOutcome(error="Failed to submit Google search")

        if detect_captcha(sb, "google"):
            v = detect_recaptcha_version(sb)
            logger.warning("CAPTCHA after search — version=%s", v)
            if v == "v3":
                return SerpSearchOutcome(captcha_hit=True, error="CAPTCHA after search — reCAPTCHA v3 (score-based, unsolvable; rotate proxy)")
            solved = solve_sorry_captcha(sb, max_attempts=CAPTCHA_MAX_ATTEMPTS)
            if not solved:
                return SerpSearchOutcome(captcha_hit=True, error="CAPTCHA after search — token solve failed")
            random_pause(2, 4)

        human_scroll(sb, random.randint(300, 600))
        random_pause(2, 5)

        return find_target_in_serp(sb, target_domain, engine="google")

    except Exception as e:
        logger.error("Google search flow error: %s", e, exc_info=True)
        return SerpSearchOutcome(error=str(e))


def google_click_target(sb, target_result: SerpResult, competitor_click_chance: float = 0.0) -> None:
    click_target_with_variation(sb, target_result, engine="google",
                                competitor_click_chance=competitor_click_chance)
