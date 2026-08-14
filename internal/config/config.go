// Package config loads and parses the YAML configuration for the search-automation orchestrator.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration loaded from config/config.yaml.
type Config struct {
	Domains           []string          `yaml:"domains"`
	EngineRatio       EngineRatio       `yaml:"engine_ratio"`
	Scheduler         SchedulerConfig   `yaml:"scheduler"`
	Proxy             ProxyConfig       `yaml:"proxy"`
	GRPC              GRPCConfig        `yaml:"grpc"`
	ArticleCollection ArticleCollection `yaml:"article_collection"`
}

// EngineRatio defines the percentage split between search engines.
type EngineRatio struct {
	Google int `yaml:"google"`
	Bing   int `yaml:"bing"`
}

// SchedulerConfig holds all scheduler-related tuning knobs.
type SchedulerConfig struct {
	MaxSearchPerProxy   int `yaml:"max_search_per_proxy"`
	NewArticleBoost     int `yaml:"new_article_boost"`
	RegularMax          int `yaml:"regular_max"`
	CaptchaPauseHours   int `yaml:"captcha_pause_hours"`
	MinCooldownSeconds  int `yaml:"min_cooldown_seconds"`
	MaxCooldownSeconds  int `yaml:"max_cooldown_seconds"`
	PostExitCooldownMin int `yaml:"post_exit_cooldown_min"`
	PostExitCooldownMax int `yaml:"post_exit_cooldown_max"`
	ActiveHoursStart    int `yaml:"active_hours_start"`
	ActiveHoursEnd      int `yaml:"active_hours_end"`
}

// ProxyConfig configures proxy scraping and health checking.
type ProxyConfig struct {
	RefreshIntervalHours int      `yaml:"refresh_interval_hours"`
	HealthCheckTimeout   int      `yaml:"health_check_timeout"`
	WebshareAPIKey       string   `yaml:"webshare_api_key"`  // legacy single key (backward compat)
	WebshareAPIKeys      []string `yaml:"webshare_api_keys"` // multi-key rotation
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
}

// Load reads and parses the YAML config from the given path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	cfg.applyDefaults()
	return &cfg, nil
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
}
