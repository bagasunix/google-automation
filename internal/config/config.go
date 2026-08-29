// Package config loads and parses the YAML configuration for the search-automation orchestrator.
package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration loaded from config/config.yaml.
type Config struct {
	Domains           []string          `yaml:"domains"`
	EngineRatio       EngineRatio       `yaml:"engine_ratio"`
	Scheduler         SchedulerConfig   `yaml:"scheduler"`
	Proxy             ProxyConfig       `yaml:"proxy"`
	GRPC              GRPCConfig        `yaml:"grpc"`
	Bandwidth         BandwidthConfig   `yaml:"bandwidth"`
	ArticleCollection ArticleCollection `yaml:"article_collection"`
	Telegram          TelegramConfig    `yaml:"telegram"`
}

// TelegramConfig configures the optional Telegram daily-summary notification.
type TelegramConfig struct {
	Enabled  bool   `yaml:"enabled"`
	BotToken string `yaml:"bot_token"`
	ChatID   string `yaml:"chat_id"`
}

// EngineRatio defines the traffic source distribution split.
type EngineRatio struct {
	Google int `yaml:"google"`
	Bing   int `yaml:"bing"`
	Direct int `yaml:"direct"`
	Social int `yaml:"social"`
}

// SchedulerConfig holds all scheduler-related tuning knobs.
type SchedulerConfig struct {
	Concurrency              int     `yaml:"concurrency"`            // number of concurrent workers (1-10)
	MaxSearchPerProxy        int     `yaml:"max_search_per_proxy"`
	NewArticleBoost         int     `yaml:"new_article_boost"`
	RegularMax              int     `yaml:"regular_max"`
	CaptchaPauseHours       int     `yaml:"captcha_pause_hours"`
	MinCooldownSeconds      int     `yaml:"min_cooldown_seconds"`
	MaxCooldownSeconds      int     `yaml:"max_cooldown_seconds"`
	PostExitCooldownMin     int     `yaml:"post_exit_cooldown_min"`
	PostExitCooldownMax     int     `yaml:"post_exit_cooldown_max"`
	ActiveHoursStart        int     `yaml:"active_hours_start"`
	ActiveHoursEnd          int     `yaml:"active_hours_end"`
	PreSearchEnabled        bool    `yaml:"pre_search_enabled"`
	PreSearch2Chance        float64 `yaml:"pre_search_2_chance"`
	SerpCasualClickChance   float64 `yaml:"serp_casual_click_chance"`
	CompetitorClickChance   float64 `yaml:"competitor_click_chance"`
	DistractionExitChance   float64 `yaml:"distraction_exit_chance"`
	SerpDwellSecondsMin     int     `yaml:"serp_dwell_seconds_min"`
	SerpDwellSecondsMax     int     `yaml:"serp_dwell_seconds_max"`
	MaxSearchesPerDomainPerDay int  `yaml:"max_searches_per_domain_per_day"`
}

// ProxyConfig configures proxy scraping and health checking.
type ProxyConfig struct {
	Provider             string   `yaml:"provider"`               // "webshare" | "residential" | "custom_file"
	RefreshIntervalHours int      `yaml:"refresh_interval_hours"`
	HealthCheckTimeout   int      `yaml:"health_check_timeout"`
	WebshareAPIKey       string   `yaml:"webshare_api_key"`       // legacy single key (backward compat)
	WebshareAPIKeys      []string `yaml:"webshare_api_keys"`      // multi-key rotation
	ResidentialHost      string   `yaml:"residential_host"`       // e.g. "gate.smartproxy.com"
	ResidentialPort      int      `yaml:"residential_port"`       // e.g. 7000
	ResidentialUser      string   `yaml:"residential_user"`       // username
	ResidentialPassword  string   `yaml:"residential_password"`   // password
	ResidentialCountry   string   `yaml:"residential_country"`    // e.g. "id", "us"
	CustomProxyFile      string   `yaml:"custom_proxy_file"`      // path to proxies.txt
	Sources              []string `yaml:"sources"`
}

// GRPCConfig configures the gRPC connection to the Python worker.
type GRPCConfig struct {
	Port          int `yaml:"port"`
	WorkerTimeout int `yaml:"worker_timeout"`
}

// ArticleCollection configures how articles are scraped from the target domain.
type ArticleCollection struct {
	Method               string `yaml:"method"`
	RefreshIntervalHours int    `yaml:"refresh_interval_hours"`
	MaxConcurrentFetches int    `yaml:"max_concurrent_fetches"`
}

// BandwidthConfig configures proxy bandwidth tracking and conservation.
type BandwidthConfig struct {
	MonthlyLimitMB          int  `yaml:"monthly_limit_mb"`
	BlockImages             bool `yaml:"block_images"`
	BlockMedia              bool `yaml:"block_media"`
	BlockFonts              bool `yaml:"block_fonts"`
	BlockStylesheets        bool `yaml:"block_stylesheets"`
	WarnThresholdPercent    int  `yaml:"warn_threshold_percent"`
	PauseThresholdPercent   int  `yaml:"pause_threshold_percent"`
}

