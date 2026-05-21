package mcpspec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/upstream"
)

// Provision establishes (tenant, upstream) → OAuth client credentials,
// using either RFC 7591 Dynamic Client Registration when the AS supports
// it, or a pre-provisioned static client from UpstreamStrategyConfig
// when it doesn't (e.g. GitHub). Idempotent.
func (s *Strategy) Provision(ctx context.Context, lctx upstream.LinkContext) error {
	if lctx.Tenant == nil || lctx.Upstream == nil {
		return errors.New("mcpspec: tenant/upstream missing")
	}
	tenantStr := strconv.FormatInt(lctx.Tenant.ID, 10)

	if exists, err := s.registrationExists(ctx, lctx.Tenant.ID, lctx.Upstream.ID); err != nil {
		return err
	} else if exists {
		return nil
	}

	prm, as, err := s.discover(ctx, lctx)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDiscoveryFailed, err)
	}

	var (
		clientID                string
		clientSecret            string
		registrationAccessToken string
		registrationClientURI   string
	)
	switch {
	case as.RegistrationEndpoint != "":
		dcr, err := s.dynamicallyRegister(ctx, lctx, as.RegistrationEndpoint)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrDCRFailed, err)
		}
		clientID = dcr.ClientID
		clientSecret = dcr.ClientSecret
		registrationAccessToken = dcr.RegistrationAccessToken
		registrationClientURI = dcr.RegistrationClientURI
	default:
		cfg, err := s.loadConfig(ctx, lctx.Tenant.ID, lctx.Upstream.ID)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrDiscoveryFailed, err)
		}
		if !cfg.HasStaticClient() {
			return fmt.Errorf("%w: AS %s, upstream %q", ErrStaticClientRequired, as.Issuer, lctx.Upstream.Name)
		}
		clientID = cfg.ClientID
		clientSecret = cfg.ClientSecret
	}

	row := s.buildRegistrationRow(lctx, tenantStr, prm.primaryResource(), as.Issuer,
		clientID, clientSecret, registrationAccessToken, registrationClientURI)
	if err := s.persistRegistration(ctx, lctx.Tenant.ID, row); err != nil {
		return fmt.Errorf("%w: %v", ErrPersistFailed, err)
	}
	s.invalidate(lctx.Upstream)
	return nil
}

// registrationExists reports whether an UpstreamRegistration row already
// exists for (tenant, upstream). Used as Provision's idempotency check.
func (s *Strategy) registrationExists(ctx context.Context, tenantID, upstreamID int64) (bool, error) {
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenantID))
	if err != nil {
		return false, fmt.Errorf("mcpspec: open session: %w", err)
	}
	var existing storage.UpstreamRegistration
	queryErr := tx.Where("upstream_id = ?", upstreamID).First(&existing).Error
	if commitErr := commit(); commitErr != nil {
		return false, commitErr
	}
	return queryErr == nil, nil
}

type dcrResult struct {
	ClientID                string `json:"client_id"`
	ClientSecret            string `json:"client_secret"`
	RegistrationAccessToken string `json:"registration_access_token"`
	RegistrationClientURI   string `json:"registration_client_uri"`
}

// dynamicallyRegister performs the RFC 7591 POST to the AS's
// registration_endpoint and returns the issued credentials.
func (s *Strategy) dynamicallyRegister(ctx context.Context, lctx upstream.LinkContext, endpoint string) (*dcrResult, error) {
	redirect := s.redirFn(lctx.Tenant.PublicID, lctx.Upstream.Name)
	dcrReq := map[string]any{
		"redirect_uris":              []string{redirect},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "client_secret_basic",
		"client_name":                "Limen Gateway (" + lctx.Tenant.Name + ")",
	}
	if s.swID != "" {
		dcrReq["software_id"] = s.swID
	}
	if s.swVer != "" {
		dcrReq["software_version"] = s.swVer
	}
	body, _ := json.Marshal(dcrReq)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mcpspec: DCR POST %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("mcpspec: DCR %s: %s: %s", endpoint, resp.Status, string(rb))
	}
	var out dcrResult
	if err := json.Unmarshal(rb, &out); err != nil {
		return nil, fmt.Errorf("mcpspec: parse DCR response: %w", err)
	}
	if out.ClientID == "" {
		return nil, errors.New("mcpspec: DCR response missing client_id")
	}
	return &out, nil
}

func (s *Strategy) buildRegistrationRow(lctx upstream.LinkContext, tenantStr, resource, issuer,
	clientID, clientSecret, regAccessToken, regClientURI string) *storage.UpstreamRegistration {
	cs := crypto.NewSecret([]byte(clientSecret))
	cs.SetAAD(tenantStr, "", kindClientSecret)
	rat := crypto.NewSecret([]byte(regAccessToken))
	rat.SetAAD(tenantStr, "", kindRegAccessToken)
	return &storage.UpstreamRegistration{
		TenantID:                lctx.Tenant.ID,
		UpstreamID:              lctx.Upstream.ID,
		Issuer:                  issuer,
		ClientID:                clientID,
		ClientSecret:            cs,
		RegistrationAccessToken: rat,
		RegistrationClientURI:   regClientURI,
		ResourceURI:             resource,
	}
}

func (s *Strategy) persistRegistration(ctx context.Context, tenantID int64, row *storage.UpstreamRegistration) error {
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenantID))
	if err != nil {
		return err
	}
	if err := tx.Create(row).Error; err != nil {
		_ = commit()
		return fmt.Errorf("mcpspec: persist registration: %w", err)
	}
	return commit()
}

// loadRegistration fetches the persisted (tenant, upstream) client.
func (s *Strategy) loadRegistration(ctx context.Context, tenantID, upstreamID int64) (*storage.UpstreamRegistration, error) {
	tenantStr := strconv.FormatInt(tenantID, 10)
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenantID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = commit() }()

	var row storage.UpstreamRegistration
	if err := tx.Where("upstream_id = ?", upstreamID).First(&row).Error; err != nil {
		return nil, fmt.Errorf("mcpspec: load registration: %w", err)
	}
	if err := row.ClientSecret.Decrypt(tenantStr, "", kindClientSecret); err != nil {
		return nil, fmt.Errorf("mcpspec: decrypt client_secret: %w", err)
	}
	if err := row.RegistrationAccessToken.Decrypt(tenantStr, "", kindRegAccessToken); err != nil {
		return nil, fmt.Errorf("mcpspec: decrypt registration_access_token: %w", err)
	}
	return &row, nil
}
