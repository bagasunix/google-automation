"""
captcha/audio_sorry.py
======================
Solve Google /sorry/ reCAPTCHA v2 via audio challenge — free, no API key.

Flow (SeleniumBase sync):
  1. Click checkbox inside anchor iframe
  2. Switch to bframe challenge iframe
  3. Click audio button
  4. Download MP3 audio (requests direct, fallback browser fetch)
  5. Transcribe via captcha/audio.py (Groq/Whisper/google_web — reads config)
  6. Type answer into audio-response field + verify
  7. Check redirect away from /sorry/
"""

from __future__ import annotations

import base64
import json
import logging
import random
import time

import requests as req

from captcha.audio import transcribe_audio

logger = logging.getLogger("worker.captcha.audio_sorry")

ANCHOR_IFRAME = 'iframe[src*="recaptcha"][src*="anchor"]'
BFRAME_IFRAME  = 'iframe[src*="recaptcha"][src*="bframe"]'
UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

# Signatures of the browser/chromedriver process having died mid-flow (seen
# repeatedly on this host, likely RAM pressure while rendering the
# iframe-heavy reCAPTCHA widget) rather than the audio/checkbox element
# genuinely being absent. When this happens every retry attempt reuses the
# same (dead) `sb` session and fails identically — logged as "could not
# click audio button", which reads like the button was never there, when
# actually the whole browser tab is gone.
_DEAD_SESSION_SIGNATURES = (
    "connection refused",
    "max retries exceeded",
    "invalid session id",
    "chrome not reachable",
    "session deleted because of page crash",
    "target window already closed",
)


class SessionDeadError(Exception):
    """Raised when the browser/WebDriver session itself is gone — further
    retries against the same `sb` object are pointless."""


def _check_session_alive(exc: Exception) -> None:
    msg = str(exc).lower()
    if any(sig in msg for sig in _DEAD_SESSION_SIGNATURES):
        raise SessionDeadError(f"browser session appears dead: {exc}") from exc


def _is_doscaptcha(sb) -> bool:
    """Check if Google has blocked the audio challenge due to automated queries detection."""
    try:
        # This ALSO always reported "no doscaptcha block" via the classic
        # (non-CDP) Selenium path — a bare IIFE call with no leading
        # `return` returns None to Python there, not just under the CDP
        # mode this comment originally called out. Explicitly returning the
        # IIFE's result on its own final line works under both dispatch
        # paths (see solver.py's detect_recaptcha_version for the full
        # explanation).
        result = sb.execute_script("""
            var __result = (function() {
                const msg = document.querySelector('.rc-doscaptcha-body, .rc-doscaptcha-header, div[class*="doscaptcha"]');
                if (msg && msg.innerText && msg.innerText.length > 0) return true;
                const text = document.body ? document.body.innerText : '';
                return text.includes("automated queries") || text.includes("can't process your request");
            })();
            return __result;
        """)
        return bool(result)
    except Exception as e:
        _check_session_alive(e)
        return False


def _click_checkbox(sb) -> bool:
    try:
        sb.switch_to_default_content()
        sb.wait_for_element_visible(ANCHOR_IFRAME, timeout=10)
        sb.switch_to_frame(ANCHOR_IFRAME)
        
        # Simulate human straight-line cursor movement (tarik garis lurus)
        sb.execute_script("""
            const el = document.querySelector('.recaptcha-checkbox, #recaptcha-anchor');
            if (!el) return;
            
            const rect = el.getBoundingClientRect();
            const targetX = rect.x + rect.width / 2;
            const targetY = rect.y + rect.height / 2;
            
            // Start from somewhere on the left side
            let startX = 0;
            let startY = rect.y;
            
            const steps = 25;
            const dx = (targetX - startX) / steps;
            const dy = (targetY - startY) / steps;
            
            let i = 0;
            function step() {
                if (i > steps) return;
                startX += dx;
                startY += dy;
                
                // Add a very slight random offset to simulate hand micro-tremor, but still straight
                const wobbleX = (Math.random() - 0.5) * 1.5;
                const wobbleY = (Math.random() - 0.5) * 1.5;
                
                document.dispatchEvent(new MouseEvent('mousemove', {
                    clientX: startX + wobbleX,
                    clientY: startY + wobbleY,
                    bubbles: true,
                    cancelable: true
                }));
                
                i++;
                // Random interval between 15ms and 35ms per step
                setTimeout(step, 15 + Math.random() * 20);
            }
            step();
        """)
        
        time.sleep(1.0) # Wait for mouse movement animation to finish
        sb.click(".recaptcha-checkbox, #recaptcha-anchor")
        sb.switch_to_default_content()
        time.sleep(2.5) # Wait for visual challenge to pop up
        return True
    except Exception as e:
        _check_session_alive(e)
        logger.exception("checkbox click failed: %s", e)
        try:
            sb.switch_to_default_content()
        except Exception:
            pass
        return False


