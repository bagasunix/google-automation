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
import logging
import time

import requests as req

from captcha.audio import transcribe_audio

logger = logging.getLogger("worker.captcha.audio_sorry")

ANCHOR_IFRAME = 'iframe[src*="recaptcha"][src*="anchor"]'
BFRAME_IFRAME  = 'iframe[src*="recaptcha"][src*="bframe"]'
UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"


def _is_doscaptcha(sb) -> bool:
    """Check if Google has blocked the audio challenge due to automated queries detection."""
    try:
        return sb.execute_script("""
            const msg = document.querySelector('.rc-doscaptcha-body, .rc-doscaptcha-header, div[class*="doscaptcha"]');
            if (msg && msg.innerText && msg.innerText.length > 0) return true;
            const text = document.body ? document.body.innerText : '';
            return text.includes("automated queries") || text.includes("can't process your request");
        """)
    except Exception:
        return False


def _click_checkbox(sb) -> bool:
    try:
        sb.switch_to_default_content()
        sb.switch_to_frame(ANCHOR_IFRAME)
        sb.click(".recaptcha-checkbox, #recaptcha-anchor")
        sb.switch_to_default_content()
        time.sleep(2)
        return True
    except Exception as e:
        logger.debug("checkbox click failed: %s", e)
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
            except Exception:
                pass
    except Exception as e:
        logger.debug("audio button click failed: %s", e)
    return False


def _get_audio_src(sb) -> str | None:
    try:
        return sb.execute_script("""
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
        """)
    except Exception as e:
        logger.debug("audio src read failed: %s", e)
        return None


def _download_audio(sb, audio_src: str) -> bytes | None:
    # Try direct requests download first — reCAPTCHA audio URLs are signed
    try:
        resp = req.get(audio_src, headers={"User-Agent": UA}, timeout=15)
        if resp.status_code == 200 and len(resp.content) > 500:
            logger.info("audio downloaded via direct requests: %d bytes", len(resp.content))
            return resp.content
    except Exception as e:
        logger.debug("direct requests download failed: %s", e)

    # Fallback: download through browser (respects session/cookies/proxy)
    try:
        b64 = sb.execute_async_script("""
            const [url, done] = arguments;
            fetch(url)
                .then(r => r.arrayBuffer())
                .then(buf => {
                    const bytes = new Uint8Array(buf);
                    let bin = '';
                    bytes.forEach(b => bin += String.fromCharCode(b));
                    done(btoa(bin));
                })
                .catch(() => done(null));
        """, audio_src)
        if b64:
            data = base64.b64decode(b64)
            logger.info("audio downloaded via browser fetch: %d bytes", len(data))
            return data
    except Exception as e:
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
            except Exception:
                pass

        # Click verify button
        for btn in ["#recaptcha-verify-button", "button#recaptcha-verify-button", "button[id*='verify']"]:
            try:
                if sb.is_element_visible(btn):
                    sb.click(btn)
                    time.sleep(3)
                    return True
            except Exception:
                pass
    except Exception as e:
        logger.debug("submit answer failed: %s", e)
    return False


def solve_sorry_audio(sb, max_attempts: int = 3) -> bool:
    """
    Attempt to solve Google /sorry/ reCAPTCHA via free audio challenge.
    Returns True if page redirects away from /sorry/.
    """
    for attempt in range(1, max_attempts + 1):
        logger.info("audio_sorry attempt %d/%d", attempt, max_attempts)

        sb.switch_to_default_content()

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
            sb.switch_to_default_content()
            continue

        # Check for DosCaptcha block after audio click
        if _is_doscaptcha(sb):
            logger.warning("Google blocked audio challenge for this IP (DosCaptcha 'automated queries') — skipping audio attempts")
            sb.switch_to_default_content()
            return False

        audio_src = _get_audio_src(sb)
        if not audio_src:
            logger.warning("no audio src found on attempt %d", attempt)
            sb.switch_to_default_content()
            continue

        logger.info("found audio challenge URL: %s", audio_src[:90])

        audio_bytes = _download_audio(sb, audio_src)
        if not audio_bytes:
            logger.warning("audio download failed on attempt %d", attempt)
            sb.switch_to_default_content()
            continue

        answer = transcribe_audio(audio_bytes)
        if not answer:
            logger.warning("transcription returned empty on attempt %d", attempt)
            sb.switch_to_default_content()
            continue

        logger.info("submitting transcription: '%s'", answer)

        if not _submit_answer(sb, answer):
            logger.warning("submit answer failed on attempt %d", attempt)
            sb.switch_to_default_content()
            continue

        sb.switch_to_default_content()

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

    logger.error("solve_sorry_audio failed after %d attempts", max_attempts)
    return False
