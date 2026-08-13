"""
engagement/dwell.py
===================
Post-click reading engagement simulation.

Responsibilities:
  - Initial pause (5-15s) after page load — scanning the top of the article
  - Scroll down in chunks (200-500px per scroll, not jump)
  - Pause at H2/H3 headings (3-8s each — reading the section)
  - Pause longer at code blocks / images (5-12s — studying)
  - Occasional scroll back up (re-reading previous section)
  - Random micro-pauses (1-3s) mid-paragraph (thinking)
  - Total reading time: 60-300s depending on article length
  - Track scroll depth percentage
  - Mouse hover naturally over text/links/images

The dwell function returns the total dwell time (seconds) and max scroll depth (%).
"""

from __future__ import annotations

import asyncio
import random
import logging
import time
from typing import Tuple

from browser.humanizer import (
    human_scroll,
    random_pause,
    mouse_bezier,
    random_mouse_jitter,
    get_scroll_depth_percent,
)

logger = logging.getLogger("worker.engagement.dwell")


# ---------------------------------------------------------------------------
# Element detection helpers
# ---------------------------------------------------------------------------

async def _find_content_elements(page) -> dict:
    """
    Find content elements on the article page that we should "read":
      - H2/H3 headings
      - Code blocks (<pre>, <code>)
      - Images (<img>)
      - Paragraphs (<p>)

    Returns a dict with lists of Y-positions for each element type.
    """
    elements = await page.evaluate("""
        () => {
            const result = { headings: [], code_blocks: [], images: [], paragraphs: [] };

            // H2/H3 headings
            document.querySelectorAll('h2, h3').forEach(el => {
                const rect = el.getBoundingClientRect();
                if (rect.top + window.scrollY > 0) {
                    result.headings.push({ y: rect.top + window.scrollY, text: el.textContent.substring(0, 60) });
                }
            });

            // Code blocks
            document.querySelectorAll('pre, code, .highlight, .code-block').forEach(el => {
                const rect = el.getBoundingClientRect();
                if (rect.top + window.scrollY > 0) {
                    result.code_blocks.push({ y: rect.top + window.scrollY });
                }
            });

            // Images
            document.querySelectorAll('img').forEach(el => {
                const rect = el.getBoundingClientRect();
                if (rect.width > 100 && rect.top + window.scrollY > 0) {
                    result.images.push({ y: rect.top + window.scrollY });
                }
            });

            // Paragraphs (sample — every 3rd paragraph)
            const paras = document.querySelectorAll('article p, .post-content p, .entry-content p, main p');
            for (let i = 0; i < paras.length; i += 3) {
                const rect = paras[i].getBoundingClientRect();
                if (rect.top + window.scrollY > 0) {
                    result.paragraphs.push({ y: rect.top + window.scrollY });
                }
            }

            return result;
        }
    """)
    return elements


async def _get_page_height(page) -> float:
    """Get the total scrollable page height."""
    try:
        height = await page.evaluate("() => document.documentElement.scrollHeight")
        return float(height)
    except Exception:
        return 2000.0


async def _get_current_scroll_y(page) -> float:
    """Get current scroll Y position."""
    try:
        y = await page.evaluate("() => window.scrollY")
        return float(y)
    except Exception:
        return 0.0


# ---------------------------------------------------------------------------
# Main dwell function
# ---------------------------------------------------------------------------

