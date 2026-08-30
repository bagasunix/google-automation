"""
browser/stealth.py
==================
Anti-fingerprint configuration for the Playwright browser session.

Responsibilities:
  - Canvas fingerprint spoofing (inject noise into toDataURL / getImageData)
  - WebGL renderer/vendor randomization
  - Font list hardening (avoid leaking headless environment)
  - Timezone spoofing (match proxy geo if known, otherwise randomise)
  - User-Agent + viewport rotation (per session)
  - WebRTC leak prevention (disable RTCPeerConnection)
  - Navigator property patches (webdriver=false, plugins, languages, platform)
  - Screen resolution consistency with viewport
  - Cookie jar is handled per session in session.py (fresh context each time)

All stealth scripts are injected via page.add_init_script() so they run before
any page JavaScript executes.
"""

from __future__ import annotations

import random
import logging
from dataclasses import dataclass, field

logger = logging.getLogger("worker.browser.stealth")

# ---------------------------------------------------------------------------
# Rotation pools
# ---------------------------------------------------------------------------

# Realistic desktop User-Agents (Chrome/Edge/Firefox on Windows/Mac/Linux, recent versions)
USER_AGENTS = [
    # Chrome on Windows 10/11
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Windows NT 11.0; Win64; x64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
    # Chrome on macOS
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 13_6_1) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
    # Chrome on Linux
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
    # Edge on Windows
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0",
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0",
    # Firefox on Windows
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:132.0) Gecko/20100101 Firefox/132.0",
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:131.0) Gecko/20100101 Firefox/131.0",
    # Firefox on macOS
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:132.0) Gecko/20100101 Firefox/132.0",
    # Safari on macOS
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 "
    "(KHTML, like Gecko) Version/17.0 Safari/605.1.15",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 13_6) AppleWebKit/605.1.15 "
    "(KHTML, like Gecko) Version/16.6 Safari/605.1.15",
]

# Desktop Viewports
VIEWPORTS = [
    {"width": 1920, "height": 1080},
    {"width": 1536, "height": 864},
    {"width": 1440, "height": 900},
    {"width": 1366, "height": 768},
    {"width": 1280, "height": 720},
    {"width": 1680, "height": 1050},
]

# Mobile User-Agents (Android & iOS)
MOBILE_USER_AGENTS = [
    "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36",
    "Mozilla/5.0 (Linux; Android 14; Pixel 8 Pro) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36",
    "Mozilla/5.0 (Linux; Android 14; SM-S928B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36",
    "Mozilla/5.0 (Linux; Android 13; SM-G991B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36",
    "Mozilla/5.0 (Linux; Android 13; SM-A536B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36",
    "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
    "Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/131.0.6778.73 Mobile/15E148 Safari/604.1",
]

# Mobile Viewports
MOBILE_VIEWPORTS = [
    {"width": 393, "height": 852},   # iPhone 15 / 16
    {"width": 412, "height": 915},   # Pixel 8 / Galaxy S24
    {"width": 390, "height": 844},   # iPhone 13 / 14
    {"width": 360, "height": 800},   # Galaxy A series
    {"width": 430, "height": 932},   # iPhone 15 Pro Max
]

# Timezone candidates (pick one that loosely matches proxy geo or random)
TIMEZONES = [
    "America/New_York",
    "America/Chicago",
    "America/Los_Angeles",
    "America/Denver",
    "Europe/London",
    "Europe/Berlin",
    "Europe/Amsterdam",
    "Asia/Jakarta",
    "Asia/Singapore",
    "Asia/Tokyo",
]

# Locale candidates
LOCALES = ["en-US", "en-GB", "en-CA", "en-AU"]

# Country → IANA timezone mapping (for proxy geo consistency).
# Webshare returns country_code (ISO 3166-1 alpha-2).
_COUNTRY_TIMEZONES = {
    "GB": "Europe/London",
    "US": "America/New_York",
    "ES": "Europe/Madrid",
    "DE": "Europe/Berlin",
    "FR": "Europe/Paris",
    "NL": "Europe/Amsterdam",
    "PL": "Europe/Warsaw",
    "JP": "Asia/Tokyo",
    "SG": "Asia/Singapore",
    "ID": "Asia/Jakarta",
    "AU": "Australia/Sydney",
    "CA": "America/Toronto",
    "BR": "America/Sao_Paulo",
    "IN": "Asia/Kolkata",
    "RU": "Europe/Moscow",
    "KR": "Asia/Seoul",
    "IT": "Europe/Rome",
    "SE": "Europe/Stockholm",
    "CH": "Europe/Zurich",
    "IE": "Europe/Dublin",
    "NO": "Europe/Oslo",
    "DK": "Europe/Copenhagen",
    "FI": "Europe/Helsinki",
    "PT": "Europe/Lisbon",
    "CZ": "Europe/Prague",
    "AT": "Europe/Vienna",
    "BE": "Europe/Brussels",
    "MX": "America/Mexico_City",
    "AR": "America/Argentina/Buenos_Aires",
    "CL": "America/Santiago",
    "ZA": "Africa/Johannesburg",
    "TR": "Europe/Istanbul",
    "AE": "Asia/Dubai",
    "TH": "Asia/Bangkok",
    "VN": "Asia/Ho_Chi_Minh",
    "PH": "Asia/Manila",
    "MY": "Asia/Kuala_Lumpur",
    "HK": "Asia/Hong_Kong",
    "TW": "Asia/Taipei",
}

