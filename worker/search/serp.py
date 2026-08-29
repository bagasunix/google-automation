"""
search/serp.py
==============
SERP parsing and target domain finding (SeleniumBase sync).
"""

from __future__ import annotations

import random
import logging
import time
from dataclasses import dataclass, field
from urllib.parse import urlparse

from browser.humanizer import human_scroll, human_click_element, random_pause, mouse_bezier

logger = logging.getLogger("worker.search.serp")


@dataclass
class SerpResult:
    position: int
    title: str
    url: str
    domain: str
    snippet: str = ""
    element_ref = None


@dataclass
class SerpSearchOutcome:
    found: bool = False
    position: int = 0
    target_element_ref = None
    total_results: int = 0
    results: list = field(default_factory=list)
    captcha_hit: bool = False
    error: str = ""


# ---------------------------------------------------------------------------
# CAPTCHA detection
# ---------------------------------------------------------------------------

GOOGLE_CAPTCHA_INDICATORS = [
    "unusual traffic",
    "detected unusual traffic",
    "Our systems have detected",
    "g-recaptcha",
    "captcha",
    "/sorry/",
    "ipv4.google.com/sorry",
]

BING_CAPTCHA_INDICATORS = [
    "captcha",
    "are you a human",
    "robot",
    "automated queries",
]


def detect_captcha(sb, engine: str = "google") -> bool:
    try:
        url = sb.get_current_url().lower()
        # URL-based detection is most reliable — false positives rare
        url_indicators = ["/sorry/", "ipv4.google.com/sorry", "bing.com/images/captcha"]
        if any(ind in url for ind in url_indicators):
            return True
        # Check for visible reCAPTCHA element (not just source text which has false positives)
        has_recaptcha = sb.execute_script(
            "return !!document.querySelector('.g-recaptcha, iframe[src*=\"recaptcha\"][src*=\"anchor\"]')"
        )
        return bool(has_recaptcha)
    except Exception:
        return False


# ---------------------------------------------------------------------------
# SERP parsing — Google
# ---------------------------------------------------------------------------

GOOGLE_RESULT_SELECTORS = [
    "div.g",
    "div[data-sokoban-container]",
    "div.tF2Cxc",
    "div.yuRUbf",
]


def _parse_google_serp(sb, start_offset: int = 1) -> list[SerpResult]:
    results = []
    position = start_offset

    # Try JS-based extraction first (most reliable)
    try:
        raw = sb.execute_script("""
            const results = [];
            const containers = document.querySelectorAll('div.g, div.tF2Cxc, div[data-sokoban-container]');
            for (const c of containers) {
                const a = c.querySelector('h3 a, a h3, a[href]');
                const h3 = c.querySelector('h3');
                const snippet = c.querySelector('.VwiC3b, .s3v9rd, span[class]');
                if (!a || !a.href || !a.href.startsWith('http')) continue;
                results.push({
                    url: a.href,
                    title: h3 ? h3.innerText : a.innerText,
                    snippet: snippet ? snippet.innerText : ''
                });
            }
            return results;
        """)
        for item in (raw or []):
            url = item.get("url", "")
            if not url:
                continue
            domain = urlparse(url).netloc.lstrip("www.")
            results.append(SerpResult(
                position=position,
                title=item.get("title", ""),
                url=url,
                domain=domain,
                snippet=item.get("snippet", ""),
            ))
            position += 1
    except Exception as e:
        logger.warning("JS Google SERP parse failed: %s", e)

    # Element refs for clickable results
    try:
        elements = sb.find_elements("div.g h3 a, div.tF2Cxc h3 a")
        for i, el in enumerate(elements):
            if i < len(results):
                results[i].element_ref = el
    except Exception:
        pass

    logger.info("Google SERP: parsed %d results (starting pos %d)", len(results), start_offset)
    return results


# ---------------------------------------------------------------------------
# SERP parsing — Bing
# ---------------------------------------------------------------------------

