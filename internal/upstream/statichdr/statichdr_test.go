package statichdr

import (
	"context"
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
		{"ok tenant", Config{HeaderName: "Authorization", HeaderTemplate: "Bearer {value}", Mode: ModeTenant, TenantSecret: "s"}, false},
		{"ok user", Config{HeaderName: "X-Api-Key", HeaderTemplate: "{value}", Mode: ModeUser}, false},
		{"no header name", Config{HeaderTemplate: "{value}", Mode: ModeUser}, true},
		{"bad header name", Config{HeaderName: "Bad Name", HeaderTemplate: "{value}", Mode: ModeUser}, true},
		{"no placeholder", Config{HeaderName: "X", HeaderTemplate: "no-placeholder", Mode: ModeUser}, true},
		{"tenant no secret", Config{HeaderName: "X", HeaderTemplate: "{value}", Mode: ModeTenant}, true},
		{"user with tenant secret", Config{HeaderName: "X", HeaderTemplate: "{value}", Mode: ModeUser, TenantSecret: "x"}, true},
		{"unknown mode", Config{HeaderName: "X", HeaderTemplate: "{value}", Mode: "weird"}, true},
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

func TestEncodeConfig_RoundTrip(t *testing.T) {
	c := newTestCipher(t)
	crypto.SetCipher(c)
	t.Cleanup(func() { crypto.SetCipher(nil) })

	cfg := Config{HeaderName: "Authorization", HeaderTemplate: "Bearer {value}", Mode: ModeTenant, TenantSecret: "secret-1"}
	sf, err := EncodeConfig(42, cfg)
	if err != nil {
		t.Fatalf("EncodeConfig: %v", err)
	}
	// AAD is bound; the field carries the plaintext until Value() runs.
	if sf.IsZero() {
		t.Fatalf("EncodeConfig produced zero SecretField")
	}
}

// integration-style tests that touch the database use testcontainers.
func TestStrategy_PersistUserSecret_RoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	store := storagetest.OpenMigrated(t)

	cipher := newTestCipher(t)
	crypto.SetCipher(cipher)
	t.Cleanup(func() { crypto.SetCipher(nil) })

	ctx := context.Background()

	// Seed tenant, user, upstream + strategy config.
	tenant := &storage.Tenant{Name: "acme", ZitadelOrgID: "z-org-1"}
	adminTx, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		t.Fatalf("super session: %v", err)
	}
	if err := adminTx.Create(tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	user := &storage.User{TenantID: tenant.ID, Email: "u@example.com", Name: "u", ZitadelSubject: "sub-1"}
	if err := adminTx.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	up := &storage.Upstream{TenantID: tenant.ID, Identifier: "github", StrategyType: string(upstream.StrategyStaticHeader), McpServerURL: "https://example.com/mcp"}
	if err := adminTx.Create(up).Error; err != nil {
		t.Fatalf("create upstream: %v", err)
	}
	sf, err := EncodeConfig(tenant.ID, Config{HeaderName: "X-Api-Key", HeaderTemplate: "{value}", Mode: ModeUser})
	if err != nil {
		t.Fatalf("EncodeConfig: %v", err)
	}
	cfgRow := &storage.UpstreamStrategyConfig{TenantID: tenant.ID, UpstreamID: up.ID, Type: string(upstream.StrategyStaticHeader), ConfigJSON: sf}
	if err := adminTx.Create(cfgRow).Error; err != nil {
		t.Fatalf("create strategy config: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	s := New(store, cipher, nil)
	lctx := upstream.LinkContext{Tenant: tenant, User: user, Upstream: up}

	if err := s.PersistUserSecret(ctx, lctx, "tok-123"); err != nil {
		t.Fatalf("PersistUserSecret: %v", err)
	}

	// Reload the link so Headers can read it.
	tx, commit2, err := store.Session(storage.WithTenant(ctx, tenant.ID))
	if err != nil {
		t.Fatalf("tenant session: %v", err)
	}
	var link storage.UpstreamLink
	link.ExtraJSON.SetAAD(intStr(tenant.ID), intStr(user.ID), "upstream.extra")
	if err := tx.Where("tenant_id = ? AND user_id = ? AND upstream_id = ?", tenant.ID, user.ID, up.ID).First(&link).Error; err != nil {
		t.Fatalf("load link: %v", err)
	}
	_ = commit2()

	lctx.Link = &link
	headers, err := s.Headers(ctx, lctx)
	if err != nil {
		t.Fatalf("Headers: %v", err)
	}
	if headers["X-Api-Key"] != "tok-123" {
		t.Fatalf("X-Api-Key = %q, want %q", headers["X-Api-Key"], "tok-123")
	}

	// Rotate the secret.
	if err := s.PersistUserSecret(ctx, lctx, "tok-456"); err != nil {
		t.Fatalf("rotate: %v", err)
	}
}

func intStr(i int64) string {
	// Tiny helper to avoid importing strconv just for tests.
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
