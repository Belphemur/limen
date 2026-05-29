package statichdr

import (
	"context"
	"strconv"
	"testing"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/storage/storagetest"
	"github.com/belphemur/limen/internal/upstream"
)

func newTestCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	var key crypto.Key
	for i := range key {
		key[i] = byte(i + 1)
	}
	c, err := crypto.NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	return c
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"valid config", Config{HeaderName: "Authorization", HeaderTemplate: "Bearer {value}", SharedSecret: "s"}, false},
		{"no header name", Config{HeaderTemplate: "{value}", SharedSecret: "s"}, true},
		{"bad header name", Config{HeaderName: "Bad Name", HeaderTemplate: "{value}", SharedSecret: "s"}, true},
		{"no placeholder", Config{HeaderName: "X", HeaderTemplate: "no-placeholder", SharedSecret: "s"}, true},
		{"empty secret", Config{HeaderName: "X", HeaderTemplate: "{value}"}, true},
		{"whitespace secret", Config{HeaderName: "X", HeaderTemplate: "{value}", SharedSecret: "   "}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("validate err=%v, wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestParseConfig(t *testing.T) {
	// Mode is now a separate parameter — the map no longer carries it.
	cfg, err := ParseConfig(map[string]string{
		"header_name":     "Authorization",
		"header_template": "Bearer {value}",
		"value":           "shared-1",
	}, "override")
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.SharedSecret != "shared-1" {
		t.Fatalf("ParseConfig got %+v", cfg)
	}
	if _, err := ParseConfig(map[string]string{
		"header_name":     "X",
		"header_template": "{value}",
	}, "shared"); err == nil {
		t.Fatalf("ParseConfig: want error on missing value")
	}
}

func TestEncodeConfig_RoundTrip(t *testing.T) {
	c := newTestCipher(t)
	crypto.SetCipher(c)
	t.Cleanup(func() { crypto.SetCipher(nil) })

	sf, err := EncodeConfig(42, Config{HeaderName: "Authorization", HeaderTemplate: "Bearer {value}", SharedSecret: "secret-1"})
	if err != nil {
		t.Fatalf("EncodeConfig: %v", err)
	}
	if sf.IsZero() {
		t.Fatalf("EncodeConfig produced zero SecretField")
	}
}

// seedFixture stands up tenant + user + upstream + strategy config and
// returns the bits every integration test below needs. Keeps the
// individual Test* funcs free of GORM plumbing.
type seedFixture struct {
	tenant *storage.Tenant
	user   *storage.User
	up     *storage.Upstream
}

// seed creates test data. cfg is the encrypted config payload; mode is
// the Mode column value ("shared" or "override").
func seed(t *testing.T, store *storage.Store, cfg Config, mode string) seedFixture {
	t.Helper()
	ctx := context.Background()
	tx, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		t.Fatalf("super session: %v", err)
	}
	tenant := &storage.Tenant{Name: "acme", ZitadelOrgID: "z-" + t.Name()}
	if err := tx.Create(tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	user := &storage.User{TenantID: tenant.ID, Email: "u@example.com", Name: "u", ZitadelSubject: "sub-" + t.Name()}
	if err := tx.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	up := &storage.Upstream{TenantID: tenant.ID, Identifier: "github", StrategyType: string(upstream.StrategyStaticHeader), McpServerURL: "https://example.com/mcp"}
	if err := tx.Create(up).Error; err != nil {
		t.Fatalf("create upstream: %v", err)
	}
	sf, err := EncodeConfig(tenant.ID, cfg)
	if err != nil {
		t.Fatalf("EncodeConfig: %v", err)
	}
	if mode == "" {
		mode = "shared"
	}
	if err := tx.Create(&storage.UpstreamStrategyConfig{TenantID: tenant.ID, UpstreamID: up.ID, Type: string(upstream.StrategyStaticHeader), ConfigJSON: sf, Mode: mode}).Error; err != nil {
		t.Fatalf("create strategy config: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	return seedFixture{tenant: tenant, user: user, up: up}
}

// reloadLink re-fetches the user's link with ExtraJSON AAD bound so
// callers can read decrypted overrides via Headers.
func reloadLink(t *testing.T, store *storage.Store, f seedFixture) *storage.UpstreamLink {
	t.Helper()
	ctx := context.Background()
	tx, commit, err := store.Session(storage.WithTenant(ctx, f.tenant.ID))
	if err != nil {
		t.Fatalf("tenant session: %v", err)
	}
	defer func() { _ = commit() }()
	var link storage.UpstreamLink
	link.ExtraJSON.SetAAD(strconv.FormatInt(f.tenant.ID, 10), strconv.FormatInt(f.user.ID, 10), "upstream.extra")
	if err := tx.Where("user_id = ? AND upstream_id = ?", f.user.ID, f.up.ID).First(&link).Error; err != nil {
		return nil
	}
	return &link
}

func TestStrategy_Headers_SharedOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	store := storagetest.OpenMigrated(t)
	cipher := newTestCipher(t)
	crypto.SetCipher(cipher)
	t.Cleanup(func() { crypto.SetCipher(nil) })

	f := seed(t, store, Config{
		HeaderName:     "Authorization",
		HeaderTemplate: "Bearer {value}",
		SharedSecret:   "org-token",
	}, "shared")
	s := New(store, cipher, nil)

	// PersistUserSecret must reject when override is disabled.
	lctx := upstream.LinkContext{Tenant: f.tenant, User: f.user, Upstream: f.up}
	if err := s.PersistUserSecret(context.Background(), lctx, "anything"); err != upstream.ErrUnsupported {
		t.Fatalf("PersistUserSecret err=%v, want ErrUnsupported", err)
	}

	headers, err := s.Headers(context.Background(), lctx)
	if err != nil {
		t.Fatalf("Headers: %v", err)
	}
	if got := headers["Authorization"]; got != "Bearer org-token" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer org-token")
	}
}

func TestStrategy_Headers_OverrideWinsThenFallsBack(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	store := storagetest.OpenMigrated(t)
	cipher := newTestCipher(t)
	crypto.SetCipher(cipher)
	t.Cleanup(func() { crypto.SetCipher(nil) })

	f := seed(t, store, Config{
		HeaderName:     "X-Api-Key",
		HeaderTemplate: "{value}",
		SharedSecret:   "fallback",
	}, "override")
	s := New(store, cipher, nil)
	ctx := context.Background()
	lctx := upstream.LinkContext{Tenant: f.tenant, User: f.user, Upstream: f.up}

	// No link yet → falls back to shared.
	headers, err := s.Headers(ctx, lctx)
	if err != nil {
		t.Fatalf("Headers no-link: %v", err)
	}
	if got := headers["X-Api-Key"]; got != "fallback" {
		t.Fatalf("no-link X-Api-Key = %q, want fallback", got)
	}

	// Persist the user's override.
	if err := s.PersistUserSecret(ctx, lctx, "user-token"); err != nil {
		t.Fatalf("PersistUserSecret: %v", err)
	}
	lctx.Link = reloadLink(t, store, f)
	headers, err = s.Headers(ctx, lctx)
	if err != nil {
		t.Fatalf("Headers override: %v", err)
	}
	if got := headers["X-Api-Key"]; got != "user-token" {
		t.Fatalf("override X-Api-Key = %q, want user-token", got)
	}

	// Simulate the Phase 8 reactive-401 path flipping NeedsRelink — we
	// must transparently fall back to the shared secret so the user's
	// tools keep working.
	lctx.Link.NeedsRelink = true
	headers, err = s.Headers(ctx, lctx)
	if err != nil {
		t.Fatalf("Headers needs-relink: %v", err)
	}
	if got := headers["X-Api-Key"]; got != "fallback" {
		t.Fatalf("needs-relink X-Api-Key = %q, want fallback", got)
	}
}

func TestStrategy_ClearUserOverride_FallsBackToShared(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	store := storagetest.OpenMigrated(t)
	cipher := newTestCipher(t)
	crypto.SetCipher(cipher)
	t.Cleanup(func() { crypto.SetCipher(nil) })

	f := seed(t, store, Config{
		HeaderName:     "X-Api-Key",
		HeaderTemplate: "{value}",
		SharedSecret:   "fallback",
	}, "override")
	s := New(store, cipher, nil)
	ctx := context.Background()
	lctx := upstream.LinkContext{Tenant: f.tenant, User: f.user, Upstream: f.up}

	if err := s.PersistUserSecret(ctx, lctx, "user-token"); err != nil {
		t.Fatalf("PersistUserSecret: %v", err)
	}
	if err := s.ClearUserOverride(ctx, lctx); err != nil {
		t.Fatalf("ClearUserOverride: %v", err)
	}
	lctx.Link = reloadLink(t, store, f)
	if lctx.Link == nil {
		t.Fatalf("link disappeared after clear; want preserved")
	}
	if !lctx.Link.ExtraJSON.IsZero() {
		t.Fatalf("ExtraJSON not cleared")
	}
	headers, err := s.Headers(ctx, lctx)
	if err != nil {
		t.Fatalf("Headers post-clear: %v", err)
	}
	if got := headers["X-Api-Key"]; got != "fallback" {
		t.Fatalf("post-clear X-Api-Key = %q, want fallback", got)
	}
}
