package proxy

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"google-automation/internal/bandwidth"
)

// Pool manages the rotation of proxies with a STRICT 1-proxy-1-search rule:
// every proxy is used at most once per cycle (between pool resets).
// A proxy that triggers a CAPTCHA is blacklisted permanently.
type Pool struct {
	mu sync.Mutex

	// available holds proxies not yet used in the current cycle.
	available []PooledProxy

	// usedThisCycle tracks proxies consumed in the current cycle.
	usedThisCycle []PooledProxy

	// blacklisted is the set of permanently removed proxies.
	blacklisted map[int64]string // proxyID -> reason

	// quarantined holds temporary bans with an expiry timestamp.
	quarantined map[int64]time.Time // proxyID -> until

	// consecutiveErrors tracks failure count before auto-quarantine/blacklist.
	consecutiveErrors map[int64]int // proxyID -> count

	// allKnown keeps every proxy the pool has ever held for analytics.
	allKnown []PooledProxy

	// bwTracker tracks bandwidth per API key; proxies whose key is exhausted
	// are skipped during Acquire and pushed to the back of the queue.
	bwTracker *bandwidth.Tracker
}

// PooledProxy is a proxy enriched with health-check metadata and a DB ID.
type PooledProxy struct {
	ID          int64
	IP          string
	Port        int
	Protocol    string
	Country     string
	Timezone    string
	Latency     time.Duration
	// Auth credentials (for authenticated proxies like Webshare).\
	Username    string
	Password    string
	// APIKeyIndex tracks which Webshare API key this proxy came from.
	// Used for key rotation: exhaust all proxies from key #0 before key #1.
	APIKeyIndex int
}

// NewPool creates an empty proxy pool.
func NewPool() *Pool {
	return &Pool{
		blacklisted:       make(map[int64]string),
		quarantined:       make(map[int64]time.Time),
		consecutiveErrors: make(map[int64]int),
	}
}

// SetBandwidthTracker wires the bandwidth tracker so Acquire can skip
// proxies whose API key has hit the monthly bandwidth cap.
func (p *Pool) SetBandwidthTracker(t *bandwidth.Tracker) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bwTracker = t
}

// BandwidthTracker returns the wired bandwidth tracker (may be nil).
func (p *Pool) BandwidthTracker() *bandwidth.Tracker {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bwTracker
}

// Load replaces the pool contents with a fresh batch of health-checked proxies.
// This is called after each scrape+healthcheck cycle. Resets the cycle.
// Proxies are sorted by APIKeyIndex so key #0 is always exhausted before key #1.
func (p *Pool) Load(proxies []PooledProxy) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.available = make([]PooledProxy, 0, len(proxies))
	for _, px := range proxies {
		// Skip proxies that were blacklisted in a previous cycle.
		if _, banned := p.blacklisted[px.ID]; banned {
			continue
		}
		p.available = append(p.available, px)
	}

	// Sort by APIKeyIndex ascending — key #0 proxies are always used first.
	// Within same key, order is preserved (stable sort).
	sort.SliceStable(p.available, func(i, j int) bool {
		return p.available[i].APIKeyIndex < p.available[j].APIKeyIndex
	})

	p.usedThisCycle = nil
	p.allKnown = proxies

	fmt.Printf("[proxy-pool] loaded %d proxies (blacklisted: %d)\n",
		len(p.available), len(p.blacklisted))
}

// Acquire returns the next available proxy for this cycle. STRICT rotation:
// each proxy is returned at most once per cycle. If no proxies are available,
// returns false — the caller must wait for a pool refresh.
// Proxies whose API key has hit the bandwidth pause threshold or are currently
// under quarantine are skipped and rotated.
func (p *Pool) Acquire() (PooledProxy, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.available) == 0 {
		return PooledProxy{}, false
	}

	now := time.Now()

	// Scan for the first proxy whose API key still has bandwidth and is not
	// quarantined. Always inspect the front and rotate skipped ones to the
	// back — bounded by the original count (checked), not by len(p.available),
	// which stays constant across rotations and would otherwise never let
	// this terminate when every proxy is currently unusable.
	total := len(p.available)
	for checked := 0; checked < total; checked++ {
		px := p.available[0]

		// Check quarantine
		if until, q := p.quarantined[px.ID]; q {
			if now.Before(until) {
				// Still in quarantine — rotate to back
				p.available = append(p.available[1:], px)
				continue
			}
			// Quarantine expired — remove
			delete(p.quarantined, px.ID)
		}

		if p.bwTracker != nil && !p.bwTracker.IsKeyAvailable(px.APIKeyIndex) {
			// Key exhausted — rotate this proxy to the back and keep scanning.
			p.available = append(p.available[1:], px)
			continue
		}

		// Usable proxy found — pop it from available.
		p.available = p.available[1:]
		p.usedThisCycle = append(p.usedThisCycle, px)
		return px, true
	}

	return PooledProxy{}, false
}

