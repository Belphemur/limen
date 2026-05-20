// Package statichdr implements the "static_header" upstream strategy.
//
// A "static_header" upstream attaches a single, configurable HTTP header
// to every outbound MCP request. Two modes are supported:
//
//   - Tenant mode: the secret lives in UpstreamStrategyConfig (encrypted
//     with AAD tenant|""|"upstream.strategy_config") and applies to every
//     user in the tenant. Useful for shared API keys.
//   - User mode: each user supplies their own secret via the portal. The
//     secret lives in UpstreamLink.ExtraJSON (encrypted with AAD
//     tenant|user|"upstream.extra"). Useful for per-user PATs / API keys.
//
// HeaderTemplate is a literal HTTP header value with "{value}" substituted
// at request time — e.g. "Bearer {value}" or "X-Api-Key {value}". The
// template is stored in the strategy config; the substituted secret never
// touches disk in plaintext.
package statichdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/upstream"
)

// Mode selects whether the secret is tenant-wide or per-user.
type Mode string

const (
	ModeTenant Mode = "tenant"
	ModeUser   Mode = "user"
)

// kindStrategyConfig is the SecretField AAD kind for UpstreamStrategyConfig.
const kindStrategyConfig = "upstream.strategy_config"

// kindUserExtra is the SecretField AAD kind for UpstreamLink.ExtraJSON in
// user mode.
const kindUserExtra = "upstream.extra"

// placeholder is the literal substituted with the secret value at request
// time. Kept simple so admins don't accidentally introduce a templating
// vulnerability.
const placeholder = "{value}"

// Config is the JSON payload encrypted into UpstreamStrategyConfig.ConfigJSON.
// HeaderName is e.g. "Authorization" or "X-Api-Key". HeaderTemplate is the
// literal header value with "{value}" substituted at request time.
//
// In tenant mode TenantSecret carries the shared secret; in user mode it
// is empty and per-user secrets live on UpstreamLink.ExtraJSON.
type Config struct {
	HeaderName     string `json:"header_name"`
	HeaderTemplate string `json:"header_template"`
	Mode           Mode   `json:"mode"`
	TenantSecret   string `json:"tenant_secret,omitempty"`
}

// userExtra is the JSON shape of UpstreamLink.ExtraJSON for user mode.
type userExtra struct {
	Secret string `json:"secret"`
}

// validate checks a Config for obvious mistakes. HeaderName must look like
// a valid HTTP field name; HeaderTemplate must contain the {value}
// placeholder so the secret actually gets substituted in. Mode must be one
// of the two known values; tenant mode requires a non-empty TenantSecret.
func (c Config) validate() error {
	if strings.TrimSpace(c.HeaderName) == "" {
		return errors.New("statichdr: header_name is required")
	}
	if !validHeaderName(c.HeaderName) {
		return fmt.Errorf("statichdr: header_name %q is not a valid HTTP field name", c.HeaderName)
	}
	if !strings.Contains(c.HeaderTemplate, placeholder) {
		return fmt.Errorf("statichdr: header_template must contain %q", placeholder)
	}
	switch c.Mode {
	case ModeTenant:
		if strings.TrimSpace(c.TenantSecret) == "" {
			return errors.New("statichdr: tenant mode requires tenant_secret")
		}
	case ModeUser:
		if c.TenantSecret != "" {
			return errors.New("statichdr: user mode must not carry tenant_secret")
		}
	default:
		return fmt.Errorf("statichdr: unknown mode %q", c.Mode)
	}
	return nil
}

func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z',
			r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// Strategy implements upstream.Strategy for StrategyStaticHeader.
type Strategy struct {
	store    *storage.Store
	cipher   *crypto.Cipher
	portalFn PortalLinkPathFunc
}

// PortalLinkPathFunc returns the SPA path the portal navigates to when
// user mode StartLink is invoked. Provided by wiring so this package
// doesn't hard-code the SPA URL shape.
type PortalLinkPathFunc func(tenantPublic string, upstreamPublic string) string

// New builds a Strategy. The PortalLinkPathFunc resolves the SPA path
// returned by StartLink in user mode; pass nil to default to
// "/t/<tenant>/portal/upstreams/<upstream>/api-key".
func New(store *storage.Store, cipher *crypto.Cipher, portalFn PortalLinkPathFunc) *Strategy {
	if portalFn == nil {
		portalFn = defaultPortalPath
	}
	return &Strategy{store: store, cipher: cipher, portalFn: portalFn}
}

func defaultPortalPath(tenantPublic, upstreamPublic string) string {
	return fmt.Sprintf("/t/%s/portal/upstreams/%s/api-key", tenantPublic, upstreamPublic)
}

// Type implements upstream.Strategy.
func (s *Strategy) Type() upstream.StrategyType { return upstream.StrategyStaticHeader }

