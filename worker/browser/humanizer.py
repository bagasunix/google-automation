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

    # Focus the field with human mouse movement (not raw page.click)
    element = await page.query_selector(selector)
    if element:
        await human_click_element(page, element)
    else:
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


def _ease_out_quint(t: float) -> float:
    """Ease-out quintic: fast start, gradually decelerating — models Fitts's correction phase."""
    return 1 - (1 - t) ** 5


def _ease_in_out_cubic(t: float) -> float:
    """Ease in-out cubic: slow start, fast middle, slow end — natural acceleration profile."""
    if t < 0.5:
        return 4 * t * t * t
    return 1 - (-2 * t + 2) ** 3 / 2


_last_mouse_pos: Tuple[float, float] = (400.0, 300.0)


async def mouse_bezier(page, target_x: float, target_y: float) -> None:
    """
    Move mouse to (target_x, target_y) simulating Fitts's Law.

    Human mouse movement has two phases:
      1. Ballistic: fast movement roughly toward the target
      2. Correction: decelerate near target, small adjustments

    Path is nearly direct with a slight natural curve (not zigzag).
    Speed follows ease-in-out profile (slow → fast → slow).
    """
    global _last_mouse_pos

    start_x, start_y = _last_mouse_pos

    dx = target_x - start_x
    dy = target_y - start_y
    distance = math.sqrt(dx * dx + dy * dy)

    if distance < 3:
        await page.mouse.move(target_x, target_y)
        _last_mouse_pos = (target_x, target_y)
        return

    # Slight perpendicular curve — humans naturally arc slightly, not perfectly straight.
    # Scale: ~5-15% of distance, smaller for longer distances (Fitts's Law: longer = more direct)
    curve_amount = min(distance * 0.08, 12) * random.choice([-1, 1])

    # Perpendicular direction
    perp_x = -dy / distance
    perp_y = dx / distance

    # Single control point offset perpendicular to the path, biased toward the middle.
    mid_x = (start_x + target_x) / 2 + perp_x * curve_amount
    mid_y = (start_y + target_y) / 2 + perp_y * curve_amount

    # Two control points near the mid for a gentle arc (not zigzag)
    c1 = (start_x + (mid_x - start_x) * 0.4, start_y + (mid_y - start_y) * 0.4)
    c2 = (mid_x + (target_x - mid_x) * 0.6, mid_y + (target_y - mid_y) * 0.6)

    # Step count scales with distance: longer = more steps, but capped.
    # Human mouse polling rate ~125Hz, movement takes 200-800ms.
    num_steps = max(15, min(60, int(distance / 8)))

    # Generate timing: ease-in-out (slow start, fast middle, slow end)
    for i in range(1, num_steps + 1):
        t = i / num_steps
        eased_t = _ease_in_out_cubic(t)

        # Interpolate along the quadratic-ish curve
        u = eased_t
        x = (1 - u) ** 3 * start_x + 3 * (1 - u) ** 2 * u * c1[0] + 3 * (1 - u) * u ** 2 * c2[0] + u ** 3 * target_x
        y = (1 - u) ** 3 * start_y + 3 * (1 - u) ** 2 * u * c1[1] + 3 * (1 - u) * u ** 2 * c2[1] + u ** 3 * target_y

        await page.mouse.move(x, y)

        # Timing: slow at start/end, fast in middle.
        # Derivative of ease-in-out gives the speed profile.
        prev_t = (i - 1) / num_steps
        prev_eased = _ease_in_out_cubic(prev_t)
        speed = max(eased_t - prev_eased, 0.01)

        # Base delay inversely proportional to speed: fast middle = short delay,
        # slow start/end = longer delay. Range: ~2-18ms.
        delay = 0.020 / speed
        delay = max(0.002, min(0.018, delay))
        await asyncio.sleep(delay)

    # Micro-correction near target: 1-3 tiny adjustments (human overshoot + correct)
    if random.random() < 0.35:
        corrections = random.randint(1, 2)
        for _ in range(corrections):
            ox = target_x + random.uniform(-2, 2)
            oy = target_y + random.uniform(-2, 2)
            await page.mouse.move(ox, oy)
            await asyncio.sleep(random.uniform(0.015, 0.04))
        await page.mouse.move(target_x, target_y)
        await asyncio.sleep(random.uniform(0.01, 0.03))

    _last_mouse_pos = (target_x, target_y)
    logger.debug("Mouse moved to (%.0f, %.0f) dist=%.0f steps=%d", target_x, target_y, distance, num_steps)


async def random_mouse_jitter(page, duration_s: float = 2.0) -> None:
    """
    Simulate idle mouse micro-movements (hand resting on mouse, slight drift).

    Humans at rest don't teleport mouse across the screen — they make small
    drifts of 10-40px within a local area. Occasional slightly larger shifts.
    """
    global _last_mouse_pos
    end_time = asyncio.get_event_loop().time() + duration_s

    while asyncio.get_event_loop().time() < end_time:
        # Small drift from current position
        drift_x = _last_mouse_pos[0] + random.uniform(-35, 35)
        drift_y = _last_mouse_pos[1] + random.uniform(-25, 25)

        # Keep within viewport
        vp = await page.evaluate("() => [window.innerWidth, window.innerHeight]")
        drift_x = max(50, min(vp[0] - 50, drift_x))
        drift_y = max(50, min(vp[1] - 50, drift_y))

        await mouse_bezier(page, drift_x, drift_y)
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
