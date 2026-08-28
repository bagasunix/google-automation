"""
search/serp.py
==============
SERP (Search Engine Results Page) parsing and target domain finding.

Responsibilities:
  - Parse Google SERP results from the rendered page
  - Parse Bing SERP results
  - Find the target domain in the results and return its position
  - Detect CAPTCHA / "unusual traffic" pages (Google & Bing)
  - SERP click position variation strategies:
      50% direct click
      30% scroll past target then back
      20% click competitor first then back to SERP then click target

The SERP parsing uses Playwright's DOM evaluation (page.evaluate / query_selector_all)
with BeautifulSoup as a fallback parser. Google/Bing SERP HTML structures change
frequently, so selectors are kept flexible with multiple fallbacks.
"""

from __future__ import annotations

import asyncio
import random
import logging
from dataclasses import dataclass, field
from typing import Optional
from urllib.parse import urlparse

from browser.humanizer import (
    human_scroll,
    human_click_element,
    random_pause,
    mouse_bezier,
)

logger = logging.getLogger("worker.search.serp")


# ---------------------------------------------------------------------------
# Data classes
# ---------------------------------------------------------------------------

@dataclass
class SerpResult:
    """A single SERP result entry."""
    position: int             # 1-indexed position on the page
    title: str
    url: str
    domain: str
    snippet: str = ""
    element_ref = None        # Playwright ElementHandle (not serialised)


@dataclass
class SerpSearchOutcome:
    """Outcome of searching for the target domain in the SERP."""
    found: bool = False
    position: int = 0         # 0 = not found
    target_element_ref = None # ElementHandle of the target result
    total_results: int = 0
    results: list = field(default_factory=list)
    captcha_hit: bool = False
    error: str = ""


# ---------------------------------------------------------------------------
# CAPTCHA detection
# ---------------------------------------------------------------------------

# Google CAPTCHA / unusual traffic indicators
GOOGLE_CAPTCHA_INDICATORS = [
    "unusual traffic",
    "detected unusual traffic",
    "Our systems have detected",
    "g-recaptcha",
    "captcha",
    "Sorry/images/google.gif",
    "ipv4.google.com/sorry",
    "/sorry/",
    "Our systems have detected unusual traffic",
]

# Bing CAPTCHA indicators
BING_CAPTCHA_INDICATORS = [
    "To continue, please type the characters",
    "Bing Captcha",
    "Verify you are a human",
    "are you a robot",
    "blocked",
    "blocked this site",
    "captcha",
]


async def detect_captcha(page, engine: str = "google") -> bool:
    """
    Check whether the current page is a CAPTCHA / "unusual traffic" page.

    Checks:
      1. Page title for known CAPTCHA strings
      2. Page URL for CAPTCHA redirect paths (e.g. /sorry/)
      3. Page body text for CAPTCHA indicators
    """
    engine_indicators = GOOGLE_CAPTCHA_INDICATORS if engine == "google" else BING_CAPTCHA_INDICATORS

    try:
        title = await page.title()
        url = page.url

        # Check URL for CAPTCHA redirect
        url_lower = url.lower()
        if "/sorry/" in url_lower or "sorry/index" in url_lower:
            logger.warning("CAPTCHA detected: URL redirect to sorry page: %s", url)
            return True

        # Check title
        title_lower = title.lower()
        for indicator in engine_indicators:
            if indicator.lower() in title_lower:
                logger.warning("CAPTCHA detected: title contains '%s'", indicator)
                return True

        # Check body text (sample first 2000 chars for performance)
        body_text = await page.evaluate("""
            () => document.body ? document.body.innerText.substring(0, 2000) : ''
        """)
        body_lower = body_text.lower()
        for indicator in engine_indicators:
            if indicator.lower() in body_lower:
                logger.warning("CAPTCHA detected: body contains '%s'", indicator)
                return True

        # Check for reCAPTCHA iframe (Google-specific)
        recaptcha = await page.evaluate("""
            () => {
                const iframe = document.querySelector('iframe[src*="recaptcha"]');
                return iframe !== null;
            }
        """)
        if recaptcha:
            logger.warning("CAPTCHA detected: reCAPTCHA iframe found")
            return True

    except Exception as e:
        logger.warning("Error during CAPTCHA detection: %s", e)

    return False


# ---------------------------------------------------------------------------
# SERP parsing
# ---------------------------------------------------------------------------