// Quarantine temporarily isolates a proxy for a specified duration.
func (p *Pool) Quarantine(px PooledProxy, duration time.Duration, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.quarantined[px.ID] = time.Now().Add(duration)
	fmt.Printf("[proxy-pool] quarantined %s:%d for %v — %s\n", px.IP, px.Port, duration, reason)
}

// RecordSuccess marks a proxy as successfully executed and resets its error score.
func (p *Pool) RecordSuccess(px PooledProxy) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.consecutiveErrors[px.ID] = 0
}

// RecordFailure records a failure for the proxy and applies auto-quarantine or blacklist.
func (p *Pool) RecordFailure(px PooledProxy, isCaptcha bool, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.consecutiveErrors[px.ID]++
	errCount := p.consecutiveErrors[px.ID]

	if isCaptcha {
		if errCount >= 2 {
			// 2nd CAPTCHA in a row — blacklist permanently
			p.blacklisted[px.ID] = "Repeated CAPTCHA: " + reason
			fmt.Printf("[proxy-pool] blacklisted %s:%d (consecutive CAPTCHAs: %d)\n", px.IP, px.Port, errCount)
		} else {
			// 1st CAPTCHA — quarantine for 4 hours
			p.quarantined[px.ID] = time.Now().Add(4 * time.Hour)
			fmt.Printf("[proxy-pool] auto-quarantined %s:%d for 4h after CAPTCHA\n", px.IP, px.Port)
		}
		return
	}

	// Non-captcha errors (timeouts, network drops)
	if errCount >= 3 {
		p.quarantined[px.ID] = time.Now().Add(2 * time.Hour)
		fmt.Printf("[proxy-pool] auto-quarantined %s:%d for 2h after %d network failures\n", px.IP, px.Port, errCount)
	}
}

// Release returns a proxy back to the available pool (e.g. task failed with
// a transient error, not a CAPTCHA). This does NOT violate the 1-proxy-1-search
// rule because the search was never completed — the proxy slot is re-opened.
func (p *Pool) Release(px PooledProxy) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.available = append(p.available, px)
	// Remove from usedThisCycle.
	for i, u := range p.usedThisCycle {
		if u.ID == px.ID {
			p.usedThisCycle = append(p.usedThisCycle[:i], p.usedThisCycle[i+1:]...)
			break
		}
	}
}

// Blacklist permanently removes a proxy (e.g. CAPTCHA hit) and records the reason.
func (p *Pool) Blacklist(px PooledProxy, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.blacklisted[px.ID] = reason
	// Also remove from available if present.
	for i, a := range p.available {
		if a.ID == px.ID {
			p.available = append(p.available[:i], p.available[i+1:]...)
			break
		}
	}
	fmt.Printf("[proxy-pool] blacklisted %s:%d — %s\n", px.IP, px.Port, reason)
}

// AvailableCount returns how many proxies are still usable in this cycle.
func (p *Pool) AvailableCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.available)
}

// CycleUsedCount returns how many proxies were consumed this cycle.
func (p *Pool) CycleUsedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.usedThisCycle)
}

// TotalCount returns the total number of proxies (available + used + blacklisted-excluded).
func (p *Pool) TotalCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.available) + len(p.usedThisCycle)
}

// HasUsableProxy reports whether at least one proxy in the available pool is
// currently neither quarantined nor bandwidth-exhausted, without acquiring
// it. Read-only — used to detect a fully-exhausted pool for alerting.
func (p *Pool) HasUsableProxy() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	for _, px := range p.available {
		if until, q := p.quarantined[px.ID]; q && now.Before(until) {
			continue
		}
		if p.bwTracker != nil && !p.bwTracker.IsKeyAvailable(px.APIKeyIndex) {
			continue
		}
		return true
	}
	return false
}

// ResetCycle moves all used proxies back to available for a new cycle.
// Called after the pool has been refreshed with new health-checked proxies.
func (p *Pool) ResetCycle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.available = append(p.available, p.usedThisCycle...)
	p.usedThisCycle = nil
}

// DailyReset performs a midnight cycle reset: returns all used proxies to the
// available pool so they can be reused for a fresh day. Blacklisted proxies
// stay blacklisted (CAPTCHA bans are permanent). Non-blacklisted proxies that
// were deactivated get reactivated for the new day.
func (p *Pool) DailyReset() {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Clear expired quarantines
	now := time.Now()
	for id, until := range p.quarantined {
		if now.After(until) {
			delete(p.quarantined, id)
		}
	}

	recovered := 0
	for _, px := range p.usedThisCycle {
		if _, banned := p.blacklisted[px.ID]; !banned {
			p.available = append(p.available, px)
			recovered++
		}
	}
	p.usedThisCycle = nil
	fmt.Printf("[proxy-pool] daily reset: recovered %d proxies (blacklisted: %d, quarantined: %d)\n",
		recovered, len(p.blacklisted), len(p.quarantined))
}

// AllKnown returns a snapshot of every proxy the pool currently knows about.
func (p *Pool) AllKnown() []PooledProxy {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PooledProxy, len(p.allKnown))
	copy(out, p.allKnown)
	return out
}
