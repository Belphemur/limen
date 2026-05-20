package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/zitadel/oidc/v3/pkg/client/rp"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/tenancy"
)

// Cookie names. Both are scoped by Path attribute, never by Domain.
const (
	stateCookieName  = "limen_state"
	portalCookieName = "limen_portal"

	// stateCookiePath is the callback path; the state cookie is only sent
	// back to the callback handler.
	stateCookiePath = "/auth/callback"

	// rolesClaim is the Zitadel project-roles claim. Shape:
	//
	//	{"urn:zitadel:iam:org:project:roles": {
	//	    "<role>": {"<orgID>": "<orgName>"}
	//	}}
	rolesClaim = "urn:zitadel:iam:org:project:roles"

	// orgIDClaim binds a token to a Zitadel organization.
	orgIDClaim = "urn:zitadel:iam:user:resourceowner:id"

	// cookieAADKind binds the portal cookie ciphertext to its purpose.
	cookieAADKind = "portal.oidc.tokens"
)

// OIDCConfig wires the relying-party handlers to a configured Zitadel
// instance. The redirect URI is tenant-agnostic so Zitadel only needs one
// app registration for the whole portal; the tenant public id travels in
// the signed state cookie.
type OIDCConfig struct {
	// Issuer is the Zitadel issuer URL (e.g. https://auth.limen.example.com).
	Issuer string
	// ClientID identifies the Portal app registered in Zitadel.
	ClientID string
	// ClientSecret is empty for a public PKCE client.
	ClientSecret string
	// RedirectURI is the absolute URL for the root callback handler, e.g.
	// https://limen.example.com/auth/callback. This is the default and is
	// always registered as a valid redirect URI.
	RedirectURI string
	// AllowedRedirectURIs lists additional absolute callback URLs the
	// relying party may use. Useful in dev where the portal and admin
	// SPAs run on different vite ports and each needs its own same-origin
	// /auth/callback. The login handler picks the entry matching the
	// request's origin; the matched URI is then carried in the signed
	// state cookie so the callback can pair it back to the same relying
	// party for code exchange. Each entry must be absolute http(s).
	AllowedRedirectURIs []string
	// Scopes requested at /authorize. Must include "openid"; usually also
	// "profile", "email", "offline_access", and the Zitadel-specific scope
	// "urn:zitadel:iam:user:resourceowner" which causes Zitadel to emit the
	// urn:zitadel:iam:user:resourceowner:id claim in the ID token — Limen
	// uses it to enforce that the user's home org matches the tenant in
	// the URL (see docs/security.md “Tenant <-> Zitadel org binding”).
	Scopes []string
	// Secure controls the cookie Secure attribute. Set true in prod.
	Secure bool
	// DefaultReturnPath is appended to /t/{tenant} when no return_to is
	// supplied. Default "/".
	DefaultReturnPath string
}

func (c OIDCConfig) validate() error {
	if strings.TrimSpace(c.Issuer) == "" {
		return errors.New("auth: OIDCConfig.Issuer is required")
	}
	if strings.TrimSpace(c.ClientID) == "" {
		return errors.New("auth: OIDCConfig.ClientID is required")
	}
	if strings.TrimSpace(c.RedirectURI) == "" {
		return errors.New("auth: OIDCConfig.RedirectURI is required")
	}
	for _, u := range c.AllowedRedirectURIs {
		if strings.TrimSpace(u) == "" {
			return errors.New("auth: OIDCConfig.AllowedRedirectURIs contains an empty entry")
		}
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return fmt.Errorf("auth: OIDCConfig.AllowedRedirectURIs entry %q must be an absolute http(s) URL", u)
		}
	}
	if len(c.Scopes) == 0 {
		return errors.New("auth: OIDCConfig.Scopes is required")
	}
	hasOpenID := slices.Contains(c.Scopes, oidc.ScopeOpenID)
	if !hasOpenID {
		return errors.New(`auth: OIDCConfig.Scopes must include "openid"`)
	}
	return nil
}

