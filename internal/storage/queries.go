package storage

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Proxy queries
// ---------------------------------------------------------------------------

// UpsertProxy inserts a proxy or updates it if the (ip, port, username) pair already exists.
func (db *DB) UpsertProxy(p *Proxy) (int64, error) {
	res, err := db.conn.Exec(
		`INSERT INTO proxies (ip, port, protocol, country, timezone, username, password, api_key_index, active, latency_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?)
		 ON CONFLICT(ip, port, username) DO UPDATE SET
		   protocol=excluded.protocol,
		   country=excluded.country,
		   timezone=excluded.timezone,
		   password=excluded.password,
		   api_key_index=excluded.api_key_index,
		   active=1,
		   latency_ms=excluded.latency_ms`,
		p.IP, p.Port, p.Protocol, p.Country, p.Timezone, p.Username, p.Password, p.APIKeyIndex, p.LatencyMs,
	)
	if err != nil {
		return 0, fmt.Errorf("upsert proxy %s:%d:%s: %w", p.IP, p.Port, p.Username, err)
	}

	// Query it back for correctness.
	var id int64
	err = db.conn.QueryRow(
		`SELECT id FROM proxies WHERE ip=? AND port=? AND username=?`, p.IP, p.Port, p.Username,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("lookup proxy id: %w", err)
	}
	_ = res
	return id, nil
}

// MarkProxyUsed increments the used_count and sets last_used_at.
func (db *DB) MarkProxyUsed(proxyID int64) error {
	_, err := db.conn.Exec(
		`UPDATE proxies SET used_count = used_count + 1, last_used_at = ? WHERE id = ?`,
		time.Now(), proxyID,
	)
	return err
}

// BlacklistProxy permanently disables a proxy (e.g. triggered a CAPTCHA).
func (db *DB) BlacklistProxy(proxyID int64, reason string) error {
	_, err := db.conn.Exec(
		`UPDATE proxies SET blacklisted = 1, blacklist_reason = ?, active = 0 WHERE id = ?`,
		reason, proxyID,
	)
	return err
}

