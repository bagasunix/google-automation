import json
import random
import time
import logging
from selenium.webdriver.common.keys import Keys

logger = logging.getLogger("worker.browser.humanizer")


def random_pause(min_s: float = 1.0, max_s: float = 3.0) -> None:
    time.sleep(random.uniform(min_s, max_s))


ADJACENT_KEYS = {
    'a': ['s', 'q', 'z'], 'b': ['v', 'g', 'h', 'n'], 'c': ['x', 'd', 'v'],
    'd': ['s', 'e', 'f', 'c', 'x'], 'e': ['w', 'r', 'd', 's'],
    'f': ['d', 'r', 't', 'g', 'v', 'c'], 'g': ['f', 't', 'y', 'h', 'b', 'v'],
    'h': ['g', 'y', 'u', 'j', 'n', 'b'], 'i': ['u', 'o', 'k', 'j'],
    'j': ['h', 'u', 'i', 'k', 'm', 'n'], 'k': ['j', 'i', 'o', 'l', 'm'],
    'l': ['k', 'o', 'p'], 'm': ['n', 'j', 'k'], 'n': ['b', 'h', 'j', 'm'],
    'o': ['i', 'p', 'l', 'k'], 'p': ['o', 'l'], 'q': ['w', 'a'],
    'r': ['e', 't', 'f', 'd'], 's': ['a', 'w', 'e', 'd', 'x', 'z'],
    't': ['r', 'y', 'g', 'f'], 'u': ['y', 'i', 'j', 'h'],
    'v': ['c', 'f', 'g', 'b'], 'w': ['q', 'e', 's', 'a'],
    'x': ['z', 's', 'd', 'c'], 'y': ['t', 'u', 'h', 'g'],
    'z': ['a', 's', 'x'],
}


def type_humanized(sb, selector: str, text: str, typo_chance: float = 0.04) -> None:
    logger.info("Human typing into '%s': %s", selector, text[:60])

    sb.click(selector)
    time.sleep(random.uniform(0.2, 0.4))

    try:
        sb.clear(selector)
    except Exception:
        pass
    time.sleep(random.uniform(0.1, 0.25))

    for char in text:
        # Simulate occasional human typo
        if typo_chance > 0 and char.lower() in ADJACENT_KEYS and random.random() < typo_chance:
            wrong_char = random.choice(ADJACENT_KEYS[char.lower()])
            if char.isupper():
                wrong_char = wrong_char.upper()
            try:
                sb.add_text(selector, wrong_char)
                time.sleep(random.uniform(0.12, 0.25))
                # NOTE: sending Keys.BACKSPACE (U+E003) here does not act as
                # a real backspace under add_text's CDP-based typing — it
                # gets inserted as a literal Private Use Area character
                # instead, corrupting the query (e.g. "Koneksi" -> garbled
                # "Konej[U+E003]ksi") and looking exactly like bot traffic to
                # Google's CAPTCHA detection. Trim the field's value directly
                # instead. execute_script() drops extra args under CDP mode
                # (arguments[0] would be undefined), so the selector is
                # embedded directly rather than passed as an argument.
                sb.execute_script(
                    "var el = document.querySelector(%s); if (el) { "
                    "el.value = el.value.slice(0, -1); "
                    "el.dispatchEvent(new Event('input', {bubbles: true})); }"
                    % json.dumps(selector)
                )
                time.sleep(random.uniform(0.08, 0.15))
            except Exception:
                pass

        try:
            sb.add_text(selector, char)
        except Exception:
            try:
                sb.send_keys(selector, char)
            except Exception:
                sb.type(selector, char)

        # Realistic typing speed variation (40ms - 150ms per keystroke)
        time.sleep(random.uniform(0.04, 0.14))
        if char in (' ', ',', '.', '-', ':', '/'):
            time.sleep(random.uniform(0.1, 0.3))


