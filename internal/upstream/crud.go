package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/contextblob"
	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/storage"
)

// ErrUpstreamAlreadyExists is returned by CreateUpstream when an
// upstream with the same identifier already exists for the tenant.
var ErrUpstreamAlreadyExists = errors.New("upstream: identifier already exists")

// ErrCannotReindexWithoutLink is returned by ReindexCatalog when the
// upstream's strategy needs per-user credentials but the calling user
// has no enabled link.
var ErrCannotReindexWithoutLink = errors.New("upstream: cannot reindex without a calling user link")

// ErrUserNotFound is returned by PreviewContext when the supplied
// public_id does not resolve to a user under the caller's tenant.
var ErrUserNotFound = errors.New("upstream: user not found")

// CreateUpstreamInput is the canonical create payload consumed by both
// the admin Connect RPC and the limenctl create-upstream CLI.
//
// StrategyConfig is the generic key/value bag; the strategy itself
// validates the shape. For strategies whose config is opaque /
// encrypted (mcp_spec static OAuth client; static_header tenant
// secret) callers pre-encode the payload via the strategy's
// EncodeConfig helper and pass the resulting crypto.SecretField as
// EncodedStrategyConfig — keeps this package strategy-agnostic.
type CreateUpstreamInput struct {
	Identifier            string
	DisplayName           string
	MCPServerURL          string
	StrategyType          StrategyType
	StrategySubMode       string // "tenant"/"user" for static_header; empty otherwise
	StrategyConfig        map[string]string
	EncodedStrategyConfig crypto.SecretField // optional; zero-value = skip
	DefaultsJSON          []byte             // validated by contextblob.ValidateContextBlob
}

// UpdateUpstreamPatch carries the optional mutations CreateUpstream
// callers want applied.
//
// Use nil pointers / nil byte slices to mean "leave alone".
type UpdateUpstreamPatch struct {
	DisplayName  *string
	DefaultsJSON []byte // nil = leave alone; []byte("{}") = clear
}

// CreateUpstream is the canonical upstream-creation path used by both
// the limenctl CLI and the admin Connect RPC. It writes the Upstream
// row plus any pre-encoded strategy-specific config; callers are
// responsible for the follow-up ProvisionTenantMode call (so the CLI
// can skip it for strategies whose drivers it doesn't carry).
func (s *Service) CreateUpstream(ctx context.Context, tenant *storage.Tenant, in CreateUpstreamInput) (*storage.Upstream, error) {
	if tenant == nil {
		return nil, errors.New("upstream: tenant required")
	}
	identifier := strings.TrimSpace(in.Identifier)
	url := strings.TrimSpace(in.MCPServerURL)
	if identifier == "" {
		return nil, errors.New("upstream: identifier required")
	}
	if url == "" {
		return nil, errors.New("upstream: mcp_url required")
	}
	// Validate defaults_json upfront so we never persist a bad blob.
	defaults := in.DefaultsJSON
	if len(defaults) == 0 {
		defaults = []byte("{}")
	} else if _, vErr := contextblob.ValidateContextBlob(defaults); vErr != nil {
		return nil, vErr
	}

	up := &storage.Upstream{
		TenantID:     tenant.ID,
		Identifier:   identifier,
		DisplayName:  strings.TrimSpace(in.DisplayName),
		StrategyType: string(in.StrategyType),
		McpServerURL: url,
		DefaultsJSON: defaults,
	}

	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenant.ID))
	if err != nil {
		return nil, fmt.Errorf("upstream: open session: %w", err)
	}
	var existing storage.Upstream
	err = tx.Where("identifier = ?", identifier).First(&existing).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		// continue
	case err != nil:
		_ = commit()
		return nil, fmt.Errorf("upstream: lookup existing: %w", err)
	default:
		_ = commit()
		return nil, ErrUpstreamAlreadyExists
	}
	if err := tx.Create(up).Error; err != nil {
		_ = commit()
		return nil, fmt.Errorf("upstream: create row: %w", err)
	}
	if !in.EncodedStrategyConfig.IsZero() {
		row := &storage.UpstreamStrategyConfig{
			TenantID:   tenant.ID,
			UpstreamID: up.ID,
			Type:       string(in.StrategyType),
			ConfigJSON: in.EncodedStrategyConfig,
		}
		if err := tx.Create(row).Error; err != nil {
			_ = commit()
			return nil, fmt.Errorf("upstream: create strategy config: %w", err)
		}
	}
	if err := commit(); err != nil {
		return nil, err
	}
	return up, nil
}

