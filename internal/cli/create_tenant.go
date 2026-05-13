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
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/zitadel"
)

type createTenantFlags struct {
	slug       string
	name       string
	ownerEmail string
	givenName  string
	familyName string
	// existingUserID, when set, skips human-user creation and grants the
	// `owner` role to that Zitadel user id. Useful for re-running the
	// command after a partial failure.
	existingUserID string
}

func newCreateTenantCommand(rflags *rootFlags, v *viper.Viper) *cobra.Command {
	f := &createTenantFlags{}

	cmd := &cobra.Command{
		Use:   "create-tenant",
		Short: "Provision a new tenant (Zitadel org + Limen row + seed owner)",
		Long: `Create a brand-new tenant. The command performs three coupled
operations in order:

  1. Validates the slug against the customer regex and reserved list.
  2. Creates a Zitadel organization named after the tenant, with a seed
     human user as ORG_OWNER. Zitadel emails an initialization link
     (MailHog in dev — see Phase 0).
  3. Grants the seed user the "owner" project role on the Limen project.
  4. Persists the Tenant + User rows in Limen, keyed by zitadel_org_id
     and zitadel_subject respectively.

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

			if err := tenancy.ValidateSlug(f.slug); err != nil {
				return fmt.Errorf("invalid slug: %w", err)
			}
			if strings.TrimSpace(f.name) == "" {
				return errors.New("--name is required")
			}
			if f.existingUserID == "" && strings.TrimSpace(f.ownerEmail) == "" {
				return errors.New("--owner-email is required (or pass --owner-user-id to reuse an existing Zitadel user)")
			}

			ctx := cmd.Context()

			store, err := storage.Open(cfg.Database)
			if err != nil {
				return fmt.Errorf("open storage: %w", err)
			}
			defer func() { _ = store.Close() }()

			// Pre-flight: a row with this slug must not already exist.
			if existing, err := tenancy.Resolve(ctx, store, f.slug); err == nil && existing != nil {
				return fmt.Errorf("tenant %q already exists (id=%d, zitadel_org=%s)", f.slug, existing.ID, existing.ZitadelOrgID)
			} else if err != nil && !errors.Is(err, tenancy.ErrNotFound) {
				return fmt.Errorf("pre-flight slug check: %w", err)
			}

			zclient, err := zitadel.NewClient(ctx, zitadel.Config{
				Domain:      cfg.Zitadel.Domain,
				AuthMode:    zitadel.AuthMode(cfg.Zitadel.AuthMode),
				PAT:         cfg.Zitadel.PAT,
				JWTKeyPath:  cfg.Zitadel.JWTKeyPath,
				ProjectID:   cfg.Zitadel.ProjectID,
				HTTPTimeout: cfg.Zitadel.HTTPTimeout,
			})
			if err != nil {
				return fmt.Errorf("zitadel client: %w", err)
			}

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

			tenant := &storage.Tenant{
				Slug:         f.slug,
				Name:         f.name,
				ZitadelOrgID: org.ID,
			}
			user := &storage.User{
				Email:          f.ownerEmail,
				Name:           strings.TrimSpace(f.givenName + " " + f.familyName),
				ZitadelSubject: org.AdminUserID,
			}

			if err := persistTenantAndOwner(ctx, store, tenant, user); err != nil {
				return fmt.Errorf("persist tenant: %w", err)
			}

			consoleURL := strings.TrimRight(cfg.Zitadel.Domain, "/") + "/ui/console?org=" + org.ID
			fmt.Printf("Tenant %q created.\n", f.slug)
			fmt.Printf("  Limen tenant id : %d (public %s)\n", tenant.ID, tenant.PublicID)
			fmt.Printf("  Zitadel org id  : %s\n", org.ID)
			fmt.Printf("  Owner user id   : %s\n", org.AdminUserID)
			fmt.Printf("  Console (hand to owner):\n    %s\n", consoleURL)
			fmt.Println("\nInvites, role changes, password reset, MFA enrollment, and IdP")
			fmt.Println("federation are self-serve from the Zitadel Console above.")
			return nil
		},
	}

	cmd.Flags().StringVar(&f.slug, "slug", "", "tenant URL slug (lowercase, 1–32 chars, no leading/trailing hyphen)")
	cmd.Flags().StringVar(&f.name, "name", "", "human-readable tenant name (also used as the Zitadel org name)")
	cmd.Flags().StringVar(&f.ownerEmail, "owner-email", "", "email address of the seed owner (receives the Zitadel invite)")
	cmd.Flags().StringVar(&f.givenName, "owner-given-name", "", "seed owner given name (optional)")
	cmd.Flags().StringVar(&f.familyName, "owner-family-name", "", "seed owner family name (optional)")
	cmd.Flags().StringVar(&f.existingUserID, "owner-user-id", "", "reuse an existing Zitadel user id as the seed owner instead of creating a new human")

	_ = cmd.MarkFlagRequired("slug")
	_ = cmd.MarkFlagRequired("name")

	bindFlag(v, "slug")
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
