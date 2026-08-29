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
import time
from dataclasses import dataclass, field
from typing import Optional

logger = logging.getLogger("worker.browser.ip_health")

_IPAPI_URL = "https://ipapi.co/{ip}/json/"
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

    url = _IPAPI_URL.format(ip="json")
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
            if "ip" in data:
                return data
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
    is_tor = "tor" in org

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
