"""
captcha/solver.py
=================
CAPTCHA handling via SeleniumBase UC.

SB UC mode can:
  - uc_gui_click_captcha(): click the reCAPTCHA checkbox via GUI (OS-level click,
    bypasses JS detection, works in headed and most headless setups)
  - uc_gui_handle_captcha(): full challenge handler (checkbox + challenge)

Audio transcription fallback kept for cases where uc_gui fails.
"""

from __future__ import annotations

import logging
import time

logger = logging.getLogger("worker.captcha.solver")


def detect_recaptcha_version(sb) -> str:
    """
    Detect which reCAPTCHA version is present on the current page.

    Returns:
      "v2"     — visible checkbox/challenge iframe (solvable)
      "v3"     — invisible score-based token (NOT solvable; prevention only)
      "none"   — no reCAPTCHA detected
    """
    try:
        result = sb.execute_script("""
            // v2 anchor iframe is the definitive signal for a solvable challenge
            const v2Anchor = document.querySelector(
                'iframe[src*="recaptcha"][src*="anchor"]'
            );
            if (v2Anchor) return "v2";

            // v3 leaves no visible iframe; it injects a badge or calls grecaptcha.execute
            const badge = document.querySelector('.grecaptcha-badge');
            if (badge) return "v3";

            // Check for v3 execute calls in inline scripts
            const scripts = Array.from(document.querySelectorAll('script'));
            for (const s of scripts) {
                if (s.textContent && s.textContent.includes('grecaptcha.execute')) {
                    return "v3";
                }
            }

            // v2 checkbox widget (non-iframe embed)
            const checkbox = document.querySelector(
                '.g-recaptcha, .recaptcha-checkbox'
            );
            if (checkbox) return "v2";

            return "none";
        """)
        return result or "none"
    except Exception as e:
        logger.debug("detect_recaptcha_version error: %s", e)
        return "none"


def solve_sorry_captcha(sb, max_attempts: int = 2) -> bool:
    """
    Handle Google /sorry/ reCAPTCHA.

    Detects version first:
      v3 — score-based, invisible, NO solver can help. Returns False immediately
           so the caller can rotate proxy/session rather than waste attempts.
      v2 — tries audio challenge then token solver fallback.
    """
    version = detect_recaptcha_version(sb)
    if version == "v3":
        logger.error(
            "reCAPTCHA v3 detected — score-based challenge, no solver can help. "
            "Rotate proxy or reduce request rate."
        )
        return False

    from captcha.audio_sorry import solve_sorry_audio
    if solve_sorry_audio(sb, max_attempts=max_attempts):
        return True

    logger.warning("audio solve failed — trying token solver fallback")
    from captcha.token_solver import solve_sorry_page
    return solve_sorry_page(sb, max_attempts=max_attempts)


def solve_captcha(sb, max_attempts: int = 3) -> bool:
    """
    Attempt to solve a reCAPTCHA.

    In headed mode: uses SB UC GUI click (most reliable).
    In headless mode: uses JS click on the checkbox iframe.
    Returns True if solved, False otherwise.
    """
    for attempt in range(1, max_attempts + 1):
        logger.info("CAPTCHA solve attempt %d/%d", attempt, max_attempts)

        # Try headed GUI approach first (works when display is available)
        try:
            sb.uc_gui_click_captcha()
            time.sleep(2)
            if _is_solved(sb):
                logger.info("CAPTCHA solved via uc_gui_click on attempt %d", attempt)
                return True
        except Exception as e:
            logger.debug("uc_gui_click_captcha failed: %s", e)

        # JS-based fallback: click the checkbox inside the reCAPTCHA iframe
        try:
            clicked = sb.execute_script("""
                const iframes = document.querySelectorAll('iframe[src*="recaptcha"][src*="anchor"]');
                for (const f of iframes) {
                    try {
                        const cb = f.contentDocument.querySelector('.recaptcha-checkbox, #recaptcha-anchor');
                        if (cb) { cb.click(); return true; }
                    } catch(e) {}
                }
                return false;
            """)
            if clicked:
                time.sleep(2)
                if _is_solved(sb):
                    logger.info("CAPTCHA solved via JS click on attempt %d", attempt)
                    return True
        except Exception as e:
            logger.debug("JS captcha click failed: %s", e)

        time.sleep(random_backoff(attempt))

    logger.error("CAPTCHA not solved after %d attempts", max_attempts)
    return False


def _is_solved(sb) -> bool:
    """Check if reCAPTCHA checkbox is marked as checked."""
    try:
        result = sb.execute_script("""
            const frames = document.querySelectorAll('iframe[src*="recaptcha"]');
            for (const f of frames) {
                try {
                    const cb = f.contentDocument.querySelector('.recaptcha-checkbox');
                    if (cb && cb.getAttribute('aria-checked') === 'true') return true;
                } catch (e) {}
            }
            return false;
        """)
        return bool(result)
    except Exception:
        return False


def random_backoff(attempt: int) -> float:
    import random
    return random.uniform(2, 5) * attempt
