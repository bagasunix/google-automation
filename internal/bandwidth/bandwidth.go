// Package bandwidth tracks proxy bandwidth usage per Webshare API key.
// Webshare free plan = 1GB per key per month; exceeding it makes every proxy
// behind that key return HTTP 402 until the next month.
// Usage is persisted to data/bandwidth.json — the same file the Python worker
// writes, so Go and Python see one consistent view.
package bandwidth

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	bandwidthFile = "data/bandwidth.json"
	defaultLimitMB = 1024
	defaultPauseThresholdPercent = 95
)

// Tracker tracks bandwidth usage for all API keys in the current month.
// Thread-safe; persists to disk on every update.
type Tracker struct {
	mu       sync.Mutex
	filePath string
	limitKB  float64
	pausePct float64

	// entries[keyIndex] = month usage (only current-month entries are kept)
	entries map[string]*entry
}

type entry struct {
	UsedKB     float64 `json:"used_kb"`
	TasksCount int     `json:"tasks_count"`
	LastUpdated float64 `json:"last_updated"`
}

// NewTracker creates a tracker rooted at the project base dir.
// limitMB and pausePercent mirror the Python-side config values.
func NewTracker(baseDir string, limitMB int, pausePercent int) *Tracker {
	if limitMB <= 0 {
		limitMB = defaultLimitMB
	}
	if pausePercent <= 0 || pausePercent > 100 {
		pausePercent = defaultPauseThresholdPercent
	}

	t := &Tracker{
		filePath: filepath.Join(baseDir, bandwidthFile),
		limitKB:  float64(limitMB) * 1024,
		pausePct: float64(pausePercent),
		entries:  make(map[string]*entry),
	}
	t.load()
	return t
}

// currentMonthKey returns "YYYY-MM" in UTC, matching the Python implementation.
func currentMonthKey() string {
	return time.Now().UTC().Format("2006-01")
}

// storageKey returns the JSON key for a given API key index in the current month.
func storageKey(apiKeyIndex int) string {
	return fmt.Sprintf("key%d_%s", apiKeyIndex, currentMonthKey())
}

// load reads persisted usage from disk, keeping only current-month entries.
func (t *Tracker) load() {
	data, err := os.ReadFile(t.filePath)
	if err != nil {
		return // fresh start
	}

	var raw map[string]entry
	if err := json.Unmarshal(data, &raw); err != nil {
		log.Printf("[bandwidth] failed to parse %s: %v (starting fresh)", t.filePath, err)
		return
	}

	month := currentMonthKey()
	for k, v := range raw {
		// Drop stale months so the file never grows unbounded.
		if len(k) >= 7 && k[len(k)-7:] != month {
			continue
		}
		e := v
		t.entries[k] = &e
	}
}

// save persists all current-month entries to disk.
func (t *Tracker) save() {
	raw := make(map[string]entry, len(t.entries))
	for k, v := range t.entries {
		raw[k] = *v
	}

	if err := os.MkdirAll(filepath.Dir(t.filePath), 0o755); err != nil {
		log.Printf("[bandwidth] mkdir failed: %v", err)
		return
	}

	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(t.filePath, data, 0o644); err != nil {
		log.Printf("[bandwidth] write failed: %v", err)
	}
}

// RecordUsage adds usedKB to the given API key's monthly total and persists.
func (t *Tracker) RecordUsage(apiKeyIndex int, usedKB float64) {
	if usedKB <= 0 {
		return
	}

	t.mu.Lock()
	k := storageKey(apiKeyIndex)
	e, ok := t.entries[k]
	if !ok {
		e = &entry{}
		t.entries[k] = e
	}
	e.UsedKB += usedKB
	e.TasksCount++
	e.LastUpdated = float64(time.Now().Unix())
	t.save()
	paused := t.pausedLocked(e.UsedKB)
	used := e.UsedKB
	t.mu.Unlock()

	log.Printf("[bandwidth] key#%d +%.1f KB → %.1f/%.0f MB (%.1f%%)%s",
		apiKeyIndex, usedKB, used/1024, t.limitKB/1024,
		used/t.limitKB*100, map[bool]string{true: " — PAUSED", false: ""}[paused])
}

// pausedLocked computes the pause state (used only for the informational
// "— PAUSED" label in RecordUsage's log line); caller must hold t.mu.
// Bandwidth exhaustion no longer gates which proxies get used — that's
// handled per-proxy via quarantine on a live 402 (see proxy/manager.go),
// since one proxy hitting its limit says nothing about the other proxies
// sharing its API key.
func (t *Tracker) pausedLocked(usedKB float64) bool {
	return usedKB >= t.limitKB*t.pausePct/100
}

// Summary returns a one-line usage report for all known key indexes.
func (t *Tracker) Summary(numKeys int) string {
	t.mu.Lock()
	defer t.mu.Unlock()

	out := ""
	for i := 0; i < numKeys; i++ {
		e, ok := t.entries[storageKey(i)]
		if !ok {
			out += fmt.Sprintf(" key#%d: 0 MB", i)
			continue
		}
		out += fmt.Sprintf(" key#%d: %.0f/%.0f MB (%d tasks)", i, e.UsedKB/1024, t.limitKB/1024, e.TasksCount)
	}
	return out
}