async def parse_google_serp(page) -> list[SerpResult]:
    """
    Parse Google SERP results from the rendered page.

    Google's result structure changes frequently. We try multiple selector
    strategies and fall back gracefully.

    Primary strategy: div.g > div[data-href] or a[href] (organic results)
    Fallback: div[data-sokoban-container] > div > div
    """
    results = []

    # Strategy 1: Standard organic result containers (div.g)
    # Each result typically has: a link (a[href]), a title (h3), and a snippet
    raw_results = await page.evaluate("""
        () => {
            const results = [];
            // Google's organic results are in div.g elements
            const blocks = document.querySelectorAll('div.g, div[data-sokoban-container] > div');
            blocks.forEach((block, idx) => {
                const link = block.querySelector('a[href]');
                const titleEl = block.querySelector('h3');
                const snippetEl = block.querySelector('[data-sncf], .IsZvec, .VwiC3b, .B6xPof');

                if (link && titleEl) {
                    results.push({
                        index: idx,
                        href: link.href,
                        title: titleEl.textContent,
                        snippet: snippetEl ? snippetEl.textContent : '',
                    });
                }
            });
            return results;
        }
    """)

    if not raw_results:
        # Fallback: try broader selectors
        logger.debug("Primary Google SERP selectors returned nothing, trying fallback")
        raw_results = await page.evaluate("""
            () => {
                const results = [];
                const links = document.querySelectorAll('a[href^="http"]');
                for (const link of links) {
                    const titleEl = link.querySelector('h3') || link.closest('div')?.querySelector('h3');
                    if (titleEl && !link.href.includes('google.com') && !link.href.includes('webcache.')) {
                        results.push({
                            index: results.length,
                            href: link.href,
                            title: titleEl.textContent,
                            snippet: '',
                        });
                    }
                }
                return results;
            }
        """)

    for i, r in enumerate(raw_results):
        url = r.get("href", "")
        if not url or "google.com" in url or "webcache." in url:
            continue

        domain = _extract_domain(url)
        results.append(SerpResult(
            position=i + 1,
            title=r.get("title", ""),
            url=url,
            domain=domain,
            snippet=r.get("snippet", ""),
        ))

    # Filter out non-organic results (ads, etc.) — Google ads have different containers
    results = [r for r in results if r.url.startswith("http")]

    logger.info("Parsed %d Google SERP results", len(results))
    return results


async def parse_bing_serp(page) -> list[SerpResult]:
    """
    Parse Bing SERP results from the rendered page.

    Bing organic results are in li.b_algo containers, each with an h2 > a link.
    """
    # Wait for results to appear before parsing
    try:
        await page.wait_for_selector("li.b_algo, li.b_algoSite, #b_results", timeout=8000)
    except Exception:
        pass  # Continue anyway — might still have results

    raw_results = await page.evaluate("""
        () => {
            const results = [];
            // Try primary selectors first, fallback to broader selectors
            let blocks = document.querySelectorAll('li.b_algo, li.b_algoSite');
            if (blocks.length === 0) {
                // Fallback: any list item with an h2 link in results area
                blocks = document.querySelectorAll('#b_results li');
            }
            blocks.forEach((block, idx) => {
                const link = block.querySelector('h2 a, a.tilk');
                const titleEl = block.querySelector('h2');
                const snippetEl = block.querySelector('.b_caption p, p');

                if (link && link.href && link.href.startsWith('http')) {
                    results.push({
                        index: idx,
                        href: link.href,
                        title: titleEl ? titleEl.textContent : link.textContent,
                        snippet: snippetEl ? snippetEl.textContent : '',
                    });
                }
            });
            return results;
        }
    """)

    results = []
    for i, r in enumerate(raw_results):
        url = r.get("href", "")
        if not url:
            continue
        domain = _extract_domain(url)
        results.append(SerpResult(
            position=i + 1,
            title=r.get("title", ""),
            url=url,
            domain=domain,
            snippet=r.get("snippet", ""),
        ))

    logger.info("Parsed %d Bing SERP results", len(results))
    if len(results) == 0:
        try:
            import os, time as _time
            page_url = page.url
            page_title = await page.title()
            logger.warning("0 Bing results — URL: %s | Title: %s", page_url[:120], page_title[:80])
            os.makedirs("/home/bagasunix/Project/google-automation/screenshots", exist_ok=True)
            path = f"/home/bagasunix/Project/google-automation/screenshots/debug_bing_0results_{int(_time.time())}.png"
            await page.screenshot(path=path, full_page=False)
            logger.warning("0 Bing results — debug screenshot: %s", path)
        except Exception as e:
            logger.warning("Debug screenshot failed: %s", e)
    return results