// OIDC drives the browser portal's OIDC handshake against Zitadel and
// manages the per-tenant portal cookie carrying the resulting tokens.
//
// Limen is a relying party: Zitadel renders the login UI, enforces MFA,
// owns the user store, and issues the tokens. Limen never sees passwords.
type OIDC struct {
	cfg OIDCConfig
	// parties maps a redirect-URI key (scheme+host[:port]) to the
	// relying party configured for that callback URL. The default
	// RedirectURI is always present and indexed by defaultKey; any
	// additional AllowedRedirectURIs add further entries.
	parties      map[string]rp.RelyingParty
	defaultKey   string
	defaultParty rp.RelyingParty
	cipher       *crypto.Cipher
	signer       *StateSigner
	logger       *zap.Logger
	timeNow      func() time.Time
}

// NewOIDC discovers the Zitadel issuer and builds the RP. The discovery
// call hits the network, so callers should pass a context with a sane
// timeout.
func NewOIDC(ctx context.Context, cfg OIDCConfig, cipher *crypto.Cipher, signer *StateSigner, logger *zap.Logger) (*OIDC, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cipher == nil {
		return nil, errors.New("auth: OIDC cipher is required")
	}
	if signer == nil {
		return nil, errors.New("auth: OIDC state signer is required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	if cfg.DefaultReturnPath == "" {
		cfg.DefaultReturnPath = "/"
	}
	parties := make(map[string]rp.RelyingParty, 1+len(cfg.AllowedRedirectURIs))
	defaultKey, defaultParty, err := buildParty(ctx, cfg, cfg.RedirectURI)
	if err != nil {
		return nil, err
	}
	parties[defaultKey] = defaultParty
	for _, uri := range cfg.AllowedRedirectURIs {
		if uri == cfg.RedirectURI {
			continue
		}
		k, party, perr := buildParty(ctx, cfg, uri)
		if perr != nil {
			return nil, perr
		}
		if _, dup := parties[k]; dup {
			continue
		}
		parties[k] = party
	}
	return &OIDC{
		cfg:          cfg,
		parties:      parties,
		defaultKey:   defaultKey,
		defaultParty: defaultParty,
		cipher:       cipher,
		signer:       signer,
		logger:       logger,
		timeNow:      time.Now,
	}, nil
}

// buildParty discovers the issuer and builds a relying party scoped to
// redirectURI. The returned key is the scheme+host[:port] derived from
// redirectURI and is used to route incoming requests to the matching
// party.
func buildParty(ctx context.Context, cfg OIDCConfig, redirectURI string) (string, rp.RelyingParty, error) {
	u, err := url.Parse(redirectURI)
	if err != nil || !u.IsAbs() {
		return "", nil, fmt.Errorf("auth: invalid redirect uri %q", redirectURI)
	}
	party, err := rp.NewRelyingPartyOIDC(ctx, cfg.Issuer, cfg.ClientID, cfg.ClientSecret, redirectURI, cfg.Scopes)
	if err != nil {
		return "", nil, fmt.Errorf("auth: build relying party for %q: %w", redirectURI, err)
	}
	return originKey(u.Scheme, u.Host), party, nil
}

// originKey is the canonical map key for a redirect URI: lowercased
// scheme + "://" + host[:port].
func originKey(scheme, host string) string {
	return strings.ToLower(scheme) + "://" + strings.ToLower(host)
}

// requestOriginKey derives the originKey of the inbound request,
// honouring X-Forwarded-Proto and X-Forwarded-Host when present so
// reverse-proxied traffic resolves to the public origin the SPA sees.
func requestOriginKey(r *http.Request) string {
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return originKey(scheme, host)
}

// partyForRequest returns the relying party whose redirect URI matches
// the request origin, falling back to the default. The returned key
// must be persisted in state so the callback can pair the same party.
func (o *OIDC) partyForRequest(r *http.Request) (string, rp.RelyingParty) {
	k := requestOriginKey(r)
	if p, ok := o.parties[k]; ok {
		return k, p
	}
	return o.defaultKey, o.defaultParty
}

// partyForKey returns the party indexed by k, falling back to the
// default when k is empty or unknown.
func (o *OIDC) partyForKey(k string) rp.RelyingParty {
	if k == "" {
		return o.defaultParty
	}
	if p, ok := o.parties[k]; ok {
		return p
	}
	return o.defaultParty
}

// LoginHandler is mounted at /t/{tenant}/auth/login behind RequireTenant
// AND at /auth/login at the root. The tenant-scoped mount picks the
// tenant out of the URL; the root mount mints an empty-tenant state so
// the callback resolves the tenant from the user's Zitadel home-org id
// claim instead.
func (o *OIDC) LoginHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var tenantPublicID string
		if t, ok := tenancy.TenantFromContext(r.Context()); ok {
			tenantPublicID = t.PublicID
		}

		key, party := o.partyForRequest(r)
		returnTo := safeReturnTo(r.URL.Query().Get("return_to"))
		st, err := NewState(tenantPublicID, returnTo)
		if err != nil {
			o.logger.Error("state mint", zap.Error(err))
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		st.RedirectKey = key
		signed, err := o.signer.Sign(st)
		if err != nil {
			o.logger.Error("state sign", zap.Error(err))
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     stateCookieName,
			Value:    signed,
			Path:     stateCookiePath,
			HttpOnly: true,
			Secure:   o.cfg.Secure,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(stateTTL / time.Second),
		})
		http.Redirect(w, r, rp.AuthURL(signed, party), http.StatusFound)
	}
}

