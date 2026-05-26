// Package statichdr implements the "static_header" upstream strategy.
//
// A "static_header" upstream attaches a single, configurable HTTP header
// to every outbound MCP request. The admin always supplies a shared
// secret — used for "Test Connection" at provision time, for the
// catalog indexer, and as the working default for every user. The
// admin can optionally allow individual users to override the shared
// secret with their own value (e.g. their personal API key); in that
// mode each user's override lives on UpstreamLink.ExtraJSON (encrypted
// with AAD tenant|user|"upstream.extra") and shadows the shared
// secret. With AllowUserOverride=false no override is ever consulted
// and the per-user UpstreamLink row exists only as an opt-out marker
// (Enabled=false).
//
// HeaderTemplate is a literal HTTP header value with "{value}"
// substituted at request time — e.g. "Bearer {value}" or
// "X-Api-Key {value}". The template is stored in the strategy config;
// the substituted secret never touches disk in plaintext.
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

// kindStrategyConfig is the SecretField AAD kind for UpstreamStrategyConfig.
const kindStrategyConfig = "upstream.strategy_config"

// kindUserExtra is the SecretField AAD kind for UpstreamLink.ExtraJSON
// when a user has supplied an override secret.
const kindUserExtra = "upstream.extra"

// placeholder is the literal substituted with the secret value at
// request time. Kept simple so admins don't accidentally introduce a
// templating vulnerability.
const placeholder = "{value}"

// SubMode values surfaced via the optional subModeProvider hook on the
// upstream package. The portal SPA renders different CTAs based on
// this string; never persisted, derived from Config at read time.
const (
	SubModeShared   = "shared"
	SubModeOverride = "override"
)

// Config is the JSON payload encrypted into
// UpstreamStrategyConfig.ConfigJSON. SharedSecret is mandatory in all
// cases — it powers Test Connection at provision time, the catalog
// indexer, and serves as the working default for every user when
// AllowUserOverride is false or the user hasn't submitted an override.
type Config struct {
	HeaderName        string `json:"header_name"`
	HeaderTemplate    string `json:"header_template"`
	SharedSecret      string `json:"shared_secret"`
	AllowUserOverride bool   `json:"allow_user_override,omitempty"`
}

