package mcpspec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/upstream"
)

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

type tokenErrResp struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// Headers returns an Authorization: Bearer header for the link, refreshing
// the access token first if it's within ProactiveWindow of expiry.
func (s *Strategy) Headers(ctx context.Context, lctx upstream.LinkContext) (map[string]string, error) {
	link, err := s.ensureFresh(ctx, lctx, false)
	if err != nil {
		return nil, err
	}
	return headersFromLink(link), nil
}

// HeadersForceRefresh refreshes unconditionally before returning headers.
func (s *Strategy) HeadersForceRefresh(ctx context.Context, lctx upstream.LinkContext) (map[string]string, error) {
	link, err := s.ensureFresh(ctx, lctx, true)
	if err != nil {
		return nil, err
	}
	return headersFromLink(link), nil
}

func headersFromLink(link *storage.UpstreamLink) map[string]string {
	return map[string]string{"Authorization": "Bearer " + link.AccessToken.String()}
}

// ensureFresh returns a usable link, refreshing the token if needed
// (force=true) or if it expires inside ProactiveWindow. Concurrent calls
// for the same link coalesce via singleflight.
func (s *Strategy) ensureFresh(ctx context.Context, lctx upstream.LinkContext, force bool) (*storage.UpstreamLink, error) {
	if lctx.Tenant == nil || lctx.User == nil || lctx.Upstream == nil || lctx.Link == nil {
		return nil, errors.New("mcpspec: tenant/user/upstream/link missing")
	}
	if lctx.Link.NeedsRelink {
		return nil, upstream.ErrNeedsRelink
	}
	if !force && lctx.Link.ExpiresAt != nil && time.Until(*lctx.Link.ExpiresAt) > s.proWin {
		return lctx.Link, nil
	}
	key := fmt.Sprintf("refresh:%d", lctx.Link.ID)
	v, err, _ := s.sf.Do(key, func() (any, error) {
		return s.refreshLink(ctx, lctx)
	})
	if err != nil {
		return nil, err
	}
	return v.(*storage.UpstreamLink), nil
}

// Maintain is the background refresher's entry point.
func (s *Strategy) Maintain(ctx context.Context, lctx upstream.LinkContext) error {
	if lctx.Link == nil {
		return nil
	}
	if lctx.Link.NeedsRelink || !lctx.Link.Enabled || lctx.Link.AutoDisabledAt != nil {
		return nil
	}
	if lctx.Link.ExpiresAt == nil || time.Until(*lctx.Link.ExpiresAt) > s.proWin {
		return nil
	}
	_, err := s.ensureFresh(ctx, lctx, false)
	return err
}

// refreshLink runs refresh_token under SELECT FOR UPDATE SKIP LOCKED so
// concurrent processes (separate gateway replicas) don't double-refresh.
func (s *Strategy) refreshLink(ctx context.Context, lctx upstream.LinkContext) (*storage.UpstreamLink, error) {
	reg, err := s.loadRegistration(ctx, lctx.Tenant.ID, lctx.Upstream.ID)
	if err != nil {
		return nil, err
	}
	_, as, err := s.discover(ctx, lctx)
	if err != nil {
		return nil, err
	}

	tenantStr := strconv.FormatInt(lctx.Tenant.ID, 10)
	userStr := strconv.FormatInt(lctx.User.ID, 10)

	tx, commit, err := s.store.Session(storage.WithTenant(ctx, lctx.Tenant.ID))
	if err != nil {
		return nil, err
	}

	var locked storage.UpstreamLink
	if err := tx.Raw(`SELECT * FROM upstream_links WHERE id = ? FOR UPDATE SKIP LOCKED`, lctx.Link.ID).
		Scan(&locked).Error; err != nil {
		_ = commit()
		return nil, fmt.Errorf("mcpspec: select for update: %w", err)
	}
	if locked.ID == 0 {
		// Another worker holds the lock; reload from caller's link.
		_ = commit()
		return lctx.Link, nil
	}
	if err := locked.AccessToken.Decrypt(tenantStr, userStr, kindAccessToken); err != nil {
		_ = commit()
		return nil, fmt.Errorf("mcpspec: decrypt access_token: %w", err)
	}
	if err := locked.RefreshToken.Decrypt(tenantStr, userStr, kindRefreshToken); err != nil {
		_ = commit()
		return nil, fmt.Errorf("mcpspec: decrypt refresh_token: %w", err)
	}
	// Another worker may have already refreshed it.
	if locked.ExpiresAt != nil && time.Until(*locked.ExpiresAt) > s.proWin {
		_ = commit()
		return &locked, nil
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", locked.RefreshToken.String())
	if locked.ResourceURI != "" {
		form.Set("resource", locked.ResourceURI)
	}
	tok, errResp, err := s.tokenRequest(ctx, as.TokenEndpoint, reg, form)
	if err != nil {
		_ = commit()
		if errResp != nil && errResp.Error == errInvalidGrant {
			s.markNeedsRelink(ctx, lctx.Link.ID)
			return nil, upstream.ErrNeedsRelink
		}
		return nil, err
	}

	locked.AccessToken = crypto.NewSecret([]byte(tok.AccessToken))
	locked.AccessToken.SetAAD(tenantStr, userStr, kindAccessToken)
	if tok.RefreshToken != "" {
		locked.RefreshToken = crypto.NewSecret([]byte(tok.RefreshToken))
		locked.RefreshToken.SetAAD(tenantStr, userStr, kindRefreshToken)
	}
	if tok.ExpiresIn > 0 {
		t := time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
		locked.ExpiresAt = &t
	}
	if tok.Scope != "" {
		locked.Scopes = tok.Scope
	}
	locked.ConsecutiveFailures = 0
	locked.FirstFailureAt = nil
	locked.LastFailureAt = nil
	locked.LastFailureReason = ""
	if saveErr := tx.Save(&locked).Error; saveErr != nil {
		_ = commit()
		return nil, fmt.Errorf("mcpspec: persist refreshed token: %w", saveErr)
	}
	if commitErr := commit(); commitErr != nil {
		return nil, commitErr
	}
	return &locked, nil
}

// markNeedsRelink flips needs_relink and stamps last_failure_* in a single
// UPDATE. Best-effort: errors are swallowed because the caller is already
// returning ErrNeedsRelink to upstream code.
func (s *Strategy) markNeedsRelink(ctx context.Context, linkID int64) {
	tx, commit, err := s.store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		return
	}
	_ = tx.Exec(`UPDATE upstream_links
		SET needs_relink = true,
		    last_failure_at = now(),
		    first_failure_at = COALESCE(first_failure_at, now()),
		    last_failure_reason = ?,
		    consecutive_failures = consecutive_failures + 1
		WHERE id = ?`, string(upstream.ReasonInvalidGrant), linkID).Error
	_ = commit()
}

// tokenRequest POSTs to the token endpoint with client_secret_basic auth
// and form-encoded body. Returns (token, errResp, err): errResp is non-nil
// only when the AS returned an RFC 6749 error object.
func (s *Strategy) tokenRequest(ctx context.Context, endpoint string, reg *storage.UpstreamRegistration, form url.Values) (*tokenResp, *tokenErrResp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(reg.ClientID, reg.ClientSecret.String())

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("mcpspec: token endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var er tokenErrResp
		_ = json.Unmarshal(body, &er)
		return nil, &er, fmt.Errorf("mcpspec: token endpoint %s: %s: %s", endpoint, resp.Status, string(body))
	}
	var tok tokenResp
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, nil, fmt.Errorf("mcpspec: parse token response: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, nil, errors.New("mcpspec: token response missing access_token")
	}
	return &tok, nil, nil
}
