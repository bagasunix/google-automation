package proxy

import (
	"fmt"
	"log"
	"time"

	"google-automation/internal/config"
	"google-automation/internal/storage"
)

// Manager orchestrates proxy scraping, health checking, and pool rotation.
// It runs a background refresh loop and exposes a thread-safe Pool.
type Manager struct {
	cfg    *config.ProxyConfig
	db     *storage.DB
	pool   *Pool
	scraper *Scraper
	checker *Checker

	// lastRefresh records when we last scraped new proxies.
	lastRefresh time.Time

	// notifyCh is closed and recreated when a fresh batch is loaded, so
	// waiters can block until proxies become available.
	notifyCh chan struct{}
}

// NewManager creates a proxy manager.
func NewManager(cfg *config.ProxyConfig, db *storage.DB) *Manager {
	var scraper *Scraper

	// Build merged key list: WebshareAPIKeys takes priority, fallback to legacy WebshareAPIKey
	allKeys := cfg.WebshareAPIKeys
	if len(allKeys) == 0 && cfg.WebshareAPIKey != "" {
		allKeys = []string{cfg.WebshareAPIKey}
	}

	if len(allKeys) > 0 {
		scraper = NewScraperWithWebshareKeys(cfg.Sources, allKeys)
	} else {
		scraper = NewScraper(cfg.Sources)
	}

	return &Manager{
		cfg:      cfg,
		db:       db,
		pool:     NewPool(),
		scraper:  scraper,
		checker:  NewChecker(cfg.HealthCheckTimeout),
		notifyCh: make(chan struct{}),
	}
}

// Pool returns the underlying rotation pool for direct Acquire/Release calls.
func (m *Manager) Pool() *Pool {
	return m.pool
}

// InitialRefresh scrapes, health-checks, and loads proxies synchronously.
// Call this at startup so the scheduler has proxies immediately.
func (m *Manager) InitialRefresh() error {
	return m.refresh()
}

// StartRefreshLoop runs in the background, re-scraping proxies at the configured interval.
func (m *Manager) StartRefreshLoop() {
	interval := time.Duration(m.cfg.RefreshIntervalHours) * time.Hour
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		log.Println("[proxy-manager] periodic refresh triggered")
		if err := m.refresh(); err != nil {
			log.Printf("[proxy-manager] refresh failed: %v", err)
		}
	}
}

// refresh performs the full scrape → health-check → persist → load cycle.
func (m *Manager) refresh() error {
	log.Println("[proxy-manager] === PROXY REFRESH START ===")
	var proxies []Proxy
	var err error

	if m.cfg.Provider == "residential" && m.cfg.ResidentialHost != "" {
		log.Printf("[proxy-manager] generating residential rotating proxies (%s:%d, country=%s)...",
			m.cfg.ResidentialHost, m.cfg.ResidentialPort, m.cfg.ResidentialCountry)
		proxies = GenerateResidentialProxies(m.cfg, 20)
	} else if m.cfg.Provider == "custom_file" && m.cfg.CustomProxyFile != "" {
		log.Printf("[proxy-manager] loading custom proxies from %s...", m.cfg.CustomProxyFile)
		proxies, err = LoadCustomProxyFile(m.cfg.CustomProxyFile)
		if err != nil {
			log.Printf("[proxy-manager] custom file error (%v) — falling back to scraper", err)
			proxies, err = m.scraper.Scrape()
		}
	} else {
		log.Println("[proxy-manager] scraping proxies via Webshare / configured sources…")
		proxies, err = m.scraper.Scrape()
	}

	if err != nil {
		return fmt.Errorf("scrape: %w", err)
	}
	if len(proxies) == 0 {
		return fmt.Errorf("no proxies loaded from configured provider")
	}

	log.Printf("[proxy-manager] health-checking %d proxies (parallel goroutines, 100 concurrent)…", len(proxies))
	results := m.checker.CheckAll(proxies)
	if len(results) == 0 {
		return fmt.Errorf("no healthy proxies after health check")
	}

	// Persist healthy proxies to the DB and build PooledProxy list.
	//
	// Bandwidth exhaustion is handled PER-PROXY, not per-key: a 402 on one
	// proxy only means that one proxy's connection is currently rejected —
	// it says nothing about the other 9 proxies sharing the same API key,
	// which are very likely still within budget (Webshare's own dashboard
	// can show e.g. 59% used on a key while individual proxy 402s were
	// already blocking 100% of that key's traffic here). Quarantining just
	// the one proxy — the same mechanism already used for CAPTCHA hits —
	// keeps the rest of that key's proxies in rotation.
	const bandwidthQuarantine = 1 * time.Hour

	// A proxy permanently blacklisted in the DB (repeated CAPTCHA hits) must
	// stay out of rotation even across an orchestrator restart, when the
	// pool's in-memory blacklist map starts empty. Fetch the current set
	// once so we can skip re-adding those IDs to the fresh pool below.
	banned, err := m.db.BlacklistedProxyIDs()
	if err != nil {
		log.Printf("[proxy-manager] failed to load blacklisted proxy IDs: %v", err)
		banned = map[int64]bool{}
	}

	var pooled []PooledProxy
	for _, r := range results {
		sp := &storage.Proxy{
			IP:        r.Proxy.IP,
			Port:      r.Proxy.Port,
			Protocol:  r.Proxy.Protocol,
			Country:   r.Country,
			Timezone:  r.Timezone,
			Username:    r.Proxy.Username,
			Password:    r.Proxy.Password,
			APIKeyIndex: r.Proxy.APIKeyIndex,
			Active:      true,
			LatencyMs:   int(r.Latency.Milliseconds()),
		}
		id, err := m.db.UpsertProxy(sp)
		if err != nil {
			log.Printf("[proxy-manager] failed to upsert proxy %s:%d: %v", r.Proxy.IP, r.Proxy.Port, err)
			continue
		}
		if banned[id] {
			log.Printf("[proxy-manager] skipping %s:%d — permanently blacklisted in DB", r.Proxy.IP, r.Proxy.Port)
			continue
		}
		px := PooledProxy{
			ID:          id,
			IP:          r.Proxy.IP,
			Port:        r.Proxy.Port,
			Protocol:    r.Proxy.Protocol,
			Country:     r.Country,
			Timezone:    r.Timezone,
			Latency:     r.Latency,
			Username:    r.Proxy.Username,
			Password:    r.Proxy.Password,
			APIKeyIndex: r.Proxy.APIKeyIndex,
		}
		if r.BandwidthExhausted {
			m.pool.Quarantine(px, bandwidthQuarantine, "bandwidth exhausted (402)")
		}
		pooled = append(pooled, px)
	}

	m.pool.Load(pooled)
	m.lastRefresh = time.Now()

	// Notify any waiters that proxies are available.
	close(m.notifyCh)
	m.notifyCh = make(chan struct{})

	log.Printf("[proxy-manager] pool ready: %d healthy proxies (persisted to SQLite)", len(pooled))
	log.Printf("[proxy-manager] next refresh in %d hours", m.cfg.RefreshIntervalHours)
	log.Println("[proxy-manager] === PROXY REFRESH DONE ===")
	return nil
}

// WaitUntilAvailable blocks until at least one proxy is available in the pool.
// Returns true if proxies became available, false if the context (via timeout) expired.
func (m *Manager) WaitUntilAvailable() {
	for m.pool.AvailableCount() == 0 {
		ch := m.notifyCh
		<-ch // block until next refresh
	}
}
