package config

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

oauth_server:
  signing_algorithm: RS256
  access_token_ttl: 10m
  refresh_token_ttl: 720h
  authorize_consent: skip

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
	if cfg.OAuthServer.AccessTokenTTL != 10*time.Minute {
		t.Fatalf("access_token_ttl: %v", cfg.OAuthServer.AccessTokenTTL)
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

oauth_server:
  access_token_ttl: 5m
  refresh_token_ttl: 24h

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
oauth_server:
  access_token_ttl: 1m
  refresh_token_ttl: 1h
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
	if cfg.OAuthServer.DCRInitialAccessToken != "fallback-token" {
		t.Fatalf("fallback not applied: %q", cfg.OAuthServer.DCRInitialAccessToken)
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
oauth_server:
  access_token_ttl: 1m
  refresh_token_ttl: 1h
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
oauth_server: { access_token_ttl: 1m, refresh_token_ttl: 1h }
`,
		"trailing-slash base_url": `
server: { port: 8080, base_url: "https://example.com/" }
database: { dsn: "postgres://localhost/limen" }
security: { token_encryption_key: "` + key + `" }
oauth_server: { access_token_ttl: 1m, refresh_token_ttl: 1h }
`,
		"missing dsn": `
server: { port: 8080 }
database: { dsn: "" }
security: { token_encryption_key: "` + key + `" }
oauth_server: { access_token_ttl: 1m, refresh_token_ttl: 1h }
`,
		"short key": `
server: { port: 8080 }
database: { dsn: "postgres://localhost/limen" }
security: { token_encryption_key: "abcd" }
oauth_server: { access_token_ttl: 1m, refresh_token_ttl: 1h }
`,
		"negative access_token_ttl": `
server: { port: 8080 }
database: { dsn: "postgres://localhost/limen" }
security: { token_encryption_key: "` + key + `" }
oauth_server: { access_token_ttl: -1s, refresh_token_ttl: 1h }
`,
		"bad consent mode": `
server: { port: 8080 }
database: { dsn: "postgres://localhost/limen" }
security: { token_encryption_key: "` + key + `" }
oauth_server: { access_token_ttl: 1m, refresh_token_ttl: 1h, authorize_consent: maybe }
`,
		"bad signing algo": `
server: { port: 8080 }
database: { dsn: "postgres://localhost/limen" }
security: { token_encryption_key: "` + key + `" }
oauth_server: { access_token_ttl: 1m, refresh_token_ttl: 1h, signing_algorithm: HS256 }
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
