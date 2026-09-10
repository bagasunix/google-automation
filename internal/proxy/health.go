package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// HealthResult holds the outcome of a single proxy health check.
type HealthResult struct {
	Proxy               Proxy
	Healthy             bool
	BandwidthExhausted  bool
	Latency             time.Duration
	Country             string
	Timezone            string // real IANA timezone from DetectGeoIP, e.g. "Europe/Berlin"
	IPRemote            string // detected external IP through the proxy
	IsDatacenter        bool   // ip-api flagged this as hosting/datacenter range — Google auto-CAPTCHAs these
}

// Checker health-checks proxies in parallel using Go concurrency.
type Checker struct {
	timeout time.Duration
}

// NewChecker creates a health checker with the given per-proxy timeout.
func NewChecker(timeoutSec int) *Checker {
	return &Checker{timeout: time.Duration(timeoutSec) * time.Second}
}

// CheckAll tests all proxies concurrently and returns only the healthy ones.
// Concurrency is limited to 100 workers to avoid exhausting file descriptors.
// Each source proxy is checked in its own goroutine.
func (c *Checker) CheckAll(proxies []Proxy) []HealthResult {
	var (
		mu       sync.Mutex
		results  []HealthResult
		wg       sync.WaitGroup
		sem      = make(chan struct{}, 100) // concurrency limiter
		failCount int
		totalTime time.Time = time.Now()
	)

	for _, p := range proxies {
		wg.Add(1)
		go func(px Proxy) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			res := c.checkOne(px)
			mu.Lock()
			if res.Healthy {
				results = append(results, res)
			} else {
				failCount++
			}
			mu.Unlock()
		}(p)
	}
	wg.Wait()

	elapsed := time.Since(totalTime)
	fmt.Printf("[proxy-health] checked %d proxies in %v\n", len(proxies), elapsed.Round(time.Millisecond))
	fmt.Printf("[proxy-health] healthy: %d | failed: %d | success rate: %.1f%%\n",
		len(results), failCount, float64(len(results))/float64(len(proxies))*100)
	return results
}

// checkOne tests a single proxy by making an HTTP request through it to a
// reliable IP echo endpoint. We try multiple endpoints in order — if one is
// down or returns a bad response, we fall back to the next.
// Supports both anonymous proxies and authenticated proxies (Webshare).
func (c *Checker) checkOne(p Proxy) HealthResult {
	proxyURLStr := fmt.Sprintf("%s://%s:%d", p.Protocol, p.IP, p.Port)
	result := HealthResult{Proxy: p}

	parsedURL, err := url.Parse(proxyURLStr)
	if err != nil {
		return result
	}

	// Add auth credentials if available (Webshare proxies need this).
	if p.Username != "" && p.Password != "" {
		parsedURL.User = url.UserPassword(p.Username, p.Password)
		proxyURLStr = parsedURL.String()
	}

	transport := &http.Transport{
		Proxy: http.ProxyURL(parsedURL),
		DialContext: (&net.Dialer{
			Timeout: c.timeout,
		}).DialContext,
		ResponseHeaderTimeout: c.timeout,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   c.timeout,
	}

	// Multiple fallback endpoints — if one fails, try the next.
	// All return plain text containing the apparent IP address.
	testURLs := []string{
		"http://checkip.amazonaws.com/",
		"http://ifconfig.me/ip",
		"http://ip-api.com/line/?fields=query",
	}

	start := time.Now()
	var resp *http.Response
	for _, testURL := range testURLs {
		req, err := http.NewRequestWithContext(context.Background(), "GET", testURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0")

		resp, err = client.Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			result.Latency = time.Since(start)
			result.Healthy = true
			c.resolveGeo(&result, p)
			result.IPRemote = p.IP
			return result
		}

		// 402 = proxy reachable but bandwidth exhausted — proxy is alive,
		// mark healthy so the bandwidth tracker can handle skipping it.
		if resp.StatusCode == http.StatusPaymentRequired {
			result.Latency = time.Since(start)
			result.Healthy = true
			result.BandwidthExhausted = true
			c.resolveGeo(&result, p)
			result.IPRemote = p.IP
			fmt.Printf("[proxy-health] %s:%d bandwidth exhausted (402) — marking available, bw-tracker will skip\n", p.IP, p.Port)
			return result
		}
	}

	result.Latency = time.Since(start)
	return result
}

// resolveGeo fills country/timezone/datacenter on a health result. For a
// gateway residential proxy the gateway IP is NOT the exit IP, so geo-locating
// or datacenter-flagging it is wrong (the gateway often sits in a hosting
// range while the exit it hands out is residential). Instead we trust the
// preset country/timezone that GenerateResidentialProxies set from config, and
// force IsDatacenter=false so the scheduler will send it to google/bing.
func (c *Checker) resolveGeo(result *HealthResult, p Proxy) {
	if p.IsResidential {
		result.Country = p.Country
		result.Timezone = p.Timezone
		result.IsDatacenter = false
		return
	}
	result.Country, result.Timezone, result.IsDatacenter = DetectGeoIP(p.IP)
}

