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

	// pausedUntil is set when a CAPTCHA spike is detected.
	pausedUntil time.Time

	// captchaCountToday tracks CAPTCHAs seen today (refreshed from DB).
	captchaCountToday int
	totalToday        int
}

// New creates a scheduler wired to the proxy manager and database.
func New(cfg *config.Config, db *storage.DB, mgr *proxy.Manager) *Scheduler {
	return &Scheduler{
		cfg:  cfg,
		db:   db,
		pool: mgr.Pool(),
		mgr:  mgr,
	}
}

// MaxSearchesToday computes the dynamic throttle: the number of active proxies
// caps the maximum searches for the current cycle.
func (s *Scheduler) MaxSearchesToday() int {
	count, err := s.db.ActiveProxyCount()
	if err != nil {
		return 0
	}
	return count
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

// CheckCaptchaRate evaluates whether the CAPTCHA rate exceeds the threshold
// (default 10%). If so, it sets a pause of 2-4 hours (configured).
func (s *Scheduler) CheckCaptchaRate() bool {
	todayCount, _ := s.db.TodayTaskCount()
	if todayCount < 10 {
		return false // not enough data
	}

	captchaCount, _ := s.db.TodayCaptchaCount()
	rate := float64(captchaCount) / float64(todayCount)

	if rate > 0.10 {
		// Pause for the configured number of hours with a small random jitter.
		pauseHours := s.cfg.Scheduler.CaptchaPauseHours
		jitter := time.Duration(rand.Intn(60)) * time.Minute
		s.pausedUntil = time.Now().Add(time.Duration(pauseHours)*time.Hour + jitter)
		fmt.Printf("[scheduler] CAPTCHA rate %.1f%% > 10%% — pausing until %s\n",
			rate*100, s.pausedUntil.Format("15:04:05"))
		return true
	}
	return false
}

// TriggerCaptchaPause forces an immediate pause for the configured duration.
// Called when a single task hits a CAPTCHA (conservative approach).
func (s *Scheduler) TriggerCaptchaPause() {
	pauseHours := s.cfg.Scheduler.CaptchaPauseHours
	s.pausedUntil = time.Now().Add(time.Duration(pauseHours) * time.Hour)
	fmt.Printf("[scheduler] CAPTCHA detected — pausing until %s\n",
		s.pausedUntil.Format("15:04:05"))
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
// time is within active hours. If all proxies are outside active hours, it
// calculates how long to sleep until the earliest proxy hits its 7am window.
// Returns the proxy and nil on success, or empty proxy and a wait duration.
func (s *Scheduler) AcquireUsableProxy() (proxy.PooledProxy, bool) {
	for {
		px, ok := s.pool.Acquire()
		if !ok {
			return proxy.PooledProxy{}, false
		}

		if s.IsProxyWithinActiveHours(px) {
			return px, true
		}

		// Proxy is outside active hours — release it back and try the next one.
		// But track it so we don't spin forever.
		s.pool.Release(px)
	}

	// If we exhaust the loop, all proxies are outside active hours.
	// The caller should call WaitForEarliestActiveHour().
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
