package mcpspec

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/upstream"
	"github.com/belphemur/limen/internal/upstream/oauthstate"
)

// StartLink builds the AS authorization URL with PKCE + state and stores
// the verifier in the oauthstate cache.
func (s *Strategy) StartLink(ctx context.Context, lctx upstream.LinkContext) (upstream.StartLinkResult, error) {
	if lctx.Tenant == nil || lctx.User == nil || lctx.Upstream == nil {
		return upstream.StartLinkResult{}, errors.New("mcpspec: tenant/user/upstream missing")
	}
	reg, err := s.loadRegistration(ctx, lctx.Tenant.ID, lctx.Upstream.ID)
	if err != nil {
		return upstream.StartLinkResult{}, err
	}
	_, as, err := s.discover(ctx, lctx)
	if err != nil {
		return upstream.StartLinkResult{}, err
	}
	cfg, _ := s.tryLoadConfig(ctx, lctx)

	verifier, challenge, err := newPKCE()
	if err != nil {
		return upstream.StartLinkResult{}, err
	}
	nonce, err := randomB64(16)
	if err != nil {
		return upstream.StartLinkResult{}, err
	}
	env := oauthstate.Envelope{
		TenantID:     lctx.Tenant.ID,
		UserID:       lctx.User.ID,
		UpstreamID:   lctx.Upstream.ID,
		ReturnTo:     lctx.ReturnTo,
		PKCEVerifier: verifier,
		Nonce:        nonce,
	}
	stateVal, err := s.state.Put(ctx, env)
	if err != nil {
		return upstream.StartLinkResult{}, fmt.Errorf("mcpspec: put state: %w", err)
	}

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", reg.ClientID)
	q.Set("redirect_uri", s.redirFn(lctx.Tenant.PublicID, lctx.Upstream.Identifier))
	q.Set("state", stateVal)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if reg.ResourceURI != "" {
		q.Set("resource", reg.ResourceURI)
	}
	if len(cfg.Scopes) > 0 {
		q.Set("scope", strings.Join(cfg.Scopes, " "))
	}

	authURL := as.AuthorizationEndpoint
	if strings.Contains(authURL, "?") {
		authURL += "&" + q.Encode()
	} else {
		authURL += "?" + q.Encode()
	}
	return upstream.StartLinkResult{RedirectURL: authURL}, nil
}

// FinishLink exchanges the authorization code for tokens and persists
// the resulting UpstreamLink. The captured ReturnTo from StartLink is
// returned so the callback handler can land the SPA where it started.
func (s *Strategy) FinishLink(ctx context.Context, lctx upstream.LinkContext, callbackQuery string) (string, error) {
	if lctx.Tenant == nil || lctx.User == nil || lctx.Upstream == nil {
		return "", errors.New("mcpspec: tenant/user/upstream missing")
	}
	vals, err := url.ParseQuery(callbackQuery)
	if err != nil {
		return "", fmt.Errorf("mcpspec: parse callback query: %w", err)
	}
	stateVal := vals.Get("state")

	// RFC 6749 §4.1.2.1 — when the AS reports an error it still echoes
	// the original state. Consume the envelope first so we can recover
	// the SPA's return_to and bounce back to the popup-close page with
	// the structured error, instead of dead-ending on an HTTP 400.
	if errCode := vals.Get("error"); errCode != "" {
		authErr := &upstream.AuthorizationError{
			Code:        errCode,
			Description: vals.Get("error_description"),
		}
		if stateVal != "" {
			if env, cerr := s.state.Consume(ctx, stateVal, lctx.Tenant.ID, lctx.User.ID); cerr == nil && env.UpstreamID == lctx.Upstream.ID {
				authErr.ReturnTo = env.ReturnTo
			}
		}
		return "", authErr
	}

	code := vals.Get("code")
	if code == "" || stateVal == "" {
		return "", errors.New("mcpspec: callback missing code or state")
	}
	env, err := s.state.Consume(ctx, stateVal, lctx.Tenant.ID, lctx.User.ID)
	if err != nil {
		return "", fmt.Errorf("mcpspec: consume state: %w", err)
	}
	if env.UpstreamID != lctx.Upstream.ID {
		return "", errors.New("mcpspec: state upstream mismatch")
	}

	reg, err := s.loadRegistration(ctx, lctx.Tenant.ID, lctx.Upstream.ID)
	if err != nil {
		return "", err
	}
	_, as, err := s.discover(ctx, lctx)
	if err != nil {
		return "", err
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", s.redirFn(lctx.Tenant.PublicID, lctx.Upstream.Identifier))
	form.Set("code_verifier", env.PKCEVerifier)
	if reg.ResourceURI != "" {
		form.Set("resource", reg.ResourceURI)
	}
	tok, _, err := s.tokenRequest(ctx, as.TokenEndpoint, reg, form)
	if err != nil {
		return "", err
	}

	tenantStr := strconv.FormatInt(lctx.Tenant.ID, 10)
	userStr := strconv.FormatInt(lctx.User.ID, 10)
	at := crypto.NewSecret([]byte(tok.AccessToken))
	at.SetAAD(tenantStr, userStr, kindAccessToken)
	rt := crypto.NewSecret([]byte(tok.RefreshToken))
	rt.SetAAD(tenantStr, userStr, kindRefreshToken)
	var exp *time.Time
	if tok.ExpiresIn > 0 {
		t := time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
		exp = &t
	}

	tx, commit, err := s.store.Session(storage.WithTenant(ctx, lctx.Tenant.ID))
	if err != nil {
		return "", err
	}
	var existing storage.UpstreamLink
	err = tx.Where("user_id = ? AND upstream_id = ?", lctx.User.ID, lctx.Upstream.ID).
		First(&existing).Error
	if err != nil {
		newLink := storage.UpstreamLink{
			TenantID:     lctx.Tenant.ID,
			UserID:       lctx.User.ID,
			UpstreamID:   lctx.Upstream.ID,
			AccessToken:  at,
			RefreshToken: rt,
			ExpiresAt:    exp,
			Scopes:       tok.Scope,
			ResourceURI:  reg.ResourceURI,
			Enabled:      true,
		}
		if createErr := tx.Create(&newLink).Error; createErr != nil {
			_ = commit()
			return "", fmt.Errorf("mcpspec: create link: %w", createErr)
		}
		if cerr := commit(); cerr != nil {
			return "", cerr
		}
		return env.ReturnTo, nil
	}
	existing.AccessToken = at
	existing.RefreshToken = rt
	existing.ExpiresAt = exp
	existing.Scopes = tok.Scope
	existing.ResourceURI = reg.ResourceURI
	existing.Enabled = true
	existing.NeedsRelink = false
	existing.ConsecutiveFailures = 0
	existing.FirstFailureAt = nil
	existing.LastFailureAt = nil
	existing.LastFailureReason = ""
	existing.AutoDisabledAt = nil
	if updErr := tx.Save(&existing).Error; updErr != nil {
		_ = commit()
		return "", fmt.Errorf("mcpspec: update link: %w", updErr)
	}
	if cerr := commit(); cerr != nil {
		return "", cerr
	}
	return env.ReturnTo, nil
}

// --- PKCE helpers ------------------------------------------------------

func newPKCE() (verifier, challenge string, err error) {
	verifier, err = randomB64(32)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func randomB64(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
