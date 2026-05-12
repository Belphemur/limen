package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig     `yaml:"server"`
	Database  DatabaseConfig   `yaml:"database"`
	Upstreams []UpstreamConfig `yaml:"upstreams"`
	CodeMode  CodeModeConfig   `yaml:"codemode"`
	Auth      AuthConfig       `yaml:"auth"`
}

type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

// DatabaseConfig configures the Postgres connection used by internal/storage.
// DSN is required; the pool sizing fields fall back to sensible defaults
// (25 / 5) when zero.
type DatabaseConfig struct {
	DSN             string        `yaml:"dsn"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
}

type UpstreamConfig struct {
	Name    string            `yaml:"name"`
	URL     string            `yaml:"url"`
	Headers map[string]string `yaml:"headers,omitempty"`
	Timeout time.Duration     `yaml:"timeout"`
}

type CodeModeConfig struct {
	ExecutionTimeout time.Duration `yaml:"execution_timeout"`
	MaxMemoryMB      int           `yaml:"max_memory_mb"`
}

type AuthConfig struct {
	Enabled  bool   `yaml:"enabled"`
	JWKSURL  string `yaml:"jwks_url,omitempty"`
	Audience string `yaml:"audience,omitempty"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	// defaults
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.Host == "" {
		cfg.Server.Host = "0.0.0.0"
	}
	if cfg.CodeMode.ExecutionTimeout == 0 {
		cfg.CodeMode.ExecutionTimeout = 30 * time.Second
	}
	if cfg.CodeMode.MaxMemoryMB == 0 {
		cfg.CodeMode.MaxMemoryMB = 64
	}

	return &cfg, nil
}
