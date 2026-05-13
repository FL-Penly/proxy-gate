package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	defaultAddr             = "127.0.0.1:19527"
	defaultDataDir          = "./data"
	defaultPoolDir          = "./pool"
	defaultRoutingPriority  = "account-first"
	defaultDrainMultiplier  = 1.0
	defaultPrimaryBonus     = 0.1
	defaultPrimaryPctMax    = 1.0
	defaultSecondaryPctMax  = 1.0
	defaultPinTTL           = time.Hour
	defaultWhamPollInterval = 5 * time.Minute
	defaultLogLevel         = "info"
)

type Config struct {
	Server  ServerConfig  `toml:"server"`
	Paths   PathsConfig   `toml:"paths"`
	Routing RoutingConfig `toml:"routing"`
	Broker  BrokerConfig  `toml:"broker"`
	Wham    WhamConfig    `toml:"wham"`
	Vertex  VertexConfig  `toml:"vertex"`
	Log     LogConfig     `toml:"log"`
}

type ServerConfig struct {
	Addr          string `toml:"addr"`
	AdminToken    string `toml:"admin_token"`
	ProxyToken    string `toml:"proxy_token"`
	PublicBaseURL string `toml:"public_base_url"`
}

type PathsConfig struct {
	DataDir string `toml:"data_dir"`
	PoolDir string `toml:"pool_dir"`
}

type RoutingConfig struct {
	Priority        string  `toml:"priority"`
	DrainMultiplier float64 `toml:"drain_multiplier"`
	PrimaryBonus    float64 `toml:"primary_bonus"`
}

type BrokerConfig struct {
	PrimaryUsedPctMax   float64       `toml:"primary_used_pct_max"`
	SecondaryUsedPctMax float64       `toml:"secondary_used_pct_max"`
	PinTTL              time.Duration `toml:"pin_ttl"`
}

type WhamConfig struct {
	PollInterval time.Duration `toml:"poll_interval"`
}

type VertexConfig struct {
	ProjectID       string `toml:"project_id"`
	Location        string `toml:"location"`
	CredentialsFile string `toml:"credentials_file"`
}

type LogConfig struct {
	Level string `toml:"level"`
}

func DefaultConfig() Config {
	return Config{
		Server: ServerConfig{Addr: defaultAddr},
		Paths:  PathsConfig{DataDir: defaultDataDir, PoolDir: defaultPoolDir},
		Routing: RoutingConfig{
			Priority:        defaultRoutingPriority,
			DrainMultiplier: defaultDrainMultiplier,
			PrimaryBonus:    defaultPrimaryBonus,
		},
		Broker: BrokerConfig{
			PrimaryUsedPctMax:   defaultPrimaryPctMax,
			SecondaryUsedPctMax: defaultSecondaryPctMax,
			PinTTL:              defaultPinTTL,
		},
		Wham: WhamConfig{PollInterval: defaultWhamPollInterval},
		Log:  LogConfig{Level: defaultLogLevel},
	}
}

func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()

	if path != "" {
		if _, err := os.Stat(path); err == nil {
			if _, err := toml.DecodeFile(path, &cfg); err != nil {
				return cfg, fmt.Errorf("decode %s: %w", path, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return cfg, fmt.Errorf("stat %s: %w", path, err)
		}
	}

	applyEnvOverrides(&cfg)
	if err := cfg.normalize(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("PROXYGATE_ADDR"); v != "" {
		cfg.Server.Addr = v
	}
	if v := os.Getenv("PROXYGATE_ADMIN_TOKEN"); v != "" {
		cfg.Server.AdminToken = v
	}
	if v := os.Getenv("PROXYGATE_PROXY_TOKEN"); v != "" {
		cfg.Server.ProxyToken = v
	}
	if v := os.Getenv("PROXYGATE_PUBLIC_BASE_URL"); v != "" {
		cfg.Server.PublicBaseURL = v
	}
	if v := os.Getenv("PROXYGATE_DATA_DIR"); v != "" {
		cfg.Paths.DataDir = v
	}
	if v := os.Getenv("PROXYGATE_POOL_DIR"); v != "" {
		cfg.Paths.PoolDir = v
	}
	if v := os.Getenv("PROXYGATE_ROUTING_PRIORITY"); v != "" {
		cfg.Routing.Priority = v
	}
	if v := os.Getenv("PROXYGATE_LOG_LEVEL"); v != "" {
		cfg.Log.Level = v
	}
	if v := firstEnv("PROXYGATE_VERTEX_PROJECT_ID", "ANTHROPIC_VERTEX_PROJECT_ID", "GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT"); v != "" {
		cfg.Vertex.ProjectID = v
	}
	if v := firstEnv("PROXYGATE_VERTEX_LOCATION", "VERTEX_LOCATION", "CLOUD_ML_REGION", "GOOGLE_CLOUD_LOCATION"); v != "" {
		cfg.Vertex.Location = v
	}
	if v := firstEnv("PROXYGATE_VERTEX_CREDENTIALS_FILE", "GOOGLE_APPLICATION_CREDENTIALS"); v != "" {
		cfg.Vertex.CredentialsFile = v
	}
}

func (c *Config) normalize() error {
	if c.Server.Addr == "" {
		c.Server.Addr = defaultAddr
	}
	if c.Paths.DataDir == "" {
		c.Paths.DataDir = defaultDataDir
	}
	if c.Paths.PoolDir == "" {
		c.Paths.PoolDir = defaultPoolDir
	}
	switch strings.ToLower(c.Routing.Priority) {
	case "account-first", "apikey-first":
	case "":
		c.Routing.Priority = defaultRoutingPriority
	default:
		return fmt.Errorf("invalid routing.priority %q (want account-first|apikey-first)", c.Routing.Priority)
	}
	if c.Broker.PinTTL <= 0 {
		c.Broker.PinTTL = defaultPinTTL
	}
	if c.Wham.PollInterval <= 0 {
		c.Wham.PollInterval = defaultWhamPollInterval
	}
	if c.Routing.DrainMultiplier == 0 {
		c.Routing.DrainMultiplier = defaultDrainMultiplier
	}
	if c.Server.PublicBaseURL != "" {
		if _, err := url.Parse(c.Server.PublicBaseURL); err != nil {
			return fmt.Errorf("invalid server.public_base_url: %w", err)
		}
	}
	if c.Log.Level == "" {
		c.Log.Level = defaultLogLevel
	}
	return nil
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

func (c Config) DBPath() string {
	return filepath.Join(c.Paths.DataDir, "proxygate.db")
}

func (c Config) PoolSubdir(kind string) string {
	return filepath.Join(c.Paths.PoolDir, kind)
}
