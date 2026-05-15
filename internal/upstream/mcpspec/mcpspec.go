// Package mcpspec implements the "mcp_spec" upstream strategy: discover
// the upstream's OAuth Authorization Server via the MCP-mandated Protected
// Resource Metadata document, register a client via RFC 7591 Dynamic
// Client Registration, drive the per-user authorization code flow with
// PKCE (S256), and persist + rotate access/refresh tokens.
//
// All discovery happens on-demand; results are cached in-memory keyed on
// upstream ID. Token refresh uses an upstream-scoped singleflight so
// concurrent requests for the same link don't trigger duplicate refreshes.
package mcpspec

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/upstream"
	"github.com/belphemur/limen/internal/upstream/oauthstate"
)

const (
	kindAccessToken    = "upstream.access_token"
	kindRefreshToken   = "upstream.refresh_token"
	defaultHTTPTimeout = 30 * time.Second
)

// invalidGrant is the OAuth-spec error code that means the refresh token
// has been revoked / expired. The strategy treats it as a re-link signal.
const errInvalidGrant = "invalid_grant"

// Options configures the strategy.
type Options struct {
	// HTTPClient is used for all outbound calls (discovery, DCR, token
	// endpoints). Phase 10 replaces this with the resilient client.
	HTTPClient *http.Client
	// RedirectURL is the absolute URL the upstream AS redirects back to
	// after the user authorizes. Must be registered with the AS via DCR.
	// Shape: https://<gateway>/t/{tenant_public_id}/upstream/{name}/callback
	RedirectURLFn func(tenantPublic, upstreamName string) string
	// ProactiveWindow is the "refresh if expiring within X" threshold.
	ProactiveWindow time.Duration
	// SoftwareID / SoftwareVersion advertised in DCR. Optional.
	SoftwareID      string
	SoftwareVersion string
}

// Strategy implements upstream.Strategy for StrategyMCPSpec.
type Strategy struct {
	store   *storage.Store
	cipher  *crypto.Cipher
	state   *oauthstate.Store
	http    *http.Client
	redirFn func(tenantPublic, upstreamName string) string
	proWin  time.Duration
	swID    string
	swVer   string

	discMu sync.RWMutex
	disc   map[int64]*discoveryEntry

	sf singleflight.Group
}

type discoveryEntry struct {
	prm     *prmDoc
	as      *asMetadata
	fetched time.Time
}

// New builds the strategy.
func New(store *storage.Store, cipher *crypto.Cipher, state *oauthstate.Store, opts Options) (*Strategy, error) {
	if store == nil || cipher == nil || state == nil {
		return nil, errors.New("mcpspec: store, cipher, state are required")
	}
	if opts.RedirectURLFn == nil {
		return nil, errors.New("mcpspec: RedirectURLFn is required")
	}
	c := opts.HTTPClient
	if c == nil {
		c = &http.Client{Timeout: defaultHTTPTimeout}
	}
	pw := opts.ProactiveWindow
	if pw <= 0 {
		pw = 60 * time.Second
	}
	return &Strategy{
		store:   store,
		cipher:  cipher,
		state:   state,
		http:    c,
		redirFn: opts.RedirectURLFn,
		proWin:  pw,
		swID:    opts.SoftwareID,
		swVer:   opts.SoftwareVersion,
		disc:    make(map[int64]*discoveryEntry),
	}, nil
}

// Type implements upstream.Strategy.
func (s *Strategy) Type() upstream.StrategyType { return upstream.StrategyMCPSpec }

// RequiresLink reports that mcp_spec upstreams need a per-user link.
func (s *Strategy) RequiresLink() bool { return true }

// --- Discovery ---------------------------------------------------------

type prmDoc struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
}

type asMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	ScopesSupported                   []string `json:"scopes_supported"`
}