// CallbackHandler is mounted at /auth/callback (root, single redirect URI).
// It verifies the signed state cookie, exchanges the code for tokens,
// resolves the tenant (either from state.Tenant when the user started
// the flow at /t/{tenant}/auth/login, or from the token's Zitadel home-
// org claim when the user started at the tenant-agnostic /auth/login),
// confirms the token's Zitadel org matches the tenant, upserts the local
// User row, and sets the per-tenant portal cookie.
//
// resolveTenant is called with whichever of publicID/orgID was known up
// front. It must return the resolved tenant int64 ID, the canonical
// public id, and the org id that the token MUST match. When publicID is
// empty, look up by orgID; when both are provided, look up by publicID
// and return that tenant's bound orgID for the caller's mismatch check.
func (o *OIDC) CallbackHandler(
	resolveTenant func(ctx context.Context, publicID, orgID string) (tenantID int64, resolvedPublicID, expectedOrgID string, err error),
	upsertUser func(ctx context.Context, tenantID int64, sub, email, name string) error,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		queryState := r.URL.Query().Get("state")
		if queryState == "" {
			http.Error(w, "missing state", http.StatusBadRequest)
			return
		}
		c, err := r.Cookie(stateCookieName)
		if err != nil || c.Value != queryState {
			http.Error(w, "invalid state", http.StatusBadRequest)
			return
		}
		clearCookie(w, stateCookieName, stateCookiePath, o.cfg.Secure)
		st, err := o.signer.Verify(c.Value)
		if err != nil {
			http.Error(w, "invalid state", http.StatusBadRequest)
			return
		}
		if errMsg := r.URL.Query().Get("error"); errMsg != "" {
			o.logger.Info("authorize error", zap.String("tenant", st.Tenant), zap.String("err", errMsg))
			http.Error(w, "authorization denied", http.StatusForbidden)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}

		tokens, err := rp.CodeExchange[*oidc.IDTokenClaims](r.Context(), code, o.partyForKey(st.RedirectKey))
		if err != nil {
			o.logger.Warn("code exchange failed", zap.String("tenant", st.Tenant), zap.Error(err))
			http.Error(w, "authentication failed", http.StatusBadGateway)
			return
		}
		gotOrgID, _ := tokens.IDTokenClaims.Claims[orgIDClaim].(string)
		if gotOrgID == "" {
			o.logger.Warn("id token missing org id claim", zap.String("state_tenant", st.Tenant))
			http.Error(w, "access denied", http.StatusForbidden)
			return
		}
		tenantID, resolvedPublicID, wantOrgID, err := resolveTenant(r.Context(), st.Tenant, gotOrgID)
		if err != nil {
			o.logger.Warn("tenant resolve in callback",
				zap.String("state_tenant", st.Tenant),
				zap.String("org_id", gotOrgID),
				zap.Error(err))
			http.Error(w, "access denied", http.StatusForbidden)
			return
		}
		if gotOrgID != wantOrgID {
			o.logger.Warn("org mismatch", zap.String("tenant", resolvedPublicID), zap.String("want", wantOrgID), zap.String("got", gotOrgID))
			http.Error(w, "access denied", http.StatusForbidden)
			return
		}
		claims := tokens.IDTokenClaims
		sub := claims.GetSubject()
		email := claims.Email
		name := claims.Name
		if name == "" {
			name = strings.TrimSpace(claims.GivenName + " " + claims.FamilyName)
		}
		if upsertUser != nil {
			if err := upsertUser(r.Context(), tenantID, sub, email, name); err != nil {
				o.logger.Error("user upsert", zap.String("tenant", resolvedPublicID), zap.Error(err))
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		}
		if err := o.writePortalCookie(w, resolvedPublicID, tokens.IDToken, tokens.RefreshToken, claims.GetExpiration()); err != nil {
			o.logger.Error("portal cookie seal", zap.Error(err))
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/t/"+resolvedPublicID+st.ReturnTo, http.StatusFound)
	}
}

// LogoutHandler is mounted at /t/{tenant}/auth/logout behind RequireTenant.
// It clears the portal cookie and redirects the BROWSER to Zitadel's
// end-session endpoint with id_token_hint + post_logout_redirect_uri so
// Zitadel clears its own SSO cookie and bounces the user back. We must
// build the URL ourselves and 302 the browser — rp.EndSession() POSTs
// server-side, which would end the session at Zitadel but leave the
// browser's IdP cookie in place.
func (o *OIDC) LogoutHandler(postLogoutRedirectURI string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t := tenancy.MustTenant(r.Context())
		var idTokenHint string
		if tok, err := o.readPortalCookie(r, t.PublicID); err == nil {
			idTokenHint = tok.IDToken
		}
		clearCookie(w, portalCookieName, "/t/"+t.PublicID, o.cfg.Secure)
		// Defense-in-depth: a stale state cookie from an aborted login
		// flow would otherwise survive logout. It's path-scoped to
		// /auth/callback so this is the only place we can wipe it.
		clearCookie(w, stateCookieName, stateCookiePath, o.cfg.Secure)

		endpoint := o.defaultParty.GetEndSessionEndpoint()
		if endpoint == "" {
			o.logger.Warn("OP discovery has no end_session_endpoint, falling back to local redirect",
				zap.String("tenant", t.PublicID))
			http.Redirect(w, r, postLogoutRedirectURI, http.StatusFound)
			return
		}
		q := url.Values{}
		q.Set("client_id", o.defaultParty.OAuthConfig().ClientID)
		if idTokenHint != "" {
			q.Set("id_token_hint", idTokenHint)
		}
		if postLogoutRedirectURI != "" {
			q.Set("post_logout_redirect_uri", postLogoutRedirectURI)
		}
		sep := "?"
		if strings.Contains(endpoint, "?") {
			sep = "&"
		}
		target := endpoint + sep + q.Encode()
		o.logger.Info("redirecting browser to zitadel end_session",
			zap.String("tenant", t.PublicID),
			zap.String("end_session_endpoint", endpoint))
		http.Redirect(w, r, target, http.StatusFound)
	}
}

