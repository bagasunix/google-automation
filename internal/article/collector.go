// Package article implements sitemap.xml scraping, title/meta extraction,
// and random article selection with search-method randomization.
package article

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SitemapURL represents a single <url> entry in a sitemap.
type SitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod"`
}

// urlSet is the root element of a sitemap.xml.
type urlSet struct {
	URLs []SitemapURL `xml:"url"`
}

// sitemapIndex holds nested sitemaps (<sitemap><loc>…</loc></sitemap>).
type sitemapIndex struct {
	Sitemaps []struct {
		Loc string `xml:"loc"`
	} `xml:"sitemap"`
}

// Collector scrapes sitemap.xml for a domain and returns article URLs.
type Collector struct {
	client *http.Client
}

// NewCollector creates a collector with a reasonable HTTP timeout.
func NewCollector() *Collector {
	return &Collector{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Collect fetches the sitemap for the given domain and returns article URLs.
// It handles both flat sitemaps and sitemap indexes (nested sitemaps).
func (c *Collector) Collect(domain string) ([]SitemapURL, error) {
	sitemapURL := fmt.Sprintf("https://%s/sitemap.xml", domain)
	return c.fetchAndParseSitemap(sitemapURL, 0)
}

// fetchAndParseSitemap fetches a sitemap URL. If it turns out to be a sitemap
// index, it recurses into each sub-sitemap (max depth 2 to prevent loops).
func (c *Collector) fetchAndParseSitemap(url string, depth int) ([]SitemapURL, error) {
	if depth > 2 {
		return nil, fmt.Errorf("sitemap recursion too deep for %s", url)
	}

	body, err := c.fetch(url)
	if err != nil {
		return nil, fmt.Errorf("fetch sitemap %s: %w", url, err)
	}

	// First, try to parse as a sitemap index.
	var idx sitemapIndex
	if err := xml.Unmarshal(body, &idx); err == nil && len(idx.Sitemaps) > 0 {
		// It's a sitemap index — recurse into each sub-sitemap.
		var allURLs []SitemapURL
		for _, sm := range idx.Sitemaps {
			urls, err := c.fetchAndParseSitemap(sm.Loc, depth+1)
			if err != nil {
				// Log and continue; partial sitemap is OK.
				continue
			}
			allURLs = append(allURLs, urls...)
		}
		return allURLs, nil
	}

	// Otherwise, parse as a flat URL set.
	var set urlSet
	if err := xml.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("parse sitemap xml: %w", err)
	}

	return set.URLs, nil
}

// fetch retrieves the raw bytes from a URL.
func (c *Collector) fetch(url string) ([]byte, error) {
	resp, err := c.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	return io.ReadAll(resp.Body)
}

// IsArticleURL performs a heuristic filter to distinguish article pages from
// category, tag, author, and pagination URLs. This is platform-agnostic.
func IsArticleURL(url string) bool {
	// Reject obvious non-article paths.
	lower := strings.ToLower(url)
	exclude := []string{"/category/", "/tag/", "/tags/", "/author/", "/page/",
		"/wp-", "/admin", "/feed", "/rss", "/comments", "/replytocom",
		"sitemap", ".jpg", ".png", ".gif", ".css", ".js", ".xml", ".pdf"}
	for _, ex := range exclude {
		if strings.Contains(lower, ex) {
			return false
		}
	}

	// Must look like a content path (has at least one path segment after the domain).
	// e.g. https://example.com/my-article-title
	parts := strings.Split(url, "/")
	// parts[0] = "https:", parts[1] = "", parts[2] = "domain", parts[3+] = path
	if len(parts) < 4 {
		return false
	}
	pathSegments := parts[3:]
	// Need at least one non-empty path segment.
	hasContent := false
	for _, seg := range pathSegments {
		if strings.TrimSpace(seg) != "" {
			hasContent = true
			break
		}
	}
	return hasContent
}
