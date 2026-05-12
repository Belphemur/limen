// Package config loads and validates Limen's YAML configuration with
// environment-variable substitution.
//
// Supported substitution syntax inside YAML values:
//
//	${VAR}              required; load error if unset
//	${VAR:-fallback}    optional; uses "fallback" if VAR is unset or empty
//
// Substitution runs on the raw bytes before YAML parsing so it applies to
// every string, number, or duration scalar.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration shape.
type Config struct {
	Server      ServerConfig      `yaml:"server"`
	Database    DatabaseConfig    `yaml:"database"`
	Security    SecurityConfig    `yaml:"security"`
	OAuthServer OAuthServerConfig `yaml:"oauth_server"`
	Upstreams   []UpstreamConfig  `yaml:"upstreams"`
	CodeMode    CodeModeConfig    `yaml:"codemode"`
	Auth        AuthConfig        `yaml:"auth"`
}

// ServerConfig governs the inbound HTTP listener and the public base URL
// used as the OIDC issuer / resource identifier in later phases.
type ServerConfig struct {
	Host    string `yaml:"host"`
	Port    int    `yaml:"port"`
	BaseURL string `yaml:"base_url"`
}

// DatabaseConfig configures the Postgres connections used by
// internal/storage.
//
// DSN authenticates the request-path pool; the runtime role should be
// limen_app (no BYPASSRLS) so the Phase 3 row-level-security policies
// actually fire.
//
// AdminDSN authenticates the migration / WithSuperuser pool; the role should
// be limen_admin (BYPASSRLS). When empty it falls back to DSN — the
// dev / single-role shortcut. Production deployments must set both.
//
// Pool sizing fields fall back to sensible defaults (25 / 5) when zero.
type DatabaseConfig struct {
	DSN             string        `yaml:"dsn"`
	AdminDSN        string        `yaml:"admin_dsn"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
}

// SecurityConfig wires the at-rest encryption key and portal-cookie options.
// TokenEncryptionKey decodes to exactly 32 raw bytes (AES-128-SIV — see
// internal/crypto). Accepts base64 (standard or URL, padded or unpadded)
// or 64-character hex.
type SecurityConfig struct {
	TokenEncryptionKey        string `yaml:"token_encryption_key"`
	PortalSessionCookieName   string `yaml:"portal_session_cookie_name"`
	PortalSessionCookieSecure bool   `yaml:"portal_session_cookie_secure"`
}

// OAuthServerConfig holds the parameters consumed by Phase 5's authorization
// server / DCR proxy.
type OAuthServerConfig struct {
	SigningAlgorithm      string        `yaml:"signing_algorithm"`
	AccessTokenTTL        time.Duration `yaml:"access_token_ttl"`
	RefreshTokenTTL       time.Duration `yaml:"refresh_token_ttl"`
	DCRInitialAccessToken string        `yaml:"dcr_initial_access_token"`
	AuthorizeConsent      string        `yaml:"authorize_consent"`
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

// AuthConfig is the legacy single-issuer JWKS validator. Phase 6 replaces
// it with the multi-tenant Zitadel resource server. Retained for the
// transition.
type AuthConfig struct {
	Enabled  bool   `yaml:"enabled"`
	JWKSURL  string `yaml:"jwks_url,omitempty"`
	Audience string `yaml:"audience,omitempty"`
}

// Load reads, env-expands, parses, and validates a configuration file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	expanded, err := expandEnv(data)
	if err != nil {
		return nil, fmt.Errorf("config: expand env: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(expanded, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse yaml: %w", err)
	}

	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config: validate: %w", err)
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Server.Port == 0 {
		c.Server.Port = 8080
	}
	if c.Server.Host == "" {
		c.Server.Host = "0.0.0.0"
	}
	if c.CodeMode.ExecutionTimeout == 0 {
		c.CodeMode.ExecutionTimeout = 30 * time.Second
	}
	if c.CodeMode.MaxMemoryMB == 0 {
		c.CodeMode.MaxMemoryMB = 64
	}
	if c.Database.MaxOpenConns == 0 {
		c.Database.MaxOpenConns = 25
	}
	if c.Database.MaxIdleConns == 0 {
		c.Database.MaxIdleConns = 5
	}
	if c.OAuthServer.SigningAlgorithm == "" {
		c.OAuthServer.SigningAlgorithm = "RS256"
	}
	if c.OAuthServer.AccessTokenTTL == 0 {
		c.OAuthServer.AccessTokenTTL = 10 * time.Minute
	}
	if c.OAuthServer.RefreshTokenTTL == 0 {
		c.OAuthServer.RefreshTokenTTL = 720 * time.Hour
	}
	if c.OAuthServer.AuthorizeConsent == "" {
		c.OAuthServer.AuthorizeConsent = "skip"
	}
	if c.Security.PortalSessionCookieName == "" {
		c.Security.PortalSessionCookieName = "limen_portal"
	}
}

// Validate runs every section's validator in declaration order and reports
// the first failure.
func (c *Config) Validate() error {
	if err := c.Server.Validate(); err != nil {
		return fmt.Errorf("server: %w", err)
	}
	if err := c.Database.Validate(); err != nil {
		return fmt.Errorf("database: %w", err)
	}
	if err := c.Security.Validate(); err != nil {
		return fmt.Errorf("security: %w", err)
	}
	if err := c.OAuthServer.Validate(); err != nil {
		return fmt.Errorf("oauth_server: %w", err)
	}
	return nil
}

// Validate ensures the listener has a usable port and that base_url, if
// set, is an absolute URL without a trailing slash.
func (s ServerConfig) Validate() error {
	if s.Port <= 0 || s.Port > 65535 {
		return fmt.Errorf("port %d is out of range", s.Port)
	}
	if s.BaseURL != "" {
		u, err := url.Parse(s.BaseURL)
		if err != nil {
			return fmt.Errorf("base_url: %w", err)
		}
		if !u.IsAbs() || u.Host == "" {
			return errors.New("base_url must be an absolute URL")
		}
		if strings.HasSuffix(s.BaseURL, "/") {
			return errors.New("base_url must not have a trailing slash")
		}
	}
	return nil
}

// Validate enforces that a Postgres DSN is configured and the pool sizes
// make sense.
func (d DatabaseConfig) Validate() error {
	if strings.TrimSpace(d.DSN) == "" {
		return errors.New("dsn is required")
	}
	if !strings.Contains(d.DSN, "postgres") && !strings.Contains(d.DSN, "postgresql") && !strings.HasPrefix(d.DSN, "host=") {
		return errors.New("dsn does not look like a Postgres connection string")
	}
	if d.AdminDSN != "" &&
		!strings.Contains(d.AdminDSN, "postgres") &&
		!strings.Contains(d.AdminDSN, "postgresql") &&
		!strings.HasPrefix(d.AdminDSN, "host=") {
		return errors.New("admin_dsn does not look like a Postgres connection string")
	}
	if d.MaxOpenConns < 0 {
		return errors.New("max_open_conns must be >= 0")
	}
	if d.MaxIdleConns < 0 {
		return errors.New("max_idle_conns must be >= 0")
	}
	return nil
}

// Validate confirms the encryption key is present and decodes to 32 bytes.
// We do not parse the key here — that is internal/crypto's job — but we
// surface obvious errors at startup.
func (s SecurityConfig) Validate() error {
	key := strings.TrimSpace(s.TokenEncryptionKey)
	if key == "" {
		return errors.New("token_encryption_key is required")
	}
	if !plausibleKey(key) {
		return errors.New("token_encryption_key must decode to 32 bytes (base64 or hex)")
	}
	return nil
}

// Validate enforces non-zero TTLs and a recognised consent mode.
func (o OAuthServerConfig) Validate() error {
	if o.AccessTokenTTL <= 0 {
		return errors.New("access_token_ttl must be > 0")
	}
	if o.RefreshTokenTTL <= 0 {
		return errors.New("refresh_token_ttl must be > 0")
	}
	switch o.AuthorizeConsent {
	case "skip", "always", "first_time":
	default:
		return fmt.Errorf("authorize_consent %q is not one of skip|always|first_time", o.AuthorizeConsent)
	}
	switch o.SigningAlgorithm {
	case "RS256", "ES256", "PS256":
	default:
		return fmt.Errorf("signing_algorithm %q is not supported", o.SigningAlgorithm)
	}
	return nil
}

// plausibleKey is a quick syntactic check: 64 hex chars, or a base64 blob
// whose decoded length is 32. Crypto-level decoding happens in
// internal/crypto.ParseKey.
func plausibleKey(s string) bool {
	if len(s) == 64 {
		ok := true
		for _, r := range s {
			switch {
			case r >= '0' && r <= '9':
			case r >= 'a' && r <= 'f':
			case r >= 'A' && r <= 'F':
			default:
				ok = false
			}
			if !ok {
				break
			}
		}
		if ok {
			return true
		}
	}
	// Padded standard base64 of 32 bytes is 44 chars; unpadded is 43.
	switch len(s) {
	case 43, 44:
		return true
	}
	return false
}

// envVarPattern matches ${VAR} or ${VAR:-default}. VAR is a typical
// shell-safe identifier; the default value may contain anything except a
// closing brace.
var envVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// expandEnv substitutes ${VAR} and ${VAR:-default} tokens in raw with the
// corresponding environment variables. Missing variables without a default
// produce an error so misconfiguration fails fast.
func expandEnv(raw []byte) ([]byte, error) {
	var missing []string
	out := envVarPattern.ReplaceAllFunc(raw, func(match []byte) []byte {
		m := envVarPattern.FindSubmatch(match)
		name := string(m[1])
		hasFallback := len(m[2]) > 0
		fallback := string(m[3])
		if v, ok := os.LookupEnv(name); ok && v != "" {
			return []byte(v)
		}
		if hasFallback {
			return []byte(fallback)
		}
		missing = append(missing, name)
		return match
	})
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required environment variable(s): %s", strings.Join(missing, ", "))
	}
	return out, nil
}
