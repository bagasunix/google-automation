// Package proxy implements free-proxy scraping, health checking, and rotation.
package proxy

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Proxy is a lightweight proxy descriptor used before persistence.
type Proxy struct {
	IP       string
	Port     int
	Protocol string
	Country  string
	// Auth credentials (for authenticated proxies like Webshare).
	Username string
	Password string
}

// Scraper fetches free proxy lists from multiple sources.
type Scraper struct {
	client         *http.Client
	sources        []string
	webshareAPIKey string
}

// NewScraper creates a scraper for the given source URLs.
func NewScraper(sources []string) *Scraper {
	return &Scraper{
		client: &http.Client{
			Timeout: 20 * time.Second,
		},
		sources: sources,
	}
}

// NewScraperWithWebshare creates a scraper with Webshare API support.
func NewScraperWithWebshare(sources []string, webshareKey string) *Scraper {
	return &Scraper{
		client: &http.Client{
			Timeout: 20 * time.Second,
		},
		sources:        sources,
		webshareAPIKey: webshareKey,
	}
}

// Scrape collects proxies from all configured sources concurrently using goroutines,
// deduplicates them, and returns the combined list. Errors from individual sources
// are logged but do not abort the overall scrape.
func (s *Scraper) Scrape() ([]Proxy, error) {
	type scrapeResult struct {
		source  string
		proxies []Proxy
		err     error
	}

	var (
		mu      sync.Mutex
		all     []Proxy
		seen    = make(map[string]bool)
		results = make([]scrapeResult, 0, len(s.sources))
		wg      sync.WaitGroup
	)

	// Launch one goroutine per source — all scrape in parallel.
	for _, src := range s.sources {
		wg.Add(1)
		go func(source string) {
			defer wg.Done()
			proxies, err := s.scrapeSource(source)
			mu.Lock()
			results = append(results, scrapeResult{
				source:  source,
				proxies: proxies,
				err:     err,
			})
			mu.Unlock()
		}(src)
	}

	// Launch Webshare scraper in parallel if API key is configured.
	if s.webshareAPIKey != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			proxies, err := s.scrapeWebshare()
			mu.Lock()
			results = append(results, scrapeResult{
				source:  "webshare.io API",
				proxies: proxies,
				err:     err,
			})
			mu.Unlock()
		}()
	}
	wg.Wait()

	// Merge results: deduplicate by IP:PORT.
	successCount := 0
	failCount := 0
	for _, r := range results {
		// Shorten source name for logging.
		shortName := shortenSource(r.source)
		if r.err != nil {
			failCount++
			fmt.Printf("[proxy-scraper] %-30s FAILED: %v\n", shortName, r.err)
			continue
		}
		successCount++
		fmt.Printf("[proxy-scraper] %-30s %d proxies\n", shortName, len(r.proxies))
		for _, p := range r.proxies {
			key := fmt.Sprintf("%s:%d", p.IP, p.Port)
			if seen[key] {
				continue
			}
			seen[key] = true
			all = append(all, p)
		}
	}

	fmt.Printf("[proxy-scraper] sources: %d ok / %d failed | total unique: %d\n",
		successCount, failCount, len(all))
	return all, nil
}

// scrapeSource dispatches to the appropriate parser based on the URL/content.
// Detection strategy:
//   1. geonode.com → JSON parser
//   2. proxyscrape.com → plain text (ip:port per line)
//   3. Everything else → fetch and auto-detect (JSON vs plain text vs HTML)
func (s *Scraper) scrapeSource(src string) ([]Proxy, error) {
	// Ensure URL has scheme.
	url := src
	if !strings.HasPrefix(url, "http") {
		url = "https://" + url
	}

	// Fetch the content.
	text, err := s.fetchURL(url)
	if err != nil {
		return nil, err
	}

	// Auto-detect format based on content + URL.
	switch {
	case strings.Contains(src, "geonode.com"):
		return parseGeonodeJSON(text), nil
	case strings.Contains(src, "proxyscrape.com"):
		return extractIPPort(text), nil
	case strings.Contains(src, "free-proxy-list.net"):
		return extractIPPort(text), nil
	case strings.Contains(src, "spys.one"):
		return extractIPPort(text), nil
	default:
		// Auto-detect: try JSON first, then regex IP:PORT extraction.
		if strings.Contains(text, "\"ip\"") && strings.Contains(text, "\"port\"") {
			return parseGenericJSON(text), nil
		}
		return extractIPPort(text), nil
	}
}

