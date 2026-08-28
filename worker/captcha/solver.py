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
                frame = element.content_frame()
                if frame:
                    await frame.wait_for_load_state("domcontentloaded", timeout=10000)
                    return frame
        except Exception:
            continue
    return None


async def _switch_to_audio(page, bframe) -> bool:
    """Click the audio button in the bframe to switch from image to audio challenge."""
    for sel in AUDIO_BUTTON_SELECTORS:
        try:
            btn = await bframe.query_selector(sel)
            if btn:
                await human_click_element(page, btn)
                await asyncio.sleep(2)
                # Verify audio element appeared
                has_audio = await bframe.evaluate("""
                    () => document.querySelector('audio') !== null
                """)
                if has_audio:
                    logger.info("Switched to audio challenge")
                    return True
        except Exception as e:
            logger.debug("Audio button selector '%s' failed: %s", sel, e)
            continue

    has_audio = await bframe.evaluate("() => document.querySelector('audio') !== null")
    if has_audio:
        logger.info("Audio challenge already active")
        return True
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
