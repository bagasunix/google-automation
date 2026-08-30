package storage

import (
	"database/sql"
	"time"
)

// Proxy represents a row in the proxies table.
type Proxy struct {
	ID              int64
	IP              string
	Port            int
	Protocol        string
	Country         string
	Timezone        string
	Username        string
	Password        string
	APIKeyIndex     int
	Active          bool
	LatencyMs       int
	UsedCount       int
	LastUsedAt      sql.NullTime
	Blacklisted     bool
	BlacklistReason string
	CreatedAt       time.Time
}

// Article represents a row in the articles table.
type Article struct {
	ID               int64
	Domain           string
	URL              string
	Title            string
	MetaDesc         string
	Topic            string
	SearchedCount    int
	LastSearchedAt   sql.NullTime
	FirstSearchedAt  sql.NullTime
	SerpPosition     sql.NullInt64
	OpportunityScore float64
	CreatedAt        time.Time
}

// Task represents a row in the tasks table.
type Task struct {
	ID          int64
	ArticleID   int64
	ProxyID     int64
	Engine      string
	Status      string
	ResultJSON  string
	Error       string
	CreatedAt   time.Time
	CompletedAt sql.NullTime
}

// DailyStats represents a row in the daily_stats table.
type DailyStats struct {
	Date            string
	TotalSearch     int
	Success         int
	Fail            int
	Captcha         int
	AvgDwellSeconds float64
	AvgSerpPosition float64
}
