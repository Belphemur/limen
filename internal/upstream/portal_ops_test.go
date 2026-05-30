package upstream_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/storage/storagetest"
	"github.com/belphemur/limen/internal/upstream"
	"github.com/belphemur/limen/internal/upstream/statichdr"
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

func TestService_PortalOps_FullFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	store := storagetest.OpenMigrated(t)
	cipher := newTestCipher(t)
	crypto.SetCipher(cipher)
	t.Cleanup(func() { crypto.SetCipher(nil) })

	ctx := context.Background()

	registry := upstream.NewRegistry()
	registry.Register(statichdr.New(store, cipher, nil))
	svc := upstream.NewService(store, registry, zap.NewNop())

	tenant := &storage.Tenant{Name: "acme", ZitadelOrgID: "z-org-portal"}
	adminTx, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		t.Fatalf("super session: %v", err)
	}
	if err := adminTx.Create(tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	user := &storage.User{TenantID: tenant.ID, Email: "u@example.com", Name: "u", ZitadelSubject: "sub-portal-1"}
	if err := adminTx.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	up := &storage.Upstream{TenantID: tenant.ID, Identifier: "github", StrategyType: string(upstream.StrategyStaticHeader), McpServerURL: "https://example.com/mcp"}
	if err := adminTx.Create(up).Error; err != nil {
		t.Fatalf("create upstream: %v", err)
	}
	cfg, err := statichdr.EncodeConfig(tenant.ID, statichdr.Config{HeaderName: "X-Api-Key", HeaderTemplate: "{value}"})
	if err != nil {
		t.Fatalf("encode config: %v", err)
	}
	if err := adminTx.Create(&storage.UpstreamStrategyConfig{TenantID: tenant.ID, UpstreamID: up.ID, Type: string(upstream.StrategyStaticHeader), ConfigJSON: cfg, Mode: "byok"}).Error; err != nil {
		t.Fatalf("create cfg: %v", err)
	}
	// Create the tenant link with the admin secret.
	tenantStr := fmt.Sprintf("%d", tenant.ID)
	token := crypto.NewSecret([]byte("shared"))
	token.SetAAD(tenantStr, "", "upstream.tenant.access_token")
	if err := adminTx.Create(&storage.UpstreamTenantLink{
		TenantID:    tenant.ID,
		UpstreamID:  up.ID,
		Enabled:     true,
		AccessToken: token,
	}).Error; err != nil {
		t.Fatalf("create tenant link: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// LookupUserBySubject finds the user.
	gotUser, err := svc.LoadUserBySubject(ctx, tenant.ID, "sub-portal-1")
	if err != nil {
		t.Fatalf("LoadUserBySubject: %v", err)
	}
	if gotUser.ID != user.ID {
		t.Fatalf("user id mismatch: %d != %d", gotUser.ID, user.ID)
	}

	// Initial listing — no link yet.
	rows, err := svc.ListUpstreamsForUser(ctx, tenant, user)
	if err != nil {
		t.Fatalf("ListUpstreamsForUser: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0].LinkState != upstream.LinkStateNone {
		t.Errorf("state = %q, want none", rows[0].LinkState)
	}
	if rows[0].StrategySubMode != "byok" {
		t.Errorf("sub_mode = %q, want byok", rows[0].StrategySubMode)
	}
	if !rows[0].RequiresLink {
		t.Errorf("RequiresLink = false, want true")
	}

	// Submit API key → link created, state connected.
	if err := svc.PersistUserStaticHeaderSecret(ctx, tenant, user, up.PublicID, "tok-abc"); err != nil {
		t.Fatalf("PersistUserStaticHeaderSecret: %v", err)
	}
	rows, err = svc.ListUpstreamsForUser(ctx, tenant, user)
	if err != nil {
		t.Fatalf("ListUpstreamsForUser after persist: %v", err)
	}
	if rows[0].LinkState != upstream.LinkStateConnected {
		t.Fatalf("state after persist = %q, want connected", rows[0].LinkState)
	}

	// Disable.
	if err := svc.SetLinkEnabled(ctx, tenant, user, up.PublicID, false); err != nil {
		t.Fatalf("SetLinkEnabled false: %v", err)
	}
	rows, _ = svc.ListUpstreamsForUser(ctx, tenant, user)
	if rows[0].LinkState != upstream.LinkStateDisabled {
		t.Fatalf("state after disable = %q, want disabled", rows[0].LinkState)
	}

	// Re-enable.
	if err := svc.SetLinkEnabled(ctx, tenant, user, up.PublicID, true); err != nil {
		t.Fatalf("SetLinkEnabled true: %v", err)
	}
	rows, _ = svc.ListUpstreamsForUser(ctx, tenant, user)
	if rows[0].LinkState != upstream.LinkStateConnected {
		t.Fatalf("state after re-enable = %q, want connected", rows[0].LinkState)
	}

	// Force auto_disabled state and verify re-enable clears it.
	now := time.Now()
	link := rows[0].Link
	tx, commit3, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		t.Fatalf("super session: %v", err)
	}
	if err := tx.Model(&storage.UpstreamLink{}).Where("id = ?", link.ID).Updates(map[string]any{
		"enabled":              false,
		"auto_disabled_at":     &now,
		"consecutive_failures": 5,
		"last_failure_reason":  "tool_call_5xx",
		"first_failure_at":     &now,
		"last_failure_at":      &now,
	}).Error; err != nil {
		t.Fatalf("force auto_disabled: %v", err)
	}
	if err := commit3(); err != nil {
		t.Fatalf("commit3: %v", err)
	}
	rows, _ = svc.ListUpstreamsForUser(ctx, tenant, user)
	if rows[0].LinkState != upstream.LinkStateAutoDisabled {
		t.Fatalf("state after force-trip = %q, want auto_disabled", rows[0].LinkState)
	}

	if err := svc.SetLinkEnabled(ctx, tenant, user, up.PublicID, true); err != nil {
		t.Fatalf("SetLinkEnabled true after auto_disable: %v", err)
	}
	rows, _ = svc.ListUpstreamsForUser(ctx, tenant, user)
	if rows[0].LinkState != upstream.LinkStateConnected {
		t.Fatalf("state after recover = %q, want connected", rows[0].LinkState)
	}
	if rows[0].Link.ConsecutiveFailures != 0 || rows[0].Link.AutoDisabledAt != nil {
		t.Fatalf("counters not cleared: failures=%d auto_disabled_at=%v",
			rows[0].Link.ConsecutiveFailures, rows[0].Link.AutoDisabledAt)
	}
}

func TestService_LoadUserBySubject_NotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	store := storagetest.OpenMigrated(t)
	registry := upstream.NewRegistry()
	svc := upstream.NewService(store, registry, zap.NewNop())

	tenant := &storage.Tenant{Name: "acme", ZitadelOrgID: "z-org-nf"}
	tx, commit, err := store.Session(storage.WithSuperuser(context.Background()))
	if err != nil {
		t.Fatalf("super session: %v", err)
	}
	if err := tx.Create(tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if _, err := svc.LoadUserBySubject(context.Background(), tenant.ID, "missing"); err == nil {
		t.Fatal("expected error for missing user, got nil")
	}
}