async def simulate_reading(page, target_domain: str = "") -> Tuple[int, int]:
    """
    Simulate human reading behavior on an article page.

    Returns:
        (dwell_time_seconds, scroll_depth_percent)
    """
    start_time = time.time()

    # Determine target total reading time (60-300s)
    target_reading_time = random.randint(60, 300)
    logger.info("Starting reading simulation — target: %ds", target_reading_time)

    # --- Phase 1: Initial pause (scanning the top of the page) ---
    logger.info("Phase 1: Initial scan (5-15s)")
    await random_pause(5, 15)

    # Mouse hover over the article title / header area
    await random_mouse_jitter(page, duration_s=random.uniform(2, 4))

    # --- Phase 2: Progressive reading ---
    # Get all content elements so we know where to pause
    content_elements = await _find_content_elements(page)
    page_height = await _get_page_height(page)

    # Merge and sort all elements by Y position for sequential reading
    all_elements = []
    for el in content_elements.get("headings", []):
        all_elements.append({"y": el["y"], "type": "heading", "pause_range": (3, 8)})
    for el in content_elements.get("code_blocks", []):
        all_elements.append({"y": el["y"], "type": "code", "pause_range": (5, 12)})
    for el in content_elements.get("images", []):
        all_elements.append({"y": el["y"], "type": "image", "pause_range": (5, 12)})
    for el in content_elements.get("paragraphs", []):
        all_elements.append({"y": el["y"], "type": "paragraph", "pause_range": (1, 3)})

    all_elements.sort(key=lambda e: e["y"])

    max_scroll_depth = 0
    last_scroll_y = 0.0

    for element in all_elements:
        elapsed = time.time() - start_time
        if elapsed >= target_reading_time:
            logger.info("Target reading time reached (%.0fs) — stopping", elapsed)
            break

        target_y = element["y"] - random.uniform(50, 150)  # offset to position element in view

        # Scroll to this element in chunks
        current_y = await _get_current_scroll_y(page)
        distance = max(0, target_y - current_y)

        if distance > 0:
            scrolled = await human_scroll(page, int(distance))
            await asyncio.sleep(random.uniform(0.3, 0.8))

        # Pause at this element (reading/studying)
        pause_min, pause_max = element["pause_range"]
        pause_time = random.uniform(pause_min, pause_max)

        logger.debug("Pausing at %s (y=%.0f) for %.1fs", element["type"], element["y"], pause_time)

        # Mouse hover near the element
        try:
            viewport_height = await page.evaluate("() => window.innerHeight")
            hover_x = random.uniform(200, 800)
            hover_y = random.uniform(viewport_height * 0.3, viewport_height * 0.7)
            await mouse_bezier(page, hover_x, hover_y)
        except Exception:
            pass

        await asyncio.sleep(pause_time)

        # Track scroll depth
        current_depth = await get_scroll_depth_percent(page)
        max_scroll_depth = max(max_scroll_depth, current_depth)

        # 15% chance: scroll back up a bit (re-reading)
        if random.random() < 0.15:
            back_distance = random.randint(100, 300)
            await page.mouse.wheel(0, -back_distance)
            await asyncio.sleep(random.uniform(2, 5))
            # Then continue scrolling down
            await page.mouse.wheel(0, back_distance + random.randint(50, 100))
            await asyncio.sleep(random.uniform(0.5, 1.0))

        # 10% chance: micro-pause (thinking)
        if random.random() < 0.10:
            await asyncio.sleep(random.uniform(1, 3))

    # --- Phase 3: Scroll to the bottom if not already there ---
    elapsed = time.time() - start_time
    if elapsed < target_reading_time:
        remaining = target_reading_time - elapsed
        logger.info("Phase 3: Scrolling to bottom (%.0fs remaining)", remaining)

        # Scroll to bottom in chunks
        current_y = await _get_current_scroll_y(page)
        remaining_distance = page_height - current_y
        if remaining_distance > 100:
            await human_scroll(page, int(remaining_distance))
            await random_pause(2, 5)

        current_depth = await get_scroll_depth_percent(page)
        max_scroll_depth = max(max_scroll_depth, current_depth)

    # --- Phase 4: Final dwell at the bottom ---
    elapsed = time.time() - start_time
    if elapsed < target_reading_time:
        remaining = target_reading_time - elapsed
        # Spend remaining time at the bottom (reading footer, comments, etc.)
        final_pause = min(remaining, random.uniform(5, 20))
        logger.info("Phase 4: Final dwell (%.1fs)", final_pause)

        await random_mouse_jitter(page, duration_s=min(final_pause, 5))
        await asyncio.sleep(max(0, final_pause - 5))

    # Calculate final stats
    dwell_time = int(time.time() - start_time)
    final_depth = await get_scroll_depth_percent(page)
    max_scroll_depth = max(max_scroll_depth, final_depth)

    # Make sure we scrolled back to a realistic position (not always at bottom)
    if random.random() < 0.30:
        # Scroll back up a bit (like returning to the top)
        await page.evaluate("() => window.scrollTo({ top: 0, behavior: 'smooth' })")
        await asyncio.sleep(random.uniform(1, 3))

    logger.info(
        "Reading complete: dwell=%ds, scroll_depth=%d%%",
        dwell_time, max_scroll_depth,
    )

    return dwell_time, max_scroll_depth
