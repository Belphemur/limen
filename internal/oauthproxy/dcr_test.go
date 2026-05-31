//go:build integration

package oauthproxy

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/zitadel"

	"go.uber.org/zap"
)

// fakeAppManager satisfies appManager without round-tripping Zitadel.
type fakeAppManager struct {
	addInput  zitadel.AddOIDCAppInput
	addReturn *zitadel.OIDCApp
	addErr    error
}

func (f *fakeAppManager) AddOIDCApp(_ context.Context, in zitadel.AddOIDCAppInput) (*zitadel.OIDCApp, error) {
	f.addInput = in
	if f.addErr != nil {
		return nil, f.addErr
	}
	if f.addReturn != nil {
		return f.addReturn, nil
	}
	return &zitadel.OIDCApp{
		AppID:                  "app_123",
		ClientID:               "client_abc",
		Name:                   in.Name,
		RedirectURIs:           in.RedirectURIs,
		PostLogoutRedirectURIs: in.PostLogoutRedirectURIs,
		AppType:                in.AppType,
		AuthMethod:             in.AuthMethod,
	}, nil
}
func (f *fakeAppManager) UpdateOIDCApp(context.Context, zitadel.UpdateOIDCAppInput) error {
	return nil
}
func (f *fakeAppManager) DeleteOIDCApp(context.Context, string, string, string) error {
	return nil
}
func (f *fakeAppManager) GetOIDCApp(context.Context, string, string, string) (*zitadel.OIDCApp, error) {
	return nil, nil
}
func (f *fakeAppManager) EnsureProject(_ context.Context, _, name string) (string, error) {
	return "proj_" + name, nil
}

// fakeAllowlistLoader satisfies AllowlistPatternsLoader without hitting
// the DB. Tests assign Patterns to drive the per-tenant policy.
type fakeAllowlistLoader struct {
	Patterns []string
	Err      error
}

func (f *fakeAllowlistLoader) ListAllowlistPatterns(_ context.Context, _ *storage.Tenant) ([]string, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Patterns, nil
}

func dcrRequestCtx(req *http.Request, t *storage.Tenant) *http.Request {
	return req.WithContext(tenancy.WithTenant(req.Context(), t))
}

func newDCRHandlerForValidation(t *testing.T, cfg DCRConfig, apps appManager) *DCRHandler {
	return newDCRHandlerForValidationWithAllowlist(t, cfg, apps, nil)
}

func newDCRHandlerForValidationWithAllowlist(t *testing.T, cfg DCRConfig, apps appManager, patterns []string) *DCRHandler {
	t.Helper()
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://limen.example.com"
	}
	// Pass nil store: the validation paths under test return before any
	// DB hit. Tests that exercise persistence wire a real *storage.Store.
	h := &DCRHandler{
		cfg:       cfg,
		store:     nil,
		apps:      apps,
		allowlist: &fakeAllowlistLoader{Patterns: patterns},
		logger:    zap.NewNop(),
		baseURL:   strings.TrimRight(cfg.BaseURL, "/"),
	}
	return h
}

func TestDCR_IgnoresUnknownMetadataFields(t *testing.T) {
	// RFC 7591 §2: the authorization server MUST ignore unknown client
	// metadata. The decoder should accept and silently drop extra fields
	// rather than returning 400.
	tn := &storage.Tenant{ZitadelOrgID: "org_a", DCREnabled: true}
	tn.PublicID = "tnt_a"
	h := newDCRHandlerForValidation(t, DCRConfig{DCREnabled: true}, &fakeAppManager{})

	body := []byte(`{"redirect_uris":["https://app.example.com/cb"],"future_flag":true,"logo_uri":"https://app.example.com/logo.png"}`)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = dcrRequestCtx(req, tn)
	rr := httptest.NewRecorder()
	h.Register(rr, req)

	if rr.Code == http.StatusBadRequest {
		t.Fatalf("unknown fields should be ignored, got 400: %s", rr.Body.String())
	}
}