def press_enter_humanized(sb, selector: str = None) -> None:
    time.sleep(random.uniform(0.3, 0.8))
    # NOTE: sending Keys.RETURN (U+E006) via send_keys() has the exact same
    # problem as Keys.BACKSPACE in type_humanized() — under add_text's
    # CDP-based typing it does not act as a real Enter keypress. It silently
    # "succeeds" (no exception), appending a literal Private Use Area
    # character to the query AND never actually submitting the search — the
    # trailing tofu-box character users see, and a likely cause of searches
    # that silently never submit. Dispatch a real Enter key event via JS
    # instead, which is what already worked reliably as the old fallback.
    if selector:
        try:
            js_selector = json.dumps(selector)
            sb.execute_script(
                "var el = document.querySelector(%s); if (el) { "
                "el.value = el.value.replace(/[\\uE000-\\uF8FF]/g, ''); "
                "el.dispatchEvent(new KeyboardEvent('keydown', {key:'Enter', keyCode:13, bubbles:true})); "
                "el.dispatchEvent(new KeyboardEvent('keyup', {key:'Enter', keyCode:13, bubbles:true})); "
                "if (el.form) el.form.submit(); }" % js_selector
            )
            return
        except Exception:
            pass
    sb.execute_script(
        "if(document.activeElement) document.activeElement.dispatchEvent("
        "new KeyboardEvent('keydown',{key:'Enter',keyCode:13,bubbles:true}));"
        "if(document.activeElement && document.activeElement.form)"
        " document.activeElement.form.submit();"
    )


def human_scroll(sb, distance: int = None) -> int:
    if distance is None:
        distance = random.randint(500, 2000)

    total = 0
    sign = 1 if distance >= 0 else -1
    target = abs(distance)

    while total < target:
        chunk = random.randint(200, 500)
        sb.execute_script(f"window.scrollBy(0, {sign * chunk})")
        total += chunk
        time.sleep(random.uniform(0.3, 1.5))
        if sign > 0 and random.random() < 0.10:
            back = random.randint(50, 150)
            sb.execute_script(f"window.scrollBy(0, -{back})")
            total = max(0, total - back)
            time.sleep(random.uniform(0.5, 1.5))

    return total


def smooth_scroll_to(sb, selector: str) -> None:
    # execute_script() drops extra positional args under CDP mode (uc=True)
    # — arguments[0] is undefined there — so values are embedded directly
    # into the script string (json.dumps for safe JS string escaping)
    # instead of passed as args. Same fix as type_humanized's backspace bug.
    try:
        sb.execute_script(
            "const el = document.querySelector(%s); "
            "if (el) el.scrollIntoView({behavior:'smooth', block:'center'});"
            % json.dumps(selector)
        )
        time.sleep(random.uniform(0.5, 1.2))
    except Exception as e:
        logger.debug("smooth_scroll_to error: %s", e)


def mouse_bezier(sb, target_x: float, target_y: float) -> None:
    try:
        sb.execute_script(
            "document.dispatchEvent(new MouseEvent('mousemove', "
            "{clientX: %d, clientY: %d, bubbles: true}));"
            % (int(target_x), int(target_y))
        )
    except Exception:
        pass
    time.sleep(random.uniform(0.05, 0.15))


def human_click_element(sb, element) -> bool:
    # A native WebElement.click() already scrolls the element into view and
    # is a genuine WebDriver click action — harder for a site to distinguish
    # from a real click than a JS-dispatched one, and doesn't depend on
    # execute_script's arguments[0] (unavailable under CDP mode / uc=True).
    try:
        element.click()
        return True
    except Exception:
        return False


def random_mouse_jitter(sb, duration_s: float = 1.0) -> None:
    steps = max(2, int(duration_s * 3))
    try:
        for _ in range(steps):
            x = random.randint(100, 800)
            y = random.randint(100, 600)
            sb.execute_script(
                "document.dispatchEvent(new MouseEvent('mousemove', "
                "{clientX: %d, clientY: %d, bubbles: true}));" % (x, y)
            )
            time.sleep(duration_s / steps)
    except Exception:
        time.sleep(duration_s)


def get_scroll_depth_percent(sb) -> int:
    try:
        # IIFE with no leading `return` — sb.execute_script() under CDP mode
        # only strips `return` from the script's LAST line; the earlier
        # `if (maxScroll <= 0) return 100;` was left as illegal raw JS,
        # throwing a SyntaxError caught below and always returning 0 —
        # regardless of how much the page had actually scrolled.
        result = sb.execute_script("""
            (function() {
                const scrollTop = window.scrollY || document.documentElement.scrollTop;
                const scrollHeight = document.documentElement.scrollHeight;
                const clientHeight = document.documentElement.clientHeight;
                const maxScroll = scrollHeight - clientHeight;
                if (maxScroll <= 0) return 100;
                return Math.min(100, Math.round((scrollTop / maxScroll) * 100));
            })();
        """)
        return int(result)
    except Exception:
        return 0
