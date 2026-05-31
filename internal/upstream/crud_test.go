//go:build integration

package upstream_test

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/storage/storagetest"
	"github.com/belphemur/limen/internal/upstream"
	"github.com/belphemur/limen/internal/upstream/none"
	"github.com/belphemur/limen/internal/upstream/statichdr"
)

// crudFixture provisions a tenant + admin user the CRUD tests can
// pin requests against. Uses the superuser pool — these tests are
// not request-time code, they bootstrap the state the real handler
// reads through RLS later.
type crudFixture struct {
	tenant *storage.Tenant
	user   *storage.User
}

func setupCRUDFixture(t *testing.T, store *storage.Store) *crudFixture {
	t.Helper()
	ctx := context.Background()
	tx, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		t.Fatalf("super session: %v", err)
	}
	tenant := &storage.Tenant{Name: "acme", ZitadelOrgID: "z-crud-1"}
	if err := tx.Create(tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	user := &storage.User{TenantID: tenant.ID, Email: "owner@example.com", Name: "owner", ZitadelSubject: "sub-crud-1"}
	if err := tx.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return &crudFixture{tenant: tenant, user: user}
}

func newCRUDService(t *testing.T, store *storage.Store) *upstream.Service {
	t.Helper()
	registry := upstream.NewRegistry()
	registry.Register(none.New(nil))
	registry.Register(statichdr.New(store, newTestCipher(t), nil))
	return upstream.NewService(store, registry, zap.NewNop())
}

func TestCreateUpstream_NoneStrategy_Succeeds(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	store := storagetest.OpenMigrated(t)
	cipher := newTestCipher(t)
	crypto.SetCipher(cipher)
	t.Cleanup(func() { crypto.SetCipher(nil) })

	fix := setupCRUDFixture(t, store)
	svc := newCRUDService(t, store)
	ctx := context.Background()

	up, err := svc.CreateUpstream(ctx, fix.tenant, upstream.CreateUpstreamInput{
		Identifier:   "public-mcp",
		DisplayName:  "Public MCP",
		MCPServerURL: "https://example.com/mcp",
		StrategyType: upstream.StrategyNone,
	})
	if err != nil {
		t.Fatalf("CreateUpstream: %v", err)
	}
	if up.PublicID == "" || up.DisplayName != "Public MCP" {
		t.Fatalf("unexpected upstream row: %+v", up)
	}
	if svc.RequiresLink(upstream.StrategyNone) {
		t.Errorf("RequiresLink(none) = true, want false")
	}
}

func TestCreateUpstream_DuplicateName_ReturnsAlreadyExists(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	store := storagetest.OpenMigrated(t)
	crypto.SetCipher(newTestCipher(t))
	t.Cleanup(func() { crypto.SetCipher(nil) })

	fix := setupCRUDFixture(t, store)
	svc := newCRUDService(t, store)
	ctx := context.Background()

	in := upstream.CreateUpstreamInput{
		Identifier:   "dup",
		MCPServerURL: "https://example.com/mcp",
		StrategyType: upstream.StrategyNone,
	}
	if _, err := svc.CreateUpstream(ctx, fix.tenant, in); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := svc.CreateUpstream(ctx, fix.tenant, in)
	if err == nil || err != upstream.ErrUpstreamAlreadyExists {
		t.Fatalf("second create err = %v, want ErrUpstreamAlreadyExists", err)
	}
}

