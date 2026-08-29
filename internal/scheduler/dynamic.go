// Package scheduler implements dynamic throttling, time-of-day awareness,
// cooldowns, and CAPTCHA-pause logic for the search-automation orchestrator.
package scheduler

import (
	"fmt"
	"math/rand"
	"time"

	"google-automation/internal/config"
	"google-automation/internal/proxy"
	"google-automation/internal/storage"
)

// Scheduler controls the main task loop. It decides when to run a search,
// which proxy and article to use, and when to pause.
type Scheduler struct {
	cfg   *config.Config
	db    *storage.DB
	pool  *proxy.Pool
	mgr   *proxy.Manager

	// pausedUntil is set when a CAPTCHA spike is detected (global pause).
	pausedUntil time.Time

	// enginePausedUntil tracks per-engine CAPTCHA pauses.
	// If Google hits CAPTCHA, only Google is paused — Bing keeps running.
	enginePausedUntil map[string]time.Time

	// captchaCountToday tracks CAPTCHAs seen today (refreshed from DB).
	captchaCountToday int
	totalToday        int
}

// New creates a scheduler wired to the proxy manager and database.
func New(cfg *config.Config, db *storage.DB, mgr *proxy.Manager) *Scheduler {
	return &Scheduler{
		cfg:               cfg,
		db:                db,
		pool:              mgr.Pool(),
		mgr:               mgr,
		enginePausedUntil: make(map[string]time.Time),
	}
}

// MaxSearchesToday computes the dynamic throttle: the number of active proxies
// multiplied by searches_per_proxy caps the maximum searches for the day.
func (s *Scheduler) MaxSearchesToday() int {
	count, err := s.db.ActiveProxyCount()
	if err != nil {
		return 0
	}
	perProxy := s.cfg.Scheduler.MaxSearchPerProxy
	if perProxy <= 0 {
		perProxy = 10 // default: 10 searches per proxy per day
	}
	return count * perProxy
}

// ShouldRun decides whether the scheduler should execute a search right now.
// Returns (true, "") if OK, or (false, reason) if it should wait/sleep.
func (s *Scheduler) ShouldRun() (bool, string) {
	// 1. Check CAPTCHA pause.
	if time.Now().Before(s.pausedUntil) {
		remaining := s.pausedUntil.Sub(time.Now())
		return false, fmt.Sprintf("CAPTCHA pause: %v remaining", remaining.Round(time.Minute))
	}

	// 2. Check proxy availability — dynamic throttle.
	if s.pool.AvailableCount() == 0 {
		return false, "no proxies available in current cycle (waiting for refresh)"
	}

	// 3. Check today's search cap against active proxy count.
	todayCount, _ := s.db.TodayTaskCount()
	maxToday := s.MaxSearchesToday()
	if maxToday > 0 && todayCount >= maxToday {
		return false, fmt.Sprintf("daily cap reached: %d/%d searches", todayCount, maxToday)
	}

	return true, ""
}

// CooldownDuration returns a random duration between min and max cooldown seconds.
// This is the inter-task pause that prevents rapid-fire searches.
func (s *Scheduler) CooldownDuration() time.Duration {
	min := s.cfg.Scheduler.MinCooldownSeconds
	max := s.cfg.Scheduler.MaxCooldownSeconds
	if max <= min {
		return time.Duration(min) * time.Second
	}
	secs := min + rand.Intn(max-min)
	return time.Duration(secs) * time.Second
}

// PostExitCooldownDuration returns a random post-exit cooldown (simulating a
// human's attention shift after reading an article).
func (s *Scheduler) PostExitCooldownDuration() time.Duration {
	min := s.cfg.Scheduler.PostExitCooldownMin
	max := s.cfg.Scheduler.PostExitCooldownMax
	if max <= min {
		return time.Duration(min) * time.Second
	}
	secs := min + rand.Intn(max-min)
	return time.Duration(secs) * time.Second
}

// CheckCaptchaRate evaluates whether the CAPTCHA rate for any engine exceeds
// the threshold (default 10%). Only pauses the affected engine, not all engines.
func (s *Scheduler) CheckCaptchaRate() bool {
	paused := false
	for _, engine := range []string{"google", "bing"} {
		taskCount, _ := s.db.TodayTaskCountByEngine(engine)
		if taskCount < 5 {
			continue // not enough data for this engine
		}
		captchaCount, _ := s.db.TodayCaptchaCountByEngine(engine)
		rate := float64(captchaCount) / float64(taskCount)
		if rate > 0.10 {
			pauseHours := s.cfg.Scheduler.CaptchaPauseHours
			jitter := time.Duration(rand.Intn(60)) * time.Minute
			until := time.Now().Add(time.Duration(pauseHours)*time.Hour + jitter)
			s.enginePausedUntil[engine] = until
			fmt.Printf("[scheduler] CAPTCHA rate %s %.1f%% > 10%% — engine paused until %s\n",
				engine, rate*100, until.Format("15:04:05"))
			paused = true
		}
	}
	return paused
}

// TriggerCaptchaPause forces an immediate pause for the configured duration.
// Called when a single task hits a CAPTCHA (conservative approach).
func (s *Scheduler) TriggerCaptchaPause() {
	pauseHours := s.cfg.Scheduler.CaptchaPauseHours
	s.pausedUntil = time.Now().Add(time.Duration(pauseHours) * time.Hour)
	fmt.Printf("[scheduler] CAPTCHA detected — pausing until %s\n",
		s.pausedUntil.Format("15:04:05"))
}