// RequiresLink reports whether the named strategy needs per-user link
// rows. Returns false when the strategy isn't registered with this
// Service (the CLI's reality) so callers can degrade gracefully.
func (s *Service) RequiresLink(t StrategyType) bool {
	strat, err := s.registry.Resolve(t)
	if err != nil {
		return false
	}
	return strat.RequiresLink()
}

// UpdateUpstream mutates the editable fields on an existing upstream
// row. Patch fields that are nil/empty leave the column alone — the
// admin SPA sends only what changed.
func (s *Service) UpdateUpstream(ctx context.Context, tenant *storage.Tenant, publicID string, patch UpdateUpstreamPatch) (*storage.Upstream, error) {
	if tenant == nil {
		return nil, errors.New("upstream: tenant required")
	}
	up, err := s.loadUpstreamByPublicID(ctx, tenant.ID, publicID)
	if err != nil {
		return nil, err
	}
	if patch.DefaultsJSON != nil {
		if _, vErr := contextblob.ValidateContextBlob(patch.DefaultsJSON); vErr != nil {
			return nil, vErr
		}
	}
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenant.ID))
	if err != nil {
		return nil, err
	}
	updates := map[string]any{}
	if patch.DisplayName != nil {
		updates["display_name"] = strings.TrimSpace(*patch.DisplayName)
	}
	if patch.DefaultsJSON != nil {
		updates["defaults_json"] = patch.DefaultsJSON
	}
	if len(updates) > 0 {
		if err := tx.Model(&storage.Upstream{}).Where("id = ?", up.ID).Updates(updates).Error; err != nil {
			_ = commit()
			return nil, fmt.Errorf("upstream: update: %w", err)
		}
	}
	if err := commit(); err != nil {
		return nil, err
	}
	return s.loadUpstreamByPublicID(ctx, tenant.ID, publicID)
}

// DeleteUpstream soft-deletes the upstream row (GORM DeletedAt). The
// per-tenant uniqueIndex is filtered on deleted_at IS NULL, so a
// re-create with the same name after delete is permitted.
func (s *Service) DeleteUpstream(ctx context.Context, tenant *storage.Tenant, publicID string) error {
	if tenant == nil {
		return errors.New("upstream: tenant required")
	}
	up, err := s.loadUpstreamByPublicID(ctx, tenant.ID, publicID)
	if err != nil {
		return err
	}
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenant.ID))
	if err != nil {
		return err
	}
	if err := tx.Where("id = ?", up.ID).Delete(&storage.Upstream{}).Error; err != nil {
		_ = commit()
		return fmt.Errorf("upstream: delete: %w", err)
	}
	return commit()
}

// ReindexCatalog re-runs IndexUpstream for the given upstream using
// the calling user's link when the strategy requires one. Returns
// ErrCannotReindexWithoutLink when the strategy needs per-user
// credentials but the calling user has no usable link.
func (s *Service) ReindexCatalog(ctx context.Context, tenant *storage.Tenant, callingUser *storage.User, publicID string) (*storage.Upstream, error) {
	if tenant == nil {
		return nil, errors.New("upstream: tenant required")
	}
	up, err := s.loadUpstreamByPublicID(ctx, tenant.ID, publicID)
	if err != nil {
		return nil, err
	}
	var link *storage.UpstreamLink
	if callingUser != nil {
		l, lerr := s.loadLink(ctx, tenant.ID, callingUser.ID, up.ID)
		if lerr == nil {
			link = l
		}
	}
	if err := IndexUpstream(ctx, s.store, s.registry, tenant, up, link); err != nil {
		if errors.Is(err, ErrLinkNotFound) || errors.Is(err, ErrNeedsRelink) {
			return nil, ErrCannotReindexWithoutLink
		}
		return nil, err
	}
	return up, nil
}

