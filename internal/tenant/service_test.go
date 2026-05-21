package tenant_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/storage/storagetest"
	"github.com/belphemur/limen/internal/tenant"
)

func newTenantCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	var key crypto.Key
	for i := range key {
		key[i] = byte(i + 23)
	}
	c, err := crypto.NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	return c
}

// setup spins up a fresh Postgres + provisions a tenant + cipher.
// Returns the store, service, and the seeded tenant.
func setup(t *testing.T) (*storage.Store, *tenant.Service, *storage.Tenant) {
	t.Helper()
	if testing.Short() {
		t.Skip("requires postgres")
	}
	store := storagetest.OpenMigrated(t)
	crypto.SetCipher(newTenantCipher(t))
	t.Cleanup(func() { crypto.SetCipher(nil) })

	ctx := context.Background()
	tx, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		t.Fatalf("super session: %v", err)
	}
	tnt := &storage.Tenant{Name: "Acme", ZitadelOrgID: "z-tenant-test"}
	if err := tx.Create(tnt).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	return store, tenant.NewService(store), tnt
}

func TestLoadSettings_CreatesRowOnFirstRead(t *testing.T) {
	_, svc, tnt := setup(t)
	ctx := context.Background()

	first, org, err := svc.LoadSettings(ctx, tnt)
	if err != nil {
		t.Fatalf("first LoadSettings: %v", err)
	}
	if first == nil {
		t.Fatal("settings nil")
	}
	if first.TenantID != tnt.ID {
		t.Errorf("tenant id mismatch: got %d want %d", first.TenantID, tnt.ID)
	}
	if org != "z-tenant-test" {
		t.Errorf("zitadel org = %q", org)
	}

	second, _, err := svc.LoadSettings(ctx, tnt)
	if err != nil {
		t.Fatalf("second LoadSettings: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("second call created a new row: first=%d second=%d", first.ID, second.ID)
	}
}

func TestUpdateSettings_SetInvitedTeamAt_IsOneShot(t *testing.T) {
	_, svc, tnt := setup(t)
	ctx := context.Background()

	first, err := svc.UpdateSettings(ctx, tnt, tenant.UpdateSettingsInput{SetInvitedTeamAt: true})
	if err != nil {
		t.Fatalf("first flip: %v", err)
	}
	if first.InvitedTeamAt == nil {
		t.Fatal("InvitedTeamAt not set on first flip")
	}
	original := *first.InvitedTeamAt

	time.Sleep(5 * time.Millisecond)
	second, err := svc.UpdateSettings(ctx, tnt, tenant.UpdateSettingsInput{SetInvitedTeamAt: true})
	if err != nil {
		t.Fatalf("second flip: %v", err)
	}
	if second.InvitedTeamAt == nil || !second.InvitedTeamAt.Equal(original) {
		t.Errorf("InvitedTeamAt changed across calls: original=%v new=%v", original, second.InvitedTeamAt)
	}
}

func TestUpdateSettings_SetChoseIDEAt_IsOneShot(t *testing.T) {
	_, svc, tnt := setup(t)
	ctx := context.Background()

	first, err := svc.UpdateSettings(ctx, tnt, tenant.UpdateSettingsInput{SetChoseIDEAt: true})
	if err != nil {
		t.Fatalf("first flip: %v", err)
	}
	if first.ChoseIDEAt == nil {
		t.Fatal("ChoseIDEAt not set on first flip")
	}
	original := *first.ChoseIDEAt

	time.Sleep(5 * time.Millisecond)
	second, err := svc.UpdateSettings(ctx, tnt, tenant.UpdateSettingsInput{SetChoseIDEAt: true})
	if err != nil {
		t.Fatalf("second flip: %v", err)
	}
	if second.ChoseIDEAt == nil || !second.ChoseIDEAt.Equal(original) {
		t.Errorf("ChoseIDEAt changed across calls: original=%v new=%v", original, second.ChoseIDEAt)
	}
}

func TestDelete_ConfirmationMismatch(t *testing.T) {
	_, svc, tnt := setup(t)
	err := svc.Delete(context.Background(), tnt, "tnt_wrong")
	if !errors.Is(err, tenant.ErrConfirmationMismatch) {
		t.Fatalf("err = %v, want ErrConfirmationMismatch", err)
	}
	// Tenant still alive.
	if _, _, err := svc.LoadSettings(context.Background(), tnt); err != nil {
		t.Fatalf("settings load after mismatch: %v", err)
	}
}

func TestDelete_CascadesOwnedRowsAndIdempotent(t *testing.T) {
	store, svc, tnt := setup(t)
	ctx := context.Background()

	// Seed owned rows: settings (via LoadSettings) + a user + an
	// upstream + a strategy config + a link.
	if _, _, err := svc.LoadSettings(ctx, tnt); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	tx, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		t.Fatalf("seed tx: %v", err)
	}
	u := &storage.User{TenantID: tnt.ID, Email: "owner@example.com", ZitadelSubject: "sub-cascade"}
	if err := tx.Create(u).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	up := &storage.Upstream{
		TenantID:     tnt.ID,
		Identifier:   "u1",
		DisplayName:  "U1",
		StrategyType: "none",
		McpServerURL: "https://example.com/mcp",
	}
	if err := tx.Create(up).Error; err != nil {
		t.Fatalf("seed upstream: %v", err)
	}
	link := &storage.UpstreamLink{TenantID: tnt.ID, UserID: u.ID, UpstreamID: up.ID, Enabled: true}
	if err := tx.Create(link).Error; err != nil {
		t.Fatalf("seed link: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	if err := svc.Delete(ctx, tnt, tnt.PublicID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Verify all owned rows are soft-deleted (visible only Unscoped).
	tx2, commit2, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		t.Fatalf("verify tx: %v", err)
	}
	defer func() { _ = commit2() }()

	checkLive := func(name string, model any) {
		var n int64
		if err := tx2.Model(model).Where("tenant_id = ?", tnt.ID).Count(&n).Error; err != nil {
			t.Errorf("count live %s: %v", name, err)
			return
		}
		if n != 0 {
			t.Errorf("live %s rows remain: %d", name, n)
		}
	}
	checkLive("upstream_links", &storage.UpstreamLink{})
	checkLive("upstreams", &storage.Upstream{})
	checkLive("users", &storage.User{})
	checkLive("tenant_settings", &storage.TenantSettings{})

	var tCount int64
	if err := tx2.Model(&storage.Tenant{}).Where("id = ?", tnt.ID).Count(&tCount).Error; err != nil {
		t.Fatalf("count tenant: %v", err)
	}
	if tCount != 0 {
		t.Errorf("tenant row still live")
	}

	// Idempotent: second delete returns nil (row already soft-deleted,
	// nothing to do — RowsAffected == 0 is a no-op, not an error).
	if err := svc.Delete(ctx, tnt, tnt.PublicID); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}
