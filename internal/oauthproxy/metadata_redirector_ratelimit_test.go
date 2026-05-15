package oauthproxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
)

func tenantCtx(req *http.Request, publicID string) *http.Request {
	t := &storage.Tenant{}
	t.PublicID = publicID
	ctx := tenancy.WithTenant(req.Context(), t)
	return req.WithContext(ctx)
}

func TestMetadataHandler_Document(t *testing.T) {
	h, err := NewMetadataHandler(MetadataConfig{
		ZitadelIssuer: "https://auth.example.com/",
		BaseURL:       "https://limen.example.com/",
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/t/tnt_x/oauth/.well-known/oauth-authorization-server", nil)
	req = tenantCtx(req, "tnt_x")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d", rr.Code)
	}
	if got, want := rr.Header().Get("Content-Type"), "application/json"; got != want {
		t.Fatalf("content-type: %q", got)
	}
	var doc map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	checks := map[string]string{
		"issuer":                 "https://auth.example.com",
		"authorization_endpoint": "https://limen.example.com/t/tnt_x/oauth/authorize",
		"token_endpoint":         "https://limen.example.com/t/tnt_x/oauth/token",
		"registration_endpoint":  "https://limen.example.com/t/tnt_x/oauth/register",
		"jwks_uri":               "https://auth.example.com/oauth/v2/keys",
		"end_session_endpoint":   "https://limen.example.com/t/tnt_x/oauth/end_session",
	}
	for k, want := range checks {
		if got, _ := doc[k].(string); got != want {
			t.Errorf("doc[%q] = %q, want %q", k, got, want)
		}
	}
}

func TestMetadataHandler_MissingTenant(t *testing.T) {
	h, err := NewMetadataHandler(MetadataConfig{ZitadelIssuer: "https://z", BaseURL: "https://l"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
}

func TestRedirector_Statuses(t *testing.T) {
	mh, _ := NewMetadataHandler(MetadataConfig{
		ZitadelIssuer: "https://auth.example.com",
		BaseURL:       "https://limen.example.com",
	})
	rd := NewRedirector(mh.UpstreamEndpoints(), "")

	cases := []struct {
		name     string
		method   string
		fn       http.HandlerFunc
		wantCode int
		wantLoc  string
	}{
		{"authorize GET", http.MethodGet, rd.Authorize, http.StatusFound,
			"https://auth.example.com/oauth/v2/authorize?client_id=abc&scope=urn%3Azitadel%3Aiam%3Auser%3Aresourceowner"},
		{"userinfo GET", http.MethodGet, rd.Userinfo, http.StatusFound,
			"https://auth.example.com/oidc/v1/userinfo?client_id=abc"},
		{"end_session GET", http.MethodGet, rd.EndSession, http.StatusFound,
			"https://auth.example.com/oidc/v1/end_session?client_id=abc"},
		{"token POST", http.MethodPost, rd.Token, http.StatusTemporaryRedirect,
			"https://auth.example.com/oauth/v2/token?client_id=abc"},
		{"revoke POST", http.MethodPost, rd.Revoke, http.StatusTemporaryRedirect,
			"https://auth.example.com/oauth/v2/revoke?client_id=abc"},
		{"introspect POST", http.MethodPost, rd.Introspect, http.StatusTemporaryRedirect,
			"https://auth.example.com/oauth/v2/introspect?client_id=abc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, "/?client_id=abc", strings.NewReader(""))
			rr := httptest.NewRecorder()
			c.fn(rr, req)
			if rr.Code != c.wantCode {
				t.Fatalf("status = %d, want %d", rr.Code, c.wantCode)
			}
			if got := rr.Header().Get("Location"); got != c.wantLoc {
				t.Fatalf("Location = %q, want %q", got, c.wantLoc)
			}
		})
	}
}

func TestRedirector_Authorize_InjectsMCPRSAudienceScope(t *testing.T) {
	mh, _ := NewMetadataHandler(MetadataConfig{
		ZitadelIssuer: "https://auth.example.com",
		BaseURL:       "https://limen.example.com",
	})
	rd := NewRedirector(mh.UpstreamEndpoints(), "999111")

	req := httptest.NewRequest(http.MethodGet, "/?client_id=abc&scope=openid+profile", strings.NewReader(""))
	rr := httptest.NewRecorder()
	rd.Authorize(rr, req)
	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "urn%3Azitadel%3Aiam%3Auser%3Aresourceowner") {
		t.Errorf("Location missing resourceowner scope: %q", loc)
	}
	if !strings.Contains(loc, "urn%3Azitadel%3Aiam%3Aorg%3Aproject%3Aid%3A999111%3Aaud") {
		t.Errorf("Location missing MCP RS audience scope: %q", loc)
	}
}

func TestPerTenantRateLimit_Isolation(t *testing.T) {
	// burst=1 → exactly one request per tenant per 1s window
	mw := PerTenantRateLimit(1, 1)

	terminal := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := mw(terminal)

	hit := func(publicID string) int {
		req := httptest.NewRequest(http.MethodPost, "/t/x/oauth/register", nil)
		req = tenantCtx(req, publicID)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr.Code
	}

	if got := hit("tnt_a"); got != http.StatusOK {
		t.Fatalf("tnt_a first: %d", got)
	}
	if got := hit("tnt_a"); got != http.StatusTooManyRequests {
		t.Fatalf("tnt_a second: expected 429, got %d", got)
	}
	if got := hit("tnt_b"); got != http.StatusOK {
		t.Fatalf("tnt_b first: %d", got)
	}
}

func TestPerTenantRateLimit_MissingTenant(t *testing.T) {
	mw := PerTenantRateLimit(10, 20)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
}
