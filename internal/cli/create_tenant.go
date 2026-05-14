package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/zitadel"
)

type createTenantFlags struct {
	name       string
	ownerEmail string
	givenName  string
	familyName string
	// existingUserID, when set, skips human-user creation and grants the
	// `owner` role to that Zitadel user id. Useful for re-running the
	// command after a partial failure.
	existingUserID string
	// existingOrgID, when set, skips Zitadel org creation and binds the
	// Limen tenant row to that pre-existing org. Owner provisioning is
	// skipped — manage users from the Zitadel Console.
	existingOrgID string
}

func newCreateTenantCommand(rflags *rootFlags, v *viper.Viper) *cobra.Command {
	f := &createTenantFlags{}

	cmd := &cobra.Command{
		Use:   "create-tenant",
		Short: "Provision a new tenant (Zitadel org + Limen row + seed owner)",
		Long: `Create a brand-new tenant. The command performs four coupled
operations in order:

  1. Creates a Zitadel organization named after the tenant, with a seed
     human user as ORG_OWNER. Zitadel emails an initialization link
     (MailHog in dev — see Phase 0).
  2. Grants the seed user the "owner" project role on the Limen project.
  3. Persists the Tenant + User rows in Limen, keyed by zitadel_org_id
     and zitadel_subject respectively. The Tenant's PublicID (a
     tnt_<ULID>) is the only externally visible identifier and is used
     as the URL segment everywhere (no slug).
  4. Mirrors the Limen tenant PublicID into the Zitadel org metadata
     under the key "limen_tenant_id" so the two systems can be
     cross-referenced from either side.

User invitations, role changes, MFA enrollment, and IdP federation are
delegated to the Zitadel Console — they are not subcommands of this CLI.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			logger := newCLILogger()
			defer func() { _ = logger.Sync() }()

			cfg, err := loadConfig(rflags)
			if err != nil {
				return err
			}

			if strings.TrimSpace(f.name) == "" {
				return errors.New("--name is required")
			}
			if f.existingOrgID == "" && f.existingUserID == "" && strings.TrimSpace(f.ownerEmail) == "" {
				return errors.New("--owner-email is required (or pass --owner-user-id to reuse an existing Zitadel user, or --zitadel-org-id to bind to an existing org)")
			}

			ctx := cmd.Context()

			store, err := storage.Open(cfg.Database)
			if err != nil {
				return fmt.Errorf("open storage: %w", err)
			}
			defer func() { _ = store.Close() }()

			zclient, err := zitadel.NewClient(ctx, zitadel.Config{
				Domain:      cfg.Zitadel.Domain,
				AuthMode:    zitadel.AuthMode(cfg.Zitadel.AuthMode),
				PAT:         cfg.Zitadel.PAT,
				JWTKeyPath:  cfg.Zitadel.JWTKeyPath,
				ProjectID:   cfg.Zitadel.ProjectID,
				HTTPTimeout: cfg.Zitadel.HTTPTimeout,
			})
			if err != nil && f.existingOrgID == "" {
				return fmt.Errorf("zitadel client: %w", err)
			}

			var (
				orgID       string
				ownerUserID string
			)

			if f.existingOrgID != "" {
				orgID = f.existingOrgID
				logger.Info("binding to existing Zitadel org", zap.String("org_id", orgID))
			} else {
				seed := &zitadel.SeedAdmin{
					ExistingUserID: f.existingUserID,
					Email:          f.ownerEmail,
					GivenName:      f.givenName,
					FamilyName:     f.familyName,
				}

				logger.Info("creating Zitadel organization", zap.String("name", f.name))
				org, err := zclient.CreateOrganization(ctx, f.name, seed)
				if err != nil {
					return fmt.Errorf("create org: %w", err)
				}
				logger.Info("Zitadel org created",
					zap.String("org_id", org.ID),
					zap.String("owner_user_id", org.AdminUserID))

				if org.AdminUserID == "" {
					return errors.New("Zitadel returned no admin user id; cannot grant owner role")
				}

				logger.Info("granting owner role",
					zap.String("user_id", org.AdminUserID),
					zap.String("org_id", org.ID))
				if _, err := zclient.AddUserGrant(ctx, org.ID, org.AdminUserID, []string{"owner"}); err != nil {
					// AddUserGrant is idempotent in Zitadel; treat ALREADY_EXISTS as
					// success but propagate every other failure.
					if !strings.Contains(err.Error(), "ALREADY_EXISTS") && !strings.Contains(err.Error(), "AlreadyExists") {
						return fmt.Errorf("add owner grant: %w", err)
					}
					logger.Info("owner grant already exists, continuing")
				}

				orgID = org.ID
				ownerUserID = org.AdminUserID
			}

			tenant := &storage.Tenant{
				Name:         f.name,
				ZitadelOrgID: orgID,
			}
			var user *storage.User
			if ownerUserID != "" {
				user = &storage.User{
					Email:          f.ownerEmail,
					Name:           strings.TrimSpace(f.givenName + " " + f.familyName),
					ZitadelSubject: ownerUserID,
				}
			}

			if err := persistTenantAndOwner(ctx, store, tenant, user); err != nil {
				return fmt.Errorf("persist tenant: %w", err)
			}

			// Mirror the Limen tenant PublicID into the Zitadel org metadata
			// so operators can cross-reference the two systems from either
			// side. A failure here does not invalidate the tenant row — the
			// org and DB rows are already persisted — so log and continue.
			if zclient != nil {
				if err := zclient.SetOrgMetadata(ctx, orgID, "limen_tenant_id", []byte(tenant.PublicID)); err != nil {
					logger.Warn("failed to mirror tenant PublicID to Zitadel org metadata",
						zap.String("org_id", orgID),
						zap.String("public_id", tenant.PublicID),
						zap.Error(err))
				} else {
					logger.Info("mirrored tenant PublicID into Zitadel org metadata",
						zap.String("org_id", orgID),
						zap.String("key", "limen_tenant_id"),
						zap.String("public_id", tenant.PublicID))
				}
			}

			consoleURL := strings.TrimRight(cfg.Zitadel.Domain, "/") + "/ui/console?org=" + orgID
			fmt.Printf("Tenant %s created.\n", tenant.PublicID)
			fmt.Printf("  Limen tenant id : %d (public %s)\n", tenant.ID, tenant.PublicID)
			fmt.Printf("  Zitadel org id  : %s\n", orgID)
			if ownerUserID != "" {
				fmt.Printf("  Owner user id   : %s\n", ownerUserID)
			}
			fmt.Printf("  Console (hand to owner):\n    %s\n", consoleURL)
			fmt.Println("\nInvites, role changes, password reset, MFA enrollment, and IdP")
			fmt.Println("federation are self-serve from the Zitadel Console above.")
			return nil
		},
	}

	cmd.Flags().StringVar(&f.name, "name", "", "human-readable tenant name (also used as the Zitadel org name)")
	cmd.Flags().StringVar(&f.ownerEmail, "owner-email", "", "email address of the seed owner (receives the Zitadel invite)")
	cmd.Flags().StringVar(&f.givenName, "owner-given-name", "", "seed owner given name (optional)")
	cmd.Flags().StringVar(&f.familyName, "owner-family-name", "", "seed owner family name (optional)")
	cmd.Flags().StringVar(&f.existingUserID, "owner-user-id", "", "reuse an existing Zitadel user id as the seed owner instead of creating a new human")
	cmd.Flags().StringVar(&f.existingOrgID, "zitadel-org-id", "", "bind this tenant to an existing Zitadel org (skips org + owner creation; manage users via Zitadel Console)")

	_ = cmd.MarkFlagRequired("name")

	bindFlag(v, "name")
	bindFlag(v, "owner_email")
	bindFlag(v, "owner_given_name")
	bindFlag(v, "owner_family_name")
	bindFlag(v, "owner_user_id")

	return cmd
}

// bindFlag wires a key to the LIMEN_<KEY> env var via Viper.
func bindFlag(v *viper.Viper, key string) {
	_ = v.BindEnv(key)
}

// persistTenantAndOwner writes the Tenant + User rows in a single
// transaction using the admin pool (the tenants table is not RLS-scoped).
func persistTenantAndOwner(ctx context.Context, store *storage.Store, tenant *storage.Tenant, user *storage.User) error {
	tx, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		return err
	}
	if err := tx.Transaction(func(t *gorm.DB) error {
		if err := t.Create(tenant).Error; err != nil {
			return fmt.Errorf("insert tenant: %w", err)
		}
		if user == nil {
			return nil
		}
		user.TenantID = tenant.ID
		if err := t.Create(user).Error; err != nil {
			return fmt.Errorf("insert owner user: %w", err)
		}
		return nil
	}); err != nil {
		_ = commit()
		return err
	}
	return commit()
}
