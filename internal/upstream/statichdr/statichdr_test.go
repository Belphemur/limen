//go:build integration

package statichdr

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/storage/storagetest"
	"github.com/belphemur/limen/internal/upstream"
)

var seedCounter int64

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
		{"valid config", Config{HeaderName: "Authorization", HeaderTemplate: "Bearer {value}"}, false},
		{"no header name", Config{HeaderTemplate: "{value}"}, true},
		{"bad header name", Config{HeaderName: "Bad Name", HeaderTemplate: "{value}"}, true},
		{"no placeholder", Config{HeaderName: "X", HeaderTemplate: "no-placeholder"}, true},
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
	cfg, err := ParseConfig(map[string]string{
		"header_name":     "Authorization",
		"header_template": "Bearer {value}",
	})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.HeaderName != "Authorization" {
		t.Fatalf("ParseConfig HeaderName got %q, want %q", cfg.HeaderName, "Authorization")
	}

	cfg2, err := ParseConfig(map[string]string{
		"header_name":     "X",
		"header_template": "{value}",
	})
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg2.HeaderName != "X" {
		t.Fatalf("ParseConfig HeaderName got %q, want %q", cfg2.HeaderName, "X")
	}
}

func TestEncodeConfig_RoundTrip(t *testing.T) {
	c := newTestCipher(t)
	crypto.SetCipher(c)
	t.Cleanup(func() { crypto.SetCipher(nil) })

	cfg := Config{HeaderName: "Authorization", HeaderTemplate: "Bearer {value}"}
	sf, err := EncodeConfig(42, cfg)
	if err != nil {
		t.Fatalf("EncodeConfig: %v", err)
	}
	if sf.IsZero() {
		t.Fatalf("EncodeConfig produced zero SecretField")
	}

	decoded, err := DecodeConfig(42, sf)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if decoded.HeaderName != cfg.HeaderName {
		t.Fatalf("DecodeConfig HeaderName got %q, want %q", decoded.HeaderName, cfg.HeaderName)
	}
	if decoded.HeaderTemplate != cfg.HeaderTemplate {
		t.Fatalf("DecodeConfig HeaderTemplate got %q, want %q", decoded.HeaderTemplate, cfg.HeaderTemplate)
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

// seed creates test data. cfg is the encrypted config payload. mode is
// the Mode column value ("tenant_owner" or "byok"). secret is the
// admin credential written to UpstreamTenantLink.AccessToken; pass
// empty when the test does not call Headers().
func seed(t *testing.T, store *storage.Store, cfg Config, mode, secret string) seedFixture {
	t.Helper()
	ctx := context.Background()
	tx, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		t.Fatalf("super session: %v", err)
	}
	tenant := &storage.Tenant{Name: "acme", ZitadelOrgID: fmt.Sprintf("z-%s-%d", t.Name(), atomic.AddInt64(&seedCounter, 1))}
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
	if err := tx.Create(&storage.UpstreamStrategyConfig{
		TenantID:   tenant.ID,
		UpstreamID: up.ID,
		Type:       string(upstream.StrategyStaticHeader),
		ConfigJSON: sf,
		Mode:       mode,
	}).Error; err != nil {
		t.Fatalf("create strategy config: %v", err)
	}
	if secret != "" {
		tenantStr := fmt.Sprintf("%d", tenant.ID)
		token := crypto.NewSecret([]byte(secret))
		token.SetAAD(tenantStr, "", "upstream.tenant.access_token")
		if err := tx.Create(&storage.UpstreamTenantLink{
			TenantID:    tenant.ID,
			UpstreamID:  up.ID,
			Enabled:     true,
			AccessToken: token,
		}).Error; err != nil {
			t.Fatalf("create tenant link: %v", err)
		}
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

// reloadTenantLink re-fetches the tenant link with AccessToken AAD bound.
func reloadTenantLink(t *testing.T, store *storage.Store, f seedFixture) *storage.UpstreamTenantLink {
	t.Helper()
	ctx := context.Background()
	tx, commit, err := store.Session(storage.WithTenant(ctx, f.tenant.ID))
	if err != nil {
		t.Fatalf("tenant session: %v", err)
	}
	defer func() { _ = commit() }()
	var link storage.UpstreamTenantLink
	link.AccessToken.SetAAD(strconv.FormatInt(f.tenant.ID, 10), "", "upstream.tenant.access_token")
	if err := tx.Where("upstream_id = ?", f.up.ID).First(&link).Error; err != nil {
		return nil
	}
	return &link
}

func TestStrategy_SubMode(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	store := storagetest.OpenMigrated(t)
	cipher := newTestCipher(t)
	crypto.SetCipher(cipher)
	t.Cleanup(func() { crypto.SetCipher(nil) })

	fTenantOwner := seed(t, store, Config{
		HeaderName:     "Authorization",
		HeaderTemplate: "Bearer {value}",
	}, ModeTenantOwner, "")
	s := New(store, cipher, nil)

	lctx := upstream.LinkContext{Tenant: fTenantOwner.tenant, Upstream: fTenantOwner.up}
	sub, err := s.SubMode(context.Background(), lctx)
	if err != nil {
		t.Fatalf("SubMode tenant_owner: %v", err)
	}
	if sub != ModeTenantOwner {
		t.Fatalf("SubMode = %q, want %q", sub, ModeTenantOwner)
	}

	fBYOK := seed(t, store, Config{
		HeaderName:     "Authorization",
		HeaderTemplate: "Bearer {value}",
	}, ModeBYOK, "")
	lctxBYOK := upstream.LinkContext{Tenant: fBYOK.tenant, Upstream: fBYOK.up}
	sub, err = s.SubMode(context.Background(), lctxBYOK)
	if err != nil {
		t.Fatalf("SubMode byok: %v", err)
	}
	if sub != ModeBYOK {
		t.Fatalf("SubMode = %q, want %q", sub, ModeBYOK)
	}
}

func TestStrategy_Headers_TenantOwner(t *testing.T) {
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
	}, ModeTenantOwner, "org-token")
	s := New(store, cipher, nil)

	// PersistUserSecret must reject in TenantOwner mode.
	lctx := upstream.LinkContext{Tenant: f.tenant, User: f.user, Upstream: f.up}
	if err := s.PersistUserSecret(context.Background(), lctx, "anything"); err != upstream.ErrUnsupported {
		t.Fatalf("PersistUserSecret err=%v, want ErrUnsupported", err)
	}

	// StartLink must reject in TenantOwner mode.
	if _, err := s.StartLink(context.Background(), lctx); err != upstream.ErrUnsupported {
		t.Fatalf("StartLink err=%v, want ErrUnsupported", err)
	}

	// Headers always returns the tenant secret regardless of user/link.
	lctx.TenantLink = reloadTenantLink(t, store, f)
	headers, err := s.Headers(context.Background(), lctx)
	if err != nil {
		t.Fatalf("Headers: %v", err)
	}
	if got := headers["Authorization"]; got != "Bearer org-token" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer org-token")
	}

	// No user (system/catalog) — also uses tenant secret.
	lctxNoUser := upstream.LinkContext{Tenant: f.tenant, Upstream: f.up, TenantLink: reloadTenantLink(t, store, f)}
	headers, err = s.Headers(context.Background(), lctxNoUser)
	if err != nil {
		t.Fatalf("Headers (no user): %v", err)
	}
	if got := headers["Authorization"]; got != "Bearer org-token" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer org-token")
	}
}

