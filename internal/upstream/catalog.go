// Package upstream — IndexUpstream is Phase 8's tool-catalog indexer.
//
// The indexer connects to an upstream MCP server using whatever
// credentials the strategy makes available (tenant-wide for non-link
// strategies, the supplied link's tokens for per-user strategies),
// calls tools/list, and reconciles the result into the upstream_tools
// table. Per-upstream catalog: every authorized user of the upstream
// sees the same tool surface, so we do not key on user.
//
// Callers:
//
//   - Service.FinishCallback / SubmitUserAPIKey, after a tenant
//     owner/admin completes a link (per-user strategies).
//   - CLI create-upstream and the admin SPA CreateUpstream RPC,
//     synchronously after Provision succeeds (tenant-mode strategies).
//   - Refresher.sweep, periodically.
//
// Failure mode contract: an indexer error never blocks the caller's
// primary action — the caller logs and moves on. The next sweep
// re-attempts.
package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/belphemur/limen/internal/storage"
)

// indexTimeout caps both the initialize and list-tools round-trips. The
// upstream may be slow or wedged; we never want the indexer to hold a DB
// transaction open longer than this.
const indexTimeout = 30 * time.Second

// IndexUpstream lists tools from up and reconciles upstream_tools rows
// for it. Per-strategy `Headers()` is the gate: strategies that need a
// link (mcp_spec; static_header user-mode) return ErrNeedsRelink /
// ErrLinkNotFound when one is missing, and IndexUpstream surfaces that
// error so the caller can decide whether it's fatal (CLI bootstrap) or
// expected (tenant-mode pre-link sweep — Service.ProvisionTenantMode
// swallows it as "wait for a user link").
//
// The ctx must already carry the tenant pin (request scope) or be
// WithSuperuser (CLI / refresher). Storage RLS still enforces tenant
// isolation; TenantID is set explicitly on every row so superuser writes
// remain attributable.
func IndexUpstream(
	ctx context.Context,
	store *storage.Store,
	registry *Registry,
	tenant *storage.Tenant,
	up *storage.Upstream,
	link *storage.UpstreamLink,
) error {
	if store == nil || registry == nil || tenant == nil || up == nil {
		return fmt.Errorf("index upstream: store/registry/tenant/upstream are required")
	}
	strat, err := registry.Resolve(StrategyType(up.StrategyType))
	if err != nil {
		return fmt.Errorf("index upstream %q: %w", up.Name, err)
	}

	tools, err := listUpstreamTools(ctx, strat, tenant, up, link)
	if err != nil {
		return fmt.Errorf("index upstream %q: %w", up.Name, err)
	}

	// reconcileCatalog opens a storage session; bind the tenant so RLS
	// applies on the app pool. The refresher invokes IndexUpstream with
	// a superuser ctx for the load phase, but reconcile must scope to
	// this upstream's tenant.
	reconcileCtx := storage.WithTenant(ctx, tenant.ID)
	if err := reconcileCatalog(reconcileCtx, store, tenant.ID, up.ID, tools); err != nil {
		return fmt.Errorf("index upstream %q: %w", up.Name, err)
	}
	return nil
}

