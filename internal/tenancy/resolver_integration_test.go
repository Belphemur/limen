//go:build integration

package tenancy_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/config"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
)

// startPostgres mirrors the helper in internal/storage/storage_test.go.
// Per AGENTS.md we duplicate the bootstrap helper into each integration-test
// package rather than exporting it from production code.
func startPostgres(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pg, err := postgres.Run(ctx,
		"postgres:18-alpine",
		postgres.WithDatabase("limen"),
		postgres.WithUsername("limen"),
		postgres.WithPassword("limen_test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("conn string: %v", err)
	}
	return dsn
}

func provisionRoles(t *testing.T, bootstrapDSN string) (appDSN, adminDSN string) {
	t.Helper()
	db, err := gorm.Open(gormpostgres.Open(bootstrapDSN), &gorm.Config{})
	if err != nil {
		t.Fatalf("open bootstrap: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	for _, q := range []string{
		`DROP ROLE IF EXISTS limen_app`,
		`DROP ROLE IF EXISTS limen_admin`,
		`CREATE ROLE limen_admin LOGIN PASSWORD 'admin_pw' BYPASSRLS`,
		`CREATE ROLE limen_app   LOGIN PASSWORD 'app_pw'`,
		`GRANT limen_app TO limen_admin`,
		`GRANT ALL PRIVILEGES ON DATABASE limen TO limen_admin`,
		`GRANT CREATE, USAGE ON SCHEMA public TO limen_admin`,
		`ALTER SCHEMA public OWNER TO limen_admin`,
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatalf("provision (%s): %v", q, err)
		}
	}
	return rewriteUser(t, bootstrapDSN, "limen_app", "app_pw"),
		rewriteUser(t, bootstrapDSN, "limen_admin", "admin_pw")
}

func rewriteUser(t *testing.T, dsn, user, password string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	u.User = url.UserPassword(user, password)
	return u.String()
}

func openMigrated(t *testing.T) *storage.Store {
	t.Helper()
	bootstrap := startPostgres(t)
	appDSN, adminDSN := provisionRoles(t, bootstrap)
	s, err := storage.Open(config.DatabaseConfig{DSN: appDSN, AdminDSN: adminDSN})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

func seedTenant(t *testing.T, s *storage.Store, name, orgID string) *storage.Tenant {
	t.Helper()
	tn := &storage.Tenant{Name: name, ZitadelOrgID: orgID}
	if err := s.RawDB().Create(tn).Error; err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return tn
}

func TestResolve_FoundReturnsTenant(t *testing.T) {
	s := openMigrated(t)
	want := seedTenant(t, s, "acme", "zorg-acme")

	got, err := tenancy.Resolve(context.Background(), s, want.PublicID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ID != want.ID || got.PublicID != want.PublicID || got.ZitadelOrgID != "zorg-acme" {
		t.Errorf("Resolve returned %+v, want id=%d publicID=%s", got, want.ID, want.PublicID)
	}
}

func TestResolve_UnknownTenantReturnsErrNotFound(t *testing.T) {
	s := openMigrated(t)

	_, err := tenancy.Resolve(context.Background(), s, "tnt_0000000000000000000000000Z")
	if !errors.Is(err, tenancy.ErrNotFound) {
		t.Errorf("Resolve(unknown) err = %v, want ErrNotFound", err)
	}
}

func TestResolve_MalformedIDReturnsErrNotFound(t *testing.T) {
	s := openMigrated(t)

	_, err := tenancy.Resolve(context.Background(), s, "not-an-id")
	if !errors.Is(err, tenancy.ErrNotFound) {
		t.Errorf("Resolve(malformed) err = %v, want ErrNotFound", err)
	}
}

func TestRequireTenant_404OnUnknownTenant(t *testing.T) {
	s := openMigrated(t)
	r := chi.NewRouter()
	r.Route("/t/{tenant}", func(sub chi.Router) {
		sub.Use(tenancy.RequireTenant(s, zap.NewNop()))
		sub.Get("/portal/", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/t/tnt_0000000000000000000000000Z/portal/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestRequireTenant_PinsTenantAndStorageContext(t *testing.T) {
	s := openMigrated(t)
	want := seedTenant(t, s, "acme", "zorg-acme")

	var sawTenant *storage.Tenant
	var sawStorageID int64
	var sawStorageOK bool

	r := chi.NewRouter()
	r.Route("/t/{tenant}", func(sub chi.Router) {
		sub.Use(tenancy.RequireTenant(s, zap.NewNop()))
		sub.Get("/portal/", func(w http.ResponseWriter, req *http.Request) {
			sawTenant = tenancy.MustTenant(req.Context())
			sawStorageID, sawStorageOK = storage.TenantFromCtx(req.Context())
			w.WriteHeader(http.StatusOK)
		})
	})

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/t/" + want.PublicID + "/portal/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if sawTenant == nil || sawTenant.ID != want.ID {
		t.Errorf("handler saw tenant %+v, want id=%d", sawTenant, want.ID)
	}
	if !sawStorageOK || sawStorageID != want.ID {
		t.Errorf("storage tenant pin = (%d,%v), want (%d,true)", sawStorageID, sawStorageOK, want.ID)
	}
}

func TestRequireTenant_CrossTenantCookiePathIsolation(t *testing.T) {
	// Documents the path-scoping property the middleware relies on. Two
	// tenants exist; a request to /t/<b> cannot land in the /t/<a> handler.
	s := openMigrated(t)
	tA := seedTenant(t, s, "alpha", "zorg-alpha")
	tB := seedTenant(t, s, "beta", "zorg-beta")

	served := make(map[string]string)
	r := chi.NewRouter()
	r.Route("/t/{tenant}", func(sub chi.Router) {
		sub.Use(tenancy.RequireTenant(s, zap.NewNop()))
		sub.Get("/portal/", func(w http.ResponseWriter, req *http.Request) {
			tn := tenancy.MustTenant(req.Context())
			served[req.URL.Path] = tn.PublicID
			w.WriteHeader(http.StatusOK)
		})
	})

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	for _, tn := range []*storage.Tenant{tA, tB} {
		resp, err := http.Get(srv.URL + "/t/" + tn.PublicID + "/portal/")
		if err != nil {
			t.Fatalf("GET /t/%s: %v", tn.PublicID, err)
		}
		_ = resp.Body.Close()
	}
	if served["/t/"+tA.PublicID+"/portal/"] != tA.PublicID {
		t.Errorf("/t/<alpha> served as %q", served["/t/"+tA.PublicID+"/portal/"])
	}
	if served["/t/"+tB.PublicID+"/portal/"] != tB.PublicID {
		t.Errorf("/t/<beta> served as %q", served["/t/"+tB.PublicID+"/portal/"])
	}
}