// PreviewContext loads the merged JSON object the codemode adapter
// would expose to scripts for (upstream, user). Same merge order as
// contextblob.MergeContext: upstream defaults under link context.
func (s *Service) PreviewContext(ctx context.Context, tenant *storage.Tenant, upstreamPublicID, userPublicID string) ([]byte, error) {
	if tenant == nil {
		return nil, errors.New("upstream: tenant required")
	}
	up, err := s.loadUpstreamByPublicID(ctx, tenant.ID, upstreamPublicID)
	if err != nil {
		return nil, err
	}
	user, err := s.loadUserByPublicID(ctx, tenant.ID, userPublicID)
	if err != nil {
		return nil, err
	}
	defaults, _ := contextblob.SafeLoadContextBlob(up.DefaultsJSON)
	var linkCtx map[string]any
	if link, lerr := s.loadLink(ctx, tenant.ID, user.ID, up.ID); lerr == nil {
		linkCtx, _ = contextblob.SafeLoadContextBlob(link.ContextJSON)
	}
	merged := contextblob.MergeContext(defaults, linkCtx)
	return json.Marshal(merged)
}

func (s *Service) loadUpstreamByPublicID(ctx context.Context, tenantID int64, publicID string) (*storage.Upstream, error) {
	publicID = strings.TrimSpace(publicID)
	if publicID == "" {
		return nil, errors.New("upstream: public_id required")
	}
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenantID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = commit() }()

	var up storage.Upstream
	if err := tx.Where("public_id = ?", publicID).First(&up).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUpstreamNotFound
		}
		return nil, fmt.Errorf("upstream: load by public_id: %w", err)
	}
	return &up, nil
}

func (s *Service) loadUserByPublicID(ctx context.Context, tenantID int64, publicID string) (*storage.User, error) {
	publicID = strings.TrimSpace(publicID)
	if publicID == "" {
		return nil, ErrUserNotFound
	}
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenantID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = commit() }()

	var u storage.User
	if err := tx.Where("public_id = ?", publicID).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("upstream: load user by public_id: %w", err)
	}
	return &u, nil
}

// SummariseForAdmin renders a freshly-mutated upstream as a
// UserUpstreamSummary suitable for ToSummaryProto. callingUser may be
// nil — in which case LinkState == LinkStateNone and Link is nil.
// This is the shape the admin handler hands back after Create /
// Update / Reindex.
func (s *Service) SummariseForAdmin(ctx context.Context, tenant *storage.Tenant, callingUser *storage.User, up *storage.Upstream) UserUpstreamSummary {
	if callingUser != nil {
		return s.summariseUpstream(ctx, tenant, callingUser, up)
	}
	row := UserUpstreamSummary{
		Upstream: up,
		Aliases:  DecodeAliasesJSON(up.AliasesJSON),
	}
	if tools, err := s.loadToolCatalog(ctx, tenant.ID, up.ID); err == nil {
		row.Tools = tools
	}
	if strat, sErr := s.registry.Resolve(StrategyType(up.StrategyType)); sErr == nil {
		row.RequiresLink = strat.RequiresLink()
		if smp, ok := strat.(subModeProvider); ok {
			lctx := LinkContext{Tenant: tenant, Upstream: up}
			if sub, err := smp.SubMode(ctx, lctx); err == nil {
				row.StrategySubMode = sub
			}
		}
	}
	row.LinkState = LinkStateNone
	return row
}