# Country → locale mapping.
_COUNTRY_LOCALES = {
    "GB": "en-GB",
    "US": "en-US",
    "ES": "es-ES",
    "DE": "de-DE",
    "FR": "fr-FR",
    "NL": "nl-NL",
    "PL": "pl-PL",
    "JP": "ja-JP",
    "SG": "en-SG",
    "ID": "id-ID",
    "AU": "en-AU",
    "CA": "en-CA",
    "BR": "pt-BR",
    "IN": "en-IN",
    "RU": "ru-RU",
    "KR": "ko-KR",
    "IT": "it-IT",
    "SE": "sv-SE",
    "CH": "de-CH",
    "IE": "en-IE",
    "NO": "nb-NO",
    "DK": "da-DK",
    "FI": "fi-FI",
    "PT": "pt-PT",
    "CZ": "cs-CZ",
    "AT": "de-AT",
    "BE": "nl-BE",
    "MX": "es-MX",
    "AR": "es-AR",
    "CL": "es-CL",
    "ZA": "en-ZA",
    "TR": "tr-TR",
    "AE": "ar-AE",
    "TH": "th-TH",
    "VN": "vi-VN",
    "PH": "en-PH",
    "MY": "ms-MY",
    "HK": "zh-HK",
    "TW": "zh-TW",
}


def _pick_ua_for_locale(locale: str) -> str:
    """
    Pick a realistic User-Agent for the given locale.
    US/GB/AU/CA → mostly Windows Chrome, some Edge, occasional Firefox.
    DE/NL → more Linux Chrome (technical users).
    JP → more Mac Chrome/Safari.
    Fallback: random from the full pool.
    """
    en_windows_uas = [ua for ua in USER_AGENTS
                      if "Windows" in ua and "Chrome" in ua and "Edg" not in ua]
    en_edge_uas = [ua for ua in USER_AGENTS if "Edg" in ua]
    linux_uas = [ua for ua in USER_AGENTS if "Linux" in ua]
    mac_uas = [ua for ua in USER_AGENTS if "Macintosh" in ua]
    firefox_uas = [ua for ua in USER_AGENTS if "Firefox" in ua]

    if locale.startswith("de") or locale.startswith("nl"):
        # DE/NL: 50% Windows Chrome, 25% Linux Chrome, 25% Firefox Windows
        r = random.random()
        if r < 0.50 and en_windows_uas:
            return random.choice(en_windows_uas)
        if r < 0.75 and linux_uas:
            return random.choice(linux_uas)
        if firefox_uas:
            return random.choice(firefox_uas)
    elif locale.startswith("ja"):
        # JP: 40% Mac Chrome, 30% Windows Chrome, 20% Safari, 10% Firefox Mac
        r = random.random()
        if r < 0.40 and mac_uas:
            mac_chrome = [ua for ua in mac_uas if "Chrome" in ua and "Firefox" not in ua]
            if mac_chrome:
                return random.choice(mac_chrome)
        if r < 0.70 and en_windows_uas:
            return random.choice(en_windows_uas)
        if r < 0.90:
            safari = [ua for ua in mac_uas if "Safari" in ua and "Chrome" not in ua]
            if safari:
                return random.choice(safari)
    elif locale.startswith(("en-US", "en-GB", "en-AU", "en-CA", "en-SG", "en-IN", "en-IE", "en-ZA", "en-PH")):
        # English: 60% Windows Chrome, 20% Edge, 10% Firefox, 10% Mac Chrome
        r = random.random()
        if r < 0.60 and en_windows_uas:
            return random.choice(en_windows_uas)
        if r < 0.80 and en_edge_uas:
            return random.choice(en_edge_uas)
        if r < 0.90 and firefox_uas:
            return random.choice(firefox_uas)
        if mac_uas:
            mac_chrome = [ua for ua in mac_uas if "Chrome" in ua and "Firefox" not in ua]
            if mac_chrome:
                return random.choice(mac_chrome)

    return random.choice(USER_AGENTS)

