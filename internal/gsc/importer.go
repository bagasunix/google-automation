// Package gsc provides Google Search Console performance data parsing,
// opportunity scoring, and query prioritization for SEO automation.
package gsc

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"strings"

	"google-automation/internal/storage"
)

// GscEntry represents a single query/page performance row from GSC.
type GscEntry struct {
	Query            string  `json:"query"`
	PageURL          string  `json:"page_url"`
	Clicks           int     `json:"clicks"`
	Impressions      int     `json:"impressions"`
	CTR              float64 `json:"ctr"`
	Position         float64 `json:"position"`
	OpportunityScore float64 `json:"opportunity_score"`
}

// CalculateOpportunityScore computes the high-yield CTR potential score.
// High impressions + low CTR + positions 4-20 yield the highest score.
func CalculateOpportunityScore(impressions, clicks int, position float64) float64 {
	if impressions <= 0 {
		return 0
	}
	ctr := float64(clicks) / float64(impressions)
	// Positional multiplier (highest reward for positions 4-20)
	posWeight := 1.0
	if position >= 4.0 && position <= 20.0 {
		posWeight = (25.0 - position) / 5.0
	} else if position > 20.0 && position <= 30.0 {
		posWeight = 0.5
	} else if position < 4.0 {
		posWeight = 0.8 // already high ranking
	}

	// High impression + untapped CTR
	untappedClicks := float64(impressions) * (1.0 - ctr)
	return untappedClicks * posWeight
}

// ImportCSV parses a Google Search Console CSV export file.
func ImportCSV(filePath string) ([]GscEntry, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open gsc csv: %w", err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.LazyQuotes = true
	r.FieldsPerRecord = -1

	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read gsc csv: %w", err)
	}

	if len(rows) < 2 {
		return nil, fmt.Errorf("gsc csv is empty or missing headers")
	}

	header := rows[0]
	queryIdx, urlIdx, clicksIdx, impIdx, ctrIdx, posIdx := -1, -1, -1, -1, -1, -1

	for i, h := range header {
		clean := strings.ToLower(strings.TrimSpace(h))
		switch {
		case strings.Contains(clean, "query") || strings.Contains(clean, "top queries"):
			queryIdx = i
		case strings.Contains(clean, "page") || strings.Contains(clean, "url"):
			urlIdx = i
		case strings.Contains(clean, "click"):
			clicksIdx = i
		case strings.Contains(clean, "impression"):
			impIdx = i
		case strings.Contains(clean, "ctr"):
			ctrIdx = i
		case strings.Contains(clean, "position"):
			posIdx = i
		}
	}

	var results []GscEntry
	for _, row := range rows[1:] {
		entry := GscEntry{}
		if queryIdx >= 0 && queryIdx < len(row) {
			entry.Query = strings.TrimSpace(row[queryIdx])
		}
		if urlIdx >= 0 && urlIdx < len(row) {
			entry.PageURL = strings.TrimSpace(row[urlIdx])
		}
		if clicksIdx >= 0 && clicksIdx < len(row) {
			entry.Clicks, _ = strconv.Atoi(strings.ReplaceAll(row[clicksIdx], ",", ""))
		}
		if impIdx >= 0 && impIdx < len(row) {
			entry.Impressions, _ = strconv.Atoi(strings.ReplaceAll(row[impIdx], ",", ""))
		}
		if ctrIdx >= 0 && ctrIdx < len(row) {
			ctrStr := strings.TrimSuffix(strings.TrimSpace(row[ctrIdx]), "%")
			if v, err := strconv.ParseFloat(ctrStr, 64); err == nil {
				entry.CTR = v / 100.0
			}
		}
		if posIdx >= 0 && posIdx < len(row) {
			entry.Position, _ = strconv.ParseFloat(strings.TrimSpace(row[posIdx]), 64)
		}

		if entry.Query == "" && entry.PageURL == "" {
			continue
		}

		entry.OpportunityScore = CalculateOpportunityScore(entry.Impressions, entry.Clicks, entry.Position)
		results = append(results, entry)
	}

	return results, nil
}

// SyncGscWithDatabase updates article ranking and opportunity weights in DB.
//
// A GSC export has one row per (page, query) pair, so a page with several
// tracked queries appears several times — this aggregates to the single
// highest OpportunityScore per page (the best keyword opportunity for that
// page) rather than summing across rows, so pages with many tracked queries
// don't get an inflated score purely for having more rows.
func SyncGscWithDatabase(db *storage.DB, entries []GscEntry) (int, error) {
	bestByURL := make(map[string]GscEntry)
	for _, entry := range entries {
		if entry.PageURL == "" {
			continue
		}
		if existing, ok := bestByURL[entry.PageURL]; !ok || entry.OpportunityScore > existing.OpportunityScore {
			bestByURL[entry.PageURL] = entry
		}
	}

	updated := 0
	for url, entry := range bestByURL {
		if err := db.UpdateArticleOpportunityScoreByURL(url, entry.OpportunityScore); err != nil {
			continue
		}
		// Match article by URL suffix or exact match
		pos := int(entry.Position)
		if pos > 0 {
			if err := db.UpdateArticleSerpPositionByURL(url, pos); err != nil {
				continue
			}
		}
		updated++
	}
	return updated, nil
}