type GeoIPResponse struct {
	Status      string `json:"status"`
	Country     string `json:"country"`
	CountryCode string `json:"countryCode"`
	Timezone    string `json:"timezone"`
	Hosting     bool   `json:"hosting"`
	Proxy       bool   `json:"proxy"`
}

// ipwhoisResponse is the fallback provider's payload (ipwho.is).
type ipwhoisResponse struct {
	Success     bool   `json:"success"`
	CountryCode string `json:"country_code"`
	Timezone    struct {
		ID string `json:"id"`
	} `json:"timezone"`
	Connection struct {
		Org string `json:"org"`
		ISP string `json:"isp"`
	} `json:"connection"`
}

// queryIPAPI asks ip-api.com. Returns ok=false on any failure so the caller
// can try another provider rather than silently accepting a wrong answer.
// The `hosting` and `proxy` flags come free in the same response — no extra
// request — and tell us whether this is a datacenter/hosting IP that Google
// reflexively serves a CAPTCHA to.
func queryIPAPI(client *http.Client, ip string) (country, tz string, datacenter, ok bool) {
	resp, err := client.Get(fmt.Sprintf("http://ip-api.com/json/%s?fields=status,country,countryCode,timezone,hosting,proxy", ip))
	if err != nil {
		return "", "", false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", false, false
	}

	var data GeoIPResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil || data.Status != "success" {
		return "", "", false, false
	}

	country = data.CountryCode
	if country == "" {
		country = data.Country
	}
	if country == "" || data.Timezone == "" {
		return "", "", false, false
	}
	return country, data.Timezone, data.Hosting || data.Proxy, true
}

// queryIPWhois asks ipwho.is — a second, independently-operated provider, so
// a rate-limit or outage at ip-api.com doesn't take geo detection down with it.
// ipwho.is has no dedicated hosting flag, so datacenter is inferred from the
// connection org/ISP against the known-hosting keyword list.
func queryIPWhois(client *http.Client, ip string) (country, tz string, datacenter, ok bool) {
	resp, err := client.Get(fmt.Sprintf("https://ipwho.is/%s", ip))
	if err != nil {
		return "", "", false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", false, false
	}

	var data ipwhoisResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil || !data.Success {
		return "", "", false, false
	}
	if data.CountryCode == "" || data.Timezone.ID == "" {
		return "", "", false, false
	}
	org := strings.ToLower(data.Connection.Org + " " + data.Connection.ISP)
	datacenter = false
	for _, kw := range datacenterOrgKeywords {
		if strings.Contains(org, kw) {
			datacenter = true
			break
		}
	}
	return data.CountryCode, data.Timezone.ID, datacenter, true
}

// datacenterOrgKeywords mirrors the Python worker's _DATACENTER_ORGS list
// (browser/ip_health.py) so both sides agree on what counts as a hosting IP.
var datacenterOrgKeywords = []string{
	"amazon", "aws", "google", "azure", "microsoft", "digitalocean",
	"linode", "vultr", "hetzner", "ovh", "leaseweb", "choopa",
	"serverius", "datacamp", "m247", "tzulo", "psychz", "egihosting",
	"hosting", "datacenter", "data center", "server", "cloud",
}

// DetectGeoIP resolves an IP's country, IANA timezone, and whether it is a
// datacenter/hosting IP.
//
// Every browser session sets its timezone from this, so a wrong answer is an
// "impossible geography" signal to every site the proxy visits. Two hazards
// are handled here:
//
//  1. Falling back to "UTC" on failure silently recreates the fleet-wide
//     UTC bug fixed earlier (every proxy claiming UTC regardless of country).
//     ip-api.com allows only ~45 requests/minute while the health checker
//     fires all proxies in parallel, so hitting that limit is realistic —
//     and it would have degraded a whole refresh cycle to UTC without a
//     single log line. A second provider is tried before giving up, and a
//     genuine give-up is logged loudly rather than passed off as a real
//     timezone.
//  2. Providers genuinely disagree — 84.247.60.125 resolves to Poland on
//     ip-api and Portugal on ipwho.is — so a mismatch is logged. We keep the
//     primary's answer (picking arbitrarily wouldn't be more correct), but
//     the log makes an otherwise invisible inconsistency debuggable.
func DetectGeoIP(ip string) (country, tz string, datacenter bool) {
	client := &http.Client{Timeout: 4 * time.Second}

	country, tz, datacenter, ok := queryIPAPI(client, ip)
	if ok {
		if c2, tz2, _, ok2 := queryIPWhois(client, ip); ok2 && (c2 != country || tz2 != tz) {
			log.Printf("[geoip] %s: providers disagree — ip-api=%s/%s ipwho.is=%s/%s (using ip-api)",
				ip, country, tz, c2, tz2)
		}
		return country, tz, datacenter
	}

	log.Printf("[geoip] %s: ip-api lookup failed — trying fallback provider", ip)
	if country, tz, datacenter, ok = queryIPWhois(client, ip); ok {
		return country, tz, datacenter
	}

	log.Printf("[geoip] %s: ALL geo providers failed — falling back to UTC, which is a "+
		"detectable mismatch for a non-UTC proxy", ip)
	return "", "UTC", false
}