# WebGL renderer / vendor pairs (common real GPUs), split by platform —
# these MUST be picked to match the platform/UA already chosen for the
# profile, not randomly: Direct3D11-backed ANGLE renderers only exist on
# Windows, Metal-backed ones only on Apple platforms, real phones report
# mobile GPU vendors (Adreno/Mali/Apple GPU), never a desktop gaming card.
# A profile claiming "Macintosh" in its UA but reporting a Direct3D11
# renderer (or a "phone" reporting an RTX 3060) is exactly the kind of
# cross-fingerprint mismatch automated-traffic detection is built to catch —
# confirmed live 2026-08-30 that the single flat list below was previously
# chosen with no regard for platform at all.
WEBGL_CONFIGS_WINDOWS = [
    {"vendor": "Google Inc. (NVIDIA)", "renderer": "ANGLE (NVIDIA, NVIDIA GeForce RTX 3060 Direct3D11 vs_5_0 ps_5_0)"},
    {"vendor": "Google Inc. (NVIDIA)", "renderer": "ANGLE (NVIDIA, NVIDIA GeForce GTX 1660 Ti Direct3D11 vs_5_0 ps_5_0)"},
    {"vendor": "Google Inc. (AMD)", "renderer": "ANGLE (AMD, AMD Radeon RX 580 Direct3D11 vs_5_0 ps_5_0)"},
    {"vendor": "Google Inc. (AMD)", "renderer": "ANGLE (AMD, AMD Radeon RX 6700 XT Direct3D11 vs_5_0 ps_5_0)"},
    {"vendor": "Google Inc. (Intel)", "renderer": "ANGLE (Intel, Intel(R) UHD Graphics 770 Direct3D11 vs_5_0 ps_5_0)"},
    {"vendor": "Google Inc. (Intel)", "renderer": "ANGLE (Intel, Intel(R) Iris(R) Xe Graphics Direct3D11 vs_5_0 ps_5_0)"},
]
WEBGL_CONFIGS_MAC = [
    {"vendor": "Google Inc. (Apple)", "renderer": "ANGLE (Apple, ANGLE Metal Renderer: Apple M1 Pro, Unspecified Version)"},
    {"vendor": "Google Inc. (Apple)", "renderer": "ANGLE (Apple, ANGLE Metal Renderer: Apple M2, Unspecified Version)"},
    {"vendor": "Google Inc. (Apple)", "renderer": "ANGLE (Apple, ANGLE Metal Renderer: Apple M3, Unspecified Version)"},
]
WEBGL_CONFIGS_LINUX = [
    {"vendor": "Google Inc. (Intel)", "renderer": "ANGLE (Intel, Mesa Intel(R) UHD Graphics 620 (KBL GT2), OpenGL 4.6)"},
    {"vendor": "Google Inc. (NVIDIA Corporation)", "renderer": "ANGLE (NVIDIA Corporation, NVIDIA GeForce GTX 1660/PCIe/SSE2, OpenGL 4.6.0 NVIDIA 535.129.03)"},
    {"vendor": "Google Inc. (AMD)", "renderer": "ANGLE (AMD, AMD Radeon RX 6600 (radeonsi, navi23, LLVM 15.0.7), OpenGL 4.6)"},
]
WEBGL_CONFIGS_ANDROID = [
    {"vendor": "Qualcomm", "renderer": "Adreno (TM) 640"},
    {"vendor": "Qualcomm", "renderer": "Adreno (TM) 730"},
    {"vendor": "ARM", "renderer": "Mali-G78 MP20"},
]
WEBGL_CONFIGS_IOS = [
    {"vendor": "Apple Inc.", "renderer": "Apple GPU"},
]

# Screen colour depths
COLOR_DEPTHS = [24, 30]

# Hardware concurrency values
HARDWARE_CONCURRENCIES = [4, 8, 8, 12, 16]

# Device pixel ratios
DEVICE_PIXEL_RATIOS = [1.0, 1.25, 1.5, 2.0]


# ---------------------------------------------------------------------------
# Stealth profile dataclass
# ---------------------------------------------------------------------------