func TestStrategy_Headers_BYOK_NoUser_UsesTenantSecret(t *testing.T) {
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
	}, ModeBYOK, "setup-key")
	s := New(store, cipher, nil)
	ctx := context.Background()

	// No user (system/catalog/indexer) uses the admin's setup key.
	lctx := upstream.LinkContext{Tenant: f.tenant, Upstream: f.up, TenantLink: reloadTenantLink(t, store, f)}
	headers, err := s.Headers(ctx, lctx)
	if err != nil {
		t.Fatalf("Headers (no user): %v", err)
	}
	if got := headers["X-Api-Key"]; got != "setup-key" {
		t.Fatalf("X-Api-Key = %q, want %q", got, "setup-key")
	}
}

func TestStrategy_Headers_BYOK_UserWithoutKey_ReturnsErrNoCredentials(t *testing.T) {
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
	}, ModeBYOK, "setup-key")
	s := New(store, cipher, nil)
	ctx := context.Background()

	lctx := upstream.LinkContext{Tenant: f.tenant, User: f.user, Upstream: f.up}
	_, err := s.Headers(ctx, lctx)
	if err != upstream.ErrNoCredentials {
		t.Fatalf("Headers err=%v, want ErrNoCredentials", err)
	}
}

func TestStrategy_Headers_BYOK_UserWithKey_UsesUserKey(t *testing.T) {
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
	}, ModeBYOK, "setup-key")
	s := New(store, cipher, nil)
	ctx := context.Background()
	lctx := upstream.LinkContext{Tenant: f.tenant, User: f.user, Upstream: f.up}

	// Persist the user's BYOK key.
	if err := s.PersistUserSecret(ctx, lctx, "user-key"); err != nil {
		t.Fatalf("PersistUserSecret: %v", err)
	}
	lctx.Link = reloadLink(t, store, f)

	headers, err := s.Headers(ctx, lctx)
	if err != nil {
		t.Fatalf("Headers: %v", err)
	}
	if got := headers["X-Api-Key"]; got != "user-key" {
		t.Fatalf("X-Api-Key = %q, want %q", got, "user-key")
	}
}

func TestStrategy_Headers_BYOK_NeedsRelink_ReturnsErrNeedsRelink(t *testing.T) {
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
	}, ModeBYOK, "setup-key")
	s := New(store, cipher, nil)
	ctx := context.Background()
	lctx := upstream.LinkContext{Tenant: f.tenant, User: f.user, Upstream: f.up}

	// Persist the user's key first.
	if err := s.PersistUserSecret(ctx, lctx, "user-key"); err != nil {
		t.Fatalf("PersistUserSecret: %v", err)
	}
	lctx.Link = reloadLink(t, store, f)

	// Simulate NeedsRelink being set (e.g. by Phase 8 reactive-401).
	lctx.Link.NeedsRelink = true
	_, err := s.Headers(ctx, lctx)
	if err != upstream.ErrNeedsRelink {
		t.Fatalf("Headers err=%v, want ErrNeedsRelink", err)
	}
}