def _parse_bing_serp(sb, start_offset: int = 1) -> list[SerpResult]:
    results = []
    position = start_offset

    try:
        raw = sb.execute_script("""
            const results = [];
            const containers = document.querySelectorAll('li.b_algo');
            for (const c of containers) {
                const a = c.querySelector('h2 a');
                const snippet = c.querySelector('.b_caption p, p');
                if (!a || !a.href || !a.href.startsWith('http')) continue;
                results.push({
                    url: a.href,
                    title: a.innerText,
                    snippet: snippet ? snippet.innerText : ''
                });
            }
            return results;
        """)
        for item in (raw or []):
            url = item.get("url", "")
            if not url:
                continue
            domain = urlparse(url).netloc.lstrip("www.")
            results.append(SerpResult(
                position=position,
                title=item.get("title", ""),
                url=url,
                domain=domain,
                snippet=item.get("snippet", ""),
            ))
            position += 1
    except Exception as e:
        logger.warning("JS Bing SERP parse failed: %s", e)

    try:
        elements = sb.find_elements("li.b_algo h2 a")
        for i, el in enumerate(elements):
            if i < len(results):
                results[i].element_ref = el
    except Exception:
        pass

    logger.info("Bing SERP: parsed %d results (starting pos %d)", len(results), start_offset)
    return results


def _navigate_next_page(sb, engine: str, next_page: int) -> bool:
    """Navigate to the next page of search results."""
    from urllib.parse import urlparse, parse_qs, urlencode, urlunparse
    try:
        if engine == "google":
            for sel in ['a#pnnext', 'a[aria-label="Next page"]', f'a[aria-label="Page {next_page}"]', 'button[jsname="jT2kWb"]']:
                try:
                    if sb.is_element_visible(sel):
                        el = sb.find_element(sel)
                        human_click_element(sb, el)
                        sb.wait_for_ready_state_complete(timeout=15)
                        time.sleep(random.uniform(2, 4))
                        return True
                except Exception:
                    pass
            current_url = sb.get_current_url()
            if "google.com/search" in current_url:
                parsed = urlparse(current_url)
                qs = parse_qs(parsed.query)
                qs["start"] = [str((next_page - 1) * 10)]
                next_url = urlunparse((parsed.scheme, parsed.netloc, parsed.path, parsed.params, urlencode(qs, doseq=True), parsed.fragment))
                logger.info("Navigating to Google page %d via URL: %s", next_page, next_url)
                sb.open(next_url)
                sb.wait_for_ready_state_complete(timeout=15)
                time.sleep(random.uniform(2, 4))
                return True
        else:
            for sel in ['a.sb_pagN', 'a[title="Next page"]', f'li.b_pag a[aria-label="Page {next_page}"]']:
                try:
                    if sb.is_element_visible(sel):
                        el = sb.find_element(sel)
                        human_click_element(sb, el)
                        sb.wait_for_ready_state_complete(timeout=15)
                        time.sleep(random.uniform(2, 4))
                        return True
                except Exception:
                    pass
            current_url = sb.get_current_url()
            if "bing.com/search" in current_url:
                parsed = urlparse(current_url)
                qs = parse_qs(parsed.query)
                qs["first"] = [str((next_page - 1) * 10 + 1)]
                next_url = urlunparse((parsed.scheme, parsed.netloc, parsed.path, parsed.params, urlencode(qs, doseq=True), parsed.fragment))
                logger.info("Navigating to Bing page %d via URL: %s", next_page, next_url)
                sb.open(next_url)
                sb.wait_for_ready_state_complete(timeout=15)
                time.sleep(random.uniform(2, 4))
                return True
    except Exception as e:
        logger.warning("_navigate_next_page error: %s", e)
    return False


# ---------------------------------------------------------------------------
# Find target (with multi-page pagination support)
# ---------------------------------------------------------------------------

