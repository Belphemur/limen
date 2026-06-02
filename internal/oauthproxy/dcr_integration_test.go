//go:build integration

package oauthproxy_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/oauthproxy"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/storage/storagetest"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/tenant"
	"github.com/belphemur/limen/internal/zitadel"
)

// fakeZitadel is the oauthproxy.appManager implementation used for the
// integration tests. It records the last AddOIDCApp call and remembers
// what GetOIDCApp should return so the RFC 7592 round-trip can be driven
// without a real Zitadel.
type fakeZitadel struct {
	lastAdd       zitadel.AddOIDCAppInput
	lastUpdate    zitadel.UpdateOIDCAppInput
	addErr        error
	deleteErr     error
	deletedAppIDs []string
	state         *zitadel.OIDCApp

	projectIDs         map[string]string // org|name -> projectID
	ensureProjectErr   error
	ensureProjectCalls []ensureProjectCall
}

type ensureProjectCall struct {
	OrgID string
	Name  string
}

func (f *fakeZitadel) EnsureProject(_ context.Context, orgID, name string) (string, error) {
	if f.ensureProjectErr != nil {
		return "", f.ensureProjectErr
	}
	f.ensureProjectCalls = append(f.ensureProjectCalls, ensureProjectCall{OrgID: orgID, Name: name})
	key := orgID + "|" + name
	if f.projectIDs == nil {
		f.projectIDs = map[string]string{}
	}
	if pid, ok := f.projectIDs[key]; ok {
		return pid, nil
	}
	pid := "proj_" + name
	f.projectIDs[key] = pid
	return pid, nil
}

func (f *fakeZitadel) AddOIDCApp(_ context.Context, in zitadel.AddOIDCAppInput) (*zitadel.OIDCApp, error) {
	if f.addErr != nil {
		return nil, f.addErr
	}
	f.lastAdd = in
	app := &zitadel.OIDCApp{
		AppID:                  "app_zit",
		ClientID:               "client_zit",
		ClientSecret:           "", // public PKCE client
		Name:                   in.Name,
		RedirectURIs:           in.RedirectURIs,
		PostLogoutRedirectURIs: in.PostLogoutRedirectURIs,
		AppType:                in.AppType,
		AuthMethod:             in.AuthMethod,
	}
	f.state = app
	return app, nil
}

func (f *fakeZitadel) UpdateOIDCApp(_ context.Context, in zitadel.UpdateOIDCAppInput) error {
	if f.state == nil {
		return errors.New("update before add")
	}
	f.lastUpdate = in
	f.state.RedirectURIs = in.RedirectURIs
	f.state.PostLogoutRedirectURIs = in.PostLogoutRedirectURIs
	f.state.AppType = in.AppType
	f.state.AuthMethod = in.AuthMethod
	return nil
}

func (f *fakeZitadel) DeleteOIDCApp(_ context.Context, _, _, appID string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletedAppIDs = append(f.deletedAppIDs, appID)
	f.state = nil
	return nil
}

func (f *fakeZitadel) GetOIDCApp(_ context.Context, _, _, _ string) (*zitadel.OIDCApp, error) {
	if f.state == nil {
		return nil, errors.New("not found")
	}
	cpy := *f.state
	return &cpy, nil
}

// mountDCR builds a chi router with the DCR handler mounted under
// /t/{tenant}/oauth/register{,/{client_id}} behind RequireTenant. Returns
// the router and the fake Zitadel so the test can assert call shape.
func mountDCR(t *testing.T, store *storage.Store, cfg oauthproxy.DCRConfig) (chi.Router, *oauthproxy.DCRHandler, *fakeZitadel) {
	t.Helper()
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://limen.test"
	}
	apps := &fakeZitadel{}
	tenantSvc := tenant.NewService(store)
	h, err := oauthproxy.NewDCRHandler(cfg, store, apps, tenantSvc, zap.NewNop())
	if err != nil {
		t.Fatalf("NewDCRHandler: %v", err)
	}
	r := chi.NewRouter()
	r.Route("/t/{tenant}/oauth", func(or chi.Router) {
		or.Use(tenancy.RequireTenant(store, zap.NewNop()))
		or.Post("/register", h.Register)
		or.Get("/register/{client_id}", h.Get)
		or.Put("/register/{client_id}", h.Put)
		or.Delete("/register/{client_id}", h.Delete)
	})
	return r, h, apps
}

func seedTenant(t *testing.T, store *storage.Store, name string) *storage.Tenant {
	t.Helper()
	tn := &storage.Tenant{
		Name:         name,
		ZitadelOrgID: "zorg-" + name,
	}
	if err := store.RawDB().Create(tn).Error; err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	patterns := []storage.TenantRedirectURIAllowlist{
		{TenantID: tn.ID, Label: "Test", Pattern: "https://*.acme.test/**"},
		{TenantID: tn.ID, Label: "Test", Pattern: "http://localhost:*/**"},
	}
	for _, p := range patterns {
		if err := store.RawDB().Create(&p).Error; err != nil {
			t.Fatalf("seed allowlist: %v", err)
		}
	}
	return tn
}

