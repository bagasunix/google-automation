"""
captcha/token_solver.py
=======================
Token injection solver for Google /sorry/ reCAPTCHA v2 pages.

Flow:
  1. Extract sitekey + page URL from the /sorry/ page
  2. Submit task to capsolver or 2captcha API
  3. Poll until token returned (timeout ~120s)
  4. Inject token into g-recaptcha-response hidden textarea
  5. Trigger grecaptcha callback or click Continue button
  6. Verify redirect back to Google SERP

Supports:
  captcha.token_solver: "capsolver" | "2captcha"
  captcha.token_solver_key: "<your api key>"
"""

from __future__ import annotations

import logging
import os
import time

import requests

logger = logging.getLogger("worker.captcha.token_solver")

CAPSOLVER_CREATE_URL = "https://api.capsolver.com/createTask"
CAPSOLVER_RESULT_URL = "https://api.capsolver.com/getTaskResult"

TWOCAPTCHA_SUBMIT_URL = "https://2captcha.com/in.php"
TWOCAPTCHA_RESULT_URL = "https://2captcha.com/res.php"

# Google sorry page sitekey (stable, but we also read it dynamically)
GOOGLE_SORRY_SITEKEY_FALLBACK = "6Le-wvkSAAAAAPBMRTvw0Q4Muexq9bi0DJwx_mJ-"

POLL_INTERVAL = 5   # seconds between polls
MAX_WAIT = 120      # seconds total wait


def _load_config() -> dict:
    try:
        import yaml
        path = os.path.expanduser("~/Project/google-automation/config/config.yaml")
        with open(path) as f:
            return yaml.safe_load(f).get("captcha", {})
    except Exception:
        return {}


def _get_sitekey(sb) -> str:
    """Read sitekey from the live page; fall back to known Google sorry sitekey."""
    try:
        sitekey = sb.execute_script(
            "const el = document.querySelector('[data-sitekey]');"
            "return el ? el.getAttribute('data-sitekey') : null;"
        )
        if sitekey:
            logger.info("sitekey from page: %s", sitekey)
            return sitekey
    except Exception as e:
        logger.debug("sitekey read failed: %s", e)
    logger.info("using fallback sitekey: %s", GOOGLE_SORRY_SITEKEY_FALLBACK)
    return GOOGLE_SORRY_SITEKEY_FALLBACK


def _inject_token(sb, token: str) -> bool:
    """
    Inject reCAPTCHA v2 token and submit the sorry form.

    Three strategies tried in order:
      1. Set g-recaptcha-response + fire grecaptcha callback
      2. Set g-recaptcha-response + click the Continue button
      3. Submit the form directly
    """
    try:
        injected = sb.execute_script(f"""
            (function() {{
                // Set all g-recaptcha-response textareas (there may be more than one)
                document.querySelectorAll('textarea[name="g-recaptcha-response"]').forEach(el => {{
                    el.innerHTML = '{token}';
                    el.value = '{token}';
                }});

                // Try grecaptcha callback
                try {{
                    const cfg = window.___grecaptcha_cfg;
                    if (cfg && cfg.clients) {{
                        for (const key of Object.keys(cfg.clients)) {{
                            const client = cfg.clients[key];
                            const findCallback = (obj, depth) => {{
                                if (depth > 5 || !obj || typeof obj !== 'object') return;
                                if (typeof obj.callback === 'function') {{
                                    obj.callback('{token}');
                                    return true;
                                }}
                                for (const k of Object.keys(obj)) {{
                                    if (findCallback(obj[k], depth + 1)) return true;
                                }}
                            }};
                            if (findCallback(client, 0)) return 'callback';
                        }}
                    }}
                }} catch(e) {{}}

                // Click Continue button
                const btns = document.querySelectorAll('input[type=submit], button[type=submit], #recaptcha-demo-submit');
                for (const btn of btns) {{
                    btn.click();
                    return 'button';
                }}

                // Submit the form directly
                const form = document.querySelector('form');
                if (form) {{ form.submit(); return 'form'; }}

                return 'injected_only';
            }})()
        """)
        logger.info("token injection result: %s", injected)
        return True
    except Exception as e:
        logger.error("token injection failed: %s", e)
        return False


# ---------------------------------------------------------------------------
# Capsolver backend
# ---------------------------------------------------------------------------