func TestDCR_RequiresDCREnabledOnTenant(t *testing.T) {
	tn := &storage.Tenant{DCREnabled: false}
	tn.PublicID = "tnt_a"
	h := newDCRHandlerForValidation(t, DCRConfig{DCREnabled: true}, &fakeAppManager{})
	req := httptest.NewRequest(http.MethodPost, "/",
		bytes.NewReader([]byte(`{"redirect_uris":["https://app.example.com/cb"]}`)))
	req.Header.Set("Content-Type", "application/json")
	req = dcrRequestCtx(req, tn)
	rr := httptest.NewRecorder()
	h.Register(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
}

func TestDCR_RequiresInitialAccessToken(t *testing.T) {
	tn := &storage.Tenant{DCREnabled: true}
	tn.PublicID = "tnt_a"
	h := newDCRHandlerForValidation(t, DCRConfig{DCREnabled: true, InitialAccessToken: "s3cret"}, &fakeAppManager{})

	body := []byte(`{"redirect_uris":["https://app.example.com/cb"]}`)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = dcrRequestCtx(req, tn)
	rr := httptest.NewRecorder()
	h.Register(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: expected 401, got %d", rr.Code)
	}
	if got := rr.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
		t.Fatalf("missing WWW-Authenticate: %q", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrong")
	req = dcrRequestCtx(req, tn)
	rr = httptest.NewRecorder()
	h.Register(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: expected 401, got %d", rr.Code)
	}
}

func TestDCR_RejectsRedirectURIFailingFloor(t *testing.T) {
	tn := &storage.Tenant{DCREnabled: true}
	tn.PublicID = "tnt_a"
	h := newDCRHandlerForValidation(t, DCRConfig{DCREnabled: true}, &fakeAppManager{})

	body := []byte(`{"redirect_uris":["http://evil.example.com/cb"]}`)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = dcrRequestCtx(req, tn)
	rr := httptest.NewRecorder()
	h.Register(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestDCR_RejectsRedirectURIFailingAllowlist(t *testing.T) {
	tn := &storage.Tenant{DCREnabled: true}
	tn.PublicID = "tnt_a"
	h := newDCRHandlerForValidationWithAllowlist(t, DCRConfig{DCREnabled: true}, &fakeAppManager{}, []string{"https://*.acme.com/**"})

	body := []byte(`{"redirect_uris":["https://app.example.com/cb"]}`)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = dcrRequestCtx(req, tn)
	rr := httptest.NewRecorder()
	h.Register(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (not in allowlist), got %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestDCR_NormalizeDefaults(t *testing.T) {
	tn := &storage.Tenant{ZitadelOrgID: "org_a"}
	tn.PublicID = "tnt_a"
	h := newDCRHandlerForValidation(t, DCRConfig{DCREnabled: true}, &fakeAppManager{})

	norm, zin, err := h.normalize(context.Background(), tn, dcrRequest{RedirectURIs: []string{"https://app.example.com/cb"}})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got := norm.ApplicationType; got != "native" {
		t.Fatalf("application_type default = %q", got)
	}
	if got := norm.TokenEndpointAuthMethod; got != "none" {
		t.Fatalf("auth_method default = %q", got)
	}
	if len(norm.GrantTypes) != 2 {
		t.Fatalf("grant_types default = %v", norm.GrantTypes)
	}
	if zin.OrgID != "org_a" {
		t.Fatalf("zitadel input OrgID = %q", zin.OrgID)
	}
}

func TestDCR_NormalizeRejectsBadGrantType(t *testing.T) {
	tn := &storage.Tenant{ZitadelOrgID: "org_a"}
	h := newDCRHandlerForValidation(t, DCRConfig{DCREnabled: true}, &fakeAppManager{})
	_, _, err := h.normalize(context.Background(), tn, dcrRequest{
		RedirectURIs: []string{"https://app.example.com/cb"},
		GrantTypes:   []string{"implicit"},
	})
	if err == nil {
		t.Fatalf("expected error for implicit grant")
	}
}