// shortenSource truncates a URL to a readable short form for logging.
func shortenSource(src string) string {
	src = strings.TrimPrefix(src, "https://")
	src = strings.TrimPrefix(src, "http://")
	if len(src) > 30 {
		// Show domain + first path segment.
		parts := strings.SplitN(src, "/", 2)
		if len(parts) > 1 {
			path := parts[1]
			if len(path) > 15 {
				path = path[:15] + "..."
			}
			return parts[0] + "/" + path
		}
		return src[:30] + "..."
	}
	return src
}

// ipPortRe matches "IP:PORT" patterns in raw text/HTML.
var ipPortRe = regexp.MustCompile(`(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}):(\d{2,5})`)

// extractIPPort finds all IP:PORT pairs in the given text.
// Works for plain text lists (one per line), HTML pages, and API responses.
func extractIPPort(text string) []Proxy {
	matches := ipPortRe.FindAllStringSubmatch(text, -1)
	var proxies []Proxy
	for _, m := range matches {
		port := 0
		fmt.Sscanf(m[2], "%d", &port)
		if port == 0 {
			continue
		}
		// Basic IP validation.
		parts := strings.Split(m[1], ".")
		if len(parts) != 4 {
			continue
		}
		valid := true
		for _, p := range parts {
			n := 0
			fmt.Sscanf(p, "%d", &n)
			if n < 0 || n > 255 {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		proxies = append(proxies, Proxy{
			IP:       m[1],
			Port:     port,
			Protocol: "http",
		})
	}
	return proxies
}

// fetchURL retrieves the body of a URL as text.
func (s *Scraper) fetchURL(url string) (string, error) {
	resp, err := s.client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// ---------------------------------------------------------------------------
// JSON parsers
// ---------------------------------------------------------------------------

// jsonIPRe matches "ip":"x.x.x.x" in JSON.
var jsonIPRe = regexp.MustCompile(`"ip"\s*:\s*"(.*?)"`)

// jsonPortRe matches "port":12345 in JSON.
var jsonPortRe = regexp.MustCompile(`"port"\s*:\s*(\d+)`)

// parseGeonodeJSON parses geonode.com JSON response.
func parseGeonodeJSON(text string) []Proxy {
	ips := jsonIPRe.FindAllStringSubmatch(text, -1)
	ports := jsonPortRe.FindAllStringSubmatch(text, -1)

	var proxies []Proxy
	for i, ipm := range ips {
		if i >= len(ports) {
			break
		}
		port := 0
		fmt.Sscanf(ports[i][1], "%d", &port)
		if port == 0 {
			continue
		}
		proxies = append(proxies, Proxy{
			IP:       ipm[1],
			Port:     port,
			Protocol: "http",
		})
	}
	return proxies
}

// parseGenericJSON parses any JSON with "ip" and "port" fields.
func parseGenericJSON(text string) []Proxy {
	return parseGeonodeJSON(text)
}

// ---------------------------------------------------------------------------
// Webshare API
// ---------------------------------------------------------------------------

// scrapeWebshare fetches authenticated proxies from the Webshare API v2.
// These are datacenter proxies (free tier: 10 proxies) that require
// username:password authentication.
func (s *Scraper) scrapeWebshare() ([]Proxy, error) {
	url := "https://proxy.webshare.io/api/v2/proxy/list/?mode=direct&page_size=100"

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Token "+s.webshareAPIKey)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("webshare API HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Parse JSON response: {"count": N, "results": [{"proxy_address": "x", "port": N, "username": "...", "password": "...", "country_code": "..."}]}
	ipRe := regexp.MustCompile(`"proxy_address"\s*:\s*"(.*?)"`)
	portRe := regexp.MustCompile(`"port"\s*:\s*(\d+)`)
	userRe := regexp.MustCompile(`"username"\s*:\s*"(.*?)"`)
	passRe := regexp.MustCompile(`"password"\s*:\s*"(.*?)"`)
	countryRe := regexp.MustCompile(`"country_code"\s*:\s*"(.*?)"`)

	ips := ipRe.FindAllStringSubmatch(string(body), -1)
	ports := portRe.FindAllStringSubmatch(string(body), -1)
	users := userRe.FindAllStringSubmatch(string(body), -1)
	passes := passRe.FindAllStringSubmatch(string(body), -1)
	countries := countryRe.FindAllStringSubmatch(string(body), -1)

	var proxies []Proxy
	for i := 0; i < len(ips) && i < len(ports); i++ {
		port := 0
		fmt.Sscanf(ports[i][1], "%d", &port)
		if port == 0 {
			continue
		}
		p := Proxy{
			IP:       ips[i][1],
			Port:     port,
			Protocol: "http",
		}
		if i < len(users) {
			p.Username = users[i][1]
		}
		if i < len(passes) {
			p.Password = passes[i][1]
		}
		if i < len(countries) {
			p.Country = countries[i][1]
		}
		proxies = append(proxies, p)
	}
	return proxies, nil
}