// TriggerEnginePause pauses a specific engine only (e.g. Google hits CAPTCHA
// but Bing keeps running). Falls back to global pause if engine is unknown.
func (s *Scheduler) TriggerEnginePause(engine string) {
	pauseHours := s.cfg.Scheduler.CaptchaPauseHours
	until := time.Now().Add(time.Duration(pauseHours) * time.Hour)
	s.enginePausedUntil[engine] = until
	fmt.Printf("[scheduler] CAPTCHA on %s — engine paused until %s (other engines unaffected)\n",
		engine, until.Format("15:04:05"))
}

// IsEnginePaused returns true if the given engine is currently in CAPTCHA pause.
func (s *Scheduler) IsEnginePaused(engine string) bool {
	until, ok := s.enginePausedUntil[engine]
	if !ok {
		return false
	}
	return time.Now().Before(until)
}

// PickEngineAvailable selects an engine based on ratio, but skips engines
// that are currently paused. Falls back to whichever engine is available.
// Returns empty string if all engines are paused.
func (s *Scheduler) PickEngineAvailable() string {
	googlePaused := s.IsEnginePaused("google")
	bingPaused := s.IsEnginePaused("bing")

	if googlePaused && bingPaused {
		return "" // all paused — let ShouldRun handle it
	}
	if googlePaused {
		return "bing"
	}
	if bingPaused {
		return "google"
	}
	// Both available — use configured ratio
	roll := rand.Intn(s.cfg.EngineRatio.Google + s.cfg.EngineRatio.Bing)
	if roll < s.cfg.EngineRatio.Google {
		return "google"
	}
	return "bing"
}

// DailyReset clears all per-engine CAPTCHA pauses and the global pause at
// midnight. Called automatically by the orchestrator's daily reset goroutine.
func (s *Scheduler) DailyReset() {
	s.pausedUntil = time.Time{}
	for engine := range s.enginePausedUntil {
		s.enginePausedUntil[engine] = time.Time{}
	}
	fmt.Println("[scheduler] daily reset: cleared all engine CAPTCHA pauses")
}

// PickEngine selects a search engine based on the configured ratio (Google 70 / Bing 30).
func (s *Scheduler) PickEngine() string {
	roll := rand.Intn(s.cfg.EngineRatio.Google + s.cfg.EngineRatio.Bing)
	if roll < s.cfg.EngineRatio.Google {
		return "google"
	}
	return "bing"
}

// IsProxyWithinActiveHours checks whether the current time falls within the
// proxy's active search window (default 7am-11pm in the proxy's local timezone).
func (s *Scheduler) IsProxyWithinActiveHours(px proxy.PooledProxy) bool {
	if px.Timezone == "" {
		return true // unknown timezone — allow by default
	}

	loc, err := time.LoadLocation(px.Timezone)
	if err != nil {
		return true // can't parse timezone — allow
	}

	localHour := time.Now().In(loc).Hour()
	start := s.cfg.Scheduler.ActiveHoursStart
	end := s.cfg.Scheduler.ActiveHoursEnd

	return localHour >= start && localHour < end
}

// AcquireUsableProxy loops through the pool until it finds a proxy whose local
// time is within active hours. If all proxies are outside active hours, returns
// (empty, false) so the caller can sleep until earliest active window.
func (s *Scheduler) AcquireUsableProxy() (proxy.PooledProxy, bool) {
	total := s.pool.TotalCount()
	if total == 0 {
		return proxy.PooledProxy{}, false
	}

	// Track proxies we've already tried this round to avoid infinite spin.
	tried := make([]proxy.PooledProxy, 0, total)

	for {
		px, ok := s.pool.Acquire()
		if !ok {
			break
		}

		if s.IsProxyWithinActiveHours(px) {
			// Release the ones we skipped back to the pool.
			for _, skipped := range tried {
				s.pool.Release(skipped)
			}
			return px, true
		}

		// Outside active hours — hold it temporarily.
		tried = append(tried, px)

		// If we've tried every proxy without success, stop.
		if len(tried) >= total {
			break
		}
	}

	// All proxies outside active hours — release them back and report failure.
	for _, skipped := range tried {
		s.pool.Release(skipped)
	}
	return proxy.PooledProxy{}, false
}

// SleepUntilEarliestActiveHour calculates when the earliest proxy will enter
// its active window and returns the sleep duration.
func (s *Scheduler) SleepUntilEarliestActiveHour() time.Duration {
	now := time.Now()
	earliest := time.Now().Add(24 * time.Hour) // start with a far-future sentinel

	for _, px := range s.pool.AllKnown() {
		if px.Timezone == "" {
			continue
		}
		loc, err := time.LoadLocation(px.Timezone)
		if err != nil {
			continue
		}

		localNow := now.In(loc)
		localHour := localNow.Hour()
		start := s.cfg.Scheduler.ActiveHoursStart

		if localHour >= start {
			// Already in active hours — shouldn't happen, but no wait needed.
			return 0
		}

		// Calculate when 7am local time arrives.
		next := time.Date(localNow.Year(), localNow.Month(), localNow.Day(),
			start, 0, 0, 0, loc)
		if next.Before(localNow) {
			next = next.Add(24 * time.Hour)
		}

		wait := next.Sub(localNow)
		if wait < earliest.Sub(now) {
			earliest = now.Add(wait)
		}
	}

	dur := earliest.Sub(now)
	if dur <= 0 || dur > 24*time.Hour {
		// Fallback: sleep 1 hour and re-check.
		return time.Hour
	}
	return dur
}
