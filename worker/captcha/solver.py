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
        # NOTE: sb.execute_script() has two totally different failure modes
        # depending on driver state (see shared_utils.is_cdp_swap_needed):
        #   - CDP mode (driver disconnected): only strips a `return` keyword
        #     from the LAST line of the script — any earlier `return` (e.g.
        #     inside an `if` for early-exit) is left as illegal raw JS,
        #     throwing a silently-swallowed SyntaxError.
        #   - classic Selenium (driver connected — the common case): runs
        #     the text as a function body verbatim, so it needs a literal
        #     top-level `return` or nothing comes back to Python at all
        #     (confirmed live: a bare IIFE call with no leading `return`
        #     silently returns None here, not an error — this function
        #     always hit that and returned "none" regardless of the real
        #     state).
        # An IIFE with all its internal early-returns, assigned to a var,
        # followed by a final `return __result;` line satisfies both: the
        # CDP path strips "return " from that last line and evaluates the
        # rest as a bare expression; the classic path keeps it and needs it.
        result = sb.execute_script("""
            var __result = (function() {
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
            })();
            return __result;
        """)
        return result or "none"
    except Exception as e:
        logger.debug("detect_recaptcha_version error: %s", e)
        return "none"


def _left_sorry_page(sb) -> bool:
    """A /sorry/ challenge counts as solved only once Google navigates away."""
    try:
        return "/sorry/" not in sb.get_current_url()
    except Exception:
        return False


def _try_gui_click_sorry(sb, attempts: int = 2) -> bool:
    """
    Solve the /sorry/ checkbox with undetected-chromedriver's native OS-level
    click, without ever requesting the audio challenge.

    Returns True only if the page actually leaves /sorry/.
    """
    for attempt in range(1, attempts + 1):
        try:
            sb.switch_to_default_content()
        except Exception:
            pass

        try:
            sb.uc_gui_click_captcha()
        except Exception as e:
            # No display (headless) or PyAutoGUI unavailable — nothing more
            # this approach can do, so stop retrying and let the caller fall
            # through to the audio path.
            logger.info("GUI captcha click unavailable (%s) — falling back", e)
            return False

        # reCAPTCHA verifies asynchronously, then Google redirects off
        # /sorry/. Poll instead of guessing a single sleep duration.
        for _ in range(10):
            time.sleep(1)
            if _left_sorry_page(sb):
                logger.info(
                    "/sorry/ solved via native GUI click on attempt %d "
                    "(audio challenge never requested)", attempt
                )
                return True

        logger.warning("GUI click attempt %d did not clear /sorry/", attempt)

    return False


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

    # Try the native GUI click BEFORE anything audio-related.
    #
    # Requesting the audio challenge is what provokes Google's DosCaptcha
    # ("automated queries") block — it is the best-known automation bypass,
    # so Google withholds it aggressively from any IP it distrusts. Once
    # that block fires, every remaining attempt is dead. This path used to
    # go straight from version-detection into solve_sorry_audio(), which
    # clicks the audio button almost immediately, so the single least
    # detectable tool available — undetected-chromedriver's OS-level mouse
    # click, which produces a genuine isTrusted=true event that a JS/Selenium
    # click cannot fake — was never tried on the /sorry/ page at all (it was
    # only wired into solve_captcha(), a different path for embedded
    # reCAPTCHA that production rarely hits).
    #
    # Requires a real display: headless Chrome has no cursor for PyAutoGUI to
    # move, so this raises there and we fall through to the old audio path
    # exactly as before — no regression, just an extra chance to finish at
    # the checkbox and never ask for audio.
    if _try_gui_click_sorry(sb):
        return True

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
                var __result = (function() {
                    const iframes = document.querySelectorAll('iframe[src*="recaptcha"][src*="anchor"]');
                    for (const f of iframes) {
                        try {
                            const cb = f.contentDocument.querySelector('.recaptcha-checkbox, #recaptcha-anchor');
                            if (cb) { cb.click(); return true; }
                        } catch(e) {}
                    }
                    return false;
                })();
                return __result;
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
            var __result = (function() {
                const frames = document.querySelectorAll('iframe[src*="recaptcha"]');
                for (const f of frames) {
                    try {
                        const cb = f.contentDocument.querySelector('.recaptcha-checkbox');
                        if (cb && cb.getAttribute('aria-checked') === 'true') return true;
                    } catch (e) {}
                }
                return false;
            })();
            return __result;
        """)
        return bool(result)
    except Exception:
        return False


def random_backoff(attempt: int) -> float:
    import random
    return random.uniform(2, 5) * attempt
