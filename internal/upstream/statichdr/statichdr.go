// Package statichdr implements the "static_header" upstream strategy.
//
// A "static_header" upstream attaches a single, configurable HTTP header
// to every outbound MCP request. Two operating modes are supported:
//
//   - tenant_owner (default): the admin's secret stored on
//     UpstreamTenantLink.AccessToken is used for all users, the catalog
//     indexer, and Test Connection. No per-user overrides are allowed.
//
//   - byok (Bring Your Own Key): the admin's secret on
//     UpstreamTenantLink is used only for the catalog indexer and Test
//     Connection. Every user MUST provide their own key via the portal;
//     the per-user secret lives on UpstreamLink.ExtraJSON (encrypted
//     with AAD tenant|user|"upstream.extra") and is used for user-facing
//     MCP requests. If a user has not provided a key the gateway returns
//     ErrNoCredentials; if their key was invalidated (NeedsRelink) it
//     returns ErrNeedsRelink.
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

	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/upstream"
)

// kindStrategyConfig is the SecretField AAD kind for UpstreamStrategyConfig.
const kindStrategyConfig = "upstream.strategy_config"

// kindTenantAccessToken is the SecretField AAD kind for
// UpstreamTenantLink.AccessToken.
const kindTenantAccessToken = "upstream.tenant.access_token"

// kindUserExtra is the SecretField AAD kind for UpstreamLink.ExtraJSON
// when a user has supplied an override secret.
const kindUserExtra = "upstream.extra"

// placeholder is the literal substituted with the secret value at
// request time. Kept simple so admins don't accidentally introduce a
// templating vulnerability.
const placeholder = "{value}"

// Mode values identify the static_header operating mode. Stored in the
// Mode column of UpstreamStrategyConfig (separate from the encrypted
// ConfigJSON so SubMode lookups don't require decryption).
const (
	ModeTenantOwner = "tenant_owner"
	ModeBYOK        = "byok"
)

// Config is the JSON payload encrypted into
// UpstreamStrategyConfig.ConfigJSON. It holds only non-secret fields;
// the admin's secret lives on UpstreamTenantLink.AccessToken.
type Config struct {
	HeaderName     string `json:"header_name"`
	HeaderTemplate string `json:"header_template"`
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

// loadMode reads the Mode column from UpstreamStrategyConfig. Falls
// back to ModeTenantOwner when the row is missing or the column is
// empty, so existing upstreams without a Mode default to tenant-wide
// operation.
func (s *Strategy) loadMode(ctx context.Context, lctx upstream.LinkContext) (string, error) {
	if lctx.Tenant == nil || lctx.Upstream == nil {
		return ModeTenantOwner, nil
	}
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, lctx.Tenant.ID))
	if err != nil {
		return ModeTenantOwner, nil
	}
	var row storage.UpstreamStrategyConfig
	if err := tx.Where("upstream_id = ? AND type = ?", lctx.Upstream.ID, string(upstream.StrategyStaticHeader)).First(&row).Error; err != nil {
		_ = commit()
		return ModeTenantOwner, nil
	}
	_ = commit()
	if row.Mode == "" {
		return ModeTenantOwner, nil
	}
	return row.Mode, nil
}

// SubMode reports "tenant_owner" or "byok" by reading the Mode column
// directly (no decryption needed). Implements upstream's optional
// subModeProvider so the portal renders the right CTA.
func (s *Strategy) SubMode(ctx context.Context, lctx upstream.LinkContext) (string, error) {
	return s.loadMode(ctx, lctx)
}

// RequiresLink reports true so the upstream package creates per-user
// UpstreamLink rows on demand. The link doubles as an opt-out marker
// (Enabled=false) and, when mode is "byok", the carrier for the
// user's override secret. Headers() always succeeds even with no
// link — falls back to the shared secret.
func (s *Strategy) RequiresLink() bool { return true }