// RequireSession decrypts the portal cookie, verifies the ID token, and
// refreshes it transparently when expired. On any failure it redirects to
// /t/{tenant}/auth/login?return_to=<current>. Mount under RequireTenant.
func (o *OIDC) RequireSession() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t := tenancy.MustTenant(r.Context())
			loginURL := "/t/" + t.PublicID + "/auth/login?return_to=" + url.QueryEscape(trimTenantPrefix(r.URL.RequestURI(), t.PublicID))

			tok, err := o.readPortalCookie(r, t.PublicID)
			if err != nil {
				http.Redirect(w, r, loginURL, http.StatusFound)
				return
			}

			claims, err := rp.VerifyIDToken[*oidc.IDTokenClaims](r.Context(), tok.IDToken, o.defaultParty.IDTokenVerifier())
			if err != nil {
				if tok.RefreshToken == "" {
					o.logger.Debug("id token invalid, no refresh", zap.String("tenant", t.PublicID), zap.Error(err))
					clearCookie(w, portalCookieName, "/t/"+t.PublicID, o.cfg.Secure)
					http.Redirect(w, r, loginURL, http.StatusFound)
					return
				}
				refreshed, rerr := rp.RefreshTokens[*oidc.IDTokenClaims](r.Context(), o.defaultParty, tok.RefreshToken, "", "")
				if rerr != nil {
					o.logger.Info("refresh failed", zap.String("tenant", t.PublicID), zap.Error(rerr))
					clearCookie(w, portalCookieName, "/t/"+t.PublicID, o.cfg.Secure)
					http.Redirect(w, r, loginURL, http.StatusFound)
					return
				}
				newRefresh := refreshed.RefreshToken
				if newRefresh == "" {
					newRefresh = tok.RefreshToken
				}
				if werr := o.writePortalCookie(w, t.PublicID, refreshed.IDToken, newRefresh, refreshed.IDTokenClaims.GetExpiration()); werr != nil {
					o.logger.Error("rewrite cookie after refresh", zap.Error(werr))
					http.Error(w, "internal error", http.StatusInternalServerError)
					return
				}
				claims = refreshed.IDTokenClaims
			}

			ctx := WithClaims(r.Context(), claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireRole gates a handler to users whose Zitadel project-roles claim
// intersects want. Empty want rejects all requests.
func (o *OIDC) RequireRole(want ...string) func(http.Handler) http.Handler {
	wantSet := make(map[string]struct{}, len(want))
	for _, r := range want {
		wantSet[r] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := ClaimsFromContext(r.Context())
			if !ok {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			for _, role := range ExtractRoles(claims) {
				if _, hit := wantSet[role]; hit {
					next.ServeHTTP(w, r)
					return
				}
			}
			http.Error(w, "forbidden", http.StatusForbidden)
		})
	}
}