@dataclass
class StealthProfile:
    """A randomised browser fingerprint profile for one session."""

    user_agent: str = ""
    viewport: dict = field(default_factory=dict)
    timezone: str = "America/New_York"
    locale: str = "en-US"
    webgl_vendor: str = ""
    webgl_renderer: str = ""
    color_depth: int = 24
    hardware_concurrency: int = 8
    device_pixel_ratio: float = 1.0
    platform: str = "Win32"
    is_mobile: bool = False
    max_touch_points: int = 0
    canvas_noise_seed: int = 1

    @classmethod
    def random(cls, is_mobile: bool | None = None) -> "StealthProfile":
        """Generate a fully random profile (no proxy geo — fallback)."""
        return cls.for_proxy(country="US", is_mobile=is_mobile)

    @classmethod
    def for_proxy(cls, country: str = "", timezone: str = "", is_mobile: bool | None = None) -> "StealthProfile":
        """
        Generate a profile consistent with the proxy's geography and device type.
        """
        tz = timezone or _COUNTRY_TIMEZONES.get(country.upper(), "") or random.choice(TIMEZONES)
        loc = _COUNTRY_LOCALES.get(country.upper(), "en-US")

        if is_mobile is None:
            # 40% chance of mobile simulation
            is_mobile = random.random() < 0.40

        if is_mobile:
            ua = random.choice(MOBILE_USER_AGENTS)
            vp = random.choice(MOBILE_VIEWPORTS)
            is_android = "Android" in ua
            platform = "Linux armv8l" if is_android else "iPhone"
            max_touch = 5
            dpr = random.choice([2.0, 3.0])
            hw_conc = random.choice([6, 8])
            webgl_pool = WEBGL_CONFIGS_ANDROID if is_android else WEBGL_CONFIGS_IOS
        else:
            ua = _pick_ua_for_locale(loc)
            vp = random.choice(VIEWPORTS)
            if "Windows" in ua:
                platform, webgl_pool = "Win32", WEBGL_CONFIGS_WINDOWS
            elif "Macintosh" in ua:
                platform, webgl_pool = "MacIntel", WEBGL_CONFIGS_MAC
            else:
                platform, webgl_pool = "Linux x86_64", WEBGL_CONFIGS_LINUX
            max_touch = 0
            dpr = random.choice(DEVICE_PIXEL_RATIOS)
            hw_conc = random.choice(HARDWARE_CONCURRENCIES)

        # Pick a WebGL vendor/renderer consistent with the platform already
        # chosen above — see the WEBGL_CONFIGS_* comment for why this can't
        # be a platform-independent random.choice() across the whole pool.
        webgl = random.choice(webgl_pool)
        color_depth = random.choice(COLOR_DEPTHS)

        return cls(
            user_agent=ua,
            viewport=vp,
            timezone=tz,
            locale=loc,
            webgl_vendor=webgl["vendor"],
            webgl_renderer=webgl["renderer"],
            color_depth=color_depth,
            hardware_concurrency=hw_conc,
            device_pixel_ratio=dpr,
            platform=platform,
            is_mobile=is_mobile,
            max_touch_points=max_touch,
            # 1..255: must be non-zero, or XOR-ing with it is a no-op and
            # the canvas noise injection silently does nothing at all.
            canvas_noise_seed=random.randint(1, 255),
        )


# ---------------------------------------------------------------------------
# Stealth init script generator
# ---------------------------------------------------------------------------