// SubMode reports "tenant" or "user" by reading the strategy config.
// Implements upstream's optional subModeProvider so the portal can
// render the right CTA without re-loading the config itself. Returns
// the empty string on a load error so the listing degrades gracefully.
func (s *Strategy) SubMode(ctx context.Context, lctx upstream.LinkContext) (string, error) {
	cfg, err := s.loadConfig(ctx, lctx)
	if err != nil {
		return "", err
	}
	return string(cfg.Mode), nil
}

// RequiresLink reports per-user link rows only for user mode. The
// registry call site doesn't know the mode at registration time, so this
// returns true; tenant-mode upstreams simply never call StartLink and
// always derive headers from the strategy config. Phase 8's per-request
// header funnel handles both shapes uniformly via Headers().
func (s *Strategy) RequiresLink() bool { return true }

// Provision validates the encoded Config on UpstreamStrategyConfig and
// nothing else — there's no remote endpoint to probe.
func (s *Strategy) Provision(_ context.Context, lctx upstream.LinkContext) error {
	if lctx.Upstream == nil {
		return errors.New("statichdr: provision: upstream missing")
	}
	// The actual config row will be loaded inside Headers / StartLink.
	// Provision is wired to allow a future "validate config on attach"
	// step without churning the strategy contract; for now it's a
	// shape-check on the Upstream itself.
	return nil
}

// StartLink in user mode returns the SPA path where the user pastes their
// API key. In tenant mode it is unsupported — the operator configures the
// secret out-of-band via the admin SPA.
func (s *Strategy) StartLink(ctx context.Context, lctx upstream.LinkContext) (upstream.StartLinkResult, error) {
	cfg, err := s.loadConfig(ctx, lctx)
	if err != nil {
		return upstream.StartLinkResult{}, err
	}
	if cfg.Mode != ModeUser {
		return upstream.StartLinkResult{}, upstream.ErrUnsupported
	}
	if lctx.Tenant == nil || lctx.Upstream == nil {
		return upstream.StartLinkResult{}, errors.New("statichdr: tenant/upstream missing")
	}
	return upstream.StartLinkResult{
		RedirectURL: s.portalFn(lctx.Tenant.PublicID, lctx.Upstream.PublicID),
	}, nil
}

// FinishLink is unsupported — user mode uses an explicit SPA submit
// (PersistUserSecret); tenant mode has no per-user step.
func (s *Strategy) FinishLink(_ context.Context, _ upstream.LinkContext, _ string) (string, error) {
	return "", upstream.ErrUnsupported
}

// PersistUserSecret writes the user's API key into UpstreamLink.ExtraJSON
// for user mode. Idempotent: re-running it rotates the secret. The Phase 9b
// portal's SubmitUpstreamAPIKey Connect-RPC wraps this.
func (s *Strategy) PersistUserSecret(ctx context.Context, lctx upstream.LinkContext, secret string) error {
	cfg, err := s.loadConfig(ctx, lctx)
	if err != nil {
		return err
	}
	if cfg.Mode != ModeUser {
		return upstream.ErrUnsupported
	}
	if strings.TrimSpace(secret) == "" {
		return errors.New("statichdr: secret must not be empty")
	}
	if lctx.Tenant == nil || lctx.User == nil || lctx.Upstream == nil {
		return errors.New("statichdr: tenant/user/upstream missing")
	}
	tenantStr := fmt.Sprintf("%d", lctx.Tenant.ID)
	userStr := fmt.Sprintf("%d", lctx.User.ID)

	payload, err := json.Marshal(userExtra{Secret: secret})
	if err != nil {
		return fmt.Errorf("statichdr: marshal extra: %w", err)
	}
	extra := crypto.NewSecret(payload)
	extra.SetAAD(tenantStr, userStr, kindUserExtra)

	tx, commit, err := s.store.Session(storage.WithTenant(ctx, lctx.Tenant.ID))
	if err != nil {
		return fmt.Errorf("statichdr: open session: %w", err)
	}
	// Upsert: one link per (tenant, user, upstream).
	var existing storage.UpstreamLink
	err = tx.Where("tenant_id = ? AND user_id = ? AND upstream_id = ?", lctx.Tenant.ID, lctx.User.ID, lctx.Upstream.ID).
		First(&existing).Error
	if err != nil {
		// Create new link.
		newLink := storage.UpstreamLink{
			TenantID:   lctx.Tenant.ID,
			UserID:     lctx.User.ID,
			UpstreamID: lctx.Upstream.ID,
			Enabled:    true,
			ExtraJSON:  extra,
		}
		if createErr := tx.Create(&newLink).Error; createErr != nil {
			_ = commit()
			return fmt.Errorf("statichdr: create link: %w", createErr)
		}
		return commit()
	}
	// Update existing link's ExtraJSON.
	existing.ExtraJSON = extra
	existing.Enabled = true
	existing.NeedsRelink = false
	existing.ConsecutiveFailures = 0
	existing.FirstFailureAt = nil
	existing.LastFailureAt = nil
	existing.LastFailureReason = ""
	existing.AutoDisabledAt = nil
	if updErr := tx.Save(&existing).Error; updErr != nil {
		_ = commit()
		return fmt.Errorf("statichdr: update link: %w", updErr)
	}
	return commit()
}

