"""
browser/ip_health.py
====================
Pre-session proxy IP health check via ipapi.co.

Checks whether the outbound IP (proxy or direct) is flagged or belongs to
a datacenter/hosting range before opening a full browser session.
Returns a HealthResult so callers can skip flagged proxies early.
"""

from __future__ import annotations

import logging
import os
import re
import time
from dataclasses import dataclass, field
from typing import Optional

logger = logging.getLogger("worker.browser.ip_health")

# The Tor Project publishes every exit node's address. Matching against that
# list is the only reliable test: an exit node's org name usually says nothing
# about Tor (a real exit we checked was org="R0CKET-CLOUD"), while plenty of
# ordinary ISPs contain "tor" as a substring (Torino, Vector, Storage...).
_TOR_EXIT_LIST_URL = "https://check.torproject.org/torbulkexitlist"
_TOR_LIST_TTL = 24 * 3600
_tor_exits: Optional[set] = None
_tor_exits_loaded_at: float = 0.0

# Only a self-declared Tor org should match by name, and on word boundaries.
_TOR_ORG_RE = re.compile(r"\btor\b(?:[\s-]*(?:exit|relay|node|network|project))?", re.I)

# Self-lookup form — no explicit IP in the path. We always route this
# request through the proxy being checked (see `proxies` below), so ipapi.co
# sees whichever IP the request actually arrives from; that's the "outbound
# IP Google will see" we care about, not some other arbitrary IP.
_IPAPI_URL = "https://ipapi.co/json/"
_FALLBACK_URL = "https://ip-api.com/json/{ip}?fields=status,query,org,hosting,proxy,countryCode,timezone"

_DATACENTER_ORGS = [
    "amazon", "aws", "google", "azure", "microsoft", "digitalocean",
    "linode", "vultr", "hetzner", "ovh", "leaseweb", "choopa",
    "serverius", "datacamp", "m247", "tzulo", "psychz",
]


@dataclass
class HealthResult:
    ip: str = ""
    country: str = ""
    org: str = ""
    is_datacenter: bool = False
    is_proxy: bool = False
    is_tor: bool = False
    flagged: bool = False
    flag_reason: str = ""
    ok: bool = True
    raw: dict = field(default_factory=dict)

    def __str__(self) -> str:
        status = "OK" if self.ok else f"BLOCKED({self.flag_reason})"
        return f"HealthResult ip={self.ip} country={self.country} org={self.org!r} status={status}"


def check_proxy_health(
    proxy_ip: str = "",
    proxy_port: int = 0,
    proxy_username: str = "",
    proxy_password: str = "",
    timeout: int = 10,
) -> HealthResult:
    """
    Check outbound IP health before opening a browser session.

    If proxy credentials are provided, routes the check through the proxy
    so we see the actual egress IP Google will see.

    Returns HealthResult.ok=False when the IP is a known datacenter,
    flagged proxy, or Tor exit node.
    """
    import urllib.request
    import json

    proxies = None
    if proxy_ip and proxy_port:
        if proxy_username and proxy_password:
            proxy_url = f"http://{proxy_username}:{proxy_password}@{proxy_ip}:{proxy_port}"
        else:
            proxy_url = f"http://{proxy_ip}:{proxy_port}"
        proxies = {"http": proxy_url, "https": proxy_url}

    data = _fetch_ipapi(proxies, timeout)
    if not data:
        data = _fetch_ipapi_fallback(proxies, timeout)

    if not data:
        logger.warning("IP health check failed — both endpoints unreachable; assuming OK")
        return HealthResult(ok=True, flag_reason="check_failed")

    return _evaluate(data)


def _fetch_ipapi(proxies: Optional[dict], timeout: int) -> Optional[dict]:
    import urllib.request
    import json

    try:
        if proxies:
            proxy_handler = urllib.request.ProxyHandler(proxies)
            opener = urllib.request.build_opener(proxy_handler)
        else:
            opener = urllib.request.build_opener()

        req = urllib.request.Request(_IPAPI_URL, headers={"User-Agent": "curl/7.88.1"})
        with opener.open(req, timeout=timeout) as resp:
            raw = resp.read().decode("utf-8", errors="replace")
            data = json.loads(raw)
            # ipapi.co's error responses (rate-limited, invalid target, etc.)
            # still include an "ip" key sometimes (e.g. echoing back a bad
            # input), so checking for that alone isn't a reliable success
            # signal — explicitly reject anything carrying an "error" flag
            # rather than silently treating it as a healthy, unflagged IP.
            if "ip" in data and not data.get("error"):
                return data
            logger.debug("ipapi.co returned an error payload: %s", data)
    except Exception as e:
        logger.debug("ipapi.co failed: %s", e)
    return None


