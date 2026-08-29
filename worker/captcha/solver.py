"""
captcha/solver.py
=================
reCAPTCHA audio challenge solver.

Flow:
  1. Find reCAPTCHA anchor iframe (checkbox)
  2. Click "I'm not a robot"
  3. Wait for challenge bframe to appear
  4. Switch to audio challenge (click audio button)
  5. Download + transcribe audio
  6. Type answer + verify
  7. Retry up to max_attempts if wrong

Limitations:
  - Only handles reCAPTCHA v2 (checkbox + challenge)
  - v3/invisible reCAPTCHA has no interactive challenge to solve
  - Google may rate-limit audio API if too many attempts
"""

from __future__ import annotations

import asyncio
import logging
from typing import Optional

from .audio import download_audio, transcribe_audio
from browser.humanizer import human_click_element, random_pause

logger = logging.getLogger("worker.captcha.solver")

ANCHOR_SELECTORS = [
    'iframe[src*="recaptcha"][src*="anchor"]',
    'iframe[title*="reCAPTCHA"]',
    'iframe[src*="recaptcha"]',
]

BFRAME_SELECTORS = [
    'iframe[src*="recaptcha"][src*="bframe"]',
    'iframe[title*="recaptcha challenge"]',
]

AUDIO_BUTTON_SELECTORS = [
    '#recaptcha-audio-button',
    '.rc-button-audio',
    'button[aria-label*="audio"]',
    'button[aria-label*="Audio"]',
    'button[aria-label="Get an audio challenge"]',
    'button[aria-label="get an audio challenge"]',
    'button[title*="audio"]',
    'button[title*="Audio"]',
    '.rc-audiochallenge-play-button',
    'button.rc-button-audio',
    '[id*="audio"]',
]

AUDIO_RESPONSE_INPUT_SELECTORS = [
    '#audio-response',
    'input[name="audio-response"]',
    'input[type="text"]',
]


async def solve_recaptcha(page, max_attempts: int = 3) -> bool:
    """
    Attempt to solve a reCAPTCHA v2 challenge via audio.

    Returns True if solved, False otherwise.
    """
    from browser.humanizer import random_pause, human_click_element

    for attempt in range(1, max_attempts + 1):
        logger.info("reCAPTCHA solve attempt %d/%d", attempt, max_attempts)
        try:
            result = await _try_solve(page)
            if result:
                logger.info("reCAPTCHA solved on attempt %d", attempt)
                return True
            await random_pause(2, 5)
        except Exception as e:
            logger.warning("Attempt %d failed: %s", attempt, e)
            await random_pause(2, 5)

    logger.error("reCAPTCHA not solved after %d attempts", max_attempts)
    return False