def parse_serp(results: list[SerpResult], target_domain: str) -> SerpSearchOutcome:
    """
    Find the target domain in the parsed SERP results (pure function, no page needed).

    Args:
        results:        List of SerpResult objects (from parse_google_serp / parse_bing_serp)
        target_domain:  The domain to find (e.g. "bagasunix.com")

    Returns:
        SerpSearchOutcome with found=True and position if the domain is found.
    """
    outcome = SerpSearchOutcome(total_results=len(results))
    target_domain_clean = target_domain.lower().lstrip("www.")

    for result in results:
        result_domain_clean = result.domain.lower().lstrip("www.")
        if target_domain_clean in result_domain_clean or result_domain_clean in target_domain_clean:
            outcome.found = True
            outcome.position = result.position
            outcome.target_element_ref = result.element_ref
            outcome.results = results
            logger.info("Target domain '%s' found at SERP position %d", target_domain, result.position)
            return outcome

    outcome.results = results
    logger.info("Target domain '%s' NOT found in SERP (checked %d results)", target_domain, len(results))
    return outcome


async def find_target_in_serp(
    page,
    target_domain: str,
    engine: str = "google",
) -> SerpSearchOutcome:
    """
    Full workflow: parse the current SERP page and find the target domain.

    Checks page 1 first, then page 2 if not found.
    Returns SerpSearchOutcome with position (0 = not found) and captcha flag.
    """
    from captcha.solver import solve_recaptcha

    # First check for CAPTCHA
    captcha = await detect_captcha(page, engine)
    if captcha:
        logger.warning("CAPTCHA on SERP — attempting audio solve")
        solved = await solve_recaptcha(page, max_attempts=3)
        if not solved:
            return SerpSearchOutcome(captcha_hit=True, error="CAPTCHA on SERP — audio solve failed")
        await random_pause(2, 4)

    # Parse the current page
    if engine == "google":
        results = await parse_google_serp(page)
    else:
        results = await parse_bing_serp(page)

    outcome = parse_serp(results, target_domain)

    # If not found on page 1, try page 2
    if not outcome.found:
        logger.info("Target not on page 1, checking page 2")

        # Get the element handles for clicking later
        await _attach_element_handles(page, results, engine)

        page2_clicked = await _go_to_page_2(page, engine)
        if page2_clicked:
            await random_pause(1.0, 2.0)

            # Check CAPTCHA again
            captcha2 = await detect_captcha(page, engine)
            if captcha2:
                logger.warning("CAPTCHA on page 2 — attempting audio solve")
                solved2 = await solve_recaptcha(page, max_attempts=3)
                if not solved2:
                    return SerpSearchOutcome(captcha_hit=True, error="CAPTCHA on page 2 — audio solve failed")
                await random_pause(2, 4)

            if engine == "google":
                results_p2 = await parse_google_serp(page)
            else:
                results_p2 = await parse_bing_serp(page)

            # Positions on page 2 are offset by 10 (Google) or ~13 (Bing)
            offset = 10 if engine == "google" else 13
            for r in results_p2:
                r.position += offset

            outcome_p2 = parse_serp(results_p2, target_domain)
            if outcome_p2.found:
                return outcome_p2

    # Attach element handles for clicking
    await _attach_element_handles(page, outcome.results, engine)

    return outcome


async def _attach_element_handles(page, results: list[SerpResult], engine: str) -> None:
    """
    Attach Playwright ElementHandle references to each SerpResult.

    This is needed because page.evaluate returns plain JSON (no DOM references),
    but we need the actual element to click on it later.
    """
    try:
        if engine == "google":
            elements = await page.query_selector_all("div.g a[href], div[data-sokoban-container] a[href]")
        else:
            elements = await page.query_selector_all("li.b_algo h2 a, li.b_algoSite a")

        for i, el in enumerate(elements):
            if i < len(results):
                results[i].element_ref = el
    except Exception as e:
        logger.warning("Error attaching element handles: %s", e)


