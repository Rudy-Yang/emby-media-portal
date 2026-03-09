package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
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
	Listen              string `yaml:"listen"`
	AdminPath           string `yaml:"admin_path,omitempty"`
	AdminToken          string `yaml:"admin_token,omitempty"`
	AdminTokenEncrypted string `yaml:"admin_token_encrypted,omitempty"`
	AdminUsername       string `yaml:"admin_username"`
	AdminPassword       string `yaml:"admin_password,omitempty"`
	AdminPasswordHash   string `yaml:"admin_password_hash,omitempty"`
}

type EmbyConfig struct {
	URL             string `yaml:"url"`
	APIKey          string `yaml:"api_key,omitempty"`
	APIKeyEncrypted string `yaml:"api_key_encrypted,omitempty"`
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
	c.Server.AdminPath = NormalizeAdminPath(c.Server.AdminPath)
	if c.Server.AdminUsername == "" {
		c.Server.AdminUsername = "admin"
	}
	if c.Server.AdminPassword == "" && c.Server.AdminPasswordHash == "" {
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
	if c.Server.AdminToken == "" && c.Server.AdminTokenEncrypted != "" {
		if decrypted, err := decryptAPIKey(path, c.Server.AdminTokenEncrypted); err == nil {
			c.Server.AdminToken = decrypted
		} else {
			return nil, err
		}
	}
	if c.Emby.APIKey == "" && c.Emby.APIKeyEncrypted != "" {
		if decrypted, err := decryptAPIKey(path, c.Emby.APIKeyEncrypted); err == nil {
			c.Emby.APIKey = decrypted
		} else {
			return nil, err
		}
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

	var (
		data []byte
		err  error
	)

	sanitized := *cfg
	sanitized.Server = cfg.Server
	sanitized.Emby = cfg.Emby
	sanitized.Backend = cfg.Backend
	sanitized.RateLimits = cfg.RateLimits
	sanitized.Database = cfg.Database

	if sanitized.Server.AdminPassword != "" {
		hash, hashErr := HashAdminPassword(sanitized.Server.AdminPassword)
		if hashErr != nil {
			return hashErr
		}
		sanitized.Server.AdminPasswordHash = hash
	}
	sanitized.Server.AdminPassword = ""
	if sanitized.Server.AdminToken != "" {
		encrypted, encErr := encryptAPIKey(cfgPath, sanitized.Server.AdminToken)
		if encErr != nil {
			return encErr
		}
		sanitized.Server.AdminTokenEncrypted = encrypted
	}
	sanitized.Server.AdminToken = ""

	if sanitized.Emby.APIKey != "" {
		encrypted, encErr := encryptAPIKey(cfgPath, sanitized.Emby.APIKey)
		if encErr != nil {
			return encErr
		}
		sanitized.Emby.APIKeyEncrypted = encrypted
	}
	sanitized.Emby.APIKey = ""

	data, err = yaml.Marshal(&sanitized)
	if err != nil {
		return err
	}

	if err := os.WriteFile(cfgPath, data, 0600); err != nil {
		return err
	}
	return os.Chmod(cfgPath, 0600)
}

func NormalizeAdminPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || path == "/" {
		return "/admin"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = "/" + strings.Trim(path, "/")
	return path
}

func ListenPort(listen string) int {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return 8095
	}
	if strings.HasPrefix(listen, ":") {
		port, _ := strconv.Atoi(strings.TrimPrefix(listen, ":"))
		return port
	}
	_, rawPort, err := net.SplitHostPort(listen)
	if err != nil {
		return 0
	}
	port, _ := strconv.Atoi(rawPort)
	return port
}

func WithListenPort(listen string, port int) string {
	if port <= 0 {
		return listen
	}
	listen = strings.TrimSpace(listen)
	if listen == "" || strings.HasPrefix(listen, ":") {
		return fmt.Sprintf(":%d", port)
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Sprintf(":%d", port)
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}