def build_stealth_script(profile: StealthProfile) -> str:
    """
    Build a single JavaScript string that patches the browser environment
    to resist fingerprinting. Injected via page.add_init_script().

    Patches applied:
      1. navigator.webdriver = false
      2. navigator.platform override
      3. navigator.hardwareConcurrency override
      4. navigator.deviceMemory override
      5. navigator.plugins / mimeTypes spoofing
      6. navigator.languages override
      7. Canvas noise injection (toDataURL, getImageData)
      8. WebGL vendor/renderer override (getParameter)
      9. WebRTC block (RTCPeerConnection disabled)
     10. screen.colorDepth override
     11. window.chrome existence
     12. Permissions API patch
    """
    # Build a JS array of language tags
    langs_js = json_array([profile.locale, "en"])

    script = f"""
// === STEALTH INIT SCRIPT ===
// Generated for profile: UA={profile.user_agent[:40]}...

const navProto = (typeof navigator !== 'undefined' && Object.getPrototypeOf(navigator)) || (typeof Navigator !== 'undefined' && Navigator.prototype) || {{}};

// 1. navigator.webdriver = false
try {{
    Object.defineProperty(navProto, 'webdriver', {{
        get: () => false,
        configurable: true,
    }});
    Object.defineProperty(navigator, 'webdriver', {{
        get: () => false,
        configurable: true,
    }});
}} catch (e) {{}}

// 2. navigator.platform
try {{
    Object.defineProperty(navProto, 'platform', {{
        get: () => '{profile.platform}',
        configurable: true,
    }});
    Object.defineProperty(navigator, 'platform', {{
        get: () => '{profile.platform}',
        configurable: true,
    }});
}} catch (e) {{}}

// 3. navigator.hardwareConcurrency
try {{
    Object.defineProperty(navProto, 'hardwareConcurrency', {{
        get: () => {profile.hardware_concurrency},
        configurable: true,
    }});
    Object.defineProperty(navigator, 'hardwareConcurrency', {{
        get: () => {profile.hardware_concurrency},
        configurable: true,
    }});
}} catch (e) {{}}

// 4. navigator.deviceMemory (common values: 4, 8)
try {{
    Object.defineProperty(navProto, 'deviceMemory', {{
        get: () => 8,
        configurable: true,
    }});
    Object.defineProperty(navigator, 'deviceMemory', {{
        get: () => 8,
        configurable: true,
    }});
}} catch (e) {{}}

// 4b. navigator.maxTouchPoints (mobile support)
try {{
    Object.defineProperty(navProto, 'maxTouchPoints', {{
        get: () => {profile.max_touch_points},
        configurable: true,
    }});
    Object.defineProperty(navigator, 'maxTouchPoints', {{
        get: () => {profile.max_touch_points},
        configurable: true,
    }});
}} catch (e) {{}}

// 5. navigator.plugins — spoof a realistic Chrome plugin list
try {{
    const fakePlugins = [
        {{
            name: 'PDF Viewer',
            filename: 'internal-pdf-viewer',
            description: 'Portable Document Format',
        }},
        {{
            name: 'Chrome PDF Viewer',
            filename: 'internal-pdf-viewer',
            description: 'Portable Document Format',
        }},
        {{
            name: 'Chromium PDF Viewer',
            filename: 'internal-pdf-viewer',
            description: 'Portable Document Format',
        }},
        {{
            name: 'Microsoft Edge PDF Viewer',
            filename: 'internal-pdf-viewer',
            description: 'Portable Document Format',
        }},
        {{
            name: 'WebKit built-in PDF',
            filename: 'internal-pdf-viewer',
            description: 'Portable Document Format',
        }},
    ];

    const pluginArray = Object.create(PluginArray.prototype);
    for (let i = 0; i < fakePlugins.length; i++) {{
        const p = Object.create(Plugin.prototype);
        Object.defineProperties(p, {{
            name: {{ value: fakePlugins[i].name }},
            filename: {{ value: fakePlugins[i].filename }},
            description: {{ value: fakePlugins[i].description }},
            length: {{ value: 1 }},
        }});
        pluginArray[i] = p;
    }}
    Object.defineProperty(pluginArray, 'length', {{ value: fakePlugins.length }});
    Object.defineProperty(navigator, 'plugins', {{
        get: () => pluginArray,
        configurable: true,
    }});
}} catch (e) {{}}

// 6. navigator.languages
try {{
    Object.defineProperty(navProto, 'languages', {{
        get: () => {langs_js},
        configurable: true,
    }});
    Object.defineProperty(navigator, 'languages', {{
        get: () => {langs_js},
        configurable: true,
    }});
}} catch (e) {{}}

// 7. Canvas noise injection
// Adds a tiny, per-session-random noise to canvas pixel data so the
// fingerprint changes without being visually noticeable — WITHOUT ever
// writing the noised data back onto the real canvas. The noise is derived
// fresh from the canvas's own unmodified pixel data on every call, so
// repeated toDataURL()/getImageData() calls on unchanged canvas content
// stay stable and consistent with each other, matching how a real (if
// slightly different) browser/GPU would behave.
//
// Two earlier, both-broken versions of this: (1) toDataURL() re-applied
// the SAME noise byte a second time on top of what its own call to the
// (already-patched) getImageData had just applied — since X^N^N == X,
// that silently cancelled the noise back to the exact original value for
// this, the single most common canvas-fingerprinting API. (2) fixing that
// by writing the once-noised copy back onto the real canvas via
// putImageData instead introduced something worse: since the SOURCE
// canvas itself was now mutated, a second toDataURL()/getImageData() call
// on that already-noised canvas applied the same XOR again, flipping it
// straight back to the original value — an OSCILLATING fingerprint across
// repeated reads of unchanged content, which is an even more obvious
// automation tell than a silently-inert patch, since real canvas
// rendering is always deterministic for unchanged content. Confirmed live
// 2026-08-30 (calling toDataURL() once after reading noised pixel data
// flipped the canvas's own buffer straight back to the pre-noise value).
// This version never calls putImageData() on the real canvas at all:
// getImageData() returns a noised COPY; toDataURL() draws a noised copy
// onto a disposable off-screen canvas and encodes THAT instead — the
// source canvas's actual pixel buffer is never touched either way.
try {{
    const NOISE = {profile.canvas_noise_seed};

    function _noisyCopy(original) {{
        const copy = new ImageData(
            new Uint8ClampedArray(original.data), original.width, original.height
        );
        for (let i = 0; i < copy.data.length; i += 4 * 100) {{
            copy.data[i] = copy.data[i] ^ NOISE;
        }}
        return copy;
    }}

    const originalGetImageData = CanvasRenderingContext2D.prototype.getImageData;
    CanvasRenderingContext2D.prototype.getImageData = function(...args) {{
        return _noisyCopy(originalGetImageData.apply(this, args));
    }};

    const originalToDataURL = HTMLCanvasElement.prototype.toDataURL;
    HTMLCanvasElement.prototype.toDataURL = function(...args) {{
        try {{
            const ctx = this.getContext('2d');
            if (ctx) {{
                const w = this.width;
                const h = this.height;
                // Read via the ORIGINAL getImageData (not the patched one
                // above) to avoid double-noising, then noise a copy and
                // draw it onto a throwaway canvas — the real canvas this
                // was called on is never mutated.
                const original = originalGetImageData.call(ctx, 0, 0, Math.max(w, 1), Math.max(h, 1));
                const noised = _noisyCopy(original);
                const tmp = document.createElement('canvas');
                tmp.width = w;
                tmp.height = h;
                tmp.getContext('2d').putImageData(noised, 0, 0);
                return originalToDataURL.apply(tmp, args);
            }}
        }} catch (e) {{}}
        return originalToDataURL.apply(this, args);
    }};
}} catch (e) {{}}

// 8. WebGL vendor/renderer override
try {{
    const webglVendor = '{profile.webgl_vendor}';
    const webglRenderer = '{profile.webgl_renderer}';

    const origGetExtension = WebGLRenderingContext.prototype.getExtension;
    WebGLRenderingContext.prototype.getExtension = function(name) {{
        if (name === 'WEBGL_debug_renderer_info') {{
            return {{
                UNMASKED_VENDOR_WEBGL: 37445,
                UNMASKED_RENDERER_WEBGL: 37446,
            }};
        }}
        return origGetExtension.call(this, name);
    }};

    const getParameterProto = WebGLRenderingContext.prototype.getParameter;
    WebGLRenderingContext.prototype.getParameter = function(param) {{
        // UNMASKED_VENDOR_WEBGL = 37445
        if (param === 37445) return webglVendor;
        // UNMASKED_RENDERER_WEBGL = 37446
        if (param === 37446) return webglRenderer;
        return getParameterProto.call(this, param);
    }};

    // Also patch WebGL2 if available
    if (typeof WebGL2RenderingContext !== 'undefined') {{
        const origGetExtension2 = WebGL2RenderingContext.prototype.getExtension;
        WebGL2RenderingContext.prototype.getExtension = function(name) {{
            if (name === 'WEBGL_debug_renderer_info') {{
                return {{
                    UNMASKED_VENDOR_WEBGL: 37445,
                    UNMASKED_RENDERER_WEBGL: 37446,
                }};
            }}
            return origGetExtension2.call(this, name);
        }};

        const getParameter2Proto = WebGL2RenderingContext.prototype.getParameter;
        WebGL2RenderingContext.prototype.getParameter = function(param) {{
            if (param === 37445) return webglVendor;
            if (param === 37446) return webglRenderer;
            return getParameter2Proto.call(this, param);
        }};
    }}

    // Fallback getContext mock when headless environment has no hardware/swiftshader GPU
    const origGetContext = HTMLCanvasElement.prototype.getContext;
    HTMLCanvasElement.prototype.getContext = function(type, ...args) {{
        const ctx = origGetContext.apply(this, [type, ...args]);
        if (!ctx && (type === 'webgl' || type === 'experimental-webgl' || type === 'webgl2')) {{
            return {{
                canvas: this,
                drawingBufferWidth: this.width || 300,
                drawingBufferHeight: this.height || 150,
                getParameter: function(param) {{
                    if (param === 37445) return webglVendor;
                    if (param === 37446) return webglRenderer;
                    if (param === 7936) return 'WebKit';
                    if (param === 7937) return 'WebKit WebGL';
                    if (param === 7938) return 'WebGL 1.0 (OpenGL ES 2.0 Chromium)';
                    return 0;
                }},
                getExtension: function(name) {{
                    if (name === 'WEBGL_debug_renderer_info') {{
                        return {{ UNMASKED_VENDOR_WEBGL: 37445, UNMASKED_RENDERER_WEBGL: 37446 }};
                    }}
                    return null;
                }},
                getSupportedExtensions: () => ['WEBGL_debug_renderer_info', 'EXT_texture_filter_anisotropic'],
                viewport: () => {{}},
                clearColor: () => {{}},
                clear: () => {{}},
                enable: () => {{}},
                disable: () => {{}},
            }};
        }}
        return ctx;
    }};
}} catch (e) {{}}
// 9. WebRTC IP leak protection & AudioContext anti-fingerprinting
try {{
    if (window.RTCPeerConnection) {{
        const origCreateOffer = RTCPeerConnection.prototype.createOffer;
        RTCPeerConnection.prototype.createOffer = function(...args) {{
            return origCreateOffer.apply(this, args).then(offer => {{
                if (offer && offer.sdp) {{
                    offer.sdp = offer.sdp.replace(/c=IN IP4 [0-9.]+/g, 'c=IN IP4 0.0.0.0');
                }}
                return offer;
            }});
        }};
    }}

    // AudioContext / AudioBuffer micro-noise injection
    if (window.AudioBuffer) {{
        const origGetChannelData = AudioBuffer.prototype.getChannelData;
        AudioBuffer.prototype.getChannelData = function(channel) {{
            const data = origGetChannelData.apply(this, arguments);
            for (let i = 0; i < data.length; i += 100) {{
                data[i] += 0.000001 * (Math.sin(i) - 0.5);
            }}
            return data;
        }};
    }}
    if (window.AnalyserNode) {{
        const origGetFloatFreq = AnalyserNode.prototype.getFloatFrequencyData;
        AnalyserNode.prototype.getFloatFrequencyData = function(array) {{
            origGetFloatFreq.apply(this, arguments);
            for (let i = 0; i < array.length; i += 50) {{
                array[i] += 0.0001 * Math.sin(i);
            }}
        }};
    }}
}} catch (e) {{}}

// 10. screen.colorDepth
try {{
    Object.defineProperty(screen, 'colorDepth', {{
        get: () => {profile.color_depth},
        configurable: true,
    }});
    Object.defineProperty(screen, 'pixelDepth', {{
        get: () => {profile.color_depth},
        configurable: true,
    }});
}} catch (e) {{}}

// 11. window.chrome — make it look like real Chrome
try {{
    if (!window.chrome) {{
        window.chrome = {{
            app: {{
                isInstalled: false,
                InstallState: {{ DISABLED: 'disabled', INSTALLED: 'installed', NOT_INSTALLED: 'not_installed' }},
                RunningState: {{ CANNOT_RUN: 'cannot_run', READY_TO_RUN: 'ready_to_run', RUNNING: 'running' }},
            }},
            runtime: {{
                OnInstalledReason: {{ CHROME_UPDATE: 'chrome_update', INSTALL: 'install', SHARED_MODULE_UPDATE: 'shared_module_update', UPDATE: 'update' }},
                PlatformArch: {{ ARM: 'arm', ARM64: 'arm64', MIPS: 'mips', MIPS64: 'mips64', X86_32: 'x86-32', X86_64: 'x86-64' }},
                PlatformOs: {{ ANDROID: 'android', CROS: 'cros', LINUX: 'linux', MAC: 'mac', OPENBSD: 'openbsd', WIN: 'win' }},
                RequestUpdateCheckStatus: {{ NO_UPDATE: 'no_update', THROTTLED: 'throttled', UPDATE_AVAILABLE: 'update_available' }},
            }},
        }};
    }}
}} catch (e) {{}}

// 12. Permissions API patch — makes navigator.permissions.query return 'denied' for 'notifications'
// in a way that's consistent with a real browser (headless Chrome returns 'prompt' which is a tell)
try {{
    const origQuery = navigator.permissions.query;
    navigator.permissions.query = function(param) {{
        if (param && param.name === 'notifications') {{
            return Promise.resolve({{ state: 'denied', onchange: null }});
        }}
        return origQuery.call(navigator.permissions, param);
    }};
}} catch (e) {{}}

// 13. navigator.connection (NetworkInformation) — add a realistic effectiveType
try {{
    if (!navigator.connection) {{
        Object.defineProperty(navigator, 'connection', {{
            get: () => ({{
                effectiveType: '4g',
                rtt: 50,
                downlink: 10,
                saveData: false,
            }}),
            configurable: true,
        }});
    }}
}} catch (e) {{}}

// 14. iframe contentWindow access — propagate stealth into iframes
try {{
    const originalContentWindow = Object.getOwnPropertyDescriptor(HTMLIFrameElement.prototype, 'contentWindow');
    if (originalContentWindow) {{
        Object.defineProperty(HTMLIFrameElement.prototype, 'contentWindow', {{
            get: function() {{
                const cw = originalContentWindow.get.call(this);
                if (cw) {{
                    try {{ Object.defineProperty(cw.navigator, 'webdriver', {{ get: () => false }}); }} catch (e) {{}}
                }}
                return cw;
            }},
            configurable: true,
        }});
    }}
}} catch (e) {{}}

// END STEALTH INIT SCRIPT
"""
    return script


