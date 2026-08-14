"""
browser/humanizer.py
====================
Human-like browser interaction helpers.

Provides:
  - type_humanized():  Type text into an input with 80-200ms per-char random variance
  - human_scroll():    Scroll the page in chunks (200-500px) with pauses
  - smooth_scroll_to(): Scroll to a specific element smoothly
  - mouse_bezier():    Move mouse along a bezier curve to a target (not linear jump)
  - random_mouse_jitter(): Small random mouse movements
  - random_pause():    Sleep a random duration within a range
  - human_click():     Click with pre-mouse-move + small delay

All functions are async and accept a Playwright Page object.
"""

from __future__ import annotations

import asyncio
import random
import math
import logging
from typing import Tuple

logger = logging.getLogger("worker.browser.humanizer")


# ---------------------------------------------------------------------------
# Timing helpers
# ---------------------------------------------------------------------------

async def random_pause(min_s: float = 1.0, max_s: float = 3.0) -> None:
    """Sleep for a random duration between min_s and max_s seconds."""
    delay = random.uniform(min_s, max_s)
    logger.debug("Pausing for %.2fs", delay)
    await asyncio.sleep(delay)


async def jittered_delay(base_ms: int, variance_ms: int) -> None:
    """Sleep for base_ms ± variance_ms milliseconds."""
    delay_ms = base_ms + random.randint(-variance_ms, variance_ms)
    await asyncio.sleep(max(50, delay_ms) / 1000.0)


# ---------------------------------------------------------------------------
# Typing simulator
# ---------------------------------------------------------------------------

async def type_humanized(page, selector: str, text: str) -> None:
    """
    Type text into an input field character by character with realistic timing.

    Per-character delay: 80-200ms with random variance.
    Occasional longer pauses (thinking) ~5% chance, 300-800ms.
    Occasional typo + correction ~3% chance.
    """
    logger.info("Typing into '%s': %s", selector, text[:60])

    # Focus the field first
    await page.click(selector)
    await asyncio.sleep(random.uniform(0.2, 0.5))

    # Clear any existing text
    await page.fill(selector, "")
    await asyncio.sleep(random.uniform(0.1, 0.3))

    for i, char in enumerate(text):
        # 3% chance of a typo + correction (type wrong char, pause, backspace, type correct)
        if random.random() < 0.03 and char.isalpha():
            wrong_char = random.choice("abcdefghijklmnopqrstuvwxyz")
            await page.type(selector, wrong_char, delay=0)
            await asyncio.sleep(random.uniform(0.15, 0.35))  # realise the mistake
            await page.keyboard.press("Backspace")
            await asyncio.sleep(random.uniform(0.08, 0.2))
            await page.type(selector, char, delay=0)
        else:
            await page.type(selector, char, delay=0)

        # Normal per-char delay: 80-200ms
        char_delay = random.uniform(0.08, 0.20)
        await asyncio.sleep(char_delay)

        # 5% chance of a "thinking" pause mid-typing
        if random.random() < 0.05:
            await asyncio.sleep(random.uniform(0.3, 0.8))

    logger.debug("Finished typing: %s", text[:60])


async def press_enter_humanized(page) -> None:
    """Press Enter after a short random delay (like a human deciding to search)."""
    await asyncio.sleep(random.uniform(0.3, 0.8))
    await page.keyboard.press("Enter")


# ---------------------------------------------------------------------------
# Scrolling
# ---------------------------------------------------------------------------

async def human_scroll(page, distance: int | None = None) -> int:
    """
    Scroll down the page in human-like chunks.

    Each scroll step: 200-500px, followed by a 0.3-1.5s pause.
    Occasionally scrolls back up slightly (re-reading).

    If distance is None, scrolls a random amount (500-2000px total).
    Returns total pixels scrolled.
    """
    if distance is None:
        distance = random.randint(500, 2000)

    total_scrolled = 0

    while total_scrolled < distance:
        chunk = random.randint(200, 500)
        total_scrolled += chunk

        await page.mouse.wheel(0, chunk)
        await asyncio.sleep(random.uniform(0.3, 1.5))

        # 10% chance to scroll back up a bit (re-reading)
        if random.random() < 0.10:
            back = random.randint(50, 150)
            await page.mouse.wheel(0, -back)
            total_scrolled -= back
            await asyncio.sleep(random.uniform(0.5, 1.5))

    logger.debug("Scrolled %dpx (target %d)", total_scrolled, distance)
    return total_scrolled


async def smooth_scroll_to(page, selector: str) -> None:
    """Scroll smoothly to an element, simulating trackpad/mouse-wheel scrolling."""
    try:
        element = await page.query_selector(selector)
        if not element:
            logger.warning("Element not found for scroll: %s", selector)
            return

        # Get current scroll position and target
        current_y = await page.evaluate("() => window.scrollY")
        box = await element.bounding_box()
        if not box:
            return
        target_y = box["y"] + current_y - 100  # offset for header

        if target_y <= current_y:
            return  # already past it

        distance = target_y - current_y
        await human_scroll(page, int(distance))
    except Exception as e:
        logger.warning("smooth_scroll_to error: %s", e)


async def get_scroll_depth_percent(page) -> int:
    """Calculate current scroll depth as percentage (0-100)."""
    try:
        result = await page.evaluate("""
            () => {
                const scrollTop = window.scrollY || document.documentElement.scrollTop;
                const scrollHeight = document.documentElement.scrollHeight;
                const clientHeight = document.documentElement.clientHeight;
                const maxScroll = scrollHeight - clientHeight;
                if (maxScroll <= 0) return 100;
                return Math.min(100, Math.round((scrollTop / maxScroll) * 100));
            }
        """)
        return int(result)
    except Exception:
        return 0