async def _try_solve(page) -> bool:
    """Single attempt at solving. Returns True if passed."""
    from browser.humanizer import human_click_element, random_pause

    # Wait for reCAPTCHA iframe to appear (sorry page loads it async)
    try:
        await page.wait_for_selector(
            'iframe[src*="recaptcha"]',
            timeout=15000,
            state="attached",
        )
        await asyncio.sleep(1)
    except Exception:
        logger.warning("reCAPTCHA iframe did not appear within 15s")

    # Step 1: Find and click the reCAPTCHA checkbox
    anchor_frame = await _find_frame(page, ANCHOR_SELECTORS)
    if not anchor_frame:
        logger.warning("No reCAPTCHA anchor iframe found")
        return False

    checkbox = await anchor_frame.query_selector('.recaptcha-checkbox, #recaptcha-anchor')
    if not checkbox:
        logger.warning("No reCAPTCHA checkbox found in anchor frame")
        return False

    await human_click_element(page, checkbox)
    await random_pause(2, 4)

    # Step 2: Check if we passed immediately (sometimes happens)
    is_checked = await anchor_frame.evaluate("""
        () => {
            const cb = document.querySelector('.recaptcha-checkbox');
            return cb ? cb.getAttribute('aria-checked') === 'true' : false;
        }
    """)
    if is_checked:
        logger.info("reCAPTCHA passed without challenge")
        return True

    # Step 3: Find the challenge bframe
    bframe = await _find_frame(page, BFRAME_SELECTORS)
    if not bframe:
        logger.warning("No challenge bframe found — reCAPTCHA may have passed or failed silently")
        return is_checked

    # Step 4: Switch to audio challenge
    switched = await _switch_to_audio(page, bframe)
    if not switched:
        logger.warning("Could not switch to audio challenge")
        return False

    await random_pause(1, 3)

    # Step 5: Download + transcribe audio
    audio_bytes = await download_audio(bframe)
    if not audio_bytes:
        return False

    answer = transcribe_audio(audio_bytes)
    if not answer:
        logger.warning("No transcription result")
        return False

    # Step 6: Type answer into input
    input_el = await _find_input(bframe)
    if not input_el:
        logger.warning("No audio response input found")
        return False

    await human_click_element(page, input_el)
    await asyncio.sleep(0.5)
    await input_el.fill(answer)
    await random_pause(0.5, 1.5)

    # Step 7: Click verify
    verify_clicked = await _click_verify(page, bframe)
    if not verify_clicked:
        logger.warning("Could not click verify button — trying Enter")
        await page.keyboard.press("Enter")

    await random_pause(3, 6)

    # Step 8: Check result
    passed = await _check_solved(page, anchor_frame)
    return passed


async def _find_frame(page, selectors: list[str]):
    """Find a frame matching any of the selectors."""
    for sel in selectors:
        try:
            element = await page.query_selector(sel)
            if element:
                frame = await element.content_frame()
                if frame:
                    await frame.wait_for_load_state("domcontentloaded", timeout=10000)
                    return frame
        except Exception:
            continue

    # JS fallback: find any iframe with recaptcha in src
    try:
        iframes = await page.evaluate("""
            () => Array.from(document.querySelectorAll('iframe')).map(f => ({
                src: f.src, title: f.title, id: f.id, name: f.name
            }))
        """)
        logger.warning("All iframe selectors failed. Iframes on page: %s", iframes)

        # Try each iframe directly
        for iframe_info in iframes:
            src = iframe_info.get("src", "")
            if "recaptcha" in src:
                el = await page.query_selector(f'iframe[src="{src}"]')
                if not el:
                    # partial match
                    all_iframes = await page.query_selector_all("iframe")
                    for el in all_iframes:
                        s = await el.get_attribute("src") or ""
                        if "recaptcha" in s:
                            frame = await el.content_frame()
                            if frame:
                                await frame.wait_for_load_state("domcontentloaded", timeout=10000)
                                logger.info("Found recaptcha iframe via JS scan: %s", s[:80])
                                return frame
    except Exception as e:
        logger.debug("JS iframe scan failed: %s", e)

    return None