func (s *Strategy) discover(ctx context.Context, up *storage.Upstream) (*prmDoc, *asMetadata, error) {
	s.discMu.RLock()
	if e, ok := s.disc[up.ID]; ok {
		s.discMu.RUnlock()
		return e.prm, e.as, nil
	}
	s.discMu.RUnlock()

	prm, err := s.fetchPRM(ctx, up.McpServerURL)
	if err != nil {
		return nil, nil, err
	}
	if len(prm.AuthorizationServers) == 0 {
		return nil, nil, fmt.Errorf("mcpspec: PRM at %s lists no authorization_servers", up.McpServerURL)
	}
	issuer := prm.AuthorizationServers[0]
	as, err := s.fetchASMetadata(ctx, issuer)
	if err != nil {
		return nil, nil, err
	}
	if as.AuthorizationEndpoint == "" || as.TokenEndpoint == "" {
		return nil, nil, fmt.Errorf("mcpspec: AS %s missing endpoints", issuer)
	}
	if len(as.CodeChallengeMethodsSupported) > 0 && !sliceContains(as.CodeChallengeMethodsSupported, "S256") {
		return nil, nil, fmt.Errorf("mcpspec: AS %s does not support PKCE S256", issuer)
	}

	s.discMu.Lock()
	s.disc[up.ID] = &discoveryEntry{prm: prm, as: as, fetched: time.Now()}
	s.discMu.Unlock()
	return prm, as, nil
}

