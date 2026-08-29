"""
engagement/dwell.py
===================
Post-click reading engagement simulation (sync).
"""

from __future__ import annotations

import logging
import random
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


def _count_headings(sb) -> int:
    try:
        return sb.execute_script(
            "return document.querySelectorAll('h2, h3').length"
        ) or 0
    except Exception:
        return 0


def _count_code_blocks(sb) -> int:
    try:
        return sb.execute_script(
            "return document.querySelectorAll('pre, code').length"
        ) or 0
    except Exception:
        return 0


def _count_images(sb) -> int:
    try:
        return sb.execute_script(
            "return document.querySelectorAll('img').length"
        ) or 0
    except Exception:
        return 0


def _simulate_text_selection(sb) -> None:
    """Simulate a human highlighting/selecting a snippet of text while reading."""
    try:
        sb.execute_script("""
            const paragraphs = document.querySelectorAll('p');
            if (paragraphs.length > 0) {
                const p = paragraphs[Math.floor(Math.random() * paragraphs.length)];
                const range = document.createRange();
                const sel = window.getSelection();
                sel.removeAllRanges();
                if (p.childNodes.length > 0) {
                    range.setStart(p.childNodes[0], 0);
                    range.setEnd(p.childNodes[0], Math.min(p.innerText.length, 30));
                    sel.addRange(range);
                }
            }
        """)
        time.sleep(random.uniform(0.8, 2.0))
        # Clear selection
        sb.execute_script("window.getSelection().removeAllRanges();")
    except Exception:
        pass


def simulate_reading(sb, target_domain: str) -> Tuple[int, int]:
    """
    Simulate a human reading the article with realistic scroll patterns,
    mouse heatmaps, text highlights, and image pauses.
    Returns (dwell_time_seconds, scroll_depth_percent).
    """
    start = time.time()

    n_headings = _count_headings(sb)
    n_code = _count_code_blocks(sb)
    n_images = _count_images(sb)
    logger.info("Article elements: %d headings, %d code blocks, %d images",
                n_headings, n_code, n_images)

    # Initial scan — user lands on page, reads the first screen
    random_pause(5, 12)
    random_mouse_jitter(sb, duration_s=random.uniform(1, 3))

    # Scroll down reading the article
    viewport_h = sb.execute_script("return window.innerHeight") or 800
    doc_h = sb.execute_script("return document.documentElement.scrollHeight") or 3000
    readable_h = max(doc_h - viewport_h, viewport_h)

    steps = max(5, min(14, int(readable_h / 350)))
    chunk = readable_h // steps

    for i in range(steps):
        human_scroll(sb, random.randint(int(chunk * 0.7), int(chunk * 1.3)))

        # Pause longer at headings / code blocks / images
        if n_headings > 0 and random.random() < 0.35:
            random_pause(3, 7)
        elif n_code > 0 and random.random() < 0.30:
            random_pause(4, 10)
        elif n_images > 0 and random.random() < 0.25:
            random_pause(3, 6)
        else:
            random_pause(2, 5)

        random_mouse_jitter(sb, duration_s=random.uniform(0.5, 2))

        # 20% chance: human text selection / copy snippet simulation
        if random.random() < 0.20:
            _simulate_text_selection(sb)

        # 15% chance: scroll back up (re-reading paragraph)
        if random.random() < 0.15:
            human_scroll(sb, -random.randint(150, 350))
            random_pause(2, 4)

    scroll_depth = get_scroll_depth_percent(sb)
    dwell = int(time.time() - start)
    logger.info("Reading done: %ds dwell, %d%% scroll depth", dwell, scroll_depth)
    return dwell, scroll_depth
