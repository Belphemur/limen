package config

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func randomHexKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return hex.EncodeToString(b)
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

const minimalValid = `
server:
  host: "127.0.0.1"
  port: 8080
  base_url: "https://limen.example.com"

database:
  dsn: "postgres://limen:limen@localhost:5432/limen?sslmode=disable"

security:
  token_encryption_key: "%s"

oauth_proxy:
  dcr_enabled: true

oidc:
  issuer: "https://auth.limen.example.com"
  client_id: "limen-portal"
  redirect_uri: "https://limen.example.com/auth/callback"
  scopes: ["openid", "profile", "email"]

zitadel:
  domain: "https://auth.limen.example.com"
  auth_mode: "pat"
  pat: "dev-pat"
  project_id: "proj-1"
`

func TestLoad_MinimalValid(t *testing.T) {
	path := writeConfig(t, "header: ignored\n"+strings.Replace(minimalValid, "%s", randomHexKey(t), 1))
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 8080 {
		t.Fatalf("port: %d", cfg.Server.Port)
	}
	if !cfg.OAuthProxy.DCREnabled {
		t.Fatalf("dcr_enabled not parsed")
	}
	if cfg.OAuthProxy.RateLimit.RPS != 10 || cfg.OAuthProxy.RateLimit.Burst != 20 {
		t.Fatalf("default rate limit not applied: %+v", cfg.OAuthProxy.RateLimit)
	}
	if cfg.Database.MaxOpenConns != 25 {
		t.Fatalf("default max_open_conns not applied: %d", cfg.Database.MaxOpenConns)
	}
	if cfg.Security.PortalSessionCookieName != "limen_portal" {
		t.Fatalf("default cookie name not applied")
	}
}

func TestLoad_EnvSubstitution(t *testing.T) {
	t.Setenv("LIMEN_TOKEN_ENCRYPTION_KEY", randomHexKey(t))
	t.Setenv("LIMEN_DB_DSN", "postgres://x:y@localhost/limen?sslmode=disable")

	body := `
server:
  port: 9090
  base_url: "https://example.com"

database:
  dsn: "${LIMEN_DB_DSN}"

security:
  token_encryption_key: "${LIMEN_TOKEN_ENCRYPTION_KEY}"

oauth_proxy:
  dcr_enabled: true

oidc:
  issuer: "https://auth.example.com"
  client_id: "limen-portal"
  redirect_uri: "https://example.com/auth/callback"
  scopes: ["openid"]

zitadel:
  domain: "https://auth.example.com"
  auth_mode: "pat"
  pat: "dev-pat"
  project_id: "proj-1"
`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.HasPrefix(cfg.Database.DSN, "postgres://") {
		t.Fatalf("DSN not substituted: %q", cfg.Database.DSN)
	}
}

func TestLoad_EnvFallback(t *testing.T) {
	os.Unsetenv("LIMEN_OPTIONAL_THING")
	t.Setenv("LIMEN_TOKEN_ENCRYPTION_KEY", randomHexKey(t))

	body := `
server:
  port: 8080
  base_url: "https://example.com"
database:
  dsn: "postgres://localhost/limen"
security:
  token_encryption_key: "${LIMEN_TOKEN_ENCRYPTION_KEY}"
oauth_proxy:
  dcr_enabled: true
  dcr_initial_access_token: "${LIMEN_OPTIONAL_THING:-fallback-token}"

oidc:
  issuer: "https://auth.example.com"
  client_id: "limen-portal"
  redirect_uri: "https://example.com/auth/callback"
  scopes: ["openid"]

zitadel:
  domain: "https://auth.example.com"
  auth_mode: "pat"
  pat: "dev-pat"
  project_id: "proj-1"
`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OAuthProxy.DCRInitialAccessToken != "fallback-token" {
		t.Fatalf("fallback not applied: %q", cfg.OAuthProxy.DCRInitialAccessToken)
	}
}

func TestLoad_MissingRequiredEnv(t *testing.T) {
	os.Unsetenv("LIMEN_DEFINITELY_NOT_SET")

	body := `
server:
  port: 8080
  base_url: "https://example.com"
database:
  dsn: "${LIMEN_DEFINITELY_NOT_SET}"
security:
  token_encryption_key: "deadbeef"
`
	if _, err := Load(writeConfig(t, body)); err == nil {
		t.Fatalf("Load accepted missing required env var")
	}
}

func TestLoad_ValidationErrors(t *testing.T) {
	key := randomHexKey(t)
	cases := map[string]string{
		"non-absolute base_url": `
server: { port: 8080, base_url: "/relative" }
database: { dsn: "postgres://localhost/limen" }
security: { token_encryption_key: "` + key + `" }
`,
		"trailing-slash base_url": `
server: { port: 8080, base_url: "https://example.com/" }
database: { dsn: "postgres://localhost/limen" }
security: { token_encryption_key: "` + key + `" }
`,
		"missing dsn": `
server: { port: 8080 }
database: { dsn: "" }
security: { token_encryption_key: "` + key + `" }
`,
		"short key": `
server: { port: 8080 }
database: { dsn: "postgres://localhost/limen" }
security: { token_encryption_key: "abcd" }
`,
		"negative rps": `
server: { port: 8080 }
database: { dsn: "postgres://localhost/limen" }
security: { token_encryption_key: "` + key + `" }
oauth_proxy: { rate_limit: { rps: -1, burst: 5 } }
`,
		"burst below rps": `
server: { port: 8080 }
database: { dsn: "postgres://localhost/limen" }
security: { token_encryption_key: "` + key + `" }
oauth_proxy: { rate_limit: { rps: 50, burst: 5 } }
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, body)); err == nil {
				t.Fatalf("Load accepted invalid config")
			}
		})
	}
}
