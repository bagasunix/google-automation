package proxy

import (
	"fmt"
	"sort"
	"sync"
	"time"
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

	// allKnown keeps every proxy the pool has ever held for analytics.
	allKnown []PooledProxy
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
		blacklisted: make(map[int64]string),
	}
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
func (p *Pool) Acquire() (PooledProxy, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.available) == 0 {
		return PooledProxy{}, false
	}

	px := p.available[0]
	p.available = p.available[1:]
	p.usedThisCycle = append(p.usedThisCycle, px)
	return px, true
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

// ResetCycle moves all used proxies back to available for a new cycle.
// Called after the pool has been refreshed with new health-checked proxies.
func (p *Pool) ResetCycle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.available = append(p.available, p.usedThisCycle...)
	p.usedThisCycle = nil
}

// AllKnown returns a snapshot of every proxy the pool currently knows about.
func (p *Pool) AllKnown() []PooledProxy {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PooledProxy, len(p.allKnown))
	copy(out, p.allKnown)
	return out
}