// Headers returns the configured header with the secret substituted in.
func (s *Strategy) Headers(ctx context.Context, lctx upstream.LinkContext) (map[string]string, error) {
	cfg, err := s.loadConfig(ctx, lctx)
	if err != nil {
		return nil, err
	}
	var secret string
	switch cfg.Mode {
	case ModeTenant:
		secret = cfg.TenantSecret
	case ModeUser:
		if lctx.Link == nil || lctx.Link.ExtraJSON.IsZero() {
			return nil, upstream.ErrNeedsRelink
		}
		if lctx.Tenant == nil || lctx.User == nil {
			return nil, errors.New("statichdr: tenant/user missing for user-mode header")
		}
		tenantStr := fmt.Sprintf("%d", lctx.Tenant.ID)
		userStr := fmt.Sprintf("%d", lctx.User.ID)
		if err := lctx.Link.ExtraJSON.Decrypt(tenantStr, userStr, kindUserExtra); err != nil {
			return nil, fmt.Errorf("statichdr: decrypt extra: %w", err)
		}
		var ux userExtra
		if jsonErr := json.Unmarshal(lctx.Link.ExtraJSON.Bytes(), &ux); jsonErr != nil {
			return nil, fmt.Errorf("statichdr: parse extra: %w", jsonErr)
		}
		if strings.TrimSpace(ux.Secret) == "" {
			return nil, upstream.ErrNeedsRelink
		}
		secret = ux.Secret
	default:
		return nil, fmt.Errorf("statichdr: unknown mode %q", cfg.Mode)
	}
	value := strings.ReplaceAll(cfg.HeaderTemplate, placeholder, secret)
	return map[string]string{cfg.HeaderName: value}, nil
}

// HeadersForceRefresh is a no-op for static_header — there's no token to
// rotate. We return the same map Headers would; a 401 after this means the
// secret is bad and the user must re-submit it.
func (s *Strategy) HeadersForceRefresh(ctx context.Context, lctx upstream.LinkContext) (map[string]string, error) {
	return s.Headers(ctx, lctx)
}

// Maintain is a no-op for static_header.
func (s *Strategy) Maintain(_ context.Context, _ upstream.LinkContext) error { return nil }

// loadConfig fetches and decrypts the UpstreamStrategyConfig for the
// upstream in lctx. Tenant-scoped AAD: "" user id, "upstream.strategy_config".
func (s *Strategy) loadConfig(ctx context.Context, lctx upstream.LinkContext) (Config, error) {
	var zero Config
	if lctx.Tenant == nil || lctx.Upstream == nil {
		return zero, errors.New("statichdr: tenant/upstream missing")
	}
	tenantStr := fmt.Sprintf("%d", lctx.Tenant.ID)

	tx, commit, err := s.store.Session(storage.WithTenant(ctx, lctx.Tenant.ID))
	if err != nil {
		return zero, fmt.Errorf("statichdr: open session: %w", err)
	}
	var row storage.UpstreamStrategyConfig
	if err := tx.Where("upstream_id = ?", lctx.Upstream.ID).First(&row).Error; err != nil {
		_ = commit()
		return zero, fmt.Errorf("statichdr: load config: %w", err)
	}
	if commitErr := commit(); commitErr != nil {
		return zero, commitErr
	}
	if row.ConfigJSON.IsZero() {
		return zero, errors.New("statichdr: config row is empty")
	}
	if err := row.ConfigJSON.Decrypt(tenantStr, "", kindStrategyConfig); err != nil {
		return zero, fmt.Errorf("statichdr: decrypt config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(row.ConfigJSON.Bytes(), &cfg); err != nil {
		return zero, fmt.Errorf("statichdr: parse config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return zero, err
	}
	return cfg, nil
}

// EncodeConfig encrypts and returns a SecretField suitable for storing in
// UpstreamStrategyConfig.ConfigJSON. Provisioning tooling (admin SPA,
// `limen create-upstream` CLI) calls this once per upstream.
func EncodeConfig(tenantID int64, cfg Config) (crypto.SecretField, error) {
	if err := cfg.validate(); err != nil {
		return crypto.SecretField{}, err
	}
	payload, err := json.Marshal(cfg)
	if err != nil {
		return crypto.SecretField{}, fmt.Errorf("statichdr: marshal config: %w", err)
	}
	sf := crypto.NewSecret(payload)
	sf.SetAAD(fmt.Sprintf("%d", tenantID), "", kindStrategyConfig)
	return sf, nil
}
