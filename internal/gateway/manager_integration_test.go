package gateway_test

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"go.uber.org/zap/zaptest"

	"github.com/belphemur/limen/internal/gateway"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/storage/storagetest"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/upstream"
	"github.com/belphemur/limen/internal/upstream/none"
)

// TestManager_ToolsForUser_MultiTenantIsolation verifies that two tenants
// sharing an upstream name see disjoint tool catalogs from ToolsForUser.
//
// Both tenants register an upstream called "shared" with strategy "none"
// (no per-user link required, so every authenticated MCP user sees the
// full per-tenant catalog). Each tenant seeds a distinct pair of tool
// rows. The test then calls ToolsForUser with each tenant on the
// context and asserts:
//
//  1. tenant A sees only A's tools (correct names, correct upstream label)
//  2. tenant B sees only B's tools
//  3. no leakage in either direction even though the upstream name
//     collides verbatim
func TestManager_ToolsForUser_MultiTenantIsolation(t *testing.T) {
	store := storagetest.OpenMigrated(t)
	logger := zaptest.NewLogger(t)

	tenantA := seedTenant(t, store, "acme", "zorg-acme")
	tenantB := seedTenant(t, store, "globex", "zorg-globex")

	upA := seedUpstream(t, store, tenantA.ID, "shared", "https://a.example/mcp")
	upB := seedUpstream(t, store, tenantB.ID, "shared", "https://b.example/mcp")

	seedTool(t, store, tenantA.ID, upA.ID, "acme_search", "search inside acme")
	seedTool(t, store, tenantA.ID, upA.ID, "acme_fetch", "fetch inside acme")
	seedTool(t, store, tenantB.ID, upB.ID, "globex_search", "search inside globex")
	seedTool(t, store, tenantB.ID, upB.ID, "globex_fetch", "fetch inside globex")

	registry := upstream.NewRegistry()
	registry.Register(none.New(nil))
	mgr, err := gateway.NewManager(gateway.ManagerOptions{
		Store:    store,
		Service:  upstream.NewService(store, registry),
		Registry: registry,
		Logger:   logger,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	gotA := toolNames(t, mgr, tenantA)
	gotB := toolNames(t, mgr, tenantB)

	wantA := []string{"acme_fetch", "acme_search"}
	wantB := []string{"globex_fetch", "globex_search"}
	if !equalSorted(gotA, wantA) {
		t.Errorf("tenant A tools: got %v, want %v", gotA, wantA)
	}
	if !equalSorted(gotB, wantB) {
		t.Errorf("tenant B tools: got %v, want %v", gotB, wantB)
	}
	for _, name := range gotA {
		for _, b := range wantB {
			if name == b {
				t.Errorf("tenant A leaked tool %q from tenant B", name)
			}
		}
	}
	for _, name := range gotB {
		for _, a := range wantA {
			if name == a {
				t.Errorf("tenant B leaked tool %q from tenant A", name)
			}
		}
	}
}

func seedTenant(t *testing.T, store *storage.Store, name, orgID string) *storage.Tenant {
	t.Helper()
	ctx := storage.WithSuperuser(context.Background())
	db, commit, err := store.Session(ctx)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	tenant := &storage.Tenant{Name: name, ZitadelOrgID: orgID}
	if err := db.Create(tenant).Error; err != nil {
		_ = commit()
		t.Fatalf("create tenant %q: %v", name, err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit tenant %q: %v", name, err)
	}
	return tenant
}

func seedUpstream(t *testing.T, store *storage.Store, tenantID int64, name, url string) *storage.Upstream {
	t.Helper()
	ctx := storage.WithTenant(context.Background(), tenantID)
	db, commit, err := store.Session(ctx)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	up := &storage.Upstream{
		TenantID:     tenantID,
		Identifier:   name,
		StrategyType: string(upstream.StrategyNone),
		McpServerURL: url,
	}
	if err := db.Create(up).Error; err != nil {
		_ = commit()
		t.Fatalf("create upstream %q: %v", name, err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit upstream %q: %v", name, err)
	}
	return up
}

func seedTool(t *testing.T, store *storage.Store, tenantID, upstreamID int64, name, desc string) {
	t.Helper()
	ctx := storage.WithTenant(context.Background(), tenantID)
	db, commit, err := store.Session(ctx)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	schema, _ := json.Marshal(map[string]any{"type": "object"})
	row := &storage.UpstreamTool{
		TenantID:        tenantID,
		UpstreamID:      upstreamID,
		Name:            name,
		Description:     desc,
		InputSchemaJSON: schema,
	}
	if err := db.Create(row).Error; err != nil {
		_ = commit()
		t.Fatalf("create tool %q: %v", name, err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit tool %q: %v", name, err)
	}
}

func toolNames(t *testing.T, mgr *gateway.Manager, tenant *storage.Tenant) []string {
	t.Helper()
	ctx := tenancy.WithTenant(context.Background(), tenant)
	entries, err := mgr.ToolsForUser(ctx)
	if err != nil {
		t.Fatalf("ToolsForUser(tenant=%q): %v", tenant.Name, err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Upstream != "shared" {
			t.Errorf("tenant %q: expected upstream label %q, got %q", tenant.Name, "shared", e.Upstream)
		}
		out = append(out, e.Name)
	}
	return out
}

func equalSorted(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