async def _switch_to_audio(page, bframe) -> bool:
    """Click the audio button in the bframe to switch from image to audio challenge."""
    # Check if audio challenge already active
    has_audio = await bframe.evaluate("() => document.querySelector('audio') !== null")
    if has_audio:
        logger.info("Audio challenge already active")
        return True

    # Wait for audio button to become enabled (starts disabled while image challenge loads)
    try:
        await bframe.wait_for_function(
            """() => {
                const btn = document.querySelector('#recaptcha-audio-button');
                return btn && !btn.classList.contains('rc-button-disabled');
            }""",
            timeout=10000,
        )
        logger.info("Audio button is now enabled")
    except Exception:
        logger.debug("Timed out waiting for audio button to enable — trying anyway")

    # Try CSS selectors
    for sel in AUDIO_BUTTON_SELECTORS:
        try:
            btn = await bframe.query_selector(sel)
            if btn:
                # Skip disabled buttons
                class_attr = await btn.get_attribute("class") or ""
                if "disabled" in class_attr:
                    logger.debug("Selector '%s' found but button is disabled, skipping", sel)
                    continue
                # Use btn.click() directly — human_click_element uses main page coords
                # which misses elements inside iframes
                await btn.click()
                await asyncio.sleep(2)
                has_audio = await bframe.evaluate("() => document.querySelector('audio') !== null")
                if has_audio:
                    logger.info("Switched to audio challenge via selector: %s", sel)
                    return True
        except Exception as e:
            logger.debug("Audio button selector '%s' failed: %s", sel, e)
            continue

    # JS fallback: find any non-disabled button that looks like audio switcher
    try:
        clicked = await bframe.evaluate("""
            () => {
                const buttons = Array.from(document.querySelectorAll('button'));
                for (const btn of buttons) {
                    if (btn.classList.contains('rc-button-disabled')) continue;
                    const label = (btn.getAttribute('aria-label') || btn.textContent || btn.title || '').toLowerCase();
                    const id = (btn.id || '').toLowerCase();
                    if (label.includes('audio') || id.includes('audio')) {
                        btn.click();
                        return true;
                    }
                }
                const el = document.querySelector('[id*="audio"]:not(.rc-button-disabled)');
                if (el) { el.click(); return true; }
                return false;
            }
        """)
        if clicked:
            await asyncio.sleep(2)
            has_audio = await bframe.evaluate("() => document.querySelector('audio') !== null")
            if has_audio:
                logger.info("Switched to audio challenge via JS fallback")
                return True
    except Exception as e:
        logger.debug("JS audio button fallback failed: %s", e)

    # Log all buttons for debugging
    try:
        btns = await bframe.evaluate("""
            () => Array.from(document.querySelectorAll('button')).map(b => ({
                id: b.id, class: b.className, label: b.getAttribute('aria-label'), text: b.textContent.trim().substring(0, 40)
            }))
        """)
        logger.warning("Audio button not found. Buttons in bframe: %s", btns)
    except Exception:
        pass

    return False


async def _find_input(bframe):
    """Find the audio response text input."""
    for sel in AUDIO_RESPONSE_INPUT_SELECTORS:
        try:
            el = await bframe.query_selector(sel)
            if el and await el.is_visible():
                return el
        except Exception:
            continue
    return None


async def _click_verify(page, bframe) -> bool:
    """Click the verify/submit button."""
    verify_selectors = [
        '#recaptcha-verify-button',
        'button[type="submit"]',
        'button[aria-label*="Verify"]',
        'button[aria-label*="verify"]',
        '.rc-button-submit',
    ]
    for sel in verify_selectors:
        try:
            btn = await bframe.query_selector(sel)
            if btn and await btn.is_visible():
                await human_click_element(page, btn)
                logger.info("Clicked verify button: %s", sel)
                return True
        except Exception:
            continue
    return False


async def _check_solved(page, anchor_frame) -> bool:
    """Check if the reCAPTCHA is now solved (checkbox is green)."""
    try:
        is_checked = await anchor_frame.evaluate("""
            () => {
                const cb = document.querySelector('.recaptcha-checkbox');
                return cb ? cb.getAttribute('aria-checked') === 'true' : false;
            }
        """)
        return is_checked
    except Exception:
        return False


async def detect_and_solve_captcha(page, engine: str = "google", max_attempts: int = 3) -> bool:
    """
    Detect reCAPTCHA on the current page and attempt to solve it.

    Returns True if solved (or no CAPTCHA found), False if unsolvable.
    """
    from search.serp import detect_captcha

    has_captcha = await detect_captcha(page, engine)
    if not has_captcha:
        return True

    logger.info("reCAPTCHA detected — attempting audio solve")
    solved = await solve_recaptcha(page, max_attempts=max_attempts)
    if solved:
        logger.info("reCAPTCHA solved successfully")
    else:
        logger.error("reCAPTCHA could not be solved")
    return solved