func (s *Strategy) fetchPRM(ctx context.Context, mcpURL string) (*prmDoc, error) {
	u, err := url.Parse(mcpURL)
	if err != nil {
		return nil, fmt.Errorf("mcpspec: parse mcp url: %w", err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/.well-known/oauth-protected-resource"
	u.RawQuery = ""
	return fetchJSON[prmDoc](ctx, s.http, u.String())
}

func (s *Strategy) fetchASMetadata(ctx context.Context, issuer string) (*asMetadata, error) {
	u, err := url.Parse(issuer)
	if err != nil {
		return nil, fmt.Errorf("mcpspec: parse issuer: %w", err)
	}
	base := strings.TrimRight(u.Path, "/")
	candidates := []string{
		"/.well-known/oauth-authorization-server" + base,
		base + "/.well-known/oauth-authorization-server",
		"/.well-known/openid-configuration" + base,
		base + "/.well-known/openid-configuration",
	}
	var lastErr error
	for _, p := range candidates {
		c := *u
		c.Path = p
		c.RawQuery = ""
		md, err := fetchJSON[asMetadata](ctx, s.http, c.String())
		if err == nil {
			return md, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("mcpspec: AS metadata not found for %s: %w", issuer, lastErr)
}

func fetchJSON[T any](ctx context.Context, hc *http.Client, urlStr string) (*T, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("GET %s: %s", urlStr, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("GET %s: parse json: %w", urlStr, err)
	}
	return &out, nil
}

func sliceContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// --- Provision (DCR) ---------------------------------------------------

// Provision performs Dynamic Client Registration against the upstream AS
// and persists the result. Idempotent: if an UpstreamRegistration row
// already exists for the upstream, Provision returns nil.
func (s *Strategy) Provision(ctx context.Context, lctx upstream.LinkContext) error {
	if lctx.Tenant == nil || lctx.Upstream == nil {
		return errors.New("mcpspec: tenant/upstream missing")
	}
	tenantStr := strconv.FormatInt(lctx.Tenant.ID, 10)

	// Idempotency check.
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, lctx.Tenant.ID))
	if err != nil {
		return fmt.Errorf("mcpspec: open session: %w", err)
	}
	var existing storage.UpstreamRegistration
	existing.ClientSecret.SetAAD(tenantStr, "", "upstream.dcr.client_secret")
	existing.RegistrationAccessToken.SetAAD(tenantStr, "", "upstream.dcr.registration_access_token")
	if err := tx.Where("upstream_id = ?", lctx.Upstream.ID).First(&existing).Error; err == nil {
		_ = commit()
		return nil
	}
	if err := commit(); err != nil {
		return err
	}

	prm, as, err := s.discover(ctx, lctx.Upstream)
	if err != nil {
		return err
	}
	if as.RegistrationEndpoint == "" {
		return fmt.Errorf("mcpspec: AS %s does not advertise registration_endpoint", as.Issuer)
	}

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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, as.RegistrationEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("mcpspec: DCR POST %s: %w", as.RegistrationEndpoint, err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("mcpspec: DCR %s: %s: %s", as.RegistrationEndpoint, resp.Status, string(rb))
	}
	var dcrResp struct {
		ClientID                string `json:"client_id"`
		ClientSecret            string `json:"client_secret"`
		RegistrationAccessToken string `json:"registration_access_token"`
		RegistrationClientURI   string `json:"registration_client_uri"`
	}
	if err := json.Unmarshal(rb, &dcrResp); err != nil {
		return fmt.Errorf("mcpspec: parse DCR response: %w", err)
	}
	if dcrResp.ClientID == "" {
		return errors.New("mcpspec: DCR response missing client_id")
	}

	cs := crypto.NewSecret([]byte(dcrResp.ClientSecret))
	cs.SetAAD(tenantStr, "", "upstream.dcr.client_secret")
	rat := crypto.NewSecret([]byte(dcrResp.RegistrationAccessToken))
	rat.SetAAD(tenantStr, "", "upstream.dcr.registration_access_token")

	row := &storage.UpstreamRegistration{
		TenantID:                lctx.Tenant.ID,
		UpstreamID:              lctx.Upstream.ID,
		Issuer:                  as.Issuer,
		ClientID:                dcrResp.ClientID,
		ClientSecret:            cs,
		RegistrationAccessToken: rat,
		RegistrationClientURI:   dcrResp.RegistrationClientURI,
		ResourceURI:             prm.Resource,
	}
	tx2, commit2, err := s.store.Session(storage.WithTenant(ctx, lctx.Tenant.ID))
	if err != nil {
		return err
	}
	if err := tx2.Create(row).Error; err != nil {
		_ = commit2()
		return fmt.Errorf("mcpspec: persist registration: %w", err)
	}
	return commit2()
}

// --- StartLink ---------------------------------------------------------

func (s *Strategy) StartLink(ctx context.Context, lctx upstream.LinkContext) (upstream.StartLinkResult, error) {
	if lctx.Tenant == nil || lctx.User == nil || lctx.Upstream == nil {
		return upstream.StartLinkResult{}, errors.New("mcpspec: tenant/user/upstream missing")
	}
	reg, err := s.loadRegistration(ctx, lctx.Tenant.ID, lctx.Upstream.ID)
	if err != nil {
		return upstream.StartLinkResult{}, err
	}
	_, as, err := s.discover(ctx, lctx.Upstream)
	if err != nil {
		return upstream.StartLinkResult{}, err
	}

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
	q.Set("redirect_uri", s.redirFn(lctx.Tenant.PublicID, lctx.Upstream.Name))
	q.Set("state", stateVal)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if reg.ResourceURI != "" {
		q.Set("resource", reg.ResourceURI)
	}
	authURL := as.AuthorizationEndpoint
	if strings.Contains(authURL, "?") {
		authURL += "&" + q.Encode()
	} else {
		authURL += "?" + q.Encode()
	}
	return upstream.StartLinkResult{RedirectURL: authURL}, nil
}

// --- FinishLink --------------------------------------------------------

func (s *Strategy) FinishLink(ctx context.Context, lctx upstream.LinkContext, callbackQuery string) error {
	if lctx.Tenant == nil || lctx.User == nil || lctx.Upstream == nil {
		return errors.New("mcpspec: tenant/user/upstream missing")
	}
	vals, err := url.ParseQuery(callbackQuery)
	if err != nil {
		return fmt.Errorf("mcpspec: parse callback query: %w", err)
	}
	if errCode := vals.Get("error"); errCode != "" {
		return fmt.Errorf("mcpspec: AS returned error: %s: %s", errCode, vals.Get("error_description"))
	}
	code := vals.Get("code")
	stateVal := vals.Get("state")
	if code == "" || stateVal == "" {
		return errors.New("mcpspec: callback missing code or state")
	}
	env, err := s.state.Consume(ctx, stateVal, lctx.Tenant.ID, lctx.User.ID)
	if err != nil {
		return fmt.Errorf("mcpspec: consume state: %w", err)
	}
	if env.UpstreamID != lctx.Upstream.ID {
		return errors.New("mcpspec: state upstream mismatch")
	}

	reg, err := s.loadRegistration(ctx, lctx.Tenant.ID, lctx.Upstream.ID)
	if err != nil {
		return err
	}
	_, as, err := s.discover(ctx, lctx.Upstream)
	if err != nil {
		return err
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", s.redirFn(lctx.Tenant.PublicID, lctx.Upstream.Name))
	form.Set("code_verifier", env.PKCEVerifier)
	if reg.ResourceURI != "" {
		form.Set("resource", reg.ResourceURI)
	}
	tok, _, err := s.tokenRequest(ctx, as.TokenEndpoint, reg, form)
	if err != nil {
		return err
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
		return err
	}
	var existing storage.UpstreamLink
	err = tx.Where("tenant_id = ? AND user_id = ? AND upstream_id = ?", lctx.Tenant.ID, lctx.User.ID, lctx.Upstream.ID).
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
			return fmt.Errorf("mcpspec: create link: %w", createErr)
		}
		return commit()
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
		return fmt.Errorf("mcpspec: update link: %w", updErr)
	}
	return commit()
}

// --- Headers / Refresh -------------------------------------------------

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

func (s *Strategy) Headers(ctx context.Context, lctx upstream.LinkContext) (map[string]string, error) {
	link, err := s.ensureFresh(ctx, lctx, false)
	if err != nil {
		return nil, err
	}
	return s.headersFromLink(link), nil
}

func (s *Strategy) HeadersForceRefresh(ctx context.Context, lctx upstream.LinkContext) (map[string]string, error) {
	link, err := s.ensureFresh(ctx, lctx, true)
	if err != nil {
		return nil, err
	}
	return s.headersFromLink(link), nil
}

func (s *Strategy) headersFromLink(link *storage.UpstreamLink) map[string]string {
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

// Maintain is the background refresher's entry point. Refreshes the link
// if it's within ProactiveWindow of expiry; returns nil otherwise.
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
	_, as, err := s.discover(ctx, lctx.Upstream)
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
	locked.AccessToken.SetAAD(tenantStr, userStr, kindAccessToken)
	locked.RefreshToken.SetAAD(tenantStr, userStr, kindRefreshToken)
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
		// invalid_grant => flip NeedsRelink.
		if errResp != nil && errResp.Error == errInvalidGrant {
			s.markNeedsRelink(ctx, lctx.Link.ID, tenantStr, userStr)
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
// UPDATE. Best-effort: errors are logged-only because the caller is already
// returning ErrNeedsRelink to upstream code.
func (s *Strategy) markNeedsRelink(ctx context.Context, linkID int64, _ /*tenantStr*/, _ string) {
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

func (s *Strategy) loadRegistration(ctx context.Context, tenantID, upstreamID int64) (*storage.UpstreamRegistration, error) {
	tenantStr := strconv.FormatInt(tenantID, 10)
	tx, commit, err := s.store.Session(storage.WithTenant(ctx, tenantID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = commit() }()

	var row storage.UpstreamRegistration
	row.ClientSecret.SetAAD(tenantStr, "", "upstream.dcr.client_secret")
	row.RegistrationAccessToken.SetAAD(tenantStr, "", "upstream.dcr.registration_access_token")
	if err := tx.Where("upstream_id = ?", upstreamID).First(&row).Error; err != nil {
		return nil, fmt.Errorf("mcpspec: load registration: %w", err)
	}
	return &row, nil
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