func TestStrategy_Headers_BYOK_LinkWithoutExtra_ReturnsErrNoCredentials(t *testing.T) {
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
	}, ModeBYOK, "setup-key")
	s := New(store, cipher, nil)
	ctx := context.Background()

	// Create a link with no ExtraJSON (user hasn't provided a key).
	if err := s.PersistUserSecret(ctx, upstream.LinkContext{Tenant: f.tenant, User: f.user, Upstream: f.up}, "tmp"); err != nil {
		t.Fatalf("PersistUserSecret: %v", err)
	}
	// Now clear the override.
	if err := s.ClearUserOverride(ctx, upstream.LinkContext{Tenant: f.tenant, User: f.user, Upstream: f.up}); err != nil {
		t.Fatalf("ClearUserOverride: %v", err)
	}
	lctx := upstream.LinkContext{Tenant: f.tenant, User: f.user, Upstream: f.up, Link: reloadLink(t, store, f)}

	_, err := s.Headers(ctx, lctx)
	if err != upstream.ErrNoCredentials {
		t.Fatalf("Headers err=%v, want ErrNoCredentials", err)
	}
}

func TestStrategy_ClearUserOverride_FallsBackToTenantSecret(t *testing.T) {
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
	}, ModeBYOK, "setup-key")
	s := New(store, cipher, nil)
	ctx := context.Background()
	lctx := upstream.LinkContext{Tenant: f.tenant, User: f.user, Upstream: f.up}

	if err := s.PersistUserSecret(ctx, lctx, "user-key"); err != nil {
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

	// After clear, user has no key → ErrNoCredentials in BYOK mode.
	_, err := s.Headers(ctx, lctx)
	if err != upstream.ErrNoCredentials {
		t.Fatalf("Headers post-clear err=%v, want ErrNoCredentials", err)
	}
}

func TestStrategy_StartLink_UnsupportedWhenTenantOwner(t *testing.T) {
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
	}, ModeTenantOwner, "")
	s := New(store, cipher, nil)
	lctx := upstream.LinkContext{Tenant: f.tenant, User: f.user, Upstream: f.up}

	_, err := s.StartLink(context.Background(), lctx)
	if err != upstream.ErrUnsupported {
		t.Fatalf("StartLink err=%v, want ErrUnsupported", err)
	}
}

func TestStrategy_PersistUserSecret_UnsupportedWhenTenantOwner(t *testing.T) {
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
	}, ModeTenantOwner, "")
	s := New(store, cipher, nil)
	lctx := upstream.LinkContext{Tenant: f.tenant, User: f.user, Upstream: f.up}

	err := s.PersistUserSecret(context.Background(), lctx, "secret")
	if err != upstream.ErrUnsupported {
		t.Fatalf("PersistUserSecret err=%v, want ErrUnsupported", err)
	}
}

func TestStrategy_StartLink_WorksInBYOKMode(t *testing.T) {
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
	}, ModeBYOK, "")
	s := New(store, cipher, nil)
	lctx := upstream.LinkContext{Tenant: f.tenant, User: f.user, Upstream: f.up}

	result, err := s.StartLink(context.Background(), lctx)
	if err != nil {
		t.Fatalf("StartLink err=%v, want nil", err)
	}
	if result.RedirectURL == "" {
		t.Fatalf("StartLink RedirectURL is empty")
	}
}

func TestApplyConfigPatch(t *testing.T) {
	cur := Config{HeaderName: "X", HeaderTemplate: "{value}"}

	// Secret rotation is no longer handled by ApplyConfigPatch; it is a
	// no-op that validates the current config.
	next, err := ApplyConfigPatch(cur, map[string]string{"value": "new"})
	if err != nil {
		t.Fatalf("ApplyConfigPatch: %v", err)
	}
	if next.HeaderName != cur.HeaderName {
		t.Fatalf("HeaderName = %q, want %q", next.HeaderName, cur.HeaderName)
	}

	// Empty value is also a no-op.
	next, err = ApplyConfigPatch(cur, map[string]string{"value": ""})
	if err != nil {
		t.Fatalf("ApplyConfigPatch: %v", err)
	}
	if next.HeaderName != cur.HeaderName {
		t.Fatalf("HeaderName = %q, want %q", next.HeaderName, cur.HeaderName)
	}

	// Unknown keys are ignored.
	next, err = ApplyConfigPatch(cur, map[string]string{"allow_user_override": "true"})
	if err != nil {
		t.Fatalf("ApplyConfigPatch: %v", err)
	}
	if next.HeaderName != cur.HeaderName {
		t.Fatalf("HeaderName = %q, want %q", next.HeaderName, cur.HeaderName)
	}
}