def json_array(items: list[str]) -> str:
    """Build a JS array literal from Python strings."""
    import json
    return json.dumps(items)


def _build_client_hint_headers(profile: StealthProfile) -> dict:
    """
    Build Sec-CH-UA and Sec-Fetch-* headers consistent with the profile UA.
    Only Chrome and Edge send these; Firefox/Safari skip them entirely.
    """
    import re

    ua = profile.user_agent
    if "Chrome/" not in ua and "Edg/" not in ua:
        return {}

    chrome_match = re.search(r"Chrome/(\d+)", ua)
    if not chrome_match:
        return {}

    chrome_major = chrome_match.group(1)
    edge_match = re.search(r"Edg/(\d+)", ua)

    if edge_match:
        edge_major = edge_match.group(1)
        sec_ch_ua = (
            f'"Microsoft Edge";v="{edge_major}", '
            f'"Chromium";v="{chrome_major}", '
            f'"Not_A Brand";v="24"'
        )
    else:
        sec_ch_ua = (
            f'"Google Chrome";v="{chrome_major}", '
            f'"Chromium";v="{chrome_major}", '
            f'"Not_A Brand";v="24"'
        )

    platform_map = {"Win32": '"Windows"', "MacIntel": '"macOS"'}
    sec_ch_ua_platform = platform_map.get(profile.platform, '"Linux"')

    return {
        "sec-ch-ua": sec_ch_ua,
        "sec-ch-ua-mobile": "?0",
        "sec-ch-ua-platform": sec_ch_ua_platform,
        "sec-fetch-dest": "document",
        "sec-fetch-mode": "navigate",
        "sec-fetch-site": "none",
        "sec-fetch-user": "?1",
    }


