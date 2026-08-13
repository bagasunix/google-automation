package article

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Extractor fetches an article's HTML and extracts title, meta description, and topic.
type Extractor struct {
	client *http.Client
}

// NewExtractor creates an HTML extractor.
func NewExtractor() *Extractor {
	return &Extractor{
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

// ExtractedArticle holds the metadata pulled from an article's HTML.
type ExtractedArticle struct {
	URL      string
	Title    string
	MetaDesc string
	Topic    string
}

// Extract fetches the article HTML and parses out title, meta description, and topic.
func (e *Extractor) Extract(url string) (*ExtractedArticle, error) {
	html, err := e.fetchHTML(url)
	if err != nil {
		return nil, err
	}

	article := &ExtractedArticle{URL: url}
	article.Title = extractTitle(html)
	article.MetaDesc = extractMetaDesc(html)
	article.Topic = extractTopic(html, url)

	// Fallback: if no title found, derive from URL slug.
	if article.Title == "" {
		article.Title = titleFromURL(url)
	}

	return article, nil
}

// fetchHTML retrieves the raw HTML of a URL.
func (e *Extractor) fetchHTML(url string) (string, error) {
	resp, err := e.client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// ---------------------------------------------------------------------------
// Extraction helpers (regex-based, platform-agnostic)
// ---------------------------------------------------------------------------

// extractTitle pulls <title>…</title>, og:title, or the first <h1>.
var (
	titleRe   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	ogTitleRe = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:title["'][^>]+content=["'](.*?)["']`)
	h1Re      = regexp.MustCompile(`(?is)<h1[^>]*>(.*?)</h1>`)
)

func extractTitle(html string) string {
	// Try og:title first (most reliable across platforms).
	if m := ogTitleRe.FindStringSubmatch(html); len(m) > 1 {
		return cleanHTMLText(m[1])
	}
	if m := titleRe.FindStringSubmatch(html); len(m) > 1 {
		return cleanHTMLText(m[1])
	}
	if m := h1Re.FindStringSubmatch(html); len(m) > 1 {
		return cleanHTMLText(m[1])
	}
	return ""
}

// extractMetaDesc pulls meta[name=description] or og:description.
var (
	metaDescRe = regexp.MustCompile(`(?is)<meta[^>]+name=["']description["'][^>]+content=["'](.*?)["']`)
	ogDescRe   = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:description["'][^>]+content=["'](.*?)["']`)
)

func extractMetaDesc(html string) string {
	if m := ogDescRe.FindStringSubmatch(html); len(m) > 1 {
		return cleanHTMLText(m[1])
	}
	if m := metaDescRe.FindStringSubmatch(html); len(m) > 1 {
		return cleanHTMLText(m[1])
	}
	return ""
}

// extractTopic derives a topic from meta keywords, the URL slug, or first <h2>.
var (
	keywordsRe = regexp.MustCompile(`(?is)<meta[^>]+name=["']keywords["'][^>]+content=["'](.*?)["']`)
	h2Re       = regexp.MustCompile(`(?is)<h2[^>]*>(.*?)</h2>`)
)

func extractTopic(html, url string) string {
	if m := keywordsRe.FindStringSubmatch(html); len(m) > 1 {
		kw := cleanHTMLText(m[1])
		if kw != "" {
			// Take the first keyword if comma-separated.
			parts := strings.Split(kw, ",")
			return strings.TrimSpace(parts[0])
		}
	}

	// Fallback: first <h2>.
	if m := h2Re.FindStringSubmatch(html); len(m) > 1 {
		return cleanHTMLText(m[1])
	}

	// Final fallback: derive from URL slug.
	return topicFromURL(url)
}

// ---------------------------------------------------------------------------
// Text cleaning and URL-derived fallbacks
// ---------------------------------------------------------------------------

// tagRe matches any HTML tag for stripping.
var tagRe = regexp.MustCompile(`<[^>]*>`)

// entityRe matches common HTML entities.
var entityRe = regexp.MustCompile(`&[a-z]+;|&#\d+;`)

// cleanHTMLText strips tags, entities, and collapses whitespace.
func cleanHTMLText(s string) string {
	s = tagRe.ReplaceAllString(s, "")
	s = entityRe.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	// Collapse runs of whitespace.
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	return s
}

// titleFromURL derives a human-readable title from the URL slug.
func titleFromURL(url string) string {
	slug := slugFromURL(url)
	if slug == "" {
		return url
	}
	return strings.Title(strings.ReplaceAll(slug, "-", " "))
}

// topicFromURL extracts a single keyword from the URL slug.
func topicFromURL(url string) string {
	slug := slugFromURL(url)
	if slug == "" {
		return ""
	}
	parts := strings.Split(slug, "-")
	if len(parts) > 0 {
		return parts[0]
	}
	return slug
}

// slugFromURL extracts the last path segment of a URL as a slug.
func slugFromURL(url string) string {
	// Remove trailing slash and query string.
	url = strings.Split(url, "?")[0]
	url = strings.TrimSuffix(url, "/")
	parts := strings.Split(url, "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}