func doJSON(t *testing.T, r http.Handler, method, path, bearer string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func TestDCR_Integration_HappyPath(t *testing.T) {
	store := storagetest.OpenMigrated(t)
	tn := seedTenant(t, store, "acme")
	r, _, apps := mountDCR(t, store, oauthproxy.DCRConfig{})

	rr := doJSON(t, r, http.MethodPost, "/t/"+tn.PublicID+"/oauth/register", "", map[string]any{
		"client_name":   "Test MCP",
		"redirect_uris": []string{"https://app.acme.test/callback"},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	clientID, _ := resp["client_id"].(string)
	if clientID != "client_zit" {
		t.Fatalf("client_id=%q", clientID)
	}
	if got := resp["registration_access_token"]; got == nil || got == "" {
		t.Fatal("missing registration_access_token")
	}
	wantRCU := "https://limen.test/t/" + tn.PublicID + "/oauth/register/client_zit"
	if got, _ := resp["registration_client_uri"].(string); got != wantRCU {
		t.Fatalf("registration_client_uri=%q want=%q", got, wantRCU)
	}

	// Field mapping into the Zitadel call.
	if apps.lastAdd.OrgID != tn.ZitadelOrgID {
		t.Errorf("OrgID=%q", apps.lastAdd.OrgID)
	}
	if !strings.HasPrefix(apps.lastAdd.Name, "Test MCP ") {
		t.Errorf("Name=%q (expected prefix %q)", apps.lastAdd.Name, "Test MCP ")
	}
	if got := apps.lastAdd.AuthMethod; got != zitadel.OIDCAuthMethodNone {
		t.Errorf("AuthMethod=%q", got)
	}
	if got := apps.lastAdd.AppType; got != zitadel.OIDCAppTypeNative {
		t.Errorf("AppType=%q", got)
	}

	// Mirror row exists and stores the SHA-256 hash of the issued token.
	var row storage.ZitadelApp
	if err := store.RawDB().Where("client_id = ?", "client_zit").First(&row).Error; err != nil {
		t.Fatalf("load mirror: %v", err)
	}
	gotToken := resp["registration_access_token"].(string)
	want := sha256.Sum256([]byte(gotToken))
	if !bytes.Equal(row.RegistrationAccessTokenHash, want[:]) {
		t.Errorf("token hash mismatch")
	}
}

func TestDCR_Integration_MissingInitialAccessToken(t *testing.T) {
	store := storagetest.OpenMigrated(t)
	tn := seedTenant(t, store, "acme")
	r, _, _ := mountDCR(t, store, oauthproxy.DCRConfig{
		InitialAccessToken: "iat_xyz",
	})

	rr := doJSON(t, r, http.MethodPost, "/t/"+tn.PublicID+"/oauth/register", "", map[string]any{
		"redirect_uris": []string{"https://app.acme.test/callback"},
	})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	if got := rr.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
		t.Errorf("WWW-Authenticate=%q", got)
	}

	rr = doJSON(t, r, http.MethodPost, "/t/"+tn.PublicID+"/oauth/register", "iat_xyz", map[string]any{
		"redirect_uris": []string{"https://app.acme.test/callback"},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("with valid token: %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestDCR_Integration_Management(t *testing.T) {
	store := storagetest.OpenMigrated(t)
	tn := seedTenant(t, store, "acme")
	r, _, apps := mountDCR(t, store, oauthproxy.DCRConfig{})

	// 1. Register a client.
	rr := doJSON(t, r, http.MethodPost, "/t/"+tn.PublicID+"/oauth/register", "", map[string]any{
		"client_name":   "MCP",
		"redirect_uris": []string{"https://app.acme.test/callback"},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("register: %d", rr.Code)
	}
	var created map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	token := created["registration_access_token"].(string)
	mgmt := "/t/" + tn.PublicID + "/oauth/register/client_zit"

	// 2. GET with valid token → 200.
	if rr = doJSON(t, r, http.MethodGet, mgmt, token, nil); rr.Code != http.StatusOK {
		t.Fatalf("GET valid: %d (%s)", rr.Code, rr.Body.String())
	}
	// 3. GET with wrong token → 404 (no client_id existence leak).
	if rr = doJSON(t, r, http.MethodGet, mgmt, "wrong", nil); rr.Code != http.StatusNotFound &&
		rr.Code != http.StatusUnauthorized {
		t.Fatalf("GET wrong token: %d", rr.Code)
	}
	// Constant-time compare path: tampered token → 401 (token-shaped mismatch).
	if rr = doJSON(t, r, http.MethodGet, mgmt, token[:len(token)-1]+"x", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("GET tampered token: %d", rr.Code)
	}
	// 4. GET missing token → 401 with WWW-Authenticate.
	if rr = doJSON(t, r, http.MethodGet, mgmt, "", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("GET no token: %d", rr.Code)
	}

	// 5. PUT updates redirect_uris.
	rr = doJSON(t, r, http.MethodPut, mgmt, token, map[string]any{
		"client_name":   "MCP v2",
		"redirect_uris": []string{"https://app.acme.test/v2/callback"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT: %d (%s)", rr.Code, rr.Body.String())
	}
	if apps.lastUpdate.RedirectURIs[0] != "https://app.acme.test/v2/callback" {
		t.Errorf("PUT did not propagate redirect_uris: %v", apps.lastUpdate.RedirectURIs)
	}

	// 6. DELETE with bad token → 401, row still there.
	if rr = doJSON(t, r, http.MethodDelete, mgmt, "bad-but-same-length-as-token-..............", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("DELETE bad: %d", rr.Code)
	}
	if err := store.RawDB().Where("client_id = ?", "client_zit").First(&storage.ZitadelApp{}).Error; err != nil {
		t.Fatalf("row vanished after bad-token delete: %v", err)
	}

	// 7. DELETE with valid token → 204; row soft-deleted; Zitadel called.
	if rr = doJSON(t, r, http.MethodDelete, mgmt, token, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d (%s)", rr.Code, rr.Body.String())
	}
	if len(apps.deletedAppIDs) != 1 || apps.deletedAppIDs[0] != "app_zit" {
		t.Errorf("Zitadel delete not called: %v", apps.deletedAppIDs)
	}
	var still storage.ZitadelApp
	if err := store.RawDB().Where("client_id = ?", "client_zit").First(&still).Error; err == nil {
		t.Errorf("row still visible after delete")
	}
}