async def apply_stealth(context, profile: StealthProfile | None = None) -> StealthProfile:
    """
    Apply stealth configuration to a Playwright BrowserContext (async).

    This sets:
      - User-Agent
      - Viewport
      - Timezone
      - Locale
      - Geolocation (generic)
      - Device pixel ratio
      - The full stealth init script

    Returns the StealthProfile used (for logging / debugging).
    """
    if profile is None:
        profile = StealthProfile.random()

    logger.info(
        "Applying stealth profile: UA=%s, viewport=%s, tz=%s, locale=%s, "
        "webgl=%s/%s, platform=%s",
        profile.user_agent[:50],
        profile.viewport,
        profile.timezone,
        profile.locale,
        profile.webgl_vendor,
        profile.webgl_renderer[:40],
        profile.platform,
    )

    headers = {"Accept-Language": f"{profile.locale},en;q=0.9"}
    headers.update(_build_client_hint_headers(profile))
    await context.set_extra_http_headers(headers)

    script = build_stealth_script(profile)
    await context.add_init_script(script)

    return profile


def apply_stealth_to_sb(sb, profile: StealthProfile | None = None) -> StealthProfile:
    """
    Apply CDP stealth init script and network headers to a SeleniumBase session.

    Registers Page.addScriptToEvaluateOnNewDocument so all navigations, new tabs,
    and sub-pages automatically execute the stealth fingerprint patches.
    """
    if profile is None:
        profile = StealthProfile.random()

    logger.info(
        "Applying SB CDP stealth profile: UA=%s, viewport=%s, tz=%s, locale=%s, "
        "webgl=%s/%s, platform=%s",
        profile.user_agent[:50],
        profile.viewport,
        profile.timezone,
        profile.locale,
        profile.webgl_vendor,
        profile.webgl_renderer[:40],
        profile.platform,
    )

    script = build_stealth_script(profile)
    try:
        driver = getattr(sb, "driver", sb)
        if hasattr(driver, "execute_cdp_cmd"):
            driver.execute_cdp_cmd(
                "Page.addScriptToEvaluateOnNewDocument",
                {"source": script}
            )
            try:
                extra_headers = {"Accept-Language": f"{profile.locale},en;q=0.9"}
                driver.execute_cdp_cmd(
                    "Network.setExtraHTTPHeaders",
                    {"headers": extra_headers}
                )
            except Exception:
                pass

            if profile.is_mobile:
                try:
                    driver.execute_cdp_cmd(
                        "Emulation.setTouchEmulationEnabled",
                        {"enabled": True, "maxTouchPoints": profile.max_touch_points}
                    )
                    driver.execute_cdp_cmd(
                        "Emulation.setDeviceMetricsOverride",
                        {
                            "width": profile.viewport.get("width", 390),
                            "height": profile.viewport.get("height", 844),
                            "deviceScaleFactor": profile.device_pixel_ratio,
                            "mobile": True,
                        }
                    )
                except Exception as e:
                    logger.debug("Mobile CDP emulation setup note: %s", e)

            logger.info("CDP Page.addScriptToEvaluateOnNewDocument stealth script successfully registered (mobile=%s)", profile.is_mobile)
        elif hasattr(sb, "execute_cdp_cmd"):
            sb.execute_cdp_cmd(
                "Page.addScriptToEvaluateOnNewDocument",
                {"source": script}
            )
            logger.info("CDP stealth script registered via sb.execute_cdp_cmd")
        else:
            logger.warning("No execute_cdp_cmd available on sb/driver")
    except Exception as e:
        logger.warning("apply_stealth_to_sb error: %s", e)

    return profile

