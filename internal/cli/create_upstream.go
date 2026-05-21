package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/upstream"
	"github.com/belphemur/limen/internal/upstream/mcpspec"
	"github.com/belphemur/limen/internal/upstream/none"
)

type createUpstreamFlags struct {
	tenantPublicID string
	name           string
	strategy       string
	mcpURL         string

	// Optional static OAuth client (mcp_spec upstreams whose AS does not
	// support RFC 7591 Dynamic Client Registration — e.g. GitHub).
	clientID      string
	clientSecret  string
	issuer        string
	authEndpoint  string
	tokenEndpoint string
	scopes        []string
}

func newCreateUpstreamCommand(rflags *rootFlags, v *viper.Viper) *cobra.Command {
	f := &createUpstreamFlags{}

	cmd := &cobra.Command{
		Use:   "create-upstream",
		Short: "Register an MCP upstream for a tenant (PoC admin tool)",
		Long: `Insert an Upstream row for a tenant so the portal PoC can drive
the Phase 7 connect flow against it.

v1 supports the mcp_spec strategy (OAuth via the MCP-spec PRM
discovery flow) and the none strategy (no auth, public upstream).
The static_header strategy requires per-tenant secret material and is
plumbed through the admin SPA (Phase 9b), not this CLI.

The command rejects re-creates: pick a fresh --name or delete the
existing upstream first. For the 'none' strategy the tool catalog
is indexed synchronously after the row is written, so the upstream
is usable end-to-end as soon as the command exits.`,
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
			switch f.strategy {
			case string(upstream.StrategyMCPSpec), string(upstream.StrategyNone):
			case string(upstream.StrategyStaticHeader):
				return fmt.Errorf("--strategy %q is not plumbed through this CLI; use the admin SPA (Phase 9b) which knows how to capture the tenant header secret", f.strategy)
			default:
				return fmt.Errorf("--strategy %q is unknown (supported: %q, %q)", f.strategy, upstream.StrategyMCPSpec, upstream.StrategyNone)
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

			up, err := createUpstreamViaService(ctx, store, tenant, f)
			if err != nil {
				return err
			}

			staticCfg := mcpspec.Config{
				Issuer:                strings.TrimSpace(f.issuer),
				ClientID:              strings.TrimSpace(f.clientID),
				ClientSecret:          f.clientSecret,
				AuthorizationEndpoint: strings.TrimSpace(f.authEndpoint),
				TokenEndpoint:         strings.TrimSpace(f.tokenEndpoint),
				Scopes:                f.scopes,
			}

			if f.strategy == string(upstream.StrategyNone) {
				registry := upstream.NewRegistry()
				registry.Register(none.New(nil))
				svc := upstream.NewService(store, registry)
				if provErr := svc.ProvisionTenantMode(ctx, tenant, up); provErr != nil {
					logger.Warn("sync provision/index failed; refresher will retry",
						zap.String("upstream", up.Name),
						zap.Error(provErr))
				}
			}

			logger.Info("upstream registered",
				zap.String("public_id", up.PublicID),
				zap.String("tenant", tenant.PublicID),
				zap.String("name", up.Name),
				zap.String("strategy", up.StrategyType),
				zap.String("mcp_server_url", up.McpServerURL),
				zap.Bool("static_client", staticCfg.HasStaticClient()))

			fmt.Printf("Upstream %s ready.\n", up.PublicID)
			fmt.Printf("  Tenant     : %s (%d)\n", tenant.PublicID, tenant.ID)
			fmt.Printf("  Name       : %s\n", up.Name)
			fmt.Printf("  Strategy   : %s\n", up.StrategyType)
			fmt.Printf("  MCP URL    : %s\n", up.McpServerURL)
			if staticCfg.HasStaticClient() {
				fmt.Printf("  Static OAuth client: %s\n", staticCfg.ClientID)
			}
			connectURL := fmt.Sprintf("%s/t/%s/portal/", strings.TrimRight(cfg.Server.BaseURL, "/"), tenant.PublicID)
			printNextSteps(f.strategy, connectURL)
			return nil
		},
	}

	cmd.Flags().StringVar(&f.tenantPublicID, "tenant", "", "tenant public id (tnt_<ULID>)")
	cmd.Flags().StringVar(&f.name, "name", "", "upstream name (per-tenant unique; appears in URLs)")
	cmd.Flags().StringVar(&f.strategy, "strategy", string(upstream.StrategyMCPSpec), "linking strategy (only mcp_spec supported in v1)")
	cmd.Flags().StringVar(&f.mcpURL, "url", "", "MCP server URL (the resource the OAuth flow will discover)")
	cmd.Flags().StringVar(&f.clientID, "client-id", "", "pre-provisioned OAuth client_id (use when the AS doesn't support DCR, e.g. GitHub)")
	cmd.Flags().StringVar(&f.clientSecret, "client-secret", "", "pre-provisioned OAuth client_secret (paired with --client-id)")
	cmd.Flags().StringVar(&f.issuer, "issuer", "", "override OAuth issuer URL (used when PRM discovery can't reach it)")
	cmd.Flags().StringVar(&f.authEndpoint, "authorization-endpoint", "", "override authorization_endpoint when AS metadata is missing")
	cmd.Flags().StringVar(&f.tokenEndpoint, "token-endpoint", "", "override token_endpoint when AS metadata is missing")
	cmd.Flags().StringSliceVar(&f.scopes, "scope", nil, "OAuth scope to request (repeatable)")

	_ = cmd.MarkFlagRequired("tenant")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("url")

	bindFlag(v, "tenant")
	bindFlag(v, "name")
	bindFlag(v, "strategy")
	bindFlag(v, "url")
	bindFlag(v, "client-id")
	bindFlag(v, "client-secret")
	bindFlag(v, "issuer")
	bindFlag(v, "authorization-endpoint")
	bindFlag(v, "token-endpoint")
	bindFlag(v, "scope")

	return cmd
}