# ---------------------------------------------------------------------------
# Mouse movement
# ---------------------------------------------------------------------------

def _bezier_points(
    start: Tuple[float, float],
    control1: Tuple[float, float],
    control2: Tuple[float, float],
    end: Tuple[float, float],
    num_steps: int = 25,
) -> list[Tuple[float, float]]:
    """Generate points along a cubic bezier curve."""
    points = []
    for i in range(num_steps + 1):
        t = i / num_steps
        x = (
            (1 - t) ** 3 * start[0]
            + 3 * (1 - t) ** 2 * t * control1[0]
            + 3 * (1 - t) * t ** 2 * control2[0]
            + t ** 3 * end[0]
        )
        y = (
            (1 - t) ** 3 * start[1]
            + 3 * (1 - t) ** 2 * t * control1[1]
            + 3 * (1 - t) * t ** 2 * control2[1]
            + t ** 3 * end[1]
        )
        points.append((x, y))
    return points


async def mouse_bezier(page, target_x: float, target_y: float) -> None:
    """
    Move the mouse to (target_x, target_y) along a bezier curve.

    Uses two random control points between the current position and the target
    to create a natural, non-linear path. Speed varies slightly along the path.
    """
    # Get current mouse position (Playwright doesn't expose this directly,
    # so we track from a known starting point or use viewport center)
    try:
        current = await page.evaluate("""
            () => {
                // We don't have real mouse pos, use last known or viewport center
                return [window.innerWidth / 2, window.innerHeight / 2];
            }
        """)
        start_x, start_y = current[0], current[1]
    except Exception:
        start_x, start_y = 400.0, 300.0

    # Generate two control points with random offset perpendicular to the path
    mid_x = (start_x + target_x) / 2
    mid_y = (start_y + target_y) / 2
    offset1 = random.uniform(-80, 80)
    offset2 = random.uniform(-80, 80)

    control1 = (mid_x + offset1, mid_y + offset2 * random.choice([-1, 1]))
    control2 = (mid_x - offset2, mid_y + offset1 * random.choice([-1, 1]))

    points = _bezier_points(
        (start_x, start_y), control1, control2, (target_x, target_y),
        num_steps=random.randint(15, 30),
    )

    for px, py in points:
        await page.mouse.move(px, py)
        await asyncio.sleep(random.uniform(0.005, 0.02))  # 5-20ms per step

    logger.debug("Mouse moved via bezier to (%.0f, %.0f)", target_x, target_y)


async def random_mouse_jitter(page, duration_s: float = 2.0) -> None:
    """Make small random mouse movements for a given duration (simulates idle hand movement)."""
    end_time = asyncio.get_event_loop().time() + duration_s
    while asyncio.get_event_loop().time() < end_time:
        x = random.uniform(100, 800)
        y = random.uniform(100, 600)
        await mouse_bezier(page, x, y)
        await asyncio.sleep(random.uniform(0.3, 0.8))


async def human_click(page, selector: str) -> bool:
    """
    Click an element in a human-like way:
      1. Move mouse to the element via bezier curve
      2. Small pause (reading before clicking)
      3. Click
    Returns True if successful.
    """
    try:
        element = await page.query_selector(selector)
        if not element:
            logger.warning("human_click: element not found: %s", selector)
            return False

        box = await element.bounding_box()
        if not box:
            return False

        # Target the center of the element with slight randomisation
        target_x = box["x"] + box["width"] / 2 + random.uniform(-5, 5)
        target_y = box["y"] + box["height"] / 2 + random.uniform(-5, 5)

        # Move mouse to target via bezier
        await mouse_bezier(page, target_x, target_y)

        # Small pause before clicking (reading)
        await asyncio.sleep(random.uniform(0.3, 1.0))

        # Click
        await page.mouse.click(target_x, target_y)
        logger.info("Human-clicked: %s", selector)
        return True

    except Exception as e:
        logger.warning("human_click error on '%s': %s", selector, e)
        return False


async def human_click_element(page, element) -> bool:
    """
    Click a Playwright ElementHandle (already located) in a human-like way.
    Scrolls element into viewport first to avoid negative Y coordinates.
    Returns True if successful.
    """
    try:
        # Scroll element into view first — ensures Y is always positive
        await element.scroll_into_view_if_needed()
        await asyncio.sleep(random.uniform(0.4, 1.0))

        box = await element.bounding_box()
        if not box:
            logger.warning("human_click_element: no bounding box")
            return False

        target_x = box["x"] + box["width"] / 2 + random.uniform(-5, 5)
        target_y = box["y"] + box["height"] / 2 + random.uniform(-5, 5)

        # Sanity check — Y must be positive (in viewport)
        if target_y < 0:
            logger.warning("human_click_element: Y=%.0f still negative after scroll — skipping", target_y)
            return False

        await mouse_bezier(page, target_x, target_y)
        await asyncio.sleep(random.uniform(0.3, 1.0))
        await page.mouse.click(target_x, target_y)
        logger.info("Human-clicked element at (%.0f, %.0f)", target_x, target_y)
        return True

    except Exception as e:
        logger.warning("human_click_element error: %s", e)
        return False
