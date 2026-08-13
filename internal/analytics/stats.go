// Package analytics computes success/fail rates, dwell time averages,
// SERP position tracking, and proxy performance leaderboards.
package analytics

import (
	"fmt"
	"log"
	"time"

	"google-automation/internal/storage"
)

// Stats provides aggregate analytics computed from the tasks and daily_stats tables.
type Stats struct {
	db *storage.DB
}

// NewStats creates a stats computer.
func NewStats(db *storage.DB) *Stats {
	return &Stats{db: db}
}

// Summary holds a snapshot of today's key metrics.
type Summary struct {
	Date            string
	TotalSearch     int
	Success         int
	Fail            int
	Captcha         int
	SuccessRate     float64
	CaptchaRate     float64
	AvgDwellSeconds float64
	AvgSerpPosition float64
}

// TodaySummary computes the current day's summary from the database.
func (s *Stats) TodaySummary() (*Summary, error) {
	total, err := s.db.TodayTaskCount()
	if err != nil {
		return nil, err
	}
	success, _ := s.db.TodaySuccessCount()
	fail, _ := s.db.TodayFailCount()
	captcha, _ := s.db.TodayCaptchaCount()

	sum := &Summary{
		Date:        time.Now().Format("2006-01-02"),
		TotalSearch: total,
		Success:     success,
		Fail:        fail,
		Captcha:     captcha,
	}
	if total > 0 {
		sum.SuccessRate = float64(success) / float64(total) * 100
		sum.CaptchaRate = float64(captcha) / float64(total) * 100
	}

	// Try to pull from daily_stats for the averages (updated after each task).
	ds, err := s.db.DailyStatsFor(sum.Date)
	if err != nil {
		return nil, err
	}
	if ds != nil {
		sum.AvgDwellSeconds = ds.AvgDwellSeconds
		sum.AvgSerpPosition = ds.AvgSerpPosition
	}

	return sum, nil
}

// PrintDailyReport logs a human-readable summary of today's metrics.
func (s *Stats) PrintDailyReport() {
	summary, err := s.TodaySummary()
	if err != nil {
		log.Printf("[analytics] failed to compute summary: %v", err)
		return
	}

	fmt.Println("┌─────────────────────────────────────────────┐")
	fmt.Printf("│  Daily Report — %s                  │\n", summary.Date)
	fmt.Println("├─────────────────────────────────────────────┤")
	fmt.Printf("│  Total searches : %-26d│\n", summary.TotalSearch)
	fmt.Printf("│  Successful     : %-26d│\n", summary.Success)
	fmt.Printf("│  Failed         : %-26d│\n", summary.Fail)
	fmt.Printf("│  CAPTCHAs       : %-26d│\n", summary.Captcha)
	fmt.Printf("│  Success rate   : %-25.1f%%│\n", summary.SuccessRate)
	fmt.Printf("│  CAPTCHA rate   : %-25.1f%%│\n", summary.CaptchaRate)
	fmt.Printf("│  Avg dwell (s)  : %-26.1f│\n", summary.AvgDwellSeconds)
	fmt.Printf("│  Avg SERP pos   : %-26.1f│\n", summary.AvgSerpPosition)
	fmt.Println("└─────────────────────────────────────────────┘")
}

// HistoricalTrend returns the last N days of daily stats for trend analysis.
func (s *Stats) HistoricalTrend(days int) ([]storage.DailyStats, error) {
	rows, err := s.db.Conn().Query(
		`SELECT date, total_search, success, fail, captcha,
		        avg_dwell_seconds, avg_serp_position
		 FROM daily_stats
		 WHERE date >= date('now', ?)
		 ORDER BY date DESC`,
		fmt.Sprintf("-%d days", days),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []storage.DailyStats
	for rows.Next() {
		var ds storage.DailyStats
		if err := rows.Scan(
			&ds.Date, &ds.TotalSearch, &ds.Success, &ds.Fail,
			&ds.Captcha, &ds.AvgDwellSeconds, &ds.AvgSerpPosition,
		); err != nil {
			return nil, err
		}
		out = append(out, ds)
	}
	return out, rows.Err()
}