func TestCreateUpstream_InvalidDefaultsJSON_Rejected(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	store := storagetest.OpenMigrated(t)
	crypto.SetCipher(newTestCipher(t))
	t.Cleanup(func() { crypto.SetCipher(nil) })

	fix := setupCRUDFixture(t, store)
	svc := newCRUDService(t, store)
	ctx := context.Background()

	_, err := svc.CreateUpstream(ctx, fix.tenant, upstream.CreateUpstreamInput{
		Identifier:   "bad",
		MCPServerURL: "https://example.com/mcp",
		StrategyType: upstream.StrategyNone,
		DefaultsJSON: []byte(`["not","an","object"]`),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestUpdateUpstream_PatchesDisplayName(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	store := storagetest.OpenMigrated(t)
	crypto.SetCipher(newTestCipher(t))
	t.Cleanup(func() { crypto.SetCipher(nil) })

	fix := setupCRUDFixture(t, store)
	svc := newCRUDService(t, store)
	ctx := context.Background()

	up, err := svc.CreateUpstream(ctx, fix.tenant, upstream.CreateUpstreamInput{
		Identifier:   "u1",
		DisplayName:  "Old",
		MCPServerURL: "https://example.com/mcp",
		StrategyType: upstream.StrategyNone,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	newName := "New display"
	got, err := svc.UpdateUpstream(ctx, fix.tenant, up.PublicID, upstream.UpdateUpstreamPatch{
		DisplayName: &newName,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.DisplayName != "New display" {
		t.Errorf("display_name = %q, want %q", got.DisplayName, "New display")
	}
}

func TestUpdateUpstream_InvalidDefaultsJSON_Rejected(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	store := storagetest.OpenMigrated(t)
	crypto.SetCipher(newTestCipher(t))
	t.Cleanup(func() { crypto.SetCipher(nil) })

	fix := setupCRUDFixture(t, store)
	svc := newCRUDService(t, store)
	ctx := context.Background()

	up, err := svc.CreateUpstream(ctx, fix.tenant, upstream.CreateUpstreamInput{
		Identifier:   "u2",
		MCPServerURL: "https://example.com/mcp",
		StrategyType: upstream.StrategyNone,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = svc.UpdateUpstream(ctx, fix.tenant, up.PublicID, upstream.UpdateUpstreamPatch{
		DefaultsJSON: []byte("not json"),
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestDeleteUpstream_SoftDeletes(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	store := storagetest.OpenMigrated(t)
	crypto.SetCipher(newTestCipher(t))
	t.Cleanup(func() { crypto.SetCipher(nil) })

	fix := setupCRUDFixture(t, store)
	svc := newCRUDService(t, store)
	ctx := context.Background()

	up, err := svc.CreateUpstream(ctx, fix.tenant, upstream.CreateUpstreamInput{
		Identifier:   "to-delete",
		MCPServerURL: "https://example.com/mcp",
		StrategyType: upstream.StrategyNone,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.DeleteUpstream(ctx, fix.tenant, up.PublicID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Re-create with same name should succeed (soft-delete leaves
	// the unique index filtered).
	if _, err := svc.CreateUpstream(ctx, fix.tenant, upstream.CreateUpstreamInput{
		Identifier:   "to-delete",
		MCPServerURL: "https://example.com/mcp",
		StrategyType: upstream.StrategyNone,
	}); err != nil {
		t.Fatalf("recreate after delete: %v", err)
	}
}

// Phase 9g note: static_header stores the admin secret on
// UpstreamTenantLink, so reindex without a user link no longer hits
// ErrCannotReindexWithoutLink — the tenant secret is sufficient. The
// sentinel is now exercised only by per-user strategies (mcp_spec)
// which have their own coverage.

func TestPreviewContext_MergesDefaultsAndLink(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	store := storagetest.OpenMigrated(t)
	cipher := newTestCipher(t)
	crypto.SetCipher(cipher)
	t.Cleanup(func() { crypto.SetCipher(nil) })

	fix := setupCRUDFixture(t, store)
	svc := newCRUDService(t, store)
	ctx := context.Background()

	up, err := svc.CreateUpstream(ctx, fix.tenant, upstream.CreateUpstreamInput{
		Identifier:   "ctx-up",
		MCPServerURL: "https://example.com/mcp",
		StrategyType: upstream.StrategyNone,
		DefaultsJSON: []byte(`{"a":1,"b":"from-defaults"}`),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Seed an upstream_link with its own context_json.
	adminTx, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		t.Fatalf("super: %v", err)
	}
	uid := fix.user.ID
	link := &storage.UpstreamLink{
		TenantID: fix.tenant.ID, UserID: &uid, UpstreamID: up.ID,
		Enabled: true, ContextJSON: []byte(`{"b":"from-link","c":3}`),
	}
	if err := adminTx.Create(link).Error; err != nil {
		t.Fatalf("create link: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	merged, err := svc.PreviewContext(ctx, fix.tenant, up.PublicID, fix.user.PublicID)
	if err != nil {
		t.Fatalf("PreviewContext: %v", err)
	}
	got := string(merged)
	// link overrides defaults for "b"; "a" from defaults preserved.
	if want := `"a":1`; !contains(got, want) {
		t.Errorf("missing %s in %s", want, got)
	}
	if want := `"b":"from-link"`; !contains(got, want) {
		t.Errorf("missing %s in %s", want, got)
	}
	if want := `"c":3`; !contains(got, want) {
		t.Errorf("missing %s in %s", want, got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Silence unused fixture method warning.
var _ = (*crudFixture)(nil)
