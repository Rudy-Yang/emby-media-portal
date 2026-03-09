package config

import (
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server     ServerConfig     `yaml:"server"`
	Emby       EmbyConfig       `yaml:"emby"`
	Backend    BackendConfig    `yaml:"backend"`
	RateLimits RateLimitsConfig `yaml:"rate_limits"`
	Database   DatabaseConfig   `yaml:"database"`
}

type ServerConfig struct {
	Listen        string `yaml:"listen"`
	AdminToken    string `yaml:"admin_token"`
	AdminUsername string `yaml:"admin_username"`
	AdminPassword string `yaml:"admin_password"`
}

type EmbyConfig struct {
	URL    string `yaml:"url"`
	APIKey string `yaml:"api_key"`
}

type BackendConfig struct {
	Type     string `yaml:"type"` // "direct" forwards to Emby, "lucky" forwards to Lucky
	LuckyURL string `yaml:"lucky_url"`
	ServerID string `yaml:"server_id"`
}

type RateLimitsConfig struct {
	DefaultUpload   int64 `yaml:"default_upload"`
	DefaultDownload int64 `yaml:"default_download"`
	GlobalLimit     int64 `yaml:"global_limit"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

var (
	cfg     *Config
	cfgPath string
	cfgOnce sync.Once
	cfgMu   sync.RWMutex
)

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, err
	}

	// Set defaults
	if c.Server.Listen == "" {
		c.Server.Listen = ":8095"
	}
	if c.Server.AdminUsername == "" {
		c.Server.AdminUsername = "admin"
	}
	if c.Server.AdminPassword == "" {
		switch {
		case c.Server.AdminToken != "":
			c.Server.AdminPassword = c.Server.AdminToken
		default:
			c.Server.AdminPassword = "admin123"
		}
	}
	if c.Database.Path == "" {
		c.Database.Path = "./data/config.db"
	}

	cfgMu.Lock()
	cfg = &c
	cfgPath = path
	cfgMu.Unlock()

	return &c, nil
}

func Get() *Config {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return cfg
}

func Update(newCfg *Config) {
	cfgMu.Lock()
	cfg = newCfg
	cfgMu.Unlock()
}

func Save(current *Config) error {
	cfgMu.Lock()
	defer cfgMu.Unlock()

	if current != nil {
		cfg = current
	}
	if cfg == nil {
		return nil
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	return os.WriteFile(cfgPath, data, 0644)
}
