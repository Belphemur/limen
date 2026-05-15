package oauthproxy

import (
	"net/http"
	"net/url"
	"strings"
)

// zitadelResourceOwnerScope is the Zitadel-vendored scope that tells the
// AS to emit the `urn:zitadel:iam:user:resourceowner:id` claim on access
// tokens. Limen's MCP resource server uses that claim to bind a token
// to the calling tenant's org, so every authorize request must carry it
// — most clients (Cursor, Inspector) don't request it themselves.
const zitadelResourceOwnerScope = "urn:zitadel:iam:user:resourceowner"

// projectAudienceScopePrefix is the Zitadel-vendored scope prefix that
// adds a project id to the access token's `aud` claim. Limen's MCP
// resource server expects its project id in `aud`; since DCR-created
// apps live in their own per-client project (phase 7b), the MCP RS
// project no longer lands in `aud` automatically, so we inject this
// scope on /authorize.
const projectAudienceScopePrefix = "urn:zitadel:iam:org:project:id:"
const projectAudienceScopeSuffix = ":aud"

// UpstreamEndpoints names the Zitadel-side OAuth/OIDC URLs the redirector
// forwards user agents and MCP clients to. The metadata handler is the
// canonical owner of these values; the redirector accepts them by value
// to keep a single instance reusable in tests.
type UpstreamEndpoints struct {
	Authorize  string
	Token      string
	Userinfo   string
	Revoke     string
	Introspect string
	EndSession string
}

// Redirector forwards the OAuth/OIDC endpoints Limen advertises in its AS
// metadata to Zitadel. GETs use 302 (the user agent picks up the new
// location naturally). POSTs use 307 because the RFC 7231 status code
// preserves the method *and* the request body, which is what Zitadel's
// token / revoke / introspect endpoints actually need.
//
// The path is `/t/{tenant}/oauth/{authorize|token|userinfo|revoke|
// introspect|end_session}`; the handler reads the raw query string off the
// request and appends it untouched. No header, body, or query rewriting
// happens here — Limen is a pass-through.
type Redirector struct {
	ep UpstreamEndpoints
	// mcpRSProjectID is the Zitadel project id of the MCP resource
	// server app. Injected as a project-audience scope on /authorize
	// so the MCP RS project lands in the token's `aud`. Empty
	// disables the injection (handy in tests).
	mcpRSProjectID string
}

// NewRedirector builds a Redirector wired to the given Zitadel endpoints.
// All six endpoint fields must be set; otherwise the chosen handler will
// 500 at runtime. mcpRSProjectID, when non-empty, is added to the
// /authorize scope as `urn:zitadel:iam:org:project:id:<id>:aud` so the
// MCP resource server's audience binding survives DCR-created apps
// living in per-client projects (phase 7b).
func NewRedirector(ep UpstreamEndpoints, mcpRSProjectID string) *Redirector {
	return &Redirector{ep: ep, mcpRSProjectID: mcpRSProjectID}
}

// Authorize 302-redirects the user agent to Zitadel's `/oauth/v2/authorize`
// with the inbound query preserved. The Zitadel resource-owner scope and
// the MCP RS project-audience scope are added when missing so the
// issued access token carries both the org_id claim and the MCP RS
// audience that Limen's MCP middleware enforces.
func (h *Redirector) Authorize(w http.ResponseWriter, r *http.Request) {
	redirectWithQuery(w, r, h.ep.Authorize, http.StatusFound, h.ensureRequiredScopes(r.URL.RawQuery))
}

// Userinfo 302-redirects to Zitadel's userinfo endpoint. GET-only; bearer
// tokens travel in the Authorization header which the redirected request
// reissues unchanged.
func (h *Redirector) Userinfo(w http.ResponseWriter, r *http.Request) {
	redirect(w, r, h.ep.Userinfo, http.StatusFound)
}

// EndSession 302-redirects to Zitadel's `/oidc/v1/end_session`.
func (h *Redirector) EndSession(w http.ResponseWriter, r *http.Request) {
	redirect(w, r, h.ep.EndSession, http.StatusFound)
}

// Token 307-redirects to Zitadel's `/oauth/v2/token`. 307 (vs. 302/303)
// preserves both the POST method and the request body — clients must not
// downgrade to GET.
func (h *Redirector) Token(w http.ResponseWriter, r *http.Request) {
	redirect(w, r, h.ep.Token, http.StatusTemporaryRedirect)
}

// Revoke 307-redirects to Zitadel's `/oauth/v2/revoke`.
func (h *Redirector) Revoke(w http.ResponseWriter, r *http.Request) {
	redirect(w, r, h.ep.Revoke, http.StatusTemporaryRedirect)
}

// Introspect 307-redirects to Zitadel's `/oauth/v2/introspect`.
func (h *Redirector) Introspect(w http.ResponseWriter, r *http.Request) {
	redirect(w, r, h.ep.Introspect, http.StatusTemporaryRedirect)
}

func redirect(w http.ResponseWriter, r *http.Request, base string, status int) {
	redirectWithQuery(w, r, base, status, r.URL.RawQuery)
}

func redirectWithQuery(w http.ResponseWriter, r *http.Request, base string, status int, rawQuery string) {
	if base == "" {
		http.Error(w, "oauth proxy not configured", http.StatusInternalServerError)
		return
	}
	target := base
	if rawQuery != "" {
		if containsQuery(base) {
			target = base + "&" + rawQuery
		} else {
			target = base + "?" + rawQuery
		}
	}
	http.Redirect(w, r, target, status)
}

// ensureRequiredScopes returns rawQuery with the Zitadel resource-owner
// scope and the MCP RS project-audience scope appended to the `scope`
// parameter when not already present. The rest of the query is preserved
// verbatim, including ordering and any duplicate keys clients may have
// added.
func (h *Redirector) ensureRequiredScopes(rawQuery string) string {
	required := []string{zitadelResourceOwnerScope}
	if h.mcpRSProjectID != "" {
		required = append(required, projectAudienceScopePrefix+h.mcpRSProjectID+projectAudienceScopeSuffix)
	}
	return appendScopes(rawQuery, required)
}

func appendScopes(rawQuery string, required []string) string {
	if rawQuery == "" {
		return "scope=" + url.QueryEscape(strings.Join(append([]string{"openid"}, required...), " "))
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return rawQuery
	}
	scope := strings.TrimSpace(values.Get("scope"))
	parts := strings.Fields(scope)
	have := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		have[p] = struct{}{}
	}
	changed := false
	for _, r := range required {
		if _, ok := have[r]; ok {
			continue
		}
		parts = append(parts, r)
		have[r] = struct{}{}
		changed = true
	}
	if !changed {
		return rawQuery
	}
	values.Set("scope", strings.Join(parts, " "))
	return values.Encode()
}

func containsQuery(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '?' {
			return true
		}
	}
	return false
}
