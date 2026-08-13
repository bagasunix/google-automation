"""
engagement/click.py
===================
Internal link click simulation during engagement.

Responsibilities:
  - 50% chance: click 1 internal link on the article page
  - Read the linked page for 20-40s + scroll
  - Navigate back to the original article (not close)
  - Mouse movement: bezier curve to the link
  - Up to 2 internal link clicks total

The goal is to simulate a reader who follows a related link, reads it briefly,
then returns to the original article — common real-user behavior.
"""

from __future__ import annotations

import asyncio
import random
import logging
from typing import Tuple
from urllib.parse import urlparse

from browser.humanizer import (
    human_scroll,
    human_click_element,
    random_pause,
    mouse_bezier,
    random_mouse_jitter,
    get_scroll_depth_percent,
)

logger = logging.getLogger("worker.engagement.click")


async def _find_internal_links(page, target_domain: str, limit: int = 10) -> list:
    """
    Find internal links on the current article page.

    Internal links are <a> tags whose href points to the same domain.
    We exclude navigation, footer, and sidebar links — focusing on in-content links.

    Returns a list of Playwright ElementHandles.
    """
    # Query all links and filter in-page (JS) for domain match + in-content
    all_links = await page.query_selector_all("a[href]")

    internal_links = []
    target_domain_clean = target_domain.lower().lstrip("www.")

    for link in all_links:
        try:
            href = await link.get_attribute("href") or ""
            if not href or href.startswith("#") or href.startswith("javascript:"):
                continue

            # Resolve relative URLs
            href_domain = urlparse(href).netloc.lower().lstrip("www.")
            if not href_domain:
                # Relative link — same domain
                href_domain = target_domain_clean
            if target_domain_clean not in href_domain:
                continue

            # Skip if link is in nav/footer/sidebar (likely not in-content)
            # Check if the link is inside an <article>, <main>, or .content div
            is_in_content = await link.evaluate("""
                (el) => {
                    const contentParents = el.closest('article, main, .post-content, .entry-content, .content, .post-body, .article-body');
                    const navParents = el.closest('nav, footer, header, .sidebar, .menu, .navigation, .widget');
                    return contentParents !== null && navParents === null;
                }
            """)

            if is_in_content:
                # Check if the link has visible text (not just an image/icon)
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
    Simulate clicking internal links on the article page.

    Behavior:
      - 50% chance: click at least 1 internal link
      - If first click happens: 30% chance of a second click
      - Max 2 internal link clicks total
      - After each click: read linked page 20-40s + scroll, then go back

    Returns the number of internal clicks performed.
    """
    # 50% chance of any internal clicking
    if random.random() > 0.50:
        logger.info("No internal clicks this session (50% skip)")
        return 0

    clicks = 0
    max_clicks = 2

    # Find internal links
    links = await _find_internal_links(page, target_domain)
    if not links:
        logger.info("No internal links found — skipping click simulation")
        return 0

    # Store current URL so we can navigate back
    original_url = page.url

    while clicks < max_clicks and links:
        # Pick a random internal link (not the first one always)
        link = random.choice(links)
        links.remove(link)  # don't click the same link twice

        logger.info("Clicking internal link #%d", clicks + 1)

        # Move mouse to the link and click
        success = await human_click_element(page, link)
        if not success:
            logger.warning("Failed to click internal link — trying next")
            continue

        # Wait for the linked page to load
        try:
            await page.wait_for_load_state("domcontentloaded", timeout=15000)
            await asyncio.sleep(random.uniform(1, 3))
        except Exception as e:
            logger.warning("Internal link page load timeout: %s", e)

        # --- Read the linked page ---
        reading_time = random.uniform(20, 40)
        logger.info("Reading linked page for %.1fs", reading_time)

        # Initial pause (scanning)
        await random_pause(3, 8)

        # Scroll while reading
        await human_scroll(page, random.randint(300, 800))

        # Mouse movement (natural browsing)
        await random_mouse_jitter(page, duration_s=random.uniform(2, 5))

        # Remaining reading time
        elapsed = 3 + 8 + 3  # approx
        remaining = max(0, reading_time - elapsed)
        if remaining > 0:
            await asyncio.sleep(remaining * 0.5)

        # More scrolling
        await human_scroll(page, random.randint(200, 500))
        await asyncio.sleep(remaining * 0.5)

        clicks += 1

        # 30% chance of another click (if we haven't hit max)
        if clicks < max_clicks and random.random() < 0.30 and links:
            logger.info("Decided to click another internal link (30%% chance)")
            # Navigate back to original article first
            await page.go_back()
            try:
                await page.wait_for_load_state("domcontentloaded", timeout=15000)
            except Exception:
                pass
            await random_pause(1, 3)
        else:
            # Go back to the original article
            logger.info("Going back to original article")
            await page.go_back()
            try:
                await page.wait_for_load_state("domcontentloaded", timeout=15000)
            except Exception:
                pass
            await random_pause(2, 5)
            break

    logger.info("Internal click simulation complete: %d clicks", clicks)
    return clicks