def find_target_in_serp(sb, target_domain: str, engine: str = "google", max_pages: int = 3) -> SerpSearchOutcome:
    all_results = []

    for page in range(1, max_pages + 1):
        offset = (page - 1) * 10 + 1
        page_results = _parse_google_serp(sb, start_offset=offset) if engine == "google" else _parse_bing_serp(sb, start_offset=offset)
        all_results.extend(page_results)

        for r in page_results:
            if target_domain in r.domain or target_domain in r.url:
                logger.info("Target '%s' found on Page %d (position %d)", target_domain, page, r.position)
                return SerpSearchOutcome(
                    found=True,
                    position=r.position,
                    target_element_ref=r.element_ref,
                    total_results=len(all_results),
                    results=all_results,
                )

        if page < max_pages:
            logger.info("Target '%s' not found on Page %d — navigating to Page %d", target_domain, page, page + 1)
            navigated = _navigate_next_page(sb, engine, page + 1)
            if not navigated:
                logger.info("No next page button or navigation failed for Page %d", page + 1)
                break
            random_pause(2, 4)
            human_scroll(sb, random.randint(200, 500))
            random_pause(1, 3)

    logger.info("Target '%s' not found in %d parsed results (up to page %d)", target_domain, len(all_results), max_pages)
    return SerpSearchOutcome(found=False, total_results=len(all_results), results=all_results)


# ---------------------------------------------------------------------------
# Click variation strategies & Pogo-Sticking Engine
# ---------------------------------------------------------------------------

def click_target_with_variation(sb, target_result: SerpResult, engine: str = "google",
                                 competitor_click_chance: float = 0.20) -> None:
    """
    Simulate natural human click variation on search engine result pages.
    Strategies:
      - Direct click (50%)
      - Scroll past target then back up (25%)
      - Pogo-sticking on competitor (25%): clicks competitor, bounces back to SERP, then clicks target.
    """
    r = random.random()

    if competitor_click_chance > 0 and r < competitor_click_chance:
        logger.info("Triggering Pogo-Sticking SEO boost strategy (competitor bounce -> target dwell)")
        _pogo_sticking_flow(sb, target_result, engine)
    elif r < 0.70:
        _click_direct(sb, target_result)
    else:
        _click_scroll_past(sb, target_result)


def _click_direct(sb, result: SerpResult) -> None:
    logger.info("Click strategy: direct target click")
    if result.element_ref:
        try:
            human_click_element(sb, result.element_ref)
            return
        except Exception:
            pass
    try:
        sb.open(result.url)
    except Exception as e:
        logger.warning("Direct URL open fallback failed: %s", e)


def _click_scroll_past(sb, result: SerpResult) -> None:
    logger.info("Click strategy: scroll past target then scroll back")
    human_scroll(sb, random.randint(300, 600))
    random_pause(1.0, 2.5)
    human_scroll(sb, -random.randint(200, 450))
    random_pause(0.8, 1.8)
    _click_direct(sb, result)


def _pogo_sticking_flow(sb, result: SerpResult, engine: str) -> None:
    """
    Simulate Pogo-Sticking (Bounce on competitor -> Satisfied dwell on target):
    1. Click top competitor link.
    2. Skim competitor page for 4-8 seconds (dissatisfaction).
    3. Click browser Back to return to SERP.
    4. Pause on SERP (2-3s).
    5. Click target result and stay.
    """
    logger.info("Pogo-sticking: searching for competitor result...")
    competitor_clicked = False

    try:
        sel = "div.g h3 a, div.tF2Cxc h3 a" if engine == "google" else "li.b_algo h2 a"
        links = sb.find_elements(sel)

        for link in links:
            try:
                href = link.get_attribute("href") or ""
                if href.startswith("http") and result.url not in href and "google.com" not in href and "bing.com" not in href:
                    logger.info("Pogo-sticking: clicking competitor (%s)", href[:60])
                    human_click_element(sb, link)
                    sb.wait_for_ready_state_complete(timeout=10)

                    # Skim competitor for 4-7s then bounce back
                    random_pause(2.0, 4.0)
                    human_scroll(sb, random.randint(150, 350))
                    random_pause(2.0, 4.0)

                    logger.info("Pogo-sticking: bouncing back to SERP...")
                    sb.go_back()
                    sb.wait_for_ready_state_complete(timeout=10)
                    random_pause(1.5, 3.0)
                    competitor_clicked = True
                    break
            except Exception as ex:
                logger.debug("Competitor click element error: %s", ex)
                continue
    except Exception as e:
        logger.warning("Pogo-sticking flow error: %s", e)

    # Now click our target domain
    if competitor_clicked:
        logger.info("Pogo-sticking: now proceeding to click target domain with full satisfaction dwell")
    _click_direct(sb, result)
