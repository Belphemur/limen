package mcpspec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/storage"
)

// Config is the JSON payload encrypted into UpstreamStrategyConfig.ConfigJSON
// for upstreams whose authorization server doesn't support RFC 7591
// Dynamic Client Registration (e.g. GitHub). Operators provision a
// client out-of-band on the AS, then store the credentials here.
//
// All fields are optional; presence of ClientID is the signal that
// Provision should skip DCR. Issuer / AuthorizationEndpoint /
// TokenEndpoint are used when AS metadata discovery returns nothing
// or omits fields. Scopes are appended to the authorization request.
type Config struct {
	Issuer                string   `json:"issuer,omitempty"`
	ClientID              string   `json:"client_id,omitempty"`
	ClientSecret          string   `json:"client_secret,omitempty"`
	AuthorizationEndpoint string   `json:"authorization_endpoint,omitempty"`
	TokenEndpoint         string   `json:"token_endpoint,omitempty"`
	Scopes                []string `json:"scopes,omitempty"`
}

// HasStaticClient reports whether the config provisions a usable static
// OAuth client (skips DCR).
func (c Config) HasStaticClient() bool { return strings.TrimSpace(c.ClientID) != "" }

// IsZero reports whether no field is populated.
func (c Config) IsZero() bool {
	return c.Issuer == "" && c.ClientID == "" && c.ClientSecret == "" &&
		c.AuthorizationEndpoint == "" && c.TokenEndpoint == "" && len(c.Scopes) == 0
}

// EncodeConfig encrypts and returns a SecretField suitable for storing
// in UpstreamStrategyConfig.ConfigJSON. Provisioning tooling (admin SPA,
// `limen create-upstream` CLI) calls this once per upstream.
func EncodeConfig(tenantID int64, cfg Config) (crypto.SecretField, error) {
	if cfg.IsZero() {
		return crypto.SecretField{}, errors.New("mcpspec: empty config")
	}
	payload, err := json.Marshal(cfg)
	if err != nil {
		return crypto.SecretField{}, fmt.Errorf("mcpspec: marshal config: %w", err)
	}
	sf := crypto.NewSecret(payload)
	sf.SetAAD(strconv.FormatInt(tenantID, 10), "", kindStrategyConfig)
	return sf, nil
}

// loadConfig fetches and decrypts the static UpstreamStrategyConfig for
// the upstream. Returns a zero Config (no error) when no row exists.
func (s *Strategy) loadConfig(ctx context.Context, tenantID, upstreamID int64) (Config, error) {
	var zero Config
	tenantStr := strconv.FormatInt(tenantID, 10)
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenantID))
	if err != nil {
		return zero, fmt.Errorf("mcpspec: open session: %w", err)
	}
	var row storage.UpstreamStrategyConfig
	err = tx.Where("upstream_id = ?", upstreamID).First(&row).Error
	if commitErr := commit(); commitErr != nil && err == nil {
		err = commitErr
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return zero, nil
	}
	if err != nil {
		return zero, fmt.Errorf("mcpspec: load config: %w", err)
	}
	if row.ConfigJSON.IsZero() {
		return zero, nil
	}
	if err := row.ConfigJSON.Decrypt(tenantStr, "", kindStrategyConfig); err != nil {
		return zero, fmt.Errorf("mcpspec: decrypt config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(row.ConfigJSON.Bytes(), &cfg); err != nil {
		return zero, fmt.Errorf("mcpspec: parse config: %w", err)
	}
	return cfg, nil
}
