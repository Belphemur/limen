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
	"github.com/belphemur/limen/internal/upstream"
)

type createUpstreamFlags struct {
	tenantPublicID string
	name           string
	strategy       string
	mcpURL         string
}

func newCreateUpstreamCommand(rflags *rootFlags, v *viper.Viper) *cobra.Command {
	f := &createUpstreamFlags{}

	cmd := &cobra.Command{
		Use:   "create-upstream",
		Short: "Register an MCP upstream for a tenant (PoC admin tool)",
		Long: `Insert an Upstream row for a tenant so the portal PoC can drive
the Phase 7 connect flow against it.

v1 only supports the mcp_spec strategy (OAuth via the MCP-spec PRM
discovery flow). The "none" and "static_header" strategies will get
their own flags once Phase 9 ships a real admin UI.

The command is idempotent on the (tenant, name) tuple — re-running with
the same name updates the URL in place.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			logger := newCLILogger()
			defer func() { _ = logger.Sync() }()

			cfg, err := loadConfig(rflags)
			if err != nil {
				return err
			}
			if strings.TrimSpace(f.tenantPublicID) == "" {
				return errors.New("--tenant is required (a tnt_<ULID> public id)")
			}
			if strings.TrimSpace(f.name) == "" {
				return errors.New("--name is required")
			}
			if strings.TrimSpace(f.mcpURL) == "" {
				return errors.New("--url is required")
			}
			if f.strategy != string(upstream.StrategyMCPSpec) {
				return fmt.Errorf("--strategy %q not supported by this PoC command (only %q)", f.strategy, upstream.StrategyMCPSpec)
			}

			ctx := cmd.Context()

			store, err := storage.Open(cfg.Database)
			if err != nil {
				return fmt.Errorf("open storage: %w", err)
			}
			defer func() { _ = store.Close() }()

			tenant, err := tenancy.Resolve(ctx, store, f.tenantPublicID)
			if err != nil {
				return fmt.Errorf("resolve tenant: %w", err)
			}

			up, err := upsertUpstream(ctx, store, tenant.ID, f.name, f.strategy, f.mcpURL)
			if err != nil {
				return err
			}

			logger.Info("upstream registered",
				zap.String("public_id", up.PublicID),
				zap.String("tenant", tenant.PublicID),
				zap.String("name", up.Name),
				zap.String("strategy", up.StrategyType),
				zap.String("mcp_server_url", up.McpServerURL))

			fmt.Printf("Upstream %s ready.\n", up.PublicID)
			fmt.Printf("  Tenant     : %s (%d)\n", tenant.PublicID, tenant.ID)
			fmt.Printf("  Name       : %s\n", up.Name)
			fmt.Printf("  Strategy   : %s\n", up.StrategyType)
			fmt.Printf("  MCP URL    : %s\n", up.McpServerURL)
			fmt.Printf("  Connect at : %s/t/%s/portal/\n", strings.TrimRight(cfg.Server.BaseURL, "/"), tenant.PublicID)
			return nil
		},
	}

	cmd.Flags().StringVar(&f.tenantPublicID, "tenant", "", "tenant public id (tnt_<ULID>)")
	cmd.Flags().StringVar(&f.name, "name", "", "upstream name (per-tenant unique; appears in URLs)")
	cmd.Flags().StringVar(&f.strategy, "strategy", string(upstream.StrategyMCPSpec), "linking strategy (only mcp_spec supported in v1)")
	cmd.Flags().StringVar(&f.mcpURL, "url", "", "MCP server URL (the resource the OAuth flow will discover)")

	_ = cmd.MarkFlagRequired("tenant")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("url")

	bindFlag(v, "tenant")
	bindFlag(v, "name")
	bindFlag(v, "strategy")
	bindFlag(v, "url")

	return cmd
}

// upsertUpstream creates or updates the Upstream row for (tenant, name).
// Runs on the admin pool — this is an operator action, not a request.
func upsertUpstream(ctx context.Context, store *storage.Store, tenantID int64, name, strategy, mcpURL string) (*storage.Upstream, error) {
	tx, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		return nil, fmt.Errorf("open session: %w", err)
	}

	var existing storage.Upstream
	err = tx.Where("tenant_id = ? AND name = ?", tenantID, name).First(&existing).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		up := &storage.Upstream{
			TenantID:     tenantID,
			Name:         name,
			StrategyType: strategy,
			McpServerURL: mcpURL,
		}
		if err := tx.Create(up).Error; err != nil {
			_ = commit()
			return nil, fmt.Errorf("create upstream: %w", err)
		}
		if err := commit(); err != nil {
			return nil, err
		}
		return up, nil
	case err != nil:
		_ = commit()
		return nil, fmt.Errorf("load upstream: %w", err)
	default:
		existing.StrategyType = strategy
		existing.McpServerURL = mcpURL
		if err := tx.Save(&existing).Error; err != nil {
			_ = commit()
			return nil, fmt.Errorf("update upstream: %w", err)
		}
		if err := commit(); err != nil {
			return nil, err
		}
		return &existing, nil
	}
}
