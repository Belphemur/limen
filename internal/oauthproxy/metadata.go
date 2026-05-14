package oauthproxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/belphemur/limen/internal/tenancy"
)

// MetadataConfig captures the inputs the metadata handler needs to build
// per-tenant AS / OIDC discovery documents. ZitadelIssuer is the upstream
// Zitadel domain (e.g. https://auth.limen.example.com) and BaseURL is the
// public origin of the Limen deployment (e.g. https://limen.example.com).
type MetadataConfig struct {
	ZitadelIssuer string
	BaseURL       string
}

// validate ensures the metadata config has the two URLs it needs and that
// they parse cleanly. Called once at handler construction time so misconfig
// fails fast rather than 500-ing per-request.
func (c MetadataConfig) validate() error {
	if strings.TrimSpace(c.ZitadelIssuer) == "" {
		return errors.New("oauthproxy: ZitadelIssuer is required")
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("oauthproxy: BaseURL is required")
	}
	if _, err := url.Parse(c.ZitadelIssuer); err != nil {
		return fmt.Errorf("oauthproxy: ZitadelIssuer is not a URL: %w", err)
	}
	if _, err := url.Parse(c.BaseURL); err != nil {
		return fmt.Errorf("oauthproxy: BaseURL is not a URL: %w", err)
	}
	return nil
}

// MetadataHandler serves the AS-metadata / OpenID-configuration document
// for /t/{tenant}/oauth/. The document advertises Zitadel as the issuer
// (so issued tokens' `iss` claim matches what Phase 6 verifies against)
// and Zitadel's keys URL directly for jwks_uri (Phase 6 fetches it
// in-process). Every other endpoint — authorize / token / register /
// userinfo / revoke / introspect / end_session — is routed through Limen
// so MCP clients only need to know one origin.
type MetadataHandler struct {
	cfg            MetadataConfig
	zitadelIssuer  string // trailing slash trimmed
	baseURL        string // trailing slash trimmed
	jwksURL        string
	authzEndpoint  string
	tokenEndpoint  string
	uiEndpoint     string
	revokeEndpoint string
	introEndpoint  string
	endSession     string
}

// NewMetadataHandler validates the config and precomputes the per-call-
// invariant Zitadel-side URLs (jwks_uri + the upstream endpoints the
// redirector points at).
func NewMetadataHandler(cfg MetadataConfig) (*MetadataHandler, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	z := strings.TrimRight(cfg.ZitadelIssuer, "/")
	b := strings.TrimRight(cfg.BaseURL, "/")
	return &MetadataHandler{
		cfg:            cfg,
		zitadelIssuer:  z,
		baseURL:        b,
		jwksURL:        z + "/oauth/v2/keys",
		authzEndpoint:  z + "/oauth/v2/authorize",
		tokenEndpoint:  z + "/oauth/v2/token",
		uiEndpoint:     z + "/oidc/v1/userinfo",
		revokeEndpoint: z + "/oauth/v2/revoke",
		introEndpoint:  z + "/oauth/v2/introspect",
		endSession:     z + "/oidc/v1/end_session",
	}, nil
}

// ZitadelIssuer is the upstream issuer URL the metadata document
// advertises. The redirector reuses this so both handlers stay in sync.
func (h *MetadataHandler) ZitadelIssuer() string { return h.zitadelIssuer }

// UpstreamEndpoints exposes the Zitadel-side endpoints in the same shape
// the redirector expects, keeping a single source of truth for them.
func (h *MetadataHandler) UpstreamEndpoints() UpstreamEndpoints {
	return UpstreamEndpoints{
		Authorize:  h.authzEndpoint,
		Token:      h.tokenEndpoint,
		Userinfo:   h.uiEndpoint,
		Revoke:     h.revokeEndpoint,
		Introspect: h.introEndpoint,
		EndSession: h.endSession,
	}
}

// ServeHTTP writes the AS-metadata / OIDC-configuration JSON document. The
// handler must run behind tenancy.RequireTenant so the tenant ID is
// available on the context.
func (h *MetadataHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tenant, ok := tenancy.TenantFromContext(r.Context())
	if !ok {
		http.Error(w, "tenant not resolved", http.StatusInternalServerError)
		return
	}
	prefix := h.baseURL + "/t/" + tenant.PublicID + "/oauth"

	doc := map[string]any{
		"issuer":                                h.zitadelIssuer,
		"authorization_endpoint":                prefix + "/authorize",
		"token_endpoint":                        prefix + "/token",
		"jwks_uri":                              h.jwksURL,
		"userinfo_endpoint":                     prefix + "/userinfo",
		"registration_endpoint":                 prefix + "/register",
		"revocation_endpoint":                   prefix + "/revoke",
		"introspection_endpoint":                prefix + "/introspect",
		"end_session_endpoint":                  prefix + "/end_session",
		"scopes_supported":                      []string{"openid", "profile", "email", "offline_access"},
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_basic", "client_secret_post"},
		"resource_indicators_supported":         true,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	if err := json.NewEncoder(w).Encode(doc); err != nil {
		// Body already partially written — best-effort log via response writer
		// is impossible; let the connection close naturally.
		_ = err
	}
}
