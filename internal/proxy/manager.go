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
	log.Println("[proxy-manager] scraping free proxies (parallel goroutines per source)…")
	proxies, err := m.scraper.Scrape()
	if err != nil {
		return fmt.Errorf("scrape: %w", err)
	}
	if len(proxies) == 0 {
		return fmt.Errorf("no proxies scraped from any source")
	}

	log.Printf("[proxy-manager] health-checking %d proxies (parallel goroutines, 100 concurrent)…", len(proxies))
	results := m.checker.CheckAll(proxies)
	if len(results) == 0 {
		return fmt.Errorf("no healthy proxies after health check")
	}

	// Persist healthy proxies to the DB and build PooledProxy list.
	var pooled []PooledProxy
	for _, r := range results {
		sp := &storage.Proxy{
			IP:       r.Proxy.IP,
			Port:     r.Proxy.Port,
			Protocol: r.Proxy.Protocol,
			Country:  r.Country,
			Active:   true,
			LatencyMs: int(r.Latency.Milliseconds()),
		}
		id, err := m.db.UpsertProxy(sp)
		if err != nil {
			log.Printf("[proxy-manager] failed to upsert proxy %s:%d: %v", r.Proxy.IP, r.Proxy.Port, err)
			continue
		}
		pooled = append(pooled, PooledProxy{
			ID:          id,
			IP:          r.Proxy.IP,
			Port:        r.Proxy.Port,
			Protocol:    r.Proxy.Protocol,
			Country:     r.Country,
			Timezone:    timezoneForCountry(r.Country),
			Latency:     r.Latency,
			Username:    r.Proxy.Username,
			Password:    r.Proxy.Password,
			APIKeyIndex: r.Proxy.APIKeyIndex,
		})
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

// timezoneForCountry maps a country name to a rough IANA timezone.
// This is used for the time-of-day awareness feature in the scheduler.
func timezoneForCountry(country string) string {
	switch country {
	case "Indonesia":
		return "Asia/Jakarta"
	case "United States", "USA":
		return "America/New_York"
	case "China":
		return "Asia/Shanghai"
	case "India":
		return "Asia/Kolkata"
	case "Brazil":
		return "America/Sao_Paulo"
	case "Russia":
		return "Europe/Moscow"
	case "Germany":
		return "Europe/Berlin"
	case "United Kingdom":
		return "Europe/London"
	case "Japan":
		return "Asia/Tokyo"
	case "Singapore":
		return "Asia/Singapore"
	case "Thailand":
		return "Asia/Bangkok"
	case "Vietnam":
		return "Asia/Ho_Chi_Minh"
	case "Philippines":
		return "Asia/Manila"
	case "Malaysia":
		return "Asia/Kuala_Lumpur"
	case "Netherlands":
		return "Europe/Amsterdam"
	case "France":
		return "Europe/Paris"
	case "Canada":
		return "America/Toronto"
	case "Australia":
		return "Australia/Sydney"
	default:
		return "UTC"
	}
}