// BlacklistedProxyIDs returns the set of proxy IDs currently permanently
// blacklisted. Used at refresh time so a DB-persisted ban (e.g. repeated
// CAPTCHA hits) keeps a proxy out of rotation even across a process restart,
// when the pool's in-memory blacklist map starts empty.
func (db *DB) BlacklistedProxyIDs() (map[int64]bool, error) {
	rows, err := db.conn.Query(`SELECT id FROM proxies WHERE blacklisted = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make(map[int64]bool)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

// DeactivateProxy marks a proxy inactive without blacklisting (e.g. failed health check).
func (db *DB) DeactivateProxy(proxyID int64) error {
	_, err := db.conn.Exec(
		`UPDATE proxies SET active = 0 WHERE id = ?`, proxyID,
	)
	return err
}

// ActiveProxyCount returns the number of healthy, non-blacklisted proxies.
func (db *DB) ActiveProxyCount() (int, error) {
	var count int
	err := db.conn.QueryRow(
		`SELECT COUNT(*) FROM proxies WHERE active = 1 AND blacklisted = 0`,
	).Scan(&count)
	return count, err
}

// ActiveProxies returns all healthy, non-blacklisted proxies.
func (db *DB) ActiveProxies() ([]Proxy, error) {
	rows, err := db.conn.Query(
		`SELECT id, ip, port, protocol, country, timezone, username, password, active, latency_ms,
		        used_count, last_used_at, blacklisted, blacklist_reason, created_at
		 FROM proxies WHERE active = 1 AND blacklisted = 0`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanProxies(rows)
}

// AllProxies returns every proxy regardless of state.
func (db *DB) AllProxies() ([]Proxy, error) {
	rows, err := db.conn.Query(
		`SELECT id, ip, port, protocol, country, timezone, username, password, active, latency_ms,
		        used_count, last_used_at, blacklisted, blacklist_reason, created_at
		 FROM proxies`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanProxies(rows)
}

func scanProxies(rows *sql.Rows) ([]Proxy, error) {
	var out []Proxy
	for rows.Next() {
		var p Proxy
		var active, blacklisted int
		if err := rows.Scan(
			&p.ID, &p.IP, &p.Port, &p.Protocol, &p.Country, &p.Timezone,
			&p.Username, &p.Password,
			&active, &p.LatencyMs, &p.UsedCount, &p.LastUsedAt,
			&blacklisted, &p.BlacklistReason, &p.CreatedAt,
		); err != nil {
			return nil, err
		}
		p.Active = active == 1
		p.Blacklisted = blacklisted == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Article queries
// ---------------------------------------------------------------------------

// UpsertArticle inserts a new article or updates title/meta_desc/topic on conflict.
func (db *DB) UpsertArticle(a *Article) (int64, error) {
	res, err := db.conn.Exec(
		`INSERT INTO articles (domain, url, title, meta_desc, topic)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(url) DO UPDATE SET
		   title=excluded.title,
		   meta_desc=excluded.meta_desc,
		   topic=excluded.topic`,
		a.Domain, a.URL, a.Title, a.MetaDesc, a.Topic,
	)
	if err != nil {
		return 0, fmt.Errorf("upsert article %s: %w", a.URL, err)
	}

	var id int64
	err = db.conn.QueryRow(`SELECT id FROM articles WHERE url=?`, a.URL).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("lookup article id: %w", err)
	}
	_ = res
	return id, nil
}

// AllArticles returns every article in the database.
func (db *DB) AllArticles() ([]Article, error) {
	rows, err := db.conn.Query(
		`SELECT id, domain, url, title, meta_desc, topic,
		        searched_count, last_searched_at, first_searched_at,
		        serp_position, COALESCE(opportunity_score, 0), created_at
		 FROM articles`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Article
	for rows.Next() {
		var a Article
		if err := rows.Scan(
			&a.ID, &a.Domain, &a.URL, &a.Title, &a.MetaDesc, &a.Topic,
			&a.SearchedCount, &a.LastSearchedAt, &a.FirstSearchedAt,
			&a.SerpPosition, &a.OpportunityScore, &a.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// EligibleArticles returns articles that have not reached their search cap.
// maxSearch is the regular cap; newArticleBoost applies to articles created
// within the last 7 days.
func (db *DB) EligibleArticles(regularMax, newArticleBoost int) ([]Article, error) {
	rows, err := db.conn.Query(
		`SELECT id, domain, url, title, meta_desc, topic,
		        searched_count, last_searched_at, first_searched_at,
		        serp_position, created_at
		 FROM articles
		 WHERE searched_count < ?
		    OR (created_at >= datetime('now', '-7 days') AND searched_count < ?)`,
		regularMax, newArticleBoost,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Article
	for rows.Next() {
		var a Article
		if err := rows.Scan(
			&a.ID, &a.Domain, &a.URL, &a.Title, &a.MetaDesc, &a.Topic,
			&a.SearchedCount, &a.LastSearchedAt, &a.FirstSearchedAt,
			&a.SerpPosition, &a.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// MarkArticleSearched increments searched_count, sets last_searched_at, and
// records the first_searched_at on the initial search. Also stores the SERP position.
func (db *DB) MarkArticleSearched(articleID int64, serpPosition int) error {
	now := time.Now()
	_, err := db.conn.Exec(
		`UPDATE articles
		 SET searched_count = searched_count + 1,
		     last_searched_at = ?,
		     first_searched_at = COALESCE(first_searched_at, ?),
		     serp_position = ?
		 WHERE id = ?`,
		now, now, serpPosition, articleID,
	)
	return err
}

// UpdateArticleSerpPositionByURL updates the SERP rank of an article matching URL.
func (db *DB) UpdateArticleSerpPositionByURL(rawURL string, serpPosition int) error {
	_, err := db.conn.Exec(
		`UPDATE articles
		 SET serp_position = ?
		 WHERE url = ? OR url LIKE ?`,
		serpPosition, rawURL, "%"+strings.TrimPrefix(rawURL, "https://")+"%",
	)
	return err
}

// UpdateArticleOpportunityScoreByURL updates the GSC-derived opportunity score
// of an article matching URL (see gsc.CalculateOpportunityScore).
func (db *DB) UpdateArticleOpportunityScoreByURL(rawURL string, score float64) error {
	_, err := db.conn.Exec(
		`UPDATE articles
		 SET opportunity_score = ?
		 WHERE url = ? OR url LIKE ?`,
		score, rawURL, "%"+strings.TrimPrefix(rawURL, "https://")+"%",
	)
	return err
}

// ArticleByID retrieves an article by its ID.
func (db *DB) ArticleByID(id int64) (*Article, error) {
	var a Article
	err := db.conn.QueryRow(
		`SELECT id, domain, url, title, meta_desc, topic,
		        searched_count, last_searched_at, first_searched_at,
		        serp_position, created_at
		 FROM articles WHERE id=?`, id,
	).Scan(
		&a.ID, &a.Domain, &a.URL, &a.Title, &a.MetaDesc, &a.Topic,
		&a.SearchedCount, &a.LastSearchedAt, &a.FirstSearchedAt,
		&a.SerpPosition, &a.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ---------------------------------------------------------------------------
// Task queries
// ---------------------------------------------------------------------------

// CreateTask inserts a new task row and returns its ID.
func (db *DB) CreateTask(articleID, proxyID int64, engine string) (int64, error) {
	res, err := db.conn.Exec(
		`INSERT INTO tasks (article_id, proxy_id, engine, status)
		 VALUES (?, ?, ?, 'pending')`,
		articleID, proxyID, engine,
	)
	if err != nil {
		return 0, fmt.Errorf("create task: %w", err)
	}
	return res.LastInsertId()
}

// UpdateTaskResult marks a task complete and stores the result JSON / error.
func (db *DB) UpdateTaskResult(taskID int64, status, resultJSON, errMsg string) error {
	_, err := db.conn.Exec(
		`UPDATE tasks SET status=?, result_json=?, error=?, completed_at=?
		 WHERE id=?`,
		status, resultJSON, errMsg, time.Now(), taskID,
	)
	return err
}

// TodayTaskCount returns the number of tasks created today.
func (db *DB) TodayTaskCount() (int, error) {
	var count int
	err := db.conn.QueryRow(
		`SELECT COUNT(*) FROM tasks
		 WHERE date(created_at) = date('now')`,
	).Scan(&count)
	return count, err
}

// TodayCaptchaCount returns the number of CAPTCHA hits today.
func (db *DB) TodayCaptchaCount() (int, error) {
	var count int
	err := db.conn.QueryRow(
		`SELECT COUNT(*) FROM tasks
		 WHERE status='captcha' AND date(created_at) = date('now')`,
	).Scan(&count)
	return count, err
}

// TodayCaptchaCountByEngine returns CAPTCHA hits today for a specific engine.
func (db *DB) TodayCaptchaCountByEngine(engine string) (int, error) {
	var count int
	err := db.conn.QueryRow(
		`SELECT COUNT(*) FROM tasks
		 WHERE status='captcha' AND engine=? AND date(created_at) = date('now')`,
		engine,
	).Scan(&count)
	return count, err
}

// TodayTaskCountByEngine returns task count today for a specific engine.
func (db *DB) TodayTaskCountByEngine(engine string) (int, error) {
	var count int
	err := db.conn.QueryRow(
		`SELECT COUNT(*) FROM tasks
		 WHERE engine=? AND date(created_at) = date('now')`,
		engine,
	).Scan(&count)
	return count, err
}

// TodayTaskCountByDomain returns task count today for a specific domain.
func (db *DB) TodayTaskCountByDomain(domain string) (int, error) {
	var count int
	err := db.conn.QueryRow(
		`SELECT COUNT(*) FROM tasks t
		 JOIN articles a ON a.id = t.article_id
		 WHERE a.domain=? AND date(t.created_at) = date('now')`,
		domain,
	).Scan(&count)
	return count, err
}

// TodaySuccessCount returns the number of successful tasks today.
func (db *DB) TodaySuccessCount() (int, error) {
	var count int
	err := db.conn.QueryRow(
		`SELECT COUNT(*) FROM tasks
		 WHERE status='success' AND date(created_at) = date('now')`,
	).Scan(&count)
	return count, err
}

// TodayFailCount returns the number of failed tasks today.
func (db *DB) TodayFailCount() (int, error) {
	var count int
	err := db.conn.QueryRow(
		`SELECT COUNT(*) FROM tasks
		 WHERE status='fail' AND date(created_at) = date('now')`,
	).Scan(&count)
	return count, err
}

// ResetDailyProxyUsage resets used_count for all active proxies at midnight.
// This does NOT un-blacklist proxies — CAPTCHA bans are permanent.
func (db *DB) ResetDailyProxyUsage() error {
	_, err := db.conn.Exec(
		`UPDATE proxies SET used_count = 0 WHERE active = 1 AND blacklisted = 0`,
	)
	return err
}

// ---------------------------------------------------------------------------
// Daily stats queries
// ---------------------------------------------------------------------------

// UpdateDailyStats recomputes and upserts today's daily_stats row.
func (db *DB) UpdateDailyStats() error {
	_, err := db.conn.Exec(
		`INSERT INTO daily_stats (date, total_search, success, fail, captcha,
		                          avg_dwell_seconds, avg_serp_position)
		 SELECT
		   date('now') AS date,
		   COUNT(*) AS total_search,
		   SUM(CASE WHEN status='success' THEN 1 ELSE 0 END) AS success,
		   SUM(CASE WHEN status='fail' THEN 1 ELSE 0 END) AS fail,
		   SUM(CASE WHEN status='captcha' THEN 1 ELSE 0 END) AS captcha,
		   AVG(CASE WHEN status='success'
		            THEN CAST(json_extract(result_json, '$.dwell_time_seconds') AS REAL)
		            ELSE NULL END) AS avg_dwell_seconds,
		   AVG(CASE WHEN status='success' AND json_extract(result_json, '$.serp_position') > 0
		            THEN CAST(json_extract(result_json, '$.serp_position') AS REAL)
		            ELSE NULL END) AS avg_serp_position
		 FROM tasks
		 WHERE date(created_at) = date('now')
		 ON CONFLICT(date) DO UPDATE SET
		   total_search=excluded.total_search,
		   success=excluded.success,
		   fail=excluded.fail,
		   captcha=excluded.captcha,
		   avg_dwell_seconds=excluded.avg_dwell_seconds,
		   avg_serp_position=excluded.avg_serp_position`,
	)
	return err
}

// DailyStatsFor returns the daily_stats row for a given date string (YYYY-MM-DD).
func (db *DB) DailyStatsFor(date string) (*DailyStats, error) {
	var ds DailyStats
	err := db.conn.QueryRow(
		`SELECT date, total_search, success, fail, captcha,
		        avg_dwell_seconds, avg_serp_position
		 FROM daily_stats WHERE date=?`, date,
	).Scan(&ds.Date, &ds.TotalSearch, &ds.Success, &ds.Fail,
		&ds.Captcha, &ds.AvgDwellSeconds, &ds.AvgSerpPosition)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ds, nil
}