def _click_audio_button(sb) -> bool:
    try:
        sb.switch_to_default_content()
        sb.switch_to_frame(BFRAME_IFRAME)
        time.sleep(1)

        # Check if Google already showed DosCaptcha block before clicking audio
        if _is_doscaptcha(sb):
            logger.warning("Google DosCaptcha block active — audio challenge withheld")
            return False

        for sel in ["#recaptcha-audio-button", "button#recaptcha-audio-button", "button[title*='audio' i]", "button[aria-label*='audio' i]"]:
            try:
                if sb.is_element_visible(sel):
                    sb.click(sel)
                    time.sleep(2.5)
                    return True
            except Exception as sel_e:
                _check_session_alive(sel_e)
    except Exception as e:
        _check_session_alive(e)
        logger.debug("audio button click failed: %s", e)
    return False


def _get_audio_src(sb) -> str | None:
    try:
        # This had FIVE early returns before the final `return null;` — a
        # prior fix wrapped them in an IIFE to make those internal returns
        # legal under CDP mode's execute_script(), but the IIFE call itself
        # had no leading `return`, which fixed the CDP-mode path while
        # leaving the classic (non-CDP, default/common) Selenium path
        # completely broken the whole time: that path needs a literal
        # top-level `return` or nothing comes back to Python at all — a bare
        # IIFE call silently returns None there, not an error. So this
        # function has still been returning None on every call under normal
        # operation (no driver disconnect) — the audio challenge URL was
        # never actually found regardless of whether Google served one.
        # Assigning the IIFE's result to a var and returning it on its own
        # final line satisfies both dispatch paths (CDP strips "return "
        # from that line; classic Selenium keeps and needs it).
        return sb.execute_script("""
            var __result = (function() {
                // 1. Audio tag src
                const a = document.querySelector('audio');
                if (a && a.src && a.src.startsWith('http')) return a.src;

                // 2. Audio source tag src
                const s = document.querySelector('audio source');
                if (s && s.src && s.src.startsWith('http')) return s.src;

                // 3. Modern reCAPTCHA download link
                const dl = document.querySelector('a.rc-audiochallenge-tdownload-link, a.rc-audiochallenge-download-link, a[href*="/recaptcha/api2/payload"], a[href*="payload?p="]');
                if (dl && dl.href && dl.href.startsWith('http')) return dl.href;

                // 4. Fallback search across all links in bframe
                const allLinks = document.querySelectorAll('a[href]');
                for (const l of allLinks) {
                    if (l.href && l.href.includes('recaptcha') && l.href.includes('payload')) {
                        return l.href;
                    }
                }
                return null;
            })();
            return __result;
        """)
    except Exception as e:
        _check_session_alive(e)
        logger.debug("audio src read failed: %s", e)
        return None


def _download_audio(sb, audio_src: str) -> bytes | None:
    # Try direct requests download first — reCAPTCHA audio URLs are signed.
    # MUST go through the same proxy as the browser session (session.py
    # stashes it on sb.proxies) — without it, this request reaches Google's
    # audio CDN from this machine's own direct IP while the active browsing
    # session (and the CAPTCHA challenge itself) is on a completely
    # different IP with no shared cookies, which is exactly the kind of
    # identity mismatch automated-traffic detection looks for.
    try:
        resp = req.get(
            audio_src,
            headers={"User-Agent": getattr(sb, "real_user_agent", None) or UA},
            proxies=getattr(sb, "proxies", None),
            timeout=15,
        )
        if resp.status_code == 200 and len(resp.content) > 500:
            logger.info("audio downloaded via direct requests: %d bytes", len(resp.content))
            return resp.content
        logger.warning(
            "direct requests download rejected: status=%s bytes=%d proxied=%s",
            resp.status_code, len(resp.content), bool(getattr(sb, "proxies", None)),
        )
    except Exception as e:
        logger.warning("direct requests download failed: %s", e)

    # Fallback: download through browser (respects session/cookies/proxy).
    # NOTE: sb.execute_async_script(script, timeout=None) does not support
    # extra positional args at all — passing audio_src as a second arg was
    # silently landing in the `timeout` parameter instead (a URL string
    # where a numeric duration was expected), breaking this fallback
    # entirely. Embed the URL directly into the script instead.
    try:
        b64 = sb.execute_async_script(
            "const url = %s;"
            "const done = arguments[arguments.length - 1];"
            "fetch(url)"
            "    .then(r => r.arrayBuffer())"
            "    .then(buf => {"
            "        const bytes = new Uint8Array(buf);"
            "        let bin = '';"
            "        bytes.forEach(b => bin += String.fromCharCode(b));"
            "        done(btoa(bin));"
            "    })"
            "    .catch(() => done(null));"
            % json.dumps(audio_src)
        )
        if b64:
            data = base64.b64decode(b64)
            logger.info("audio downloaded via browser fetch: %d bytes", len(data))
            return data
    except Exception as e:
        _check_session_alive(e)
        logger.debug("browser fetch download failed: %s", e)

    return None