def _fetch_ipapi_fallback(proxies: Optional[dict], timeout: int) -> Optional[dict]:
    import urllib.request
    import json

    url = _FALLBACK_URL.format(ip="")
    # ip-api.com uses the requesting IP when no IP param given
    url = "http://ip-api.com/json/?fields=status,query,org,hosting,proxy,countryCode,timezone"
    try:
        if proxies:
            proxy_handler = urllib.request.ProxyHandler(proxies)
            opener = urllib.request.build_opener(proxy_handler)
        else:
            opener = urllib.request.build_opener()

        req = urllib.request.Request(url, headers={"User-Agent": "curl/7.88.1"})
        with opener.open(req, timeout=timeout) as resp:
            raw = resp.read().decode("utf-8", errors="replace")
            data = json.loads(raw)
            if data.get("status") == "success":
                # normalize to ipapi.co-like keys
                return {
                    "ip": data.get("query", ""),
                    "org": data.get("org", ""),
                    "country_code": data.get("countryCode", ""),
                    "timezone": data.get("timezone", ""),
                    "_hosting": data.get("hosting", False),
                    "_proxy": data.get("proxy", False),
                }
    except Exception as e:
        logger.debug("ip-api.com fallback failed: %s", e)
    return None



def _load_tor_exits() -> set:
    """Fetch (and cache for _TOR_LIST_TTL) the published Tor exit-node list."""
    global _tor_exits, _tor_exits_loaded_at

    if _tor_exits is not None and (time.time() - _tor_exits_loaded_at) < _TOR_LIST_TTL:
        return _tor_exits

    import urllib.request

    cache = os.path.join(
        os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))),
        "data", "tor_exits.txt",
    )

    text = ""
    try:
        req = urllib.request.Request(
            _TOR_EXIT_LIST_URL, headers={"User-Agent": "curl/7.88.1"}
        )
        with urllib.request.urlopen(req, timeout=15) as resp:
            text = resp.read().decode("utf-8", errors="replace")
        try:
            os.makedirs(os.path.dirname(cache), exist_ok=True)
            with open(cache, "w", encoding="utf-8") as fh:
                fh.write(text)
        except OSError as e:
            logger.debug("could not cache Tor exit list: %s", e)
    except Exception as e:
        # Network hiccup: fall back to the last good copy on disk rather than
        # silently treating every IP as non-Tor.
        logger.warning("Tor exit list fetch failed (%s) — using cached copy", e)
        try:
            with open(cache, "r", encoding="utf-8") as fh:
                text = fh.read()
        except OSError:
            logger.warning("no cached Tor exit list either — org-name check only")

    _tor_exits = {
        line.strip() for line in text.splitlines()
        if line.strip() and not line.startswith("#")
    }
    _tor_exits_loaded_at = time.time()
    logger.info("Tor exit list: %d addresses", len(_tor_exits))
    return _tor_exits


def _is_tor_exit(ip: str, org: str) -> bool:
    """True when this IP is a Tor exit node.

    Authoritative check is the published exit list; the org-name regex only
    catches self-declared exits ("TOR Exit and More") whose address the list
    fetch may have missed.
    """
    if ip and ip in _load_tor_exits():
        return True
    return bool(org and _TOR_ORG_RE.search(org))


def _evaluate(data: dict) -> HealthResult:
    ip = data.get("ip", "")
    org = (data.get("org", "") or "").lower()
    country = data.get("country_code", "") or data.get("country", "")
    timezone = data.get("timezone", "")

    is_datacenter = (
        data.get("_hosting", False)
        or any(kw in org for kw in _DATACENTER_ORGS)
    )
    is_proxy = bool(data.get("_proxy", False))
    is_tor = _is_tor_exit(ip, org)

    result = HealthResult(
        ip=ip,
        country=country,
        org=org,
        is_datacenter=is_datacenter,
        is_proxy=is_proxy,
        is_tor=is_tor,
        raw=data,
    )

    if is_tor:
        result.ok = False
        result.flagged = True
        result.flag_reason = "tor_exit_node"
    elif is_datacenter:
        result.ok = False
        result.flagged = True
        result.flag_reason = "datacenter_ip"
    else:
        result.ok = True

    logger.info(
        "IP health: %s country=%s org=%s datacenter=%s proxy=%s tor=%s → %s",
        ip, country, org, is_datacenter, is_proxy, is_tor,
        "OK" if result.ok else result.flag_reason,
    )
    return result
