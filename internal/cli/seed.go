package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/brianvoe/gofakeit/v6"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/ids"
	"github.com/belphemur/limen/internal/storage"
)

type seedFlags struct {
	tenantPublicID string
	tenantName     string
	days           int
	users          int
	sas            int
	reset          bool
}

func newSeedCommand(rflags *rootFlags, v *viper.Viper) *cobra.Command {
	f := &seedFlags{}

	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Seed the database with deterministic demo data",
		Long: `Populate a tenant with users, service accounts, upstreams,
tools, and simulated billing metrics so the local dashboard has
realistic curves to render. All random data is deterministic (seed=42)
so re-runs produce identical rows.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			logger := newCLILogger()
			defer func() { _ = logger.Sync() }()

			cfg, err := loadConfig(rflags)
			if err != nil {
				return err
			}

			if f.days <= 0 {
				return fmt.Errorf("--days must be greater than 0")
			}
			if f.users < 0 {
				return fmt.Errorf("--users must be greater than or equal to 0")
			}
			if f.sas < 0 {
				return fmt.Errorf("--sas must be greater than or equal to 0")
			}
			if f.tenantPublicID == "" {
				return fmt.Errorf("--tenant-id is required")
			}
			if _, err := ids.MustParse(ids.PrefixTenant, f.tenantPublicID); err != nil {
				return fmt.Errorf("invalid --tenant-id: %w", err)
			}

			ctx := cmd.Context()

			store, err := storage.Open(cfg.Database)
			if err != nil {
				return fmt.Errorf("open storage: %w", err)
			}
			defer func() { _ = store.Close() }()

			tx, commit, err := store.Session(storage.WithSuperuser(ctx))
			if err != nil {
				return fmt.Errorf("open session: %w", err)
			}
			defer func() { _ = commit() }()

			if err := tx.Transaction(func(t *gorm.DB) error {
				gofakeit.Seed(42)

				if f.reset {
					if err := resetTenantData(t, f.tenantPublicID); err != nil {
						return err
					}
				}

				tenant, err := seedTenant(t, f)
				if err != nil {
					return err
				}

				if err := seedTenantSettings(t, tenant); err != nil {
					return err
				}

				if err := seedRedirectURIAllowlist(t, tenant); err != nil {
					return err
				}

				users, err := seedUsers(t, tenant, f.users)
				if err != nil {
					return err
				}

				serviceAccounts, err := seedServiceAccounts(t, tenant, users, f.sas)
				if err != nil {
					return err
				}

				if err := seedUpstreams(t, tenant); err != nil {
					return err
				}

				if err := seedBillingMetrics(t, tenant, users, serviceAccounts, f.days); err != nil {
					return err
				}

				logger.Info("seed complete",
					zap.String("tenant", tenant.PublicID),
					zap.Int("users", len(users)),
					zap.Int("service_accounts", len(serviceAccounts)),
					zap.Int("days", f.days))
				return nil
			}); err != nil {
				return err
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&f.tenantPublicID, "tenant-id", "tnt_01HGPX4D1Q6G9M0C6G58V206W0", "tenant public id (tnt_<ULID>)")
	cmd.Flags().StringVar(&f.tenantName, "tenant-name", "Acme Corporation", "human-readable tenant name")
	cmd.Flags().IntVar(&f.days, "days", 30, "number of days of billing history to generate")
	cmd.Flags().IntVar(&f.users, "users", 3, "number of users to create")
	cmd.Flags().IntVar(&f.sas, "sas", 2, "number of service accounts to create")
	cmd.Flags().BoolVar(&f.reset, "reset", false, "reset existing tenant data before seeding")

	bindFlag(v, "tenant_id")
	bindFlag(v, "tenant_name")
	bindFlag(v, "days")
	bindFlag(v, "users")
	bindFlag(v, "sas")
	bindFlag(v, "reset")

	return cmd
}

// resetTenantData looks up the tenant by public id and, if found, hard-deletes
// every dependent row in strict LIFO order to avoid foreign-key violations.
func resetTenantData(t *gorm.DB, tenantPublicID string) error {
	var tenant storage.Tenant
	if err := t.Where("public_id = ?", tenantPublicID).First(&tenant).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("lookup tenant for reset: %w", err)
	}

	tid := tenant.ID

	cascade := []struct {
		model any
		cond  string
	}{
		{&storage.SAConnectionSnapshot{}, "tenant_id = ?"},
		{&storage.ActiveUserMonth{}, "tenant_id = ?"},
		{&storage.UpstreamLink{}, "tenant_id = ?"},
		{&storage.UpstreamTenantLink{}, "tenant_id = ?"},
		{&storage.UpstreamTool{}, "tenant_id = ?"},
		{&storage.UpstreamStrategyConfig{}, "tenant_id = ?"},
		{&storage.UpstreamRegistration{}, "tenant_id = ?"},
		{&storage.Upstream{}, "tenant_id = ?"},
		{&storage.ServiceAccount{}, "tenant_id = ?"},
		{&storage.User{}, "tenant_id = ?"},
		{&storage.TenantRedirectURIAllowlist{}, "tenant_id = ?"},
		{&storage.TenantSettings{}, "tenant_id = ?"},
		{&storage.ZitadelApp{}, "tenant_id = ?"},
	}

	for _, item := range cascade {
		if err := t.Unscoped().Where(item.cond, tid).Delete(item.model).Error; err != nil {
			return fmt.Errorf("reset delete %T: %w", item.model, err)
		}
	}

	if err := t.Unscoped().Delete(&storage.Tenant{}, tid).Error; err != nil {
		return fmt.Errorf("reset delete tenant: %w", err)
	}

	return nil
}

// seedTenant finds an existing tenant by public id or creates a fresh one.
func seedTenant(t *gorm.DB, f *seedFlags) (*storage.Tenant, error) {
	var tenant storage.Tenant
	err := t.Where("public_id = ?", f.tenantPublicID).First(&tenant).Error
	if err == nil {
		return &tenant, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("lookup tenant: %w", err)
	}

	tenant = storage.Tenant{
		Base:         storage.Base{PublicID: f.tenantPublicID},
		Name:         f.tenantName,
		ZitadelOrgID: "zorg-" + gofakeit.UUID(),
	}
	if err := t.Create(&tenant).Error; err != nil {
		return nil, fmt.Errorf("create tenant: %w", err)
	}
	return &tenant, nil
}

// seedTenantSettings creates a TenantSettings row if one does not already exist.
func seedTenantSettings(t *gorm.DB, tenant *storage.Tenant) error {
	var settings storage.TenantSettings
	err := t.Where("tenant_id = ?", tenant.ID).First(&settings).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("lookup tenant settings: %w", err)
	}

	dayAgo := time.Now().Add(-24 * time.Hour)
	settings = storage.TenantSettings{
		TenantID:      tenant.ID,
		InvitedTeamAt: &dayAgo,
		ConfiguredAt:  &dayAgo,
		ChoseIDEAt:    &dayAgo,
	}
	if err := t.Create(&settings).Error; err != nil {
		return fmt.Errorf("create tenant settings: %w", err)
	}
	return nil
}

// seedRedirectURIAllowlist inserts the two standard local-client patterns.
func seedRedirectURIAllowlist(t *gorm.DB, tenant *storage.Tenant) error {
	patterns := []struct {
		label   string
		pattern string
	}{
		{"Local Client", "http://localhost:3000/*"},
		{"Local Client", "http://127.0.0.1:*/*"},
	}

	for _, p := range patterns {
		var existing storage.TenantRedirectURIAllowlist
		err := t.Where("tenant_id = ? AND pattern = ?", tenant.ID, p.pattern).First(&existing).Error
		if err == nil {
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("lookup redirect uri allowlist: %w", err)
		}

		entry := storage.TenantRedirectURIAllowlist{
			TenantID: tenant.ID,
			Label:    p.label,
			Pattern:  p.pattern,
		}
		if err := t.Create(&entry).Error; err != nil {
			return fmt.Errorf("create redirect uri allowlist: %w", err)
		}
	}
	return nil
}

// seedUsers creates or upserts the requested number of users. User 0 is always
// the admin.
func seedUsers(t *gorm.DB, tenant *storage.Tenant, count int) ([]*storage.User, error) {
	users := make([]*storage.User, 0, count)

	for i := range count {
		var name, email, zsub string
		if i == 0 {
			name = "Admin User"
			email = "admin@example.com"
			zsub = "usr_admin"
		} else {
			name = gofakeit.Name()
			email = gofakeit.Email()
			zsub = "zsub_" + gofakeit.UUID()
		}

		var user storage.User
		err := t.Where("tenant_id = ? AND (email = ? OR zitadel_subject = ?)", tenant.ID, email, zsub).First(&user).Error
		if err == nil {
			users = append(users, &user)
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("lookup user %d: %w", i, err)
		}

		user = storage.User{
			TenantID:       tenant.ID,
			Name:           name,
			Email:          email,
			ZitadelSubject: zsub,
		}
		if err := t.Create(&user).Error; err != nil {
			return nil, fmt.Errorf("create user %d: %w", i, err)
		}
		users = append(users, &user)
	}

	return users, nil
}

// seedServiceAccounts creates or upserts the requested number of service
// accounts, all created by the first user.
func seedServiceAccounts(t *gorm.DB, tenant *storage.Tenant, users []*storage.User, count int) ([]*storage.ServiceAccount, error) {
	if len(users) == 0 {
		return nil, nil
	}

	admin := users[0]
	now := time.Now()
	twoDaysAgo := now.Add(-48 * time.Hour)
	twoHoursAgo := now.Add(-2 * time.Hour)

	sas := make([]*storage.ServiceAccount, 0, count)

	for i := range count {
		zuid := "zusr_" + gofakeit.UUID()

		var sa storage.ServiceAccount
		err := t.Where("tenant_id = ? AND zitadel_user_id = ?", tenant.ID, zuid).First(&sa).Error
		if err == nil {
			sas = append(sas, &sa)
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("lookup service account %d: %w", i, err)
		}

		sa = storage.ServiceAccount{
			TenantID:         tenant.ID,
			Name:             gofakeit.AppName() + " Bot",
			Description:      gofakeit.Sentence(5),
			ZitadelUserID:    zuid,
			CreatedByID:      admin.ID,
			Role:             "developer",
			TokenGeneratedAt: &twoDaysAgo,
			LastUsedAt:       &twoHoursAgo,
		}
		if err := t.Create(&sa).Error; err != nil {
			return nil, fmt.Errorf("create service account %d: %w", i, err)
		}
		sas = append(sas, &sa)
	}

	return sas, nil
}

// seedUpstreams creates two demo upstreams with their strategy configs and tool
// catalogs, upserting safely when re-run.
func seedUpstreams(t *gorm.DB, tenant *storage.Tenant) error {
	type upstreamSeed struct {
		up     storage.Upstream
		config *storage.UpstreamStrategyConfig
		tools  []storage.UpstreamTool
	}

	seeds := []upstreamSeed{
		{
			up: storage.Upstream{
				TenantID:     tenant.ID,
				Identifier:   "github",
				DisplayName:  "GitHub MCP Server",
				StrategyType: "mcp_spec",
				McpServerURL: "https://mcp.github.com",
			},
			config: &storage.UpstreamStrategyConfig{
				TenantID:   tenant.ID,
				Type:       "mcp_spec",
				ConfigJSON: crypto.SecretField{},
			},
			tools: []storage.UpstreamTool{
				{
					TenantID:        tenant.ID,
					Name:            "search_repositories",
					Description:     "Search for repositories on GitHub",
					InputSchemaJSON: []byte(`{"type":"object","properties":{"query":{"type":"string"}}}`),
				},
				{
					TenantID:        tenant.ID,
					Name:            "get_issue",
					Description:     "Get details of a GitHub issue",
					InputSchemaJSON: []byte(`{"type":"object","properties":{"owner":{"type":"string"},"repo":{"type":"string"},"issue_number":{"type":"number"}}}`),
				},
				{
					TenantID:        tenant.ID,
					Name:            "create_issue",
					Description:     "Create a new issue on GitHub",
					InputSchemaJSON: []byte(`{"type":"object","properties":{"owner":{"type":"string"},"repo":{"type":"string"},"title":{"type":"string"},"body":{"type":"string"}}}`),
				},
			},
		},
		{
			up: storage.Upstream{
				TenantID:     tenant.ID,
				Identifier:   "postgres",
				DisplayName:  "Postgres Helper",
				StrategyType: "none",
				McpServerURL: "http://localhost:8081/mcp",
			},
			tools: []storage.UpstreamTool{
				{
					TenantID:        tenant.ID,
					Name:            "query_db",
					Description:     "Execute a SQL query against the database",
					InputSchemaJSON: []byte(`{"type":"object","properties":{"query":{"type":"string"}}}`),
				},
				{
					TenantID:        tenant.ID,
					Name:            "list_tables",
					Description:     "List all tables in the database",
					InputSchemaJSON: []byte(`{"type":"object","properties":{"schema":{"type":"string"}}}`),
				},
			},
		},
	}

	for _, s := range seeds {
		var up storage.Upstream
		err := t.Where("tenant_id = ? AND identifier = ?", tenant.ID, s.up.Identifier).First(&up).Error
		if err == nil {
			// Upstream exists — ensure its tools exist.
			for _, tool := range s.tools {
				var existing storage.UpstreamTool
				err := t.Where("tenant_id = ? AND upstream_id = ? AND name = ?", tenant.ID, up.ID, tool.Name).First(&existing).Error
				if err == nil {
					continue
				}
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return fmt.Errorf("lookup upstream tool %s: %w", tool.Name, err)
				}
				tool.UpstreamID = up.ID
				if err := t.Create(&tool).Error; err != nil {
					return fmt.Errorf("create upstream tool %s: %w", tool.Name, err)
				}
			}
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("lookup upstream %s: %w", s.up.Identifier, err)
		}

		if err := t.Create(&s.up).Error; err != nil {
			return fmt.Errorf("create upstream %s: %w", s.up.Identifier, err)
		}

		if s.config != nil {
			s.config.UpstreamID = s.up.ID
			if err := t.Create(s.config).Error; err != nil {
				return fmt.Errorf("create upstream strategy config for %s: %w", s.up.Identifier, err)
			}
		}

		for _, tool := range s.tools {
			tool.UpstreamID = s.up.ID
			if err := t.Create(&tool).Error; err != nil {
				return fmt.Errorf("create upstream tool %s: %w", tool.Name, err)
			}
		}
	}

	return nil
}

// seedBillingMetrics generates deterministic daily activity for users and
// service accounts across the requested number of days.
func seedBillingMetrics(t *gorm.DB, tenant *storage.Tenant, users []*storage.User, sas []*storage.ServiceAccount, days int) error {
	now := time.Now()

	for day := days - 1; day >= 0; day-- {
		targetDate := now.AddDate(0, 0, -day)
		dateStr := targetDate.Format("2006-01") + "-01"

		for _, user := range users {
			firstSeen := targetDate.Add(-time.Duration(gofakeit.Number(0, 3600)) * time.Second)
			lastSeen := targetDate.Add(-time.Duration(gofakeit.Number(0, 1800)) * time.Second)
			if lastSeen.Before(firstSeen) {
				lastSeen = firstSeen
			}

			var existing storage.ActiveUserMonth
			err := t.Where("tenant_id = ? AND month_start = ? AND user_id = ?", tenant.ID, dateStr, user.ID).First(&existing).Error
			if err == nil {
				continue
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("lookup active user month: %w", err)
			}

			aum := storage.ActiveUserMonth{
				TenantID:    tenant.ID,
				MonthStart:  dateStr,
				UserID:      &user.ID,
				FirstSeenAt: firstSeen,
				LastSeenAt:  lastSeen,
				CallCount:   int32(gofakeit.Number(5, 100)),
			}
			if err := t.Create(&aum).Error; err != nil {
				return fmt.Errorf("create active user month: %w", err)
			}
		}

		for _, sa := range sas {
			firstSeen := targetDate.Add(-time.Duration(gofakeit.Number(0, 3600)) * time.Second)
			lastSeen := targetDate.Add(-time.Duration(gofakeit.Number(0, 1800)) * time.Second)
			if lastSeen.Before(firstSeen) {
				lastSeen = firstSeen
			}

			var existing storage.ActiveUserMonth
			err := t.Where("tenant_id = ? AND month_start = ? AND service_account_id = ?", tenant.ID, dateStr, sa.ID).First(&existing).Error
			if err == nil {
				continue
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("lookup active user month for sa: %w", err)
			}

			aum := storage.ActiveUserMonth{
				TenantID:         tenant.ID,
				MonthStart:       dateStr,
				ServiceAccountID: &sa.ID,
				FirstSeenAt:      firstSeen,
				LastSeenAt:       lastSeen,
				CallCount:        int32(gofakeit.Number(5, 100)),
			}
			if err := t.Create(&aum).Error; err != nil {
				return fmt.Errorf("create active user month for sa: %w", err)
			}
		}

		for _, sa := range sas {
			snapshotCount := gofakeit.Number(1, 3)
			for range snapshotCount {
				connectedAt := targetDate.Add(-time.Duration(gofakeit.Number(0, 7200)) * time.Second)
				duration := time.Duration(gofakeit.Number(15, 120)) * time.Minute
				disconnectedAt := connectedAt.Add(duration)

				snapshot := storage.SAConnectionSnapshot{
					TenantID:         tenant.ID,
					ServiceAccountID: sa.ID,
					ConnectedAt:      connectedAt,
					DisconnectedAt:   &disconnectedAt,
					ConcurrentCount:  int32(gofakeit.Number(1, 5)),
				}
				if err := t.Create(&snapshot).Error; err != nil {
					return fmt.Errorf("create sa connection snapshot: %w", err)
				}
			}
		}
	}

	return nil
}