// userExtra is the JSON shape of UpstreamLink.ExtraJSON when a user
// has supplied an override secret.
type userExtra struct {
	Secret string `json:"secret"`
}

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
	if strings.TrimSpace(c.SharedSecret) == "" {
		return errors.New("statichdr: shared_secret is required")
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

// PortalLinkPathFunc returns the SPA path the portal navigates to
// when override-mode StartLink is invoked.
type PortalLinkPathFunc func(tenantPublic string, upstreamPublic string) string

// New builds a Strategy. Pass nil for portalFn to use the default path
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

// SubMode reports "shared" or "override" by reading the strategy
// config. Implements upstream's optional subModeProvider so the portal
// renders the right CTA without re-loading the config itself.
func (s *Strategy) SubMode(ctx context.Context, lctx upstream.LinkContext) (string, error) {
	cfg, err := s.loadConfig(ctx, lctx)
	if err != nil {
		return "", err
	}
	if cfg.AllowUserOverride {
		return SubModeOverride, nil
	}
	return SubModeShared, nil
}

// RequiresLink reports true so the upstream package creates per-user
// UpstreamLink rows on demand. The link doubles as an opt-out marker
// (Enabled=false) and, when AllowUserOverride is true, the carrier for
// the user's override secret. Headers() always succeeds even with no
// link — falls back to the shared secret.
func (s *Strategy) RequiresLink() bool { return true }

func (s *Strategy) Provision(_ context.Context, lctx upstream.LinkContext) error {
	if lctx.Upstream == nil {
		return errors.New("statichdr: provision: upstream missing")
	}
	return nil
}

// StartLink returns the SPA path where the user pastes their override
// API key. Returns ErrUnsupported when the admin has not enabled user
// override.
func (s *Strategy) StartLink(ctx context.Context, lctx upstream.LinkContext) (upstream.StartLinkResult, error) {
	cfg, err := s.loadConfig(ctx, lctx)
	if err != nil {
		return upstream.StartLinkResult{}, err
	}
	if !cfg.AllowUserOverride {
		return upstream.StartLinkResult{}, upstream.ErrUnsupported
	}
	if lctx.Tenant == nil || lctx.Upstream == nil {
		return upstream.StartLinkResult{}, errors.New("statichdr: tenant/upstream missing")
	}
	return upstream.StartLinkResult{
		RedirectURL: s.portalFn(lctx.Tenant.PublicID, lctx.Upstream.PublicID),
	}, nil
}

func (s *Strategy) FinishLink(_ context.Context, _ upstream.LinkContext, _ string) (string, error) {
	return "", upstream.ErrUnsupported
}

// PersistUserSecret writes the user's override API key into
// UpstreamLink.ExtraJSON. Idempotent: re-running rotates the secret.
// Returns ErrUnsupported when override is disabled.
func (s *Strategy) PersistUserSecret(ctx context.Context, lctx upstream.LinkContext, secret string) error {
	cfg, err := s.loadConfig(ctx, lctx)
	if err != nil {
		return err
	}
	if !cfg.AllowUserOverride {
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
	var existing storage.UpstreamLink
	if err := tx.Where("user_id = ? AND upstream_id = ?", lctx.User.ID, lctx.Upstream.ID).
		First(&existing).Error; err != nil {
		uid := lctx.User.ID
		newLink := storage.UpstreamLink{
			TenantID:   lctx.Tenant.ID,
			UserID:     &uid,
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
	existing.ExtraJSON = extra
	existing.Enabled = true
	existing.NeedsRelink = false
	existing.ConsecutiveFailures = 0
	existing.FirstFailureAt = nil
	existing.LastFailureAt = nil
	existing.LastFailureReason = ""
	existing.AutoDisabledAt = nil
	if err := tx.Save(&existing).Error; err != nil {
		_ = commit()
		return fmt.Errorf("statichdr: update link: %w", err)
	}
	return commit()
}

// ClearUserOverride drops the per-user override on the link. The link
// row stays (preserves Enabled / opt-out state); only ExtraJSON is
// zeroed and the health counters reset so the next request falls back
// to the shared secret cleanly.
func (s *Strategy) ClearUserOverride(ctx context.Context, lctx upstream.LinkContext) error {
	if lctx.Tenant == nil || lctx.User == nil || lctx.Upstream == nil {
		return errors.New("statichdr: tenant/user/upstream missing")
	}
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, lctx.Tenant.ID))
	if err != nil {
		return fmt.Errorf("statichdr: open session: %w", err)
	}
	updates := map[string]any{
		"extra_json":           nil,
		"needs_relink":         false,
		"consecutive_failures": 0,
		"first_failure_at":     nil,
		"last_failure_at":      nil,
		"last_failure_reason":  "",
		"auto_disabled_at":     nil,
	}
	if err := tx.Model(&storage.UpstreamLink{}).
		Where("user_id = ? AND upstream_id = ?", lctx.User.ID, lctx.Upstream.ID).
		Updates(updates).Error; err != nil {
		_ = commit()
		return fmt.Errorf("statichdr: clear override: %w", err)
	}
	return commit()
}

// Headers returns the configured header with a secret substituted in.
// Resolution order:
//
//  1. If AllowUserOverride is true AND the user has an override secret
//     AND the link is not in NeedsRelink, use the user's secret.
//  2. Otherwise fall back to cfg.SharedSecret.
//
// Tools keep working when a user's override key starts failing — the
// gateway falls back to shared while the portal nudges the user to fix
// their key.
func (s *Strategy) Headers(ctx context.Context, lctx upstream.LinkContext) (map[string]string, error) {
	cfg, err := s.loadConfig(ctx, lctx)
	if err != nil {
		return nil, err
	}
	secret := cfg.SharedSecret
	if cfg.AllowUserOverride && lctx.Link != nil && !lctx.Link.ExtraJSON.IsZero() && !lctx.Link.NeedsRelink {
		if lctx.Tenant == nil || lctx.User == nil {
			return nil, errors.New("statichdr: tenant/user missing for override header")
		}
		tenantStr := fmt.Sprintf("%d", lctx.Tenant.ID)
		userStr := fmt.Sprintf("%d", lctx.User.ID)
		if err := lctx.Link.ExtraJSON.Decrypt(tenantStr, userStr, kindUserExtra); err != nil {
			return nil, fmt.Errorf("statichdr: decrypt extra: %w", err)
		}
		var ux userExtra
		if err := json.Unmarshal(lctx.Link.ExtraJSON.Bytes(), &ux); err != nil {
			return nil, fmt.Errorf("statichdr: parse extra: %w", err)
		}
		if strings.TrimSpace(ux.Secret) != "" {
			secret = ux.Secret
		}
	}
	value := strings.ReplaceAll(cfg.HeaderTemplate, placeholder, secret)
	return map[string]string{cfg.HeaderName: value}, nil
}

// HeadersForceRefresh is a no-op alias — there's no token to rotate.
func (s *Strategy) HeadersForceRefresh(ctx context.Context, lctx upstream.LinkContext) (map[string]string, error) {
	return s.Headers(ctx, lctx)
}

func (s *Strategy) Maintain(_ context.Context, _ upstream.LinkContext) error { return nil }

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

// EncodeConfig encrypts and returns a SecretField suitable for storing
// in UpstreamStrategyConfig.ConfigJSON.
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

// ParseConfig builds a Config from the flat string map carried by
// AdminService.CreateUpstreamRequest.strategy_config. Centralises the
// wire-key vocabulary so callers don't reach into the map directly.
//
// Recognised keys: "header_name", "header_template", "value" (shared
// secret), "allow_user_override" ("true"/"false"). Unknown keys are
// ignored — the field set is locked here, not at the proto level.
// The returned Config is validated; an error means the caller should
// surface it as InvalidArgument with the relevant field path.
func ParseConfig(m map[string]string) (Config, error) {
	cfg := Config{
		HeaderName:        strings.TrimSpace(m["header_name"]),
		HeaderTemplate:    m["header_template"],
		SharedSecret:      m["value"],
		AllowUserOverride: strings.EqualFold(strings.TrimSpace(m["allow_user_override"]), "true"),
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// DecodeConfig decrypts an existing UpstreamStrategyConfig.ConfigJSON
// payload back into a Config. The caller passes the SecretField loaded
// from storage and the tenant ID it was bound to. Used by the admin
// update path to merge a patch into the current config without losing
// fields the caller didn't touch.
func DecodeConfig(tenantID int64, sf crypto.SecretField) (Config, error) {
	if sf.IsZero() {
		return Config{}, errors.New("statichdr: config row is empty")
	}
	if err := sf.Decrypt(fmt.Sprintf("%d", tenantID), "", kindStrategyConfig); err != nil {
		return Config{}, fmt.Errorf("statichdr: decrypt config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(sf.Bytes(), &cfg); err != nil {
		return Config{}, fmt.Errorf("statichdr: parse config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// ApplyConfigPatch overlays the wire patch onto an existing Config.
// Recognised patch keys:
//
//	value                empty/absent = keep existing shared secret;
//	                     non-empty = rotate.
//	allow_user_override  absent = keep existing; "true"/"false" =
//	                     replace.
//
// header_name and header_template are intentionally NOT patchable
// post-creation: changing them constitutes a different upstream and
// belongs in delete-and-recreate.
func ApplyConfigPatch(cur Config, patch map[string]string) (Config, error) {
	out := cur
	if v, ok := patch["value"]; ok && strings.TrimSpace(v) != "" {
		out.SharedSecret = v
	}
	if v, ok := patch["allow_user_override"]; ok {
		out.AllowUserOverride = strings.EqualFold(strings.TrimSpace(v), "true")
	}
	if err := out.validate(); err != nil {
		return Config{}, err
	}
	return out, nil
}