def _submit_answer(sb, answer: str) -> bool:
    try:
        # Type into audio response field
        for sel in ["#audio-response", "input#audio-response", "input[id*='audio-response']"]:
            try:
                if sb.is_element_visible(sel):
                    sb.type(sel, answer)
                    time.sleep(0.5)
                    break
            except Exception as sel_e:
                _check_session_alive(sel_e)

        # Click verify button
        for btn in ["#recaptcha-verify-button", "button#recaptcha-verify-button", "button[id*='verify']"]:
            try:
                if sb.is_element_visible(btn):
                    sb.click(btn)
                    time.sleep(3)
                    return True
            except Exception as btn_e:
                _check_session_alive(btn_e)
    except Exception as e:
        _check_session_alive(e)
        logger.debug("submit answer failed: %s", e)
    return False


def solve_sorry_audio(sb, max_attempts: int = 3) -> bool:
    """
    Attempt to solve Google /sorry/ reCAPTCHA via free audio challenge.
    Returns True if page redirects away from /sorry/.
    """
    for attempt in range(1, max_attempts + 1):
        logger.info("audio_sorry attempt %d/%d", attempt, max_attempts)

        try:
            sb.switch_to_default_content()
        except Exception:
            pass

        try:
            if not _click_checkbox(sb):
                logger.warning("could not click checkbox on attempt %d", attempt)
                continue

            # Already solved (simple checkbox pass)
            try:
                if "/sorry/" not in sb.get_current_url():
                    logger.info("solved via checkbox click alone")
                    return True
            except Exception:
                pass

            if not _click_audio_button(sb):
                logger.warning("could not click audio button or DosCaptcha active on attempt %d", attempt)
                try:
                    sb.switch_to_default_content()
                except Exception:
                    pass
                continue

            # Check for DosCaptcha block after audio click
            if _is_doscaptcha(sb):
                logger.warning("Google blocked audio challenge for this IP (DosCaptcha 'automated queries') — skipping audio attempts")
                try:
                    sb.switch_to_default_content()
                except Exception:
                    pass
                return False

            audio_src = _get_audio_src(sb)
            if not audio_src:
                logger.warning("no audio src found on attempt %d", attempt)
                try:
                    sb.switch_to_default_content()
                except Exception:
                    pass
                continue

            logger.info("found audio challenge URL: %s", audio_src[:90])

            audio_bytes = _download_audio(sb, audio_src)
            if not audio_bytes:
                logger.warning("audio download failed on attempt %d", attempt)
                try:
                    sb.switch_to_default_content()
                except Exception:
                    pass
                continue

            answer = transcribe_audio(audio_bytes)
            if not answer:
                logger.warning("transcription returned empty on attempt %d", attempt)
                try:
                    sb.switch_to_default_content()
                except Exception:
                    pass
                continue

            logger.info("submitting transcription: '%s'", answer)

            if not _submit_answer(sb, answer):
                logger.warning("submit answer failed on attempt %d", attempt)
                try:
                    sb.switch_to_default_content()
                except Exception:
                    pass
                continue
        except SessionDeadError as e:
            # Every remaining attempt would reuse this same dead session and
            # fail identically — stop now instead of burning max_attempts on
            # a browser tab that's already gone (this previously looked
            # exactly like "the audio button just isn't there").
            logger.error("aborting audio_sorry — %s", e)
            return False

        try:
            sb.switch_to_default_content()
        except Exception:
            pass

        # Wait up to 8s for redirect away from /sorry/
        for _ in range(8):
            time.sleep(1)
            try:
                if "/sorry/" not in sb.get_current_url():
                    logger.info("successfully solved /sorry/ captcha via audio challenge!")
                    return True
            except Exception:
                pass

        logger.warning("still on /sorry/ after attempt %d", attempt)

        # A human who fails an audio challenge pauses before retrying —
        # this loop was retrying in the same second, which is itself a
        # bot signal on top of whatever caused the miss.
        if attempt < max_attempts:
            pause = random.uniform(3.0, 7.0)
            logger.info("pausing %.1fs before retry (human-like)", pause)
            time.sleep(pause)

    logger.error("solve_sorry_audio failed after %d attempts", max_attempts)
    return False
