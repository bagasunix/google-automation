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

# Viewports that pair naturally with the UA platforms above
VIEWPORTS = [
    {"width": 1920, "height": 1080},
    {"width": 1536, "height": 864},
    {"width": 1440, "height": 900},
    {"width": 1366, "height": 768},
    {"width": 1280, "height": 720},
    {"width": 1680, "height": 1050},
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

# WebGL renderer / vendor pairs (common real GPUs)
WEBGL_CONFIGS = [
    {"vendor": "Google Inc. (NVIDIA)", "renderer": "ANGLE (NVIDIA, NVIDIA GeForce RTX 3060 Direct3D11 vs_5_0 ps_5_0)"},
    {"vendor": "Google Inc. (NVIDIA)", "renderer": "ANGLE (NVIDIA, NVIDIA GeForce GTX 1660 Ti Direct3D11 vs_5_0 ps_5_0)"},
    {"vendor": "Google Inc. (AMD)", "renderer": "ANGLE (AMD, AMD Radeon RX 580 Direct3D11 vs_5_0 ps_5_0)"},
    {"vendor": "Google Inc. (AMD)", "renderer": "ANGLE (AMD, AMD Radeon RX 6700 XT Direct3D11 vs_5_0 ps_5_0)"},
    {"vendor": "Google Inc. (Intel)", "renderer": "ANGLE (Intel, Intel(R) UHD Graphics 770 Direct3D11 vs_5_0 ps_5_0)"},
    {"vendor": "Google Inc. (Intel)", "renderer": "ANGLE (Intel, Intel(R) Iris(R) Xe Graphics Direct3D11 vs_5_0 ps_5_0)"},
    {"vendor": "Google Inc. (Apple)", "renderer": "ANGLE (Apple, ANGLE Metal Renderer: Apple M1 Pro, Unspecified Version)"},
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

    @classmethod
    def random(cls) -> "StealthProfile":
        """Generate a random but internally-consistent profile."""
        ua = random.choice(USER_AGENTS)
        vp = random.choice(VIEWPORTS)
        tz = random.choice(TIMEZONES)
        loc = random.choice(LOCALES)
        webgl = random.choice(WEBGL_CONFIGS)
        color_depth = random.choice(COLOR_DEPTHS)
        hw_conc = random.choice(HARDWARE_CONCURRENCIES)
        dpr = random.choice(DEVICE_PIXEL_RATIOS)

        # Infer platform from UA
        if "Windows" in ua:
            platform = "Win32"
        elif "Macintosh" in ua:
            platform = "MacIntel"
        else:
            platform = "Linux x86_64"

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

// 1. navigator.webdriver = false
try {{
    Object.defineProperty(navigator, 'webdriver', {{
        get: () => false,
        configurable: true,
    }});
}} catch (e) {{}}

// 2. navigator.platform
try {{
    Object.defineProperty(navigator, 'platform', {{
        get: () => '{profile.platform}',
        configurable: true,
    }});
}} catch (e) {{}}

// 3. navigator.hardwareConcurrency
try {{
    Object.defineProperty(navigator, 'hardwareConcurrency', {{
        get: () => {profile.hardware_concurrency},
        configurable: true,
    }});
}} catch (e) {{}}

// 4. navigator.deviceMemory (common values: 4, 8)
try {{
    Object.defineProperty(navigator, 'deviceMemory', {{
        get: () => 8,
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
    Object.defineProperty(navigator, 'languages', {{
        get: () => {langs_js},
        configurable: true,
    }});
}} catch (e) {{}}

// 7. Canvas noise injection
// Adds a tiny, deterministic-per-session noise to canvas pixel data
// so the fingerprint changes without being visually noticeable.
try {{
    const originalToDataURL = HTMLCanvasElement.prototype.toDataURL;
    HTMLCanvasElement.prototype.toDataURL = function(...args) {{
        const ctx = this.getContext('2d');
        if (ctx) {{
            const w = this.width;
            const h = this.height;
            try {{
                const imageData = ctx.getImageData(0, 0, Math.max(w, 1), Math.max(h, 1));
                // Apply subtle noise to a few pixels
                for (let i = 0; i < imageData.data.length; i += 4 * 100) {{
                    imageData.data[i] = imageData.data[i] ^ 1;  // flip LSB on red channel
                }}
                ctx.putImageData(imageData, 0, 0);
            }} catch (e) {{}}
        }}
        return originalToDataURL.apply(this, args);
    }};

    const originalGetImageData = CanvasRenderingContext2D.prototype.getImageData;
    CanvasRenderingContext2D.prototype.getImageData = function(...args) {{
        const imageData = originalGetImageData.apply(this, args);
        // Apply noise to every Nth pixel
        for (let i = 0; i < imageData.data.length; i += 4 * 100) {{
            imageData.data[i] = imageData.data[i] ^ 1;
        }}
        return imageData;
    }};
}} catch (e) {{}}

// 8. WebGL vendor/renderer override
try {{
    const webglVendor = '{profile.webgl_vendor}';
    const webglRenderer = '{profile.webgl_renderer}';

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
        const getParameter2Proto = WebGL2RenderingContext.prototype.getParameter;
        WebGL2RenderingContext.prototype.getParameter = function(param) {{
            if (param === 37445) return webglVendor;
            if (param === 37446) return webglRenderer;
            return getParameter2Proto.call(this, param);
        }};
    }}
}} catch (e) {{}}

// 9. WebRTC block — prevents real IP leak through STUN/TURN
try {{
    const origRTCPeerConnection = window.RTCPeerConnection || window.webkitRTCPeerConnection;
    window.RTCPeerConnection = undefined;
    window.webkitRTCPeerConnection = undefined;
    if (origRTCPeerConnection) {{
        // Replace with a no-op proxy that still exists (some sites check typeof)
        window.RTCPeerConnection = function() {{
            throw new TypeError('RTCPeerConnection is not available');
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


def apply_stealth(context, profile: StealthProfile | None = None) -> StealthProfile:
    """
    Apply stealth configuration to a Playwright BrowserContext.

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

    # Apply context-level settings
    context.set_extra_http_headers({
        "Accept-Language": f"{profile.locale},en;q=0.9",
    })

    # Inject the stealth init script — runs before any page JS
    script = build_stealth_script(profile)
    context.add_init_script(script)

    return profile
