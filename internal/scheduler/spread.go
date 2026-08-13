package scheduler

import (
	"fmt"
	"time"

	"google-automation/internal/storage"
)

// SpreadTracker helps ensure that an article's searches are spread across
// different days rather than clustered on the same day.
type SpreadTracker struct {
	db *storage.DB
}

// NewSpreadTracker creates a spread tracker.
func NewSpreadTracker(db *storage.DB) *SpreadTracker {
	return &SpreadTracker{db: db}
}

// IsSpreadOK checks whether an article was already searched today.
// If it was, we skip it to enforce the "spread across different days" rule.
func (st *SpreadTracker) IsSpreadOK(articleID int64) bool {
	// Check if the article was searched today.
	var count int
	err := st.db.Conn().QueryRow(
		`SELECT COUNT(*) FROM tasks
		 WHERE article_id = ? AND date(created_at) = date('now')`,
		articleID,
	).Scan(&count)
	if err != nil {
		// On error, allow the search (fail-open).
		return true
	}
	return count == 0
}

// DaysSinceFirstSearch returns how many days since the article was first searched.
func (st *SpreadTracker) DaysSinceFirstSearch(articleID int64) int {
	article, err := st.getArticle(articleID)
	if err != nil || !article.FirstSearchedAt.Valid {
		return 0
	}
	return int(time.Since(article.FirstSearchedAt.Time).Hours() / 24)
}

func (st *SpreadTracker) getArticle(articleID int64) (*storage.Article, error) {
	var a storage.Article
	err := st.db.Conn().QueryRow(
		`SELECT id, domain, url, title, meta_desc, topic,
		        searched_count, last_searched_at, first_searched_at,
		        serp_position, created_at
		 FROM articles WHERE id=?`, articleID,
	).Scan(
		&a.ID, &a.Domain, &a.URL, &a.Title, &a.MetaDesc, &a.Topic,
		&a.SearchedCount, &a.LastSearchedAt, &a.FirstSearchedAt,
		&a.SerpPosition, &a.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("get article %d: %w", articleID, err)
	}
	return &a, nil
}