// createUpstreamViaService delegates to the canonical
// upstream.Service.CreateUpstream so CLI and admin RPC share the
// same validation, encoding, and persistence path.
func createUpstreamViaService(ctx context.Context, store *storage.Store, tenant *storage.Tenant, f *createUpstreamFlags) (*storage.Upstream, error) {
	staticCfg := mcpspec.Config{
		Issuer:                strings.TrimSpace(f.issuer),
		ClientID:              strings.TrimSpace(f.clientID),
		ClientSecret:          f.clientSecret,
		AuthorizationEndpoint: strings.TrimSpace(f.authEndpoint),
		TokenEndpoint:         strings.TrimSpace(f.tokenEndpoint),
		Scopes:                f.scopes,
	}
	in := upstream.CreateUpstreamInput{
		Name:         f.name,
		MCPServerURL: f.mcpURL,
		StrategyType: upstream.StrategyType(f.strategy),
	}
	if !staticCfg.IsZero() {
		if f.strategy != string(upstream.StrategyMCPSpec) {
			return nil, fmt.Errorf("static OAuth client flags (--client-id / --issuer / ...) only apply to --strategy %q", upstream.StrategyMCPSpec)
		}
		sf, err := mcpspec.EncodeConfig(tenant.ID, staticCfg)
		if err != nil {
			return nil, fmt.Errorf("encode mcpspec config: %w", err)
		}
		in.EncodedStrategyConfig = sf
	}
	registry := upstream.NewRegistry()
	registry.Register(none.New(nil))
	svc := upstream.NewService(store, registry)
	up, err := svc.CreateUpstream(ctx, tenant, in)
	if err != nil {
		return nil, fmt.Errorf("create upstream: %w", err)
	}
	return up, nil
}

// printNextSteps writes the strategy-specific bootstrap instructions the
// operator must follow before the upstream is usable. v1 only registers the
// upstream row; Phase 8's catalog indexer is what actually makes tools
// visible to end users, and for per-user strategies (mcp_spec,
// static_header user-mode) the indexer can only run after a tenant
// owner/admin has completed the portal connect flow with their own
// credentials. We surface that hard requirement here rather than letting
// dev operators discover it by seeing an empty tool list.
func printNextSteps(strategy, connectURL string) {
	fmt.Println()
	fmt.Println("Next steps")
	switch strategy {
	case string(upstream.StrategyMCPSpec):
		fmt.Println("  This upstream uses OAuth (mcp_spec). The tool catalog is empty")
		fmt.Println("  until an owner or admin completes the connect flow under their")
		fmt.Println("  own account — Limen indexes tools using the linking user's")
		fmt.Println("  credentials and the resulting catalog is shared with the rest")
		fmt.Println("  of the tenant.")
		fmt.Println()
		fmt.Printf("  1. Open the portal as an owner/admin: %s\n", connectURL)
		fmt.Println("  2. Click 'Connect' on the new upstream and finish the OAuth")
		fmt.Println("     flow with the upstream provider.")
		fmt.Println("  3. Verify the catalog populated: tool_count should flip from 0")
		fmt.Println("     to N on the upstream row.")
	default:
		// none / static_header tenant-mode index synchronously at Provision
		// time — nothing for the operator to do beyond pointing users at
		// the portal. Other strategies are not currently reachable from this
		// CLI; if one is added without updating this switch the operator
		// still gets the generic pointer.
		fmt.Println("  This strategy does not need a per-user link to bootstrap its")
		fmt.Println("  tool catalog — Limen indexes the upstream directly using the")
		fmt.Println("  configured credentials.")
		fmt.Println()
		fmt.Printf("  Users can call the upstream's tools immediately via the portal:\n  %s\n", connectURL)
	}
}
