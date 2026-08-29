"""
engagement/exit.py
==================
Exit strategy after engagement (sync).
"""

from __future__ import annotations

import logging
import random
import time

from browser.humanizer import random_pause, random_mouse_jitter

logger = logging.getLogger("worker.engagement.exit")

DISTRACTION_SITES = [
    "https://www.youtube.com",
    "https://www.reddit.com",
    "https://twitter.com",
    "https://news.ycombinator.com",
    "https://github.com",
    "https://www.wikipedia.org",
]


def exit_article(sb, domain: str, distraction_exit_chance: float = 0.0) -> None:
    """Pick and execute an exit strategy."""
    r = random.random()

    # Distraction exit: navigate to random unrelated site
    if distraction_exit_chance > 0 and random.random() < distraction_exit_chance:
        site = random.choice(DISTRACTION_SITES)
        logger.info("Exit strategy: distraction → %s", site)
        random_mouse_jitter(sb, duration_s=random.uniform(0.5, 1.5))
        try:
            sb.open(site)
        except Exception as e:
            logger.warning("Distraction navigation failed: %s", e)
        return

    if r < 0.30:
        # Navigate to a new tab / blank (simulate close)
        logger.info("Exit strategy: navigate away (simulate close)")
        random_mouse_jitter(sb, duration_s=random.uniform(0.5, 1.5))
        try:
            sb.open("about:blank")
        except Exception:
            pass

    elif r < 0.70:
        # Go back to search engine
        logger.info("Exit strategy: go back to search engine")
        random_mouse_jitter(sb, duration_s=random.uniform(0.5, 1.5))
        try:
            sb.go_back()
            time.sleep(random.uniform(1, 2))
        except Exception:
            pass

    elif r < 0.90:
        # Navigate to domain homepage
        homepage = f"https://{domain}"
        logger.info("Exit strategy: navigate to homepage %s", homepage)
        random_mouse_jitter(sb, duration_s=random.uniform(0.5, 1.5))
        try:
            sb.open(homepage)
            time.sleep(random.uniform(3, 8))
        except Exception as e:
            logger.warning("Homepage navigation failed: %s", e)

    else:
        # Navigate to distraction site
        site = random.choice(DISTRACTION_SITES)
        logger.info("Exit strategy: distraction → %s", site)
        random_mouse_jitter(sb, duration_s=random.uniform(0.5, 1.5))
        try:
            sb.open(site)
        except Exception as e:
            logger.warning("Distraction navigation failed: %s", e)


def post_exit_cooldown(min_s: int = 30, max_s: int = 120) -> None:
    delay = random.randint(min_s, max_s)
    logger.info("Post-exit cooldown: %ds", delay)
    time.sleep(delay)