// portalCookieValue is the JSON shape sealed inside the portal cookie.
type portalCookieValue struct {
	IDToken      string    `json:"id"`
	RefreshToken string    `json:"r,omitempty"`
	ExpiresAt    time.Time `json:"e"`
}

func (o *OIDC) writePortalCookie(w http.ResponseWriter, tenant, idToken, refreshToken string, idExp time.Time) error {
	cookie, err := o.buildPortalCookie(tenant, idToken, refreshToken, idExp)
	if err != nil {
		return err
	}
	http.SetCookie(w, cookie)
	return nil
}

// buildPortalCookie seals the (id, refresh, exp) triple into the portal
// cookie. Factored out of writePortalCookie so the Connect-RPC portal
// interceptor can attach a refreshed cookie to its response without
// owning an http.ResponseWriter.
func (o *OIDC) buildPortalCookie(tenant, idToken, refreshToken string, idExp time.Time) (*http.Cookie, error) {
	payload, err := json.Marshal(portalCookieValue{
		IDToken:      idToken,
		RefreshToken: refreshToken,
		ExpiresAt:    idExp,
	})
	if err != nil {
		return nil, err
	}
	sealed, err := o.cipher.Encrypt(payload, crypto.AAD{TenantID: tenant, Kind: cookieAADKind})
	if err != nil {
		return nil, err
	}
	// MaxAge: keep until the refresh token would have expired. We don't
	// know the refresh TTL from claims, so use a generous default — an
	// invalid cookie just redirects to login.
	const maxAge = 30 * 24 * 3600
	return &http.Cookie{
		Name:     portalCookieName,
		Value:    base64.RawURLEncoding.EncodeToString(sealed),
		Path:     "/t/" + tenant,
		HttpOnly: true,
		Secure:   o.cfg.Secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	}, nil
}

