package cli

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/config"
	"github.com/belphemur/limen/internal/storage"
)

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

func openMigrated(t *testing.T) (*storage.Store, string, string) {
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
	return s, appDSN, adminDSN
}

// writeMinimalConfig creates a config file with placeholder values that pass
// config.Load validation. The DSNs are syntactically valid but point at
// localhost — the seed command will fail at storage.Open before reaching the
// real flag validation, so these are fine for unit-level validation tests.
func writeMinimalConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `server:
  port: 8080
database:
  dsn: "postgres://limen_app:app_pw@localhost:5432/limen?sslmode=disable"
  admin_dsn: "postgres://limen_admin:admin_pw@localhost:5432/limen?sslmode=disable"
security:
  token_encryption_key: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
oidc:
  issuer: "https://auth.example.com"
  client_id: "test-client"
  redirect_uri: "https://example.com/auth/callback"
zitadel:
  domain: "https://auth.example.com"
  project_id: "test-project"
  mcp_resource_audience: "test-audience"
  auth_mode: "pat"
  pat: "test-pat"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write minimal config: %v", err)
	}
	return path
}

// writeTestConfig creates a config file with the real Postgres DSNs from a
// provisioned test container, plus all other fields required by config.Load.
func writeTestConfig(t *testing.T, appDSN, adminDSN string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := fmt.Sprintf(`server:
  port: 8080
database:
  dsn: %q
  admin_dsn: %q
security:
  token_encryption_key: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
oidc:
  issuer: "https://auth.example.com"
  client_id: "test-client"
  redirect_uri: "https://example.com/auth/callback"
zitadel:
  domain: "https://auth.example.com"
  project_id: "test-project"
  mcp_resource_audience: "test-audience"
  auth_mode: "pat"
  pat: "test-pat"
`, appDSN, adminDSN)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}
	return path
}

func TestSeedCommand_Validation(t *testing.T) {
	configPath := writeMinimalConfig(t)

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "invalid days",
			args:    []string{"seed", "--config", configPath, "--days", "0"},
			wantErr: "--days must be greater than 0",
		},
		{
			name:    "invalid users",
			args:    []string{"seed", "--config", configPath, "--users", "-1"},
			wantErr: "--users must be greater than or equal to 0",
		},
		{
			name:    "invalid sas",
			args:    []string{"seed", "--config", configPath, "--sas", "-1"},
			wantErr: "--sas must be greater than or equal to 0",
		},
		{
			name:    "invalid tenant id",
			args:    []string{"seed", "--config", configPath, "--tenant-id", "invalid-ulid"},
			wantErr: "invalid --tenant-id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewAdminRootCommand()
			cmd.SetArgs(tt.args)
			err := cmd.ExecuteContext(context.Background())
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestSeedCommand_IntegrationAndIdempotency(t *testing.T) {
	s, appDSN, adminDSN := openMigrated(t)
	configPath := writeTestConfig(t, appDSN, adminDSN)

	// 1. Initial Seed Run
	cmd := NewAdminRootCommand()
	cmd.SetArgs([]string{
		"seed",
		"--config", configPath,
		"--tenant-id", "tnt_01HGPX4D1Q6G9M0C6G58V206W0",
		"--days", "5",
		"--users", "3",
		"--sas", "2",
	})

	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("first seed run failed: %v", err)
	}

	// Verify database rows exist
	db := s.RawDB()

	var tenant storage.Tenant
	if err := db.Where("public_id = ?", "tnt_01HGPX4D1Q6G9M0C6G58V206W0").First(&tenant).Error; err != nil {
		t.Fatalf("tenant not found: %v", err)
	}

	var users []storage.User
	if err := db.Where("tenant_id = ?", tenant.ID).Find(&users).Error; err != nil {
		t.Fatalf("failed to query users: %v", err)
	}
	if len(users) != 3 {
		t.Errorf("expected 3 users, got %d", len(users))
	}

	var sas []storage.ServiceAccount
	if err := db.Where("tenant_id = ?", tenant.ID).Find(&sas).Error; err != nil {
		t.Fatalf("failed to query sas: %v", err)
	}
	if len(sas) != 2 {
		t.Errorf("expected 2 service accounts, got %d", len(sas))
	}

	var activeMonths []storage.ActiveUserMonth
	if err := db.Where("tenant_id = ?", tenant.ID).Find(&activeMonths).Error; err != nil {
		t.Fatalf("failed to query active user months: %v", err)
	}
	if len(activeMonths) == 0 {
		t.Error("expected active user months to be seeded, got 0")
	}

	// 2. Second Seed Run (Idempotent check, no reset)
	cmd2 := NewAdminRootCommand()
	cmd2.SetArgs([]string{
		"seed",
		"--config", configPath,
		"--tenant-id", "tnt_01HGPX4D1Q6G9M0C6G58V206W0",
		"--days", "5",
		"--users", "3",
		"--sas", "2",
	})
	if err := cmd2.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("idempotent seed run failed: %v", err)
	}

	// 3. Third Seed Run with --reset
	cmd3 := NewAdminRootCommand()
	cmd3.SetArgs([]string{
		"seed",
		"--config", configPath,
		"--tenant-id", "tnt_01HGPX4D1Q6G9M0C6G58V206W0",
		"--days", "5",
		"--users", "3",
		"--sas", "2",
		"--reset",
	})
	if err := cmd3.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("reset seed run failed: %v", err)
	}

	// Verify tenant was recreated and seeded successfully
	var tenant2 storage.Tenant
	if err := db.Where("public_id = ?", "tnt_01HGPX4D1Q6G9M0C6G58V206W0").First(&tenant2).Error; err != nil {
		t.Fatalf("tenant not found after reset: %v", err)
	}
}