def _solve_capsolver(api_key: str, page_url: str, sitekey: str) -> str | None:
    payload = {
        "clientKey": api_key,
        "task": {
            "type": "ReCaptchaV2TaskProxyLess",
            "websiteURL": page_url,
            "websiteKey": sitekey,
        },
    }
    try:
        resp = requests.post(CAPSOLVER_CREATE_URL, json=payload, timeout=15)
        data = resp.json()
    except Exception as e:
        logger.error("capsolver createTask error: %s", e)
        return None

    if data.get("errorId", 0) != 0:
        logger.error("capsolver createTask failed: %s", data.get("errorDescription"))
        return None

    task_id = data.get("taskId")
    if not task_id:
        logger.error("capsolver: no taskId in response")
        return None

    logger.info("capsolver task created: %s", task_id)

    deadline = time.time() + MAX_WAIT
    while time.time() < deadline:
        time.sleep(POLL_INTERVAL)
        try:
            poll_resp = requests.post(
                CAPSOLVER_RESULT_URL,
                json={"clientKey": api_key, "taskId": task_id},
                timeout=15,
            )
            poll = poll_resp.json()
        except Exception as e:
            logger.warning("capsolver poll error: %s", e)
            continue

        status = poll.get("status", "")
        if status == "ready":
            token = poll.get("solution", {}).get("gRecaptchaResponse")
            if token:
                logger.info("capsolver token received (%d chars)", len(token))
                return token
            logger.error("capsolver ready but no token: %s", poll)
            return None
        if status == "processing":
            logger.debug("capsolver: processing...")
            continue
        logger.error("capsolver unexpected status: %s — %s", status, poll)
        return None

    logger.error("capsolver timed out after %ds", MAX_WAIT)
    return None


# ---------------------------------------------------------------------------
# 2captcha backend
# ---------------------------------------------------------------------------

def _solve_2captcha(api_key: str, page_url: str, sitekey: str) -> str | None:
    try:
        resp = requests.post(
            TWOCAPTCHA_SUBMIT_URL,
            data={
                "key": api_key,
                "method": "userrecaptcha",
                "googlekey": sitekey,
                "pageurl": page_url,
                "json": 1,
            },
            timeout=15,
        )
        data = resp.json()
    except Exception as e:
        logger.error("2captcha submit error: %s", e)
        return None

    if data.get("status") != 1:
        logger.error("2captcha submit failed: %s", data.get("request"))
        return None

    task_id = data.get("request")
    logger.info("2captcha task created: %s", task_id)

    deadline = time.time() + MAX_WAIT
    time.sleep(15)  # 2captcha needs ~15s before first poll
    while time.time() < deadline:
        try:
            poll_resp = requests.get(
                TWOCAPTCHA_RESULT_URL,
                params={"key": api_key, "action": "get", "id": task_id, "json": 1},
                timeout=15,
            )
            poll = poll_resp.json()
        except Exception as e:
            logger.warning("2captcha poll error: %s", e)
            time.sleep(POLL_INTERVAL)
            continue

        if poll.get("status") == 1:
            token = poll.get("request")
            if token and token != "CAPCHA_NOT_READY":
                logger.info("2captcha token received (%d chars)", len(token))
                return token
        elif poll.get("request") == "CAPCHA_NOT_READY":
            logger.debug("2captcha: not ready yet")
        else:
            logger.error("2captcha error: %s", poll)
            return None

        time.sleep(POLL_INTERVAL)

    logger.error("2captcha timed out after %ds", MAX_WAIT)
    return None


# ---------------------------------------------------------------------------
# Public entry point
# ---------------------------------------------------------------------------

def solve_sorry_page(sb, max_attempts: int = 2) -> bool:
    """
    Solve the Google /sorry/ reCAPTCHA and wait for redirect to SERP.

    Returns True if we're no longer on a /sorry/ page after solving.
    """
    cfg = _load_config()
    backend = cfg.get("token_solver", "capsolver")
    # .env is the single source of truth for secrets — TOKEN_SOLVER_KEY was
    # documented in config.yaml's comments but never actually read; check it
    # first, falling back to config.yaml for anyone still setting it there.
    api_key = os.environ.get("TOKEN_SOLVER_KEY") or cfg.get("token_solver_key", "")

    if not api_key:
        logger.error(
            "TOKEN_SOLVER_KEY not set in .env (or token_solver_key in config.yaml) — cannot solve /sorry/ page"
        )
        return False

    for attempt in range(1, max_attempts + 1):
        logger.info("solve_sorry_page attempt %d/%d (backend=%s)", attempt, max_attempts, backend)

        try:
            page_url = sb.get_current_url()
        except Exception:
            page_url = "https://www.google.com/sorry/index"

        sitekey = _get_sitekey(sb)

        if backend == "capsolver":
            token = _solve_capsolver(api_key, page_url, sitekey)
        elif backend == "2captcha":
            token = _solve_2captcha(api_key, page_url, sitekey)
        else:
            logger.error("unknown token_solver backend: %s", backend)
            return False

        if not token:
            logger.warning("no token returned on attempt %d", attempt)
            continue

        ok = _inject_token(sb, token)
        if not ok:
            continue

        # Wait up to 8s for redirect away from /sorry/
        for _ in range(8):
            time.sleep(1)
            try:
                current = sb.get_current_url()
                if "/sorry/" not in current:
                    logger.info("left /sorry/ page — now at: %s", current)
                    return True
            except Exception:
                pass

        logger.warning("still on /sorry/ after token injection (attempt %d)", attempt)

    logger.error("solve_sorry_page failed after %d attempts", max_attempts)
    return False
