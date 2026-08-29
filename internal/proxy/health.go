package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
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
	IPRemote            string // detected external IP through the proxy
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
			result.Country = detectCountry(p.IP)
			result.IPRemote = p.IP
			return result
		}

		// 402 = proxy reachable but bandwidth exhausted — proxy is alive,
		// mark healthy so the bandwidth tracker can handle skipping it.
		if resp.StatusCode == http.StatusPaymentRequired {
			result.Latency = time.Since(start)
			result.Healthy = true
			result.BandwidthExhausted = true
			result.Country = detectCountry(p.IP)
			result.IPRemote = p.IP
			fmt.Printf("[proxy-health] %s:%d bandwidth exhausted (402) — marking available, bw-tracker will skip\n", p.IP, p.Port)
			return result
		}
	}

	result.Latency = time.Since(start)
	return result
}

type GeoIPResponse struct {
	Status      string `json:"status"`
	Country     string `json:"country"`
	CountryCode string `json:"countryCode"`
	Timezone    string `json:"timezone"`
}

// DetectGeoIP queries ip-api.com for the country and timezone of an IP.
func DetectGeoIP(ip string) (string, string) {
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://ip-api.com/json/%s?fields=status,country,countryCode,timezone", ip))
	if err != nil {
		return "", "UTC"
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "UTC"
	}

	var data GeoIPResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil || data.Status != "success" {
		return "", "UTC"
	}

	country := data.CountryCode
	if country == "" {
		country = data.Country
	}
	tz := data.Timezone
	if tz == "" {
		tz = "UTC"
	}
	return country, tz
}

// detectCountry is a backward-compatible wrapper returning the country code/name.
func detectCountry(ip string) string {
	c, _ := DetectGeoIP(ip)
	return c
}