func (s *Strategy) Provision(_ context.Context, lctx upstream.LinkContext) error {
	if lctx.Upstream == nil {
		return errors.New("statichdr: provision: upstream missing")
	}
	return nil
}

// StartLink returns the SPA path where the user pastes their BYOK API
// key. Returns ErrUnsupported when mode is TenantOwner (users must
// not submit per-user keys in tenant-wide mode).
func (s *Strategy) StartLink(ctx context.Context, lctx upstream.LinkContext) (upstream.StartLinkResult, error) {
	mode, err := s.loadMode(ctx, lctx)
	if err != nil {
		return upstream.StartLinkResult{}, err
	}
	if mode != ModeBYOK {
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

// PersistUserSecret writes the user's BYOK API key into
// UpstreamLink.ExtraJSON. Idempotent: re-running rotates the secret.
// Returns ErrUnsupported when mode is TenantOwner.
func (s *Strategy) PersistUserSecret(ctx context.Context, lctx upstream.LinkContext, secret string) error {
	mode, err := s.loadMode(ctx, lctx)
	if err != nil {
		return err
	}
	if mode != ModeBYOK {
		return upstream.ErrUnsupported
	}
	if strings.TrimSpace(secret) == "" {
		return errors.New("statichdr: secret must not be empty")
	}
	if lctx.Tenant == nil || (lctx.User == nil && lctx.ServiceAccountID == nil) || lctx.Upstream == nil {
		return errors.New("statichdr: tenant/user/upstream missing")
	}
	tenantStr := fmt.Sprintf("%d", lctx.Tenant.ID)
	ownerStr := lctx.OwnerIDStr()

	payload, err := json.Marshal(userExtra{Secret: secret})
	if err != nil {
		return fmt.Errorf("statichdr: marshal extra: %w", err)
	}
	extra := crypto.NewSecret(payload)
	extra.SetAAD(tenantStr, ownerStr, kindUserExtra)

	tx, commit, err := s.store.Session(storage.WithTenant(ctx, lctx.Tenant.ID))
	if err != nil {
		return fmt.Errorf("statichdr: open session: %w", err)
	}
	var existing storage.UpstreamLink
	var existingErr error
	if lctx.IsServiceAccount() {
		existingErr = tx.Where("service_account_id = ? AND upstream_id = ?", *lctx.ServiceAccountID, lctx.Upstream.ID).First(&existing).Error
	} else {
		existingErr = tx.Where("user_id = ? AND upstream_id = ?", lctx.User.ID, lctx.Upstream.ID).First(&existing).Error
	}
	if existingErr != nil {
		newLink := storage.UpstreamLink{
			TenantID:   lctx.Tenant.ID,
			UpstreamID: lctx.Upstream.ID,
			Enabled:    true,
			ExtraJSON:  extra,
		}
		if lctx.IsServiceAccount() {
			newLink.ServiceAccountID = lctx.ServiceAccountID
		} else {
			uid := lctx.User.ID
			newLink.UserID = &uid
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

// PersistTenantSecret writes the admin's secret into
// UpstreamTenantLink.AccessToken. Idempotent: re-running rotates the
// secret. Used during upstream creation and secret rotation.
func (s *Strategy) PersistTenantSecret(ctx context.Context, lctx upstream.LinkContext, secret string) error {
	if strings.TrimSpace(secret) == "" {
		return errors.New("statichdr: secret must not be empty")
	}
	if lctx.Tenant == nil || lctx.Upstream == nil {
		return errors.New("statichdr: tenant/upstream missing")
	}

	tenantStr := fmt.Sprintf("%d", lctx.Tenant.ID)
	sf := crypto.NewSecret([]byte(secret))
	sf.SetAAD(tenantStr, "", kindTenantAccessToken)

	tx, commit, err := s.store.Session(storage.WithTenant(ctx, lctx.Tenant.ID))
	if err != nil {
		return fmt.Errorf("statichdr: open session: %w", err)
	}

	var existing storage.UpstreamTenantLink
	if err := tx.Where("upstream_id = ?", lctx.Upstream.ID).First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			newLink := storage.UpstreamTenantLink{
				TenantID:    lctx.Tenant.ID,
				UpstreamID:  lctx.Upstream.ID,
				Enabled:     true,
				AccessToken: sf,
			}
			if createErr := tx.Create(&newLink).Error; createErr != nil {
				_ = commit()
				return fmt.Errorf("statichdr: create tenant link: %w", createErr)
			}
			return commit()
		}
		_ = commit()
		return fmt.Errorf("statichdr: load tenant link: %w", err)
	}

	existing.AccessToken = sf
	existing.Enabled = true
	existing.NeedsRelink = false
	existing.ConsecutiveFailures = 0
	existing.FirstFailureAt = nil
	existing.LastFailureAt = nil
	existing.LastFailureReason = ""
	existing.AutoDisabledAt = nil
	if err := tx.Save(&existing).Error; err != nil {
		_ = commit()
		return fmt.Errorf("statichdr: update tenant link: %w", err)
	}
	return commit()
}

// ClearUserOverride drops the per-user override on the link. The link
// row stays (preserves Enabled / opt-out state); only ExtraJSON is
// zeroed and the health counters reset so the next request falls back
// to the tenant secret cleanly.
func (s *Strategy) ClearUserOverride(ctx context.Context, lctx upstream.LinkContext) error {
	if lctx.Tenant == nil || (lctx.User == nil && lctx.ServiceAccountID == nil) || lctx.Upstream == nil {
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
	query := tx.Model(&storage.UpstreamLink{})
	if lctx.IsServiceAccount() {
		query = query.Where("service_account_id = ? AND upstream_id = ?", *lctx.ServiceAccountID, lctx.Upstream.ID)
	} else {
		query = query.Where("user_id = ? AND upstream_id = ?", lctx.User.ID, lctx.Upstream.ID)
	}
	if err := query.Updates(updates).Error; err != nil {
		_ = commit()
		return fmt.Errorf("statichdr: clear override: %w", err)
	}
	return commit()
}

// Headers returns the configured header with a secret substituted in.
// Resolution order depends on mode:
//
// TenantOwner:
//   - Always uses the admin secret from UpstreamTenantLink.AccessToken.
//
// BYOK:
//   - No user (system / catalog / indexer): uses the admin secret from
//     UpstreamTenantLink.AccessToken.
//   - Link exists with NeedsRelink: returns ErrNeedsRelink.
//   - Link exists with ExtraJSON (user's key): decrypts and uses it.
//   - No link or link without key: returns ErrNoCredentials.
func (s *Strategy) Headers(ctx context.Context, lctx upstream.LinkContext) (map[string]string, error) {
	cfg, err := s.loadConfig(ctx, lctx)
	if err != nil {
		return nil, err
	}
	mode, err := s.loadMode(ctx, lctx)
	if err != nil {
		return nil, err
	}

	switch mode {
	case ModeBYOK:
		if lctx.User == nil {
			// System / catalog / indexer — use the admin's setup key.
			secret, err := s.resolveTenantSecret(ctx, lctx)
			if err != nil {
				return nil, err
			}
			value := strings.ReplaceAll(cfg.HeaderTemplate, placeholder, secret)
			return map[string]string{cfg.HeaderName: value}, nil
		}
		if lctx.Link == nil || lctx.Link.ExtraJSON.IsZero() {
			return nil, upstream.ErrNoCredentials
		}
		if lctx.Link.NeedsRelink {
			return nil, upstream.ErrNeedsRelink
		}
		tenantStr := fmt.Sprintf("%d", lctx.Tenant.ID)
		ownerStr := lctx.OwnerIDStr()
		if err := lctx.Link.ExtraJSON.Decrypt(tenantStr, ownerStr, kindUserExtra); err != nil {
			return nil, fmt.Errorf("statichdr: decrypt extra: %w", err)
		}
		var ux userExtra
		if err := json.Unmarshal(lctx.Link.ExtraJSON.Bytes(), &ux); err != nil {
			return nil, fmt.Errorf("statichdr: parse extra: %w", err)
		}
		if strings.TrimSpace(ux.Secret) == "" {
			return nil, upstream.ErrNoCredentials
		}
		value := strings.ReplaceAll(cfg.HeaderTemplate, placeholder, ux.Secret)
		return map[string]string{cfg.HeaderName: value}, nil

	default: // ModeTenantOwner
		secret, err := s.resolveTenantSecret(ctx, lctx)
		if err != nil {
			return nil, err
		}
		value := strings.ReplaceAll(cfg.HeaderTemplate, placeholder, secret)
		return map[string]string{cfg.HeaderName: value}, nil
	}
}

// resolveTenantSecret loads and decrypts the admin secret from
// UpstreamTenantLink. It prefers a pre-populated lctx.TenantLink; otherwise
// it hits the database.
func (s *Strategy) resolveTenantSecret(ctx context.Context, lctx upstream.LinkContext) (string, error) {
	if lctx.Tenant == nil || lctx.Upstream == nil {
		return "", errors.New("statichdr: tenant/upstream missing")
	}

	var tenantLink *storage.UpstreamTenantLink
	if lctx.TenantLink != nil {
		tenantLink = lctx.TenantLink
	} else {
		loaded, err := s.loadTenantLink(ctx, lctx.Tenant.ID, lctx.Upstream.ID)
		if err != nil {
			return "", err
		}
		tenantLink = loaded
	}

	if tenantLink == nil || tenantLink.AccessToken.IsZero() {
		return "", upstream.ErrNoTenantLink
	}

	tenantStr := fmt.Sprintf("%d", lctx.Tenant.ID)
	localCopy := tenantLink.AccessToken
	if err := localCopy.Decrypt(tenantStr, "", kindTenantAccessToken); err != nil {
		return "", fmt.Errorf("statichdr: decrypt tenant access token: %w", err)
	}

	secret := strings.TrimSpace(string(localCopy.Bytes()))
	if secret == "" {
		return "", upstream.ErrNoTenantLink
	}
	return secret, nil
}

func (s *Strategy) loadTenantLink(ctx context.Context, tenantID, upstreamID int64) (*storage.UpstreamTenantLink, error) {
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenantID))
	if err != nil {
		return nil, fmt.Errorf("statichdr: open session: %w", err)
	}
	defer func() { _ = commit() }()

	var link storage.UpstreamTenantLink
	if err := tx.Where("upstream_id = ?", upstreamID).First(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("statichdr: load tenant link: %w", err)
	}
	return &link, nil
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
// in UpstreamStrategyConfig.ConfigJSON. NOTE: mode is stored separately
// in the Mode column, not in this encrypted payload.
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

// ParseConfig parses the admin-supplied strategy_config map for static_header.
// Accepted keys: header_name, header_template.
//
// Unknown keys are ignored — the field set is locked here, not at the
// proto level. The returned Config is validated; an error means the
// caller should surface it as InvalidArgument with the relevant field
// path.
func ParseConfig(m map[string]string) (Config, error) {
	cfg := Config{
		HeaderName:     strings.TrimSpace(m["header_name"]),
		HeaderTemplate: m["header_template"],
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
//
// header_name and header_template are intentionally NOT patchable
// post-creation: changing them constitutes a different upstream and
// belongs in delete-and-recreate. Secret rotation goes through
// PersistTenantSecret, not this patch path.
func ApplyConfigPatch(cur Config, patch map[string]string) (Config, error) {
	out := cur
	if err := out.validate(); err != nil {
		return Config{}, err
	}
	return out, nil
}
