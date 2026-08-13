package scheduler

import (
	"fmt"
	"math/rand"
	"time"

	"google-automation/internal/config"
	"google-automation/internal/storage"
)

// CooldownManager handles cooldown state between tasks and CAPTCHA pauses.
// It works alongside the Scheduler to track when the system should pause.
type CooldownManager struct {
	cfg *config.SchedulerConfig
	db  *storage.DB

	// lastTaskEnd records when the last task completed (for post-exit cooldown).
	lastTaskEnd time.Time

	// consecutiveFailures tracks back-to-back failures (for backoff).
	consecutiveFailures int
}

// NewCooldownManager creates a cooldown manager.
func NewCooldownManager(cfg *config.SchedulerConfig, db *storage.DB) *CooldownManager {
	return &CooldownManager{cfg: cfg, db: db}
}

// RecordTaskEnd marks the end of a task and resets failure counter on success.
func (cm *CooldownManager) RecordTaskEnd(success bool) {
	cm.lastTaskEnd = time.Now()
	if success {
		cm.consecutiveFailures = 0
	} else {
		cm.consecutiveFailures++
	}
}

// PostExitCooldown returns the duration to wait after a task's engagement phase.
// This simulates a human's natural attention shift before the next search.
func (cm *CooldownManager) PostExitCooldown() time.Duration {
	min := cm.cfg.PostExitCooldownMin
	max := cm.cfg.PostExitCooldownMax
	if max <= min {
		return time.Duration(min) * time.Second
	}
	secs := min + rand.Intn(max-min)
	return time.Duration(secs) * time.Second
}

// InterTaskCooldown returns the cooldown between tasks (30-120s configured).
// On consecutive failures, applies exponential backoff up to 10x.
func (cm *CooldownManager) InterTaskCooldown() time.Duration {
	min := cm.cfg.MinCooldownSeconds
	max := cm.cfg.MaxCooldownSeconds
	if max <= min {
		return time.Duration(min) * time.Second
	}
	secs := min + rand.Intn(max-min)

	// Exponential backoff on consecutive failures.
	if cm.consecutiveFailures > 0 {
		backoff := 1 << cm.consecutiveFailures // 2^N
		if backoff > 10 {
			backoff = 10
		}
		secs *= backoff
	}

	return time.Duration(secs) * time.Second
}

// ShouldBackoff returns true if there have been too many consecutive failures
// and the system should pause longer.
func (cm *CooldownManager) ShouldBackoff() bool {
	return cm.consecutiveFailures >= 3
}

// BackoffDuration returns a longer pause when consecutive failures pile up.
func (cm *CooldownManager) BackoffDuration() time.Duration {
	// 5 minutes per consecutive failure, capped at 30 minutes.
	mins := cm.consecutiveFailures * 5
	if mins > 30 {
		mins = 30
	}
	return time.Duration(mins) * time.Minute
}

// IsWithinActiveHours checks if the given hour is within the configured active window.
func (cm *CooldownManager) IsWithinActiveHours(hour int) bool {
	return hour >= cm.cfg.ActiveHoursStart && hour < cm.cfg.ActiveHoursEnd
}

// String returns a human-readable status of the cooldown manager.
func (cm *CooldownManager) String() string {
	return fmt.Sprintf("cooldown{failures=%d, lastTaskEnd=%s}",
		cm.consecutiveFailures, cm.lastTaskEnd.Format("15:04:05"))
}
