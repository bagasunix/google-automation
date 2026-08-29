"""
engagement/click.py
===================
Internal link click simulation (sync).
"""

from __future__ import annotations

import logging
import random
from urllib.parse import urlparse

from browser.humanizer import (
    human_scroll,
    human_click_element,
    random_pause,
    random_mouse_jitter,
)

logger = logging.getLogger("worker.engagement.click")


def _find_internal_links(sb, target_domain: str, limit: int = 10) -> list:
    try:
        elements = sb.find_elements("article a[href], main a[href], .content a[href], .post a[href]")
        if not elements:
            elements = sb.find_elements("a[href]")

        links = []
        seen_urls = set()
        for el in elements:
            try:
                href = el.get_attribute("href") or ""
                if not href.startswith("http"):
                    continue
                parsed = urlparse(href)
                domain = parsed.netloc.lstrip("www.")
                if target_domain not in domain:
                    continue
                if href in seen_urls:
                    continue
                # Skip navigation/footer anchors
                text = el.text.strip()
                if not text or len(text) < 3:
                    continue
                seen_urls.add(href)
                links.append(el)
                if len(links) >= limit:
                    break
            except Exception:
                continue
        return links
    except Exception as e:
        logger.warning("_find_internal_links error: %s", e)
        return []


def simulate_internal_clicks(sb, target_domain: str) -> int:
    """
    Click 1-2 internal links and read each one.
    Simulates both same-tab navigation (with back button) and multi-tab browsing (Ctrl+Click / new tab).
    Returns number of clicks.
    """
    links = _find_internal_links(sb, target_domain)
    if not links:
        logger.info("No internal links found")
        return 0

    n_clicks = random.randint(1, min(2, len(links)))
    chosen = random.sample(links, n_clicks)
    clicked = 0

    original_url = sb.get_current_url()

    for link in chosen:
        try:
            text = link.text.strip()[:60]
            href = link.get_attribute("href") or ""
            use_new_tab = random.random() < 0.40

            if use_new_tab and href:
                logger.info("Opening internal link in NEW TAB: %s", text)
                sb.execute_script("window.open(arguments[0], '_blank');", href)
                time.sleep(1.5)
                # Switch to new tab
                windows = sb.driver.window_handles
                if len(windows) > 1:
                    sb.switch_to_window(1)
                    sb.wait_for_ready_state_complete(timeout=15)
                    random_pause(4, 10)
                    random_mouse_jitter(sb, duration_s=random.uniform(1, 2.5))

                    scroll_steps = random.randint(3, 5)
                    for _ in range(scroll_steps):
                        human_scroll(sb, random.randint(200, 400))
                        random_pause(2, 6)

                    # Close new tab and return to main
                    sb.driver.close()
                    sb.switch_to_window(0)
                    time.sleep(1.0)
                    clicked += 1
                    continue

            logger.info("Clicking internal link (same tab): %s", text)
            human_click_element(sb, link)
            sb.wait_for_ready_state_complete(timeout=15)
            random_pause(1.5, 3.0)

            # Read the internal article
            random_pause(5, 12)
            random_mouse_jitter(sb, duration_s=random.uniform(1, 3))

            scroll_steps = random.randint(3, 6)
            for _ in range(scroll_steps):
                human_scroll(sb, random.randint(200, 400))
                random_pause(3, 8)

            if random.random() < 0.4:
                human_scroll(sb, -random.randint(150, 300))
                random_pause(2, 5)

            random_pause(3, 8)
            clicked += 1

            sb.go_back()
            try:
                sb.wait_for_ready_state_complete(timeout=15)
            except Exception:
                pass
            random_pause(1, 2)

        except Exception as e:
            logger.warning("Internal click failed: %s", e)
            # Ensure we're on the main tab and page
            try:
                if len(sb.driver.window_handles) > 1:
                    sb.switch_to_window(0)
                if domain not in sb.get_current_url():
                    sb.open(original_url)
            except Exception:
                pass

    logger.info("Internal clicks done: %d", clicked)
    return clicked