// listUpstreamTools opens a one-shot mcp-go client against up.McpServerURL,
// initializes the session, calls tools/list, and tears the client down.
// The client is not cached — indexing is rare and reusing a long-lived
// client here would tangle ownership with the gateway's per-request
// transport.
func listUpstreamTools(
	ctx context.Context,
	strat Strategy,
	tenant *storage.Tenant,
	up *storage.Upstream,
	link *storage.UpstreamLink,
) ([]mcp.Tool, error) {
	lctx := LinkContext{Tenant: tenant, Upstream: up, Link: link}
	if link != nil {
		lctx.User = link.User
	}
	headers, err := strat.Headers(ctx, lctx)
	if err != nil {
		return nil, fmt.Errorf("strategy headers: %w", err)
	}

	dialCtx, cancelDial := context.WithTimeout(ctx, indexTimeout)
	defer cancelDial()
	c, err := DialAndInitialize(dialCtx, up.McpServerURL, headers, nil, indexTimeout, "limen-indexer", "0.1.0")
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()

	listCtx, cancelList := context.WithTimeout(ctx, indexTimeout)
	defer cancelList()
	resp, err := c.ListTools(listCtx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	return resp.Tools, nil
}

// reconcileCatalog upserts rows for every tool in tools and hard-deletes
// rows whose names no longer appear. Wrapped in a single Session() so
// either the whole reconciliation lands or none of it does.
//
// We hard-delete (Unscoped().Delete) for removed tools because
// UpstreamTool is a cache: keeping soft-deleted rows around would only
// re-collide with the partial-unique index next time the tool comes back.
// Audit of "an upstream lost a tool" lives in audit_events (Phase 12),
// not in this table.
func reconcileCatalog(
	ctx context.Context,
	store *storage.Store,
	tenantID, upstreamID int64,
	tools []mcp.Tool,
) error {
	tx, commit, err := store.Session(ctx)
	if err != nil {
		return fmt.Errorf("open session: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = commit()
		}
	}()

	var existing []storage.UpstreamTool
	if err := tx.Where("upstream_id = ?", upstreamID).Find(&existing).Error; err != nil {
		return fmt.Errorf("load existing catalog: %w", err)
	}
	byName := make(map[string]*storage.UpstreamTool, len(existing))
	for i := range existing {
		byName[existing[i].Name] = &existing[i]
	}

	now := time.Now().UTC()
	keep := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		keep[t.Name] = struct{}{}

		schema, err := marshalInputSchema(t.InputSchema)
		if err != nil {
			return fmt.Errorf("marshal input schema for %q: %w", t.Name, err)
		}

		if row, ok := byName[t.Name]; ok {
			row.Description = t.Description
			row.InputSchemaJSON = schema
			row.LastIndexedAt = now
			if err := tx.Save(row).Error; err != nil {
				return fmt.Errorf("update tool %q: %w", t.Name, err)
			}
			continue
		}

		row := &storage.UpstreamTool{
			TenantID:        tenantID,
			UpstreamID:      upstreamID,
			Name:            t.Name,
			Description:     t.Description,
			InputSchemaJSON: schema,
			LastIndexedAt:   now,
		}
		if err := tx.Create(row).Error; err != nil {
			return fmt.Errorf("create tool %q: %w", t.Name, err)
		}
	}

	for name, row := range byName {
		if _, kept := keep[name]; kept {
			continue
		}
		if err := tx.Unscoped().Delete(row).Error; err != nil {
			return fmt.Errorf("delete stale tool %q: %w", name, err)
		}
	}

	// Phase 8c — recompute alias set from the just-reconciled tool list
	// and persist it on the upstream row. Tenant-wide collision pass
	// runs at read time (gateway.Manager), so per-upstream derivation
	// is sufficient here.
	toolNames := make([]string, 0, len(tools))
	for _, t := range tools {
		toolNames = append(toolNames, t.Name)
	}
	aliases := DeriveAliases(toolNames)
	aliasesJSON, err := json.Marshal(aliases)
	if err != nil {
		return fmt.Errorf("marshal aliases: %w", err)
	}
	if len(aliasesJSON) == 0 || string(aliasesJSON) == "null" {
		aliasesJSON = []byte("[]")
	}
	if err := tx.Model(&storage.Upstream{}).
		Where("id = ?", upstreamID).
		Update("aliases_json", aliasesJSON).Error; err != nil {
		return fmt.Errorf("update upstream aliases: %w", err)
	}

	if err := commit(); err != nil {
		return fmt.Errorf("commit catalog: %w", err)
	}
	committed = true
	return nil
}

// marshalInputSchema converts mcp-go's typed InputSchema into the raw
// jsonb bytes we persist. An empty schema becomes "{}" (the column is
// NOT NULL) rather than null.
func marshalInputSchema(schema any) ([]byte, error) {
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return []byte("{}"), nil
	}
	return raw, nil
}