// ResolvePortalSession is the Connect-RPC interceptor entry point: it
// decrypts the portal cookie carried in header for the given tenant
// public id, verifies the ID token, and transparently refreshes it on
// expiry. The returned setCookie, when non-nil, MUST be attached to the
// response so the refreshed tokens persist on the client.
func (o *OIDC) ResolvePortalSession(ctx context.Context, header http.Header, tenant string) (*oidc.IDTokenClaims, *http.Cookie, error) {
	r := &http.Request{Header: header}
	tok, err := o.readPortalCookie(r, tenant)
	if err != nil {
		return nil, nil, err
	}
	claims, err := rp.VerifyIDToken[*oidc.IDTokenClaims](ctx, tok.IDToken, o.defaultParty.IDTokenVerifier())
	if err == nil {
		return claims, nil, nil
	}
	if tok.RefreshToken == "" {
		return nil, nil, fmt.Errorf("auth: id token invalid, no refresh: %w", err)
	}
	refreshed, rerr := rp.RefreshTokens[*oidc.IDTokenClaims](ctx, o.defaultParty, tok.RefreshToken, "", "")
	if rerr != nil {
		return nil, nil, fmt.Errorf("auth: refresh failed: %w", rerr)
	}
	newRefresh := refreshed.RefreshToken
	if newRefresh == "" {
		newRefresh = tok.RefreshToken
	}
	setCookie, err := o.buildPortalCookie(tenant, refreshed.IDToken, newRefresh, refreshed.IDTokenClaims.GetExpiration())
	if err != nil {
		return nil, nil, fmt.Errorf("auth: rebuild cookie after refresh: %w", err)
	}
	return refreshed.IDTokenClaims, setCookie, nil
}

func (o *OIDC) readPortalCookie(r *http.Request, tenant string) (portalCookieValue, error) {
	var zero portalCookieValue
	c, err := r.Cookie(portalCookieName)
	if err != nil {
		return zero, err
	}
	sealed, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return zero, fmt.Errorf("auth: portal cookie decode: %w", err)
	}
	plain, err := o.cipher.Decrypt(sealed, crypto.AAD{TenantID: tenant, Kind: cookieAADKind})
	if err != nil {
		return zero, fmt.Errorf("auth: portal cookie open: %w", err)
	}
	var v portalCookieValue
	if err := json.Unmarshal(plain, &v); err != nil {
		return zero, fmt.Errorf("auth: portal cookie parse: %w", err)
	}
	return v, nil
}

// ExtractRoles parses the Zitadel project-roles claim into a flat slice of
// role keys. The claim shape is map[role]map[orgID]orgName; we drop the
// inner detail because Limen scopes by URL tenant, not by role-org pair.
func ExtractRoles(claims *oidc.IDTokenClaims) []string {
	if claims == nil || claims.Claims == nil {
		return nil
	}
	raw, ok := claims.Claims[rolesClaim].(map[string]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for role := range raw {
		out = append(out, role)
	}
	return out
}

// ctx plumbing for the parsed claims.

type ctxKey int

const ctxKeyClaims ctxKey = iota + 1

// WithClaims pins the verified ID token claims on ctx.
func WithClaims(ctx context.Context, c *oidc.IDTokenClaims) context.Context {
	return context.WithValue(ctx, ctxKeyClaims, c)
}

// ClaimsFromContext returns the claims bound by RequireSession.
func ClaimsFromContext(ctx context.Context) (*oidc.IDTokenClaims, bool) {
	c, ok := ctx.Value(ctxKeyClaims).(*oidc.IDTokenClaims)
	return c, ok && c != nil
}

// MustClaims returns the claims or panics. Use only behind RequireSession.
func MustClaims(ctx context.Context) *oidc.IDTokenClaims {
	c, ok := ClaimsFromContext(ctx)
	if !ok {
		panic("auth: claims not in context — missing RequireSession middleware")
	}
	return c
}

func clearCookie(w http.ResponseWriter, name, path string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     path,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// safeReturnTo normalizes user-supplied return_to to a path that lives
// under /t/{tenant}/, defaulting to "/". We reject anything that
// would let the redirect escape the tenant subtree.
func safeReturnTo(raw string) string {
	if raw == "" {
		return "/"
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return "/"
	}
	return raw
}

// trimTenantPrefix returns the request path minus the "/t/{tenant}" prefix.
// Used to build a return_to that survives the round-trip through the
// signed state cookie.
func trimTenantPrefix(reqURI, tenant string) string {
	prefix := "/t/" + tenant
	switch {
	case reqURI == prefix:
		return "/"
	case strings.HasPrefix(reqURI, prefix+"/"):
		return reqURI[len(prefix):]
	default:
		return "/"
	}
}
