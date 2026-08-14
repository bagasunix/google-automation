"""
engagement/click.py
===================
Internal link click simulation during engagement.

Responsibilities:
  - Always click 1-2 internal links after reading the main article
  - For each internal article: full simulate_reading (scroll, pause at headings,
    code blocks, images) — same as the main article, not a quick skim
  - Navigate back to the original article after each internal read
  - Mouse movement: bezier curve to the link

The goal is to simulate a real reader who finishes the article, then follows
related links — reading each one properly before going back.
"""

from __future__ import annotations

import asyncio
import random
import logging
from urllib.parse import urlparse

from browser.humanizer import (
    human_scroll,
    human_click_element,
    random_pause,
    random_mouse_jitter,
)

logger = logging.getLogger("worker.engagement.click")


async def _find_internal_links(page, target_domain: str, limit: int = 10) -> list:
    """
    Find internal links on the current article page.

    Internal links are <a> tags whose href points to the same domain.
    We exclude navigation, footer, and sidebar links — focusing on in-content links.

    Returns a list of Playwright ElementHandles.
    """
    all_links = await page.query_selector_all("a[href]")

    internal_links = []
    target_domain_clean = target_domain.lower().lstrip("www.")

    for link in all_links:
        try:
            href = await link.get_attribute("href") or ""
            if not href or href.startswith("#") or href.startswith("javascript:"):
                continue

            href_domain = urlparse(href).netloc.lower().lstrip("www.")
            if not href_domain:
                href_domain = target_domain_clean
            if target_domain_clean not in href_domain:
                continue

            # Skip nav/footer/sidebar links
            is_in_content = await link.evaluate("""
                (el) => {
                    const contentParents = el.closest('article, main, .post-content, .entry-content, .content, .post-body, .article-body');
                    const navParents = el.closest('nav, footer, header, .sidebar, .menu, .navigation, .widget');
                    return contentParents !== null && navParents === null;
                }
            """)

            if is_in_content:
                text = await link.inner_text()
                if text and len(text.strip()) > 3:
                    internal_links.append(link)
                    if len(internal_links) >= limit:
                        break

        except Exception:
            continue

    logger.info("Found %d internal links in article content", len(internal_links))
    return internal_links


async def simulate_internal_clicks(page, target_domain: str) -> int:
    """
    Simulate clicking 1-2 internal links on the article page.

    Behavior:
      - Always attempt to click 1-2 internal articles (human-like behavior)
      - For each: full reading simulation (scroll, pause at headings, etc.)
      - Navigate back to original article after each read

    Returns the number of internal clicks performed.
    """
    # Import here to avoid circular dependency
    from engagement.dwell import simulate_reading

    # Always click 1-2 articles — this is natural reading behavior
    target_clicks = random.randint(1, 2)
    clicks = 0

    # Find internal links
    links = await _find_internal_links(page, target_domain)
    if not links:
        logger.info("No internal links found — skipping internal click simulation")
        return 0

    # Store original URL to navigate back
    original_url = page.url

    while clicks < target_clicks and links:
        # Scroll a bit before clicking — like a reader scanning the page for the next link
        await human_scroll(page, random.randint(100, 300))
        await random_pause(2, 5)

        # Pick a random internal link
        link = random.choice(links)
        links.remove(link)

        logger.info("Clicking internal link #%d of %d", clicks + 1, target_clicks)

        # Move mouse naturally to the link and click
        success = await human_click_element(page, link)
        if not success:
            logger.warning("Failed to click internal link — trying next")
            continue

        # Wait for the linked page to load
        try:
            await page.wait_for_load_state("domcontentloaded", timeout=15000)
            await asyncio.sleep(random.uniform(1.5, 3.0))
        except Exception as e:
            logger.warning("Internal link page load timeout: %s", e)

        # --- Full reading simulation on the internal article ---
        logger.info("Reading internal article (full simulation)...")
        try:
            dwell, depth = await simulate_reading(page, target_domain)
            logger.info("Internal article: dwell=%ds scroll=%d%%", dwell, depth)
        except Exception as e:
            logger.warning("Reading simulation failed on internal article: %s", e)
            # Fallback: simple pause + scroll
            await random_pause(20, 40)
            await human_scroll(page, random.randint(300, 700))

        clicks += 1

        # Navigate back to original article
        logger.info("Going back to original article after internal read")
        try:
            await page.go_back()
            await page.wait_for_load_state("domcontentloaded", timeout=15000)
            await asyncio.sleep(random.uniform(1, 2))
        except Exception as e:
            logger.warning("Failed to go back: %s — navigating directly", e)
            try:
                await page.goto(original_url, wait_until="domcontentloaded", timeout=15000)
                await asyncio.sleep(random.uniform(1, 2))
            except Exception:
                break

        # Small pause on original article before potentially clicking another link
        if clicks < target_clicks and links:
            await random_pause(3, 8)
            await random_mouse_jitter(page, duration_s=random.uniform(1, 3))

    logger.info("Internal click simulation done: %d clicks performed", clicks)
    return clicks
