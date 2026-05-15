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
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration shape.
type Config struct {
	Server          ServerConfig          `yaml:"server"`
	Database        DatabaseConfig        `yaml:"database"`
	Security        SecurityConfig        `yaml:"security"`
	OAuthProxy      OAuthProxyConfig      `yaml:"oauth_proxy"`
	Upstreams       []UpstreamConfig      `yaml:"upstreams"`
	UpstreamRefresh UpstreamRefreshConfig `yaml:"upstream_refresh"`
	CodeMode        CodeModeConfig        `yaml:"codemode"`
	OIDC            OIDCConfig            `yaml:"oidc"`
	Zitadel         ZitadelConfig         `yaml:"zitadel"`
	Valkey          ValkeyConfig          `yaml:"valkey"`
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

// OAuthProxyConfig holds the parameters consumed by Phase 5's thin OAuth
// proxy. Limen is no longer an Authorization Server — Zitadel is — so the
// signing algorithm / token TTL / consent knobs of the old
// OAuthServerConfig are gone. What remains:
//
//   - DCREnabled: global master kill-switch for /register*. Per-tenant
//     gating still happens via Tenant.DCREnabled.
//   - DCRInitialAccessToken: when set, /register requires this token
//     (RFC 7591 §3); empty disables the check.
//   - RateLimit: per-tenant token bucket applied to /register* — see
//     internal/oauthproxy/ratelimit.go.
//
// The Zitadel PAT / project ID are reused from the top-level zitadel:
// block; do not duplicate them here.
type OAuthProxyConfig struct {
	DCREnabled            bool            `yaml:"dcr_enabled"`
	DCRInitialAccessToken string          `yaml:"dcr_initial_access_token"`
	RateLimit             RateLimitConfig `yaml:"rate_limit"`
}

// RateLimitConfig sizes a golang.org/x/time/rate token bucket.
// Zero values fall back to the Phase 5 defaults (10 rps / burst 20).
type RateLimitConfig struct {
	RPS   int `yaml:"rps"`
	Burst int `yaml:"burst"`
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

// ValkeyConfig wires the Valkey (Redis-protocol) client used by Phase 7
// for one-shot OAuth state and any future short-lived key/value needs.
// Limen owns its own logical keyspace ("limen:*"); the same Valkey
// instance Zitadel uses for its cache is fine to share.
type ValkeyConfig struct {
	// Address is host:port, e.g. "redis:6379". Required when any feature
	// that depends on Valkey is enabled (Phase 7 upstream linking).
	Address string `yaml:"address"`
	// Password is optional; empty for the dev/no-auth deployment.
	Password string `yaml:"password,omitempty"`
	// DialTimeout caps the initial TCP+AUTH dial. Default 5s.
	DialTimeout time.Duration `yaml:"dial_timeout,omitempty"`
}

// UpstreamRefreshConfig governs Phase 7's background-refresher and
// auto-disable behaviour. Zero values fall back to the defaults shown
// in applyDefaults; operators only need to override knobs they want to
// move.
type UpstreamRefreshConfig struct {
	// Interval is how often the refresher wakes up. Default 2m.
	Interval time.Duration `yaml:"interval,omitempty"`
	// RefreshWindow refreshes any token whose ExpiresAt is within this
	// window of now. Default 5m.
	RefreshWindow time.Duration `yaml:"refresh_window,omitempty"`
	// ProactiveWindow is the per-request fast-path threshold used by the
	// Headers strategy method when a tool call is about to go out.
	// Default 60s.
	ProactiveWindow time.Duration `yaml:"proactive_window,omitempty"`
	// FailThreshold is the minimum consecutive-failure count that can
	// trip auto-disable. Default 5.
	FailThreshold int `yaml:"fail_threshold,omitempty"`
	// FailWindow is the minimum elapsed time from the streak start to
	// the latest failure before auto-disable trips. Default 15m.
	FailWindow time.Duration `yaml:"fail_window,omitempty"`
	// NeedsRelinkWindow is how long NeedsRelink=true must hold before
	// auto-disable trips on the "long-broken" branch. Default 24h.
	NeedsRelinkWindow time.Duration `yaml:"needs_relink_window,omitempty"`
}

// OIDCConfig wires the portal relying-party (Phase 4) to a Zitadel issuer.
// Limen never sees the user's password — Zitadel renders the login UI,
// enforces MFA, and issues the tokens. Limen seals the resulting
// {id_token, refresh_token} pair into a per-tenant encrypted cookie.
//
// The redirect URI is intentionally tenant-agnostic so a single Zitadel
// app registration covers every tenant; the tenant slug travels in the
// signed `state` cookie. See internal/auth/oidc.go.
type OIDCConfig struct {
	// Issuer is the Zitadel issuer URL, e.g. https://auth.limen.example.com.
	Issuer string `yaml:"issuer"`
	// ClientID identifies the Portal app registered in Zitadel.
	ClientID string `yaml:"client_id"`
	// ClientSecret is empty for a public PKCE client.
	ClientSecret string `yaml:"client_secret,omitempty"`
	// RedirectURI is the absolute URL for the root /auth/callback handler,
	// e.g. https://limen.example.com/auth/callback. Must be a sub-path of
	// Server.BaseURL.
	RedirectURI string `yaml:"redirect_uri"`
	// Scopes requested at /authorize. Must include "openid"; usually also
	// "profile", "email", "offline_access", and the project-roles scope
	// urn:zitadel:iam:org:project:id:<projectID>:aud.
	Scopes []string `yaml:"scopes"`
	// PostLogoutRedirectURI is where Zitadel sends the browser after
	// end_session. Defaults to Server.BaseURL when empty.
	PostLogoutRedirectURI string `yaml:"post_logout_redirect_uri,omitempty"`
}

// ZitadelConfig wires the Management / User API client used by the CLI
// (create-tenant) and Phase 9b admin RPCs. See internal/zitadel.
type ZitadelConfig struct {
	// Domain is the Zitadel issuer URL — same value as OIDC.Issuer in the
	// usual single-instance deployment.
	Domain string `yaml:"domain"`
	// AuthMode selects the client credential: "pat" (dev) or "jwt_key" (prod).
	AuthMode string `yaml:"auth_mode"`
	// PAT is a Personal Access Token, used when auth_mode=pat.
	PAT string `yaml:"pat,omitempty"`
	// JWTKeyPath is a service-user JSON key file, used when auth_mode=jwt_key.
	JWTKeyPath string `yaml:"jwt_key_path,omitempty"`
	// ProjectID is the shared Limen project's resource id (Phase 0 bootstrap).
	// Required for AddUserGrant and for requesting the project-roles claim.
	ProjectID string `yaml:"project_id"`
	// MCPResourceAudience is the Zitadel project audience (or per-app resource
	// id) that MCP access tokens carry in `aud`. Phase 6's RS verifier rejects
	// any token whose audience set does not contain this value. Typically the
	// Zitadel project audience id emitted as `<project_id>@<project_name>`.
	MCPResourceAudience string `yaml:"mcp_resource_audience"`
	// HTTPTimeout caps any single API request. Default 30s.
	HTTPTimeout time.Duration `yaml:"http_timeout,omitempty"`
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
	if c.OAuthProxy.RateLimit.RPS == 0 {
		c.OAuthProxy.RateLimit.RPS = 10
	}
	if c.OAuthProxy.RateLimit.Burst == 0 {
		c.OAuthProxy.RateLimit.Burst = 20
	}
	if c.Security.PortalSessionCookieName == "" {
		c.Security.PortalSessionCookieName = "limen_portal"
	}
	if c.Zitadel.HTTPTimeout == 0 {
		c.Zitadel.HTTPTimeout = 30 * time.Second
	}
	if c.Valkey.DialTimeout == 0 {
		c.Valkey.DialTimeout = 5 * time.Second
	}
	if c.UpstreamRefresh.Interval == 0 {
		c.UpstreamRefresh.Interval = 2 * time.Minute
	}
	if c.UpstreamRefresh.RefreshWindow == 0 {
		c.UpstreamRefresh.RefreshWindow = 5 * time.Minute
	}
	if c.UpstreamRefresh.ProactiveWindow == 0 {
		c.UpstreamRefresh.ProactiveWindow = 60 * time.Second
	}
	if c.UpstreamRefresh.FailThreshold == 0 {
		c.UpstreamRefresh.FailThreshold = 5
	}
	if c.UpstreamRefresh.FailWindow == 0 {
		c.UpstreamRefresh.FailWindow = 15 * time.Minute
	}
	if c.UpstreamRefresh.NeedsRelinkWindow == 0 {
		c.UpstreamRefresh.NeedsRelinkWindow = 24 * time.Hour
	}
	if c.OIDC.Issuer == "" && c.Zitadel.Domain != "" {
		c.OIDC.Issuer = c.Zitadel.Domain
	}
	if c.OIDC.PostLogoutRedirectURI == "" && c.Server.BaseURL != "" {
		c.OIDC.PostLogoutRedirectURI = c.Server.BaseURL
	}
	if c.OIDC.RedirectURI == "" && c.Server.BaseURL != "" {
		c.OIDC.RedirectURI = c.Server.BaseURL + "/auth/callback"
	}
	if len(c.OIDC.Scopes) == 0 {
		c.OIDC.Scopes = []string{
			"openid",
			"profile",
			"email",
			"offline_access",
			// Required: makes Zitadel emit the
			// urn:zitadel:iam:user:resourceowner:id claim in the ID token
			// so Limen can verify the logged-in user belongs to the tenant
			// org addressed by /t/{slug}/. See docs/security.md.
			"urn:zitadel:iam:user:resourceowner",
		}
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
	if err := c.OAuthProxy.Validate(); err != nil {
		return fmt.Errorf("oauth_proxy: %w", err)
	}
	if err := c.OIDC.Validate(c.Server.BaseURL); err != nil {
		return fmt.Errorf("oidc: %w", err)
	}
	if err := c.Zitadel.Validate(); err != nil {
		return fmt.Errorf("zitadel: %w", err)
	}
	if err := c.Valkey.Validate(); err != nil {
		return fmt.Errorf("valkey: %w", err)
	}
	if err := c.UpstreamRefresh.Validate(); err != nil {
		return fmt.Errorf("upstream_refresh: %w", err)
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

// Validate enforces non-negative rate-limit parameters with burst >= rps.
func (o OAuthProxyConfig) Validate() error {
	if o.RateLimit.RPS < 0 {
		return errors.New("rate_limit.rps must be >= 0")
	}
	if o.RateLimit.Burst < 0 {
		return errors.New("rate_limit.burst must be >= 0")
	}
	if o.RateLimit.Burst < o.RateLimit.RPS {
		return fmt.Errorf("rate_limit.burst (%d) must be >= rate_limit.rps (%d)", o.RateLimit.Burst, o.RateLimit.RPS)
	}
	return nil
}

// Validate checks the OIDC RP wiring. baseURL is the configured
// Server.BaseURL; when non-empty the redirect URI must live under it so a
// stolen callback cannot redirect to an attacker-controlled origin.
func (o OIDCConfig) Validate(baseURL string) error {
	if strings.TrimSpace(o.Issuer) == "" {
		return errors.New("issuer is required")
	}
	if !strings.HasPrefix(o.Issuer, "http://") && !strings.HasPrefix(o.Issuer, "https://") {
		return errors.New("issuer must be an absolute http(s) URL")
	}
	if strings.TrimSpace(o.ClientID) == "" {
		return errors.New("client_id is required")
	}
	if strings.TrimSpace(o.RedirectURI) == "" {
		return errors.New("redirect_uri is required")
	}
	u, err := url.Parse(o.RedirectURI)
	if err != nil || !u.IsAbs() {
		return errors.New("redirect_uri must be an absolute URL")
	}
	if baseURL != "" && !strings.HasPrefix(o.RedirectURI, baseURL+"/") {
		return fmt.Errorf("redirect_uri %q must live under server.base_url %q", o.RedirectURI, baseURL)
	}
	hasOpenID := slices.Contains(o.Scopes, "openid")
	if !hasOpenID {
		return errors.New(`scopes must include "openid"`)
	}
	return nil
}

// Validate confirms the SDK client has enough config to authenticate.
func (z ZitadelConfig) Validate() error {
	if strings.TrimSpace(z.Domain) == "" {
		return errors.New("domain is required")
	}
	if strings.TrimSpace(z.ProjectID) == "" {
		return errors.New("project_id is required")
	}
	if strings.TrimSpace(z.MCPResourceAudience) == "" {
		return errors.New("mcp_resource_audience is required")
	}
	switch z.AuthMode {
	case "pat":
		if strings.TrimSpace(z.PAT) == "" {
			return errors.New(`pat is required when auth_mode="pat"`)
		}
	case "jwt_key":
		if strings.TrimSpace(z.JWTKeyPath) == "" {
			return errors.New(`jwt_key_path is required when auth_mode="jwt_key"`)
		}
	default:
		return fmt.Errorf("auth_mode %q is not one of pat|jwt_key", z.AuthMode)
	}
	return nil
}

// Validate enforces an address shape; auth/network are tested at dial time.
// An empty Address is allowed and signals "Valkey-dependent features are
// disabled" — callers that need it must error themselves.
func (v ValkeyConfig) Validate() error {
	addr := strings.TrimSpace(v.Address)
	if addr == "" {
		return nil
	}
	if !strings.Contains(addr, ":") {
		return errors.New(`address must be "host:port"`)
	}
	if v.DialTimeout < 0 {
		return errors.New("dial_timeout must be >= 0")
	}
	return nil
}

// Validate keeps the refresher tunables in a sane range. All fields are
// optional; defaults are filled in applyDefaults before this runs.
func (u UpstreamRefreshConfig) Validate() error {
	if u.Interval <= 0 {
		return errors.New("interval must be > 0")
	}
	if u.RefreshWindow <= 0 {
		return errors.New("refresh_window must be > 0")
	}
	if u.ProactiveWindow <= 0 {
		return errors.New("proactive_window must be > 0")
	}
	if u.FailThreshold <= 0 {
		return errors.New("fail_threshold must be > 0")
	}
	if u.FailWindow <= 0 {
		return errors.New("fail_window must be > 0")
	}
	if u.NeedsRelinkWindow <= 0 {
		return errors.New("needs_relink_window must be > 0")
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