// Load reads and parses the YAML config from the given path,
// and applies environment variable overrides (from .env or system environment).
func Load(path string) (*Config, error) {
	loadDotEnv(".env")

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	cfg.applyEnvOverrides()
	cfg.applyDefaults()
	return &cfg, nil
}

// loadDotEnv parses a standard .env file if it exists and sets environment variables.
func loadDotEnv(envPath string) {
	data, err := os.ReadFile(envPath)
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			k := strings.TrimSpace(parts[0])
			v := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
			if os.Getenv(k) == "" {
				_ = os.Setenv(k, v)
			}
		}
	}
}

// applyEnvOverrides allows secrets to be provided via environment variables.
func (c *Config) applyEnvOverrides() {
	if keys := os.Getenv("WEBSHARE_API_KEYS"); keys != "" {
		c.Proxy.WebshareAPIKeys = strings.Split(keys, ",")
		for i := range c.Proxy.WebshareAPIKeys {
			c.Proxy.WebshareAPIKeys[i] = strings.TrimSpace(c.Proxy.WebshareAPIKeys[i])
		}
	} else if key := os.Getenv("WEBSHARE_API_KEY"); key != "" {
		c.Proxy.WebshareAPIKey = key
	}

	if token := os.Getenv("TELEGRAM_BOT_TOKEN"); token != "" {
		c.Telegram.BotToken = token
		c.Telegram.Enabled = true
	}
	if chatID := os.Getenv("TELEGRAM_CHAT_ID"); chatID != "" {
		c.Telegram.ChatID = chatID
	}
}

// applyDefaults fills in sensible defaults for any zero-value fields.
func (c *Config) applyDefaults() {
	if c.EngineRatio.Google == 0 && c.EngineRatio.Bing == 0 {
		c.EngineRatio.Google = 70
		c.EngineRatio.Bing = 30
	}
	if c.Scheduler.MaxSearchPerProxy == 0 {
		c.Scheduler.MaxSearchPerProxy = 1
	}
	if c.Scheduler.NewArticleBoost == 0 {
		c.Scheduler.NewArticleBoost = 5
	}
	if c.Scheduler.RegularMax == 0 {
		c.Scheduler.RegularMax = 3
	}
	if c.Scheduler.CaptchaPauseHours == 0 {
		c.Scheduler.CaptchaPauseHours = 3
	}
	if c.Scheduler.MinCooldownSeconds == 0 {
		c.Scheduler.MinCooldownSeconds = 30
	}
	if c.Scheduler.MaxCooldownSeconds == 0 {
		c.Scheduler.MaxCooldownSeconds = 120
	}
	if c.Scheduler.PostExitCooldownMin == 0 {
		c.Scheduler.PostExitCooldownMin = 30
	}
	if c.Scheduler.PostExitCooldownMax == 0 {
		c.Scheduler.PostExitCooldownMax = 120
	}
	// NOTE: ActiveHoursStart/End intentionally NOT defaulted here —
	// 0 is a valid value (midnight). Set defaults only in config.yaml.
	if c.Proxy.RefreshIntervalHours == 0 {
		c.Proxy.RefreshIntervalHours = 3
	}
	if c.Proxy.HealthCheckTimeout == 0 {
		c.Proxy.HealthCheckTimeout = 5
	}
	if c.GRPC.Port == 0 {
		c.GRPC.Port = 50051
	}
	if c.GRPC.WorkerTimeout == 0 {
		c.GRPC.WorkerTimeout = 300
	}
	if c.ArticleCollection.Method == "" {
		c.ArticleCollection.Method = "sitemap"
	}
	if c.ArticleCollection.RefreshIntervalHours == 0 {
		c.ArticleCollection.RefreshIntervalHours = 6
	}
	if c.ArticleCollection.MaxConcurrentFetches == 0 {
		c.ArticleCollection.MaxConcurrentFetches = 4
	}
	if c.Bandwidth.MonthlyLimitMB == 0 {
		c.Bandwidth.MonthlyLimitMB = 1024
	}
	if c.Bandwidth.PauseThresholdPercent == 0 {
		c.Bandwidth.PauseThresholdPercent = 95
	}
	if c.Bandwidth.WarnThresholdPercent == 0 {
		c.Bandwidth.WarnThresholdPercent = 80
	}
	// Bandwidth-saving defaults: irit maksimal unless explicitly enabled
	if !c.Scheduler.PreSearchEnabled && c.Scheduler.PreSearch2Chance == 0 {
		// Only default if the field wasn't set in YAML at all.
		// If pre_search_enabled is explicitly false, we respect it.
	}
	if c.Scheduler.PreSearch2Chance == 0 && c.Scheduler.SerpCasualClickChance == 0 {
		// Defaults set in config.yaml directly; zero is a valid value here.
	}
	if c.Scheduler.SerpDwellSecondsMin == 0 {
		c.Scheduler.SerpDwellSecondsMin = 2
	}
	if c.Scheduler.SerpDwellSecondsMax == 0 {
		c.Scheduler.SerpDwellSecondsMax = 5
	}
}
