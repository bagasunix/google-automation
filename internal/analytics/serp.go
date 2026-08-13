package analytics

import (
	"fmt"
	"log"

	"google-automation/internal/storage"
)

// ProxyLeaderboardEntry represents one proxy's performance summary.
type ProxyLeaderboardEntry struct {
	ProxyID    int64
	IP         string
	Port       int
	Country    string
	UsedCount  int
	SuccessCount int
	FailCount  int
	CaptchaCount int
	SuccessRate float64
}

// SerpTracker tracks SERP position outcomes per article.
type SerpTracker struct {
	db *storage.DB
}

// NewSerpTracker creates a SERP position tracker.
func NewSerpTracker(db *storage.DB) *SerpTracker {
	return &SerpTracker{db: db}
}

// ProxyLeaderboard computes the success/fail/captcha counts per proxy
// from the tasks table, sorted by success rate descending.
func (s *Stats) ProxyLeaderboard(limit int) ([]ProxyLeaderboardEntry, error) {
	rows, err := s.db.Conn().Query(
		`SELECT p.id, p.ip, p.port, p.country,
		        COUNT(t.id) AS used_count,
		        SUM(CASE WHEN t.status='success' THEN 1 ELSE 0 END) AS success_count,
		        SUM(CASE WHEN t.status='fail' THEN 1 ELSE 0 END) AS fail_count,
		        SUM(CASE WHEN t.status='captcha' THEN 1 ELSE 0 END) AS captcha_count
		 FROM proxies p
		 LEFT JOIN tasks t ON t.proxy_id = p.id
		 GROUP BY p.id
		 ORDER BY success_count DESC
		 LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ProxyLeaderboardEntry
	for rows.Next() {
		var e ProxyLeaderboardEntry
		if err := rows.Scan(
			&e.ProxyID, &e.IP, &e.Port, &e.Country,
			&e.UsedCount, &e.SuccessCount, &e.FailCount, &e.CaptchaCount,
		); err != nil {
			return nil, err
		}
		if e.UsedCount > 0 {
			e.SuccessRate = float64(e.SuccessCount) / float64(e.UsedCount) * 100
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PrintProxyLeaderboard logs the top proxies by success count.
func (s *Stats) PrintProxyLeaderboard(limit int) {
	entries, err := s.ProxyLeaderboard(limit)
	if err != nil {
		log.Printf("[analytics] proxy leaderboard failed: %v", err)
		return
	}

	fmt.Println("\n┌─── Proxy Leaderboard ────────────────────────────────────┐")
	fmt.Printf("│ %-20s %-6s %-5s %-5s %-5s %-7s │\n",
		"IP:Port", "Used", "OK", "Fail", "CAP", "Rate%")
	fmt.Println("├──────────────────────────────────────────────────────────┤")
	for _, e := range entries {
		fmt.Printf("│ %-20s %-6d %-5d %-5d %-5d %-6.1f%%│\n",
			fmt.Sprintf("%s:%d", e.IP, e.Port),
			e.UsedCount, e.SuccessCount, e.FailCount, e.CaptchaCount, e.SuccessRate)
	}
	fmt.Println("└──────────────────────────────────────────────────────────┘")
}

// ArticleSerpSummary returns the current SERP position for each article that
// has been searched at least once.
type ArticleSerpSummary struct {
	ArticleID    int64
	Title        string
	URL          string
	SearchedCount int
	SerpPosition  int
	LastSearched  string
}

// ArticleSerpPositions returns SERP tracking data for all searched articles.
func (st *SerpTracker) ArticleSerpPositions(limit int) ([]ArticleSerpSummary, error) {
	rows, err := st.db.Conn().Query(
		`SELECT id, title, url, searched_count,
		        COALESCE(serp_position, 0),
		        COALESCE(date(last_searched_at), 'never')
		 FROM articles
		 WHERE searched_count > 0
		 ORDER BY last_searched_at DESC
		 LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ArticleSerpSummary
	for rows.Next() {
		var a ArticleSerpSummary
		if err := rows.Scan(
			&a.ArticleID, &a.Title, &a.URL, &a.SearchedCount,
			&a.SerpPosition, &a.LastSearched,
		); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// PrintSerpReport logs the SERP position tracking summary.
func (st *SerpTracker) PrintSerpReport(limit int) {
	articles, err := st.ArticleSerpPositions(limit)
	if err != nil {
		log.Printf("[analytics] SERP report failed: %v", err)
		return
	}

	fmt.Println("\n┌─── SERP Position Tracking ──────────────────────────────┐")
	fmt.Printf("│ %-40s %-5s %-6s %-12s │\n", "Title", "SRCH", "SERP", "Last")
	fmt.Println("├──────────────────────────────────────────────────────────┤")
	for _, a := range articles {
		title := a.Title
		if len(title) > 38 {
			title = title[:35] + "..."
		}
		serpStr := fmt.Sprintf("%d", a.SerpPosition)
		if a.SerpPosition == 0 {
			serpStr = "N/A"
		}
		fmt.Printf("│ %-40s %-5d %-6s %-12s │\n",
			title, a.SearchedCount, serpStr, a.LastSearched)
	}
	fmt.Println("└──────────────────────────────────────────────────────────┘")
}
