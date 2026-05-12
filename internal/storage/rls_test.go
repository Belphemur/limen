package storage_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/belphemur/limen/internal/storage"
)

// seedTwoTenants returns two committed tenants A and B for cross-tenant
// isolation assertions.
func seedTwoTenants(t *testing.T, s *storage.Store) (a, b *storage.Tenant) {
	t.Helper()
	db := s.RawDB()
	a = &storage.Tenant{Slug: "alpha", Name: "Alpha", ZitadelOrgID: "zorg-a"}
	b = &storage.Tenant{Slug: "beta", Name: "Beta", ZitadelOrgID: "zorg-b"}
	if err := db.Create(a).Error; err != nil {
		t.Fatalf("create A: %v", err)
	}
	if err := db.Create(b).Error; err != nil {
		t.Fatalf("create B: %v", err)
	}
	return a, b
}

func TestRLS_AppPoolRequiresTenantGUC(t *testing.T) {
	s := openMigrated(t)
	a, _ := seedTwoTenants(t, s)

	// Insert a user under tenant A via Session — should succeed because the
	// tenant pin satisfies the WITH CHECK clause.
	ctx := storage.WithTenant(context.Background(), a.ID)
	tx, commit, err := s.Session(ctx)
	if err != nil {
		t.Fatalf("Session(A): %v", err)
	}
	user := &storage.User{
		TenantID:       a.ID,
		Email:          "u@a.test",
		Name:           "U",
		ZitadelSubject: "zsub-u",
	}
	if err := tx.Create(user).Error; err != nil {
		t.Fatalf("create user A: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestRLS_CrossTenantSelectReturnsZeroRows(t *testing.T) {
	s := openMigrated(t)
	a, b := seedTwoTenants(t, s)

	// Insert one user into A via the admin pool (bypasses RLS).
	if err := s.RawDB().Create(&storage.User{
		TenantID: a.ID, Email: "x@a.test", Name: "X", ZitadelSubject: "zsub-x",
	}).Error; err != nil {
		t.Fatalf("seed A: %v", err)
	}

	// As tenant B over the app pool, A's row must be invisible.
	ctx := storage.WithTenant(context.Background(), b.ID)
	tx, commit, err := s.Session(ctx)
	if err != nil {
		t.Fatalf("Session(B): %v", err)
	}
	defer func() { _ = commit() }()
	var count int64
	if err := tx.Model(&storage.User{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("tenant B saw %d rows from tenant A; RLS not enforced", count)
	}
}

func TestRLS_WithCheckBlocksWrongTenantInsert(t *testing.T) {
	s := openMigrated(t)
	a, b := seedTwoTenants(t, s)

	// Authenticated as tenant B; try to insert a row into tenant A.
	ctx := storage.WithTenant(context.Background(), b.ID)
	tx, commit, err := s.Session(ctx)
	if err != nil {
		t.Fatalf("Session(B): %v", err)
	}
	defer func() { _ = commit() }()
	err = tx.Create(&storage.User{
		TenantID: a.ID, Email: "y@a.test", Name: "Y", ZitadelSubject: "zsub-y",
	}).Error
	if err == nil {
		t.Fatal("expected RLS WITH CHECK violation, got nil")
	}
}

func TestRLS_UnscopedFromTenantSessionStillFiltered(t *testing.T) {
	s := openMigrated(t)
	a, b := seedTwoTenants(t, s)

	if err := s.RawDB().Create(&storage.User{
		TenantID: a.ID, Email: "z@a.test", Name: "Z", ZitadelSubject: "zsub-z",
	}).Error; err != nil {
		t.Fatalf("seed A: %v", err)
	}

	ctx := storage.WithTenant(context.Background(), b.ID)
	tx, commit, err := s.Session(ctx)
	if err != nil {
		t.Fatalf("Session(B): %v", err)
	}
	defer func() { _ = commit() }()
	var users []storage.User
	if err := tx.Unscoped().Find(&users).Error; err != nil {
		t.Fatalf("unscoped find: %v", err)
	}
	if len(users) != 0 {
		t.Errorf("Unscoped() inside tenant B leaked %d rows from tenant A", len(users))
	}
}

func TestRLS_AppRoleWithoutGUCSeesNothing(t *testing.T) {
	s := openMigrated(t)
	a, _ := seedTwoTenants(t, s)
	if err := s.RawDB().Create(&storage.User{
		TenantID: a.ID, Email: "n@a.test", Name: "N", ZitadelSubject: "zsub-n",
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	// SET ROLE drops privileges on a single connection to limen_app, then
	// verifies that without app.current_tenant set, the policy returns zero
	// rows. Catches the most common Phase 3 bug: forgetting FORCE ROW LEVEL
	// SECURITY (which would let the table owner bypass policies).
	db, err := s.RawDB().DB()
	if err != nil {
		t.Fatalf("raw db: %v", err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "SET ROLE limen_app"); err != nil {
		t.Fatalf("set role: %v", err)
	}
	var count int
	if err := conn.QueryRowContext(context.Background(), `SELECT count(*) FROM users`).Scan(&count); err != nil {
		t.Fatalf("count as limen_app: %v", err)
	}
	if count != 0 {
		t.Errorf("limen_app without GUC saw %d rows; RLS not forced", count)
	}
}

func TestRLS_WithSuperuserSessionReadsCrossTenant(t *testing.T) {
	s := openMigrated(t)
	a, b := seedTwoTenants(t, s)
	for _, tn := range []*storage.Tenant{a, b} {
		if err := s.RawDB().Create(&storage.User{
			TenantID: tn.ID, Email: fmt.Sprintf("u-%d@x.test", tn.ID),
			Name: "U", ZitadelSubject: fmt.Sprintf("zsub-%d", tn.ID),
		}).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	ctx := storage.WithSuperuser(context.Background())
	tx, commit, err := s.Session(ctx)
	if err != nil {
		t.Fatalf("superuser Session: %v", err)
	}
	defer func() { _ = commit() }()
	var count int64
	if err := tx.Model(&storage.User{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count < 2 {
		t.Errorf("superuser saw %d rows, want >= 2", count)
	}
}

func TestSession_RejectsNoTenantNoSuperuser(t *testing.T) {
	s := openMigrated(t)
	if _, _, err := s.Session(context.Background()); !errors.Is(err, storage.ErrNoTenant) {
		t.Errorf("expected ErrNoTenant, got %v", err)
	}
}

func TestUpdatedAt_TriggerFiresOnRawUpdate(t *testing.T) {
	s := openMigrated(t)
	db := s.RawDB()
	a := &storage.Tenant{Slug: "trig", Name: "Trig", ZitadelOrgID: "zorg-trig"}
	if err := db.Create(a).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	original := a.UpdatedAt
	time.Sleep(10 * time.Millisecond)

	if err := db.Exec(`UPDATE tenants SET name = ? WHERE id = ?`, "Trig2", a.ID).Error; err != nil {
		t.Fatalf("raw update: %v", err)
	}
	var got time.Time
	if err := db.Raw(`SELECT updated_at FROM tenants WHERE id = ?`, a.ID).Scan(&got).Error; err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	if !got.After(original) {
		t.Errorf("updated_at did not advance after raw UPDATE: %v <= %v", got, original)
	}
}

func TestUpdatedAt_TriggerFiresOnSoftDelete(t *testing.T) {
	s := openMigrated(t)
	db := s.RawDB()
	a := &storage.Tenant{Slug: "sd", Name: "SD", ZitadelOrgID: "zorg-sd"}
	if err := db.Create(a).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	u := &storage.User{
		TenantID: a.ID, Email: "sd@x.test", Name: "SD", ZitadelSubject: "zsub-sd",
	}
	if err := db.Create(u).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	original := u.UpdatedAt
	time.Sleep(10 * time.Millisecond)

	if err := db.Delete(u).Error; err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	var got time.Time
	if err := db.Raw(`SELECT updated_at FROM users WHERE id = ?`, u.ID).Scan(&got).Error; err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	if !got.After(original) {
		t.Errorf("updated_at did not advance on soft-delete: %v <= %v", got, original)
	}
}