async def _go_to_page_2(page, engine: str) -> bool:
    """Navigate to page 2 of the SERP."""
    try:
        if engine == "google":
            next_btn = await page.query_selector("a#pnnext, td a.fl[href*='start=10']")
            if next_btn:
                await human_click_element(page, next_btn)
                await page.wait_for_load_state("domcontentloaded", timeout=20000)
                await asyncio.sleep(1.5)
                return True
        else:
            # Bing: build page 2 URL directly from current URL to avoid
            # accidentally clicking redirect/suggest links instead of Next button.
            current_url = page.url
            if "bing.com/search" in current_url:
                # Remove any existing pagination params and add first=11
                import urllib.parse
                parsed = urllib.parse.urlparse(current_url)
                params = urllib.parse.parse_qs(parsed.query, keep_blank_values=True)
                # Remove redirect params that cause empty results
                for k in ["rdr", "rdrig", "first", "search", "form"]:
                    params.pop(k, None)
                params["first"] = ["11"]
                params["FORM"] = ["PERE"]
                new_query = urllib.parse.urlencode(
                    {k: v[0] for k, v in params.items()}
                )
                page2_url = urllib.parse.urlunparse(parsed._replace(query=new_query))
                await page.goto(page2_url, wait_until="domcontentloaded", timeout=20000)
                await asyncio.sleep(2.0)
                return True
            # Fallback: try clicking Next button
            next_btn = await page.query_selector(
                "a.sb_pagN, a[title='Next page'], a[aria-label='Next page']"
            )
            if next_btn:
                await human_click_element(page, next_btn)
                await page.wait_for_load_state("domcontentloaded", timeout=20000)
                await asyncio.sleep(1.5)
                return True
    except Exception as e:
        logger.warning("Error navigating to page 2: %s", e)
    return False


# ---------------------------------------------------------------------------
# SERP click variation strategies
# ---------------------------------------------------------------------------

async def click_target_with_variation(
    page,
    target_result: SerpResult,
    engine: str = "google",
    competitor_click_chance: float = 0.0,
) -> None:
    """
    Click the target result with position-variation strategy.

    Strategies (configurable via competitor_click_chance):
      If competitor_click_chance > 0:
        (1-competitor_click_chance)/2 chance: Direct click (read snippet first)
        (1-competitor_click_chance)/2 chance: Scroll past target, read, scroll back, click
        competitor_click_chance: Click competitor first, dwell, back, click target
      If competitor_click_chance == 0:
        60% direct click, 40% scroll past + back (no competitor click — saves bandwidth)
    """
    if not target_result.element_ref:
        logger.warning("No element ref for target result — falling back to direct click")
        await random_pause(3, 8)
        await human_click_element(page, target_result.element_ref)
        return

    strategy = random.random()

    if competitor_click_chance > 0:
        # 3-way split: direct / scroll_past / competitor
        direct_thresh = (1 - competitor_click_chance) * 0.5
        scroll_thresh = (1 - competitor_click_chance)
    else:
        # 2-way split: 60% direct, 40% scroll past (no competitor — save bandwidth)
        direct_thresh = 0.6
        scroll_thresh = 1.0

    if strategy < direct_thresh:
        # --- Strategy 1: Direct click ---
        logger.info("Strategy: direct click with read delay")
        await random_pause(3, 8)
        await human_click_element(page, target_result.element_ref)

    elif strategy < scroll_thresh:
        # --- Strategy 2: Scroll past target, read, scroll back, click ---
        logger.info("Strategy: scroll past target then back")
        await human_scroll(page, random.randint(400, 800))
        await random_pause(5, 10)
        await page.evaluate("() => window.scrollTo(0, 0)")
        await random_pause(1, 3)
        await human_click_element(page, target_result.element_ref)

    else:
        # --- Strategy 3: Click competitor first, back to SERP, click target ---
        logger.info("Strategy: competitor first then back")
        # Find a competitor result above the target
        competitor = None
        # Query all result links and find one above the target
        if engine == "google":
            all_links = await page.query_selector_all("div.g h3 a, div.g a[href]")
        else:
            all_links = await page.query_selector_all("li.b_algo h2 a")

        target_box = await target_result.element_ref.bounding_box() if target_result.element_ref else None

        for link in all_links:
            try:
                box = await link.bounding_box()
                if box and target_box and box["y"] < target_box["y"]:
                    # This is above the target — good competitor
                    competitor = link
                    break
            except Exception:
                continue

        if competitor:
            await human_click_element(page, competitor)
            await page.wait_for_load_state("domcontentloaded", timeout=20000)
            await random_pause(10, 20)  # dwell on competitor page
            # Go back to SERP
            await page.go_back()
            await page.wait_for_load_state("domcontentloaded", timeout=20000)
            await random_pause(1, 3)
            # Now click target
            await human_click_element(page, target_result.element_ref)
        else:
            # No competitor found — fallback to direct click
            await random_pause(3, 8)
            await human_click_element(page, target_result.element_ref)

    # Wait for the target page to load after clicking
    try:
        await page.wait_for_load_state("domcontentloaded", timeout=20000)
    except Exception:
        logger.warning("Target page load timeout after SERP click — continuing")


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _extract_domain(url: str) -> str:
    """Extract the registered domain from a URL."""
    try:
        parsed = urlparse(url)
        host = parsed.netloc or parsed.hostname or ""
        # Strip port if present
        if ":" in host:
            host = host.split(":")[0]
        return host
    except Exception:
        return ""
