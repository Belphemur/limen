package transport_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-jose/go-jose/v4"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap/zaptest"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/config"
	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/transport"
)

// -----------------------------------------------------------------------------
// Stub OIDC issuer
// -----------------------------------------------------------------------------

const stubKeyID = "stub-1"

// stubIssuer is a minimal OpenID Provider sufficient for Limen's RP to
// complete a code flow. It serves discovery, JWKS, authorize, and token,
// and signs an ID token with the configurable claims captured in claims.
type stubIssuer struct {
	server   *httptest.Server
	priv     *rsa.PrivateKey
	clientID string

	// claims is read by the /token handler. Tests mutate it to alter the
	// id_token shape (sub, email, name, org_id) between sub-cases.
	claims struct {
		sub   string
		email string
		name  string
		orgID string
	}

	// lastAuthorize captures the most recent /authorize query for assertions.
	lastAuthorize url.Values
}

func newStubIssuer(t *testing.T, clientID string) *stubIssuer {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa gen: %v", err)
	}
	s := &stubIssuer{priv: priv, clientID: clientID}
	s.claims.sub = "user-1"
	s.claims.email = "alice@acme.test"
	s.claims.name = "Alice"
	s.claims.orgID = "zorg-acme"

	mux := http.NewServeMux()
	s.server = httptest.NewServer(mux)
	t.Cleanup(s.server.Close)

	mux.HandleFunc("/.well-known/openid-configuration", s.handleDiscovery)
	mux.HandleFunc("/keys", s.handleJWKS)
	mux.HandleFunc("/authorize", s.handleAuthorize)
	mux.HandleFunc("/token", s.handleToken)
	mux.HandleFunc("/end_session", func(w http.ResponseWriter, r *http.Request) {
		// Test never asserts on the OP's logout page beyond the redirect to it.
		w.WriteHeader(http.StatusOK)
	})
	return s
}

func (s *stubIssuer) URL() string { return s.server.URL }

func (s *stubIssuer) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	doc := map[string]any{
		"issuer":                                s.server.URL,
		"authorization_endpoint":                s.server.URL + "/authorize",
		"token_endpoint":                        s.server.URL + "/token",
		"jwks_uri":                              s.server.URL + "/keys",
		"end_session_endpoint":                  s.server.URL + "/end_session",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"scopes_supported":                      []string{"openid", "profile", "email", "offline_access"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post", "none"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}

func (s *stubIssuer) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	jwks := jose.JSONWebKeySet{
		Keys: []jose.JSONWebKey{{
			Key:       &s.priv.PublicKey,
			KeyID:     stubKeyID,
			Algorithm: "RS256",
			Use:       "sig",
		}},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(jwks)
}

func (s *stubIssuer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	s.lastAuthorize = r.URL.Query()
	redirect := r.URL.Query().Get("redirect_uri")
	state := r.URL.Query().Get("state")
	if redirect == "" {
		http.Error(w, "missing redirect_uri", http.StatusBadRequest)
		return
	}
	u, err := url.Parse(redirect)
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	q := u.Query()
	q.Set("code", "stub-code")
	q.Set("state", state)
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *stubIssuer) handleToken(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	claims := map[string]any{
		"iss":                                   s.server.URL,
		"aud":                                   []string{s.clientID},
		"sub":                                   s.claims.sub,
		"iat":                                   now.Unix(),
		"exp":                                   now.Add(1 * time.Hour).Unix(),
		"email":                                 s.claims.email,
		"name":                                  s.claims.name,
		"urn:zitadel:iam:user:resourceowner:id": s.claims.orgID,
	}
	idToken, err := s.signJWT(claims)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := map[string]any{
		"access_token":  "stub-access",
		"refresh_token": "stub-refresh",
		"id_token":      idToken,
		"token_type":    "Bearer",
		"expires_in":    3600,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *stubIssuer) signJWT(claims map[string]any) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: s.priv},
		(&jose.SignerOptions{}).
			WithType("JWT").
			WithHeader(jose.HeaderKey("kid"), stubKeyID),
	)
	if err != nil {
		return "", err
	}
	obj, err := signer.Sign(payload)
	if err != nil {
		return "", err
	}
	return obj.CompactSerialize()
}

// -----------------------------------------------------------------------------
// Postgres provisioning (mirrors internal/storage/storage_test.go)
// -----------------------------------------------------------------------------

func startPostgres(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pg, err := postgres.Run(ctx,
		"postgres:18-alpine",
		postgres.WithDatabase("limen"),
		postgres.WithUsername("limen"),
		postgres.WithPassword("limen_test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("conn string: %v", err)
	}
	return dsn
}

func provisionRoles(t *testing.T, bootstrapDSN string) (appDSN, adminDSN string) {
	t.Helper()
	db, err := gorm.Open(gormpostgres.Open(bootstrapDSN), &gorm.Config{})
	if err != nil {
		t.Fatalf("open bootstrap: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	stmts := []string{
		`DROP ROLE IF EXISTS limen_app`,
		`DROP ROLE IF EXISTS limen_admin`,
		`CREATE ROLE limen_admin LOGIN PASSWORD 'admin_pw' BYPASSRLS`,
		`CREATE ROLE limen_app   LOGIN PASSWORD 'app_pw'`,
		`GRANT limen_app TO limen_admin`,
		`GRANT ALL PRIVILEGES ON DATABASE limen TO limen_admin`,
		`GRANT CREATE, USAGE ON SCHEMA public TO limen_admin`,
		`ALTER SCHEMA public OWNER TO limen_admin`,
	}
	for _, q := range stmts {
		if err := db.Exec(q).Error; err != nil {
			t.Fatalf("provision (%s): %v", q, err)
		}
	}
	return rewriteUser(t, bootstrapDSN, "limen_app", "app_pw"),
		rewriteUser(t, bootstrapDSN, "limen_admin", "admin_pw")
}

func rewriteUser(t *testing.T, dsn, user, password string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	u.User = url.UserPassword(user, password)
	return u.String()
}

func openMigrated(t *testing.T) *storage.Store {
	t.Helper()
	bootstrap := startPostgres(t)
	appDSN, adminDSN := provisionRoles(t, bootstrap)
	s, err := storage.Open(config.DatabaseConfig{DSN: appDSN, AdminDSN: adminDSN})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

// -----------------------------------------------------------------------------
// Portal harness
// -----------------------------------------------------------------------------

type portalHarness struct {
	t      *testing.T
	server *httptest.Server
	stub   *stubIssuer
	store  *storage.Store
}

func newPortalHarness(t *testing.T) *portalHarness {
	t.Helper()
	store := openMigrated(t)
	logger := zaptest.NewLogger(t)

	// Build the listener first so we can derive its absolute URL for the
	// OIDC redirect_uri before NewOIDC discovers the issuer.
	r := chi.NewRouter()
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	stub := newStubIssuer(t, "limen-portal")

	// Crypto + state signer.
	var rawKey [32]byte
	for i := range rawKey {
		rawKey[i] = byte(i + 1)
	}
	keyB64 := base64.StdEncoding.EncodeToString(rawKey[:])
	parsed, err := crypto.ParseKey(keyB64)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	cipher, err := crypto.NewCipher(parsed)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	crypto.SetCipher(cipher)
	signer, err := auth.NewStateSigner(parsed[:])
	if err != nil {
		t.Fatalf("state signer: %v", err)
	}

	oidcHandler, err := auth.NewOIDC(context.Background(), auth.OIDCConfig{
		Issuer:      stub.URL(),
		ClientID:    "limen-portal",
		RedirectURI: server.URL + "/auth/callback",
		Scopes:      []string{"openid", "profile", "email"},
		Secure:      false, // httptest is plain HTTP
	}, cipher, signer, logger)
	if err != nil {
		t.Fatalf("new oidc: %v", err)
	}

	transport.MountPortal(r, transport.PortalDeps{
		Store:                 store,
		OIDC:                  oidcHandler,
		Logger:                logger,
		PostLogoutRedirectURI: server.URL + "/",
	})

	return &portalHarness{t: t, server: server, stub: stub, store: store}
}

func (h *portalHarness) seedTenant(name, orgID string) *storage.Tenant {
	h.t.Helper()
	ctx := storage.WithSuperuser(context.Background())
	db, commit, err := h.store.Session(ctx)
	if err != nil {
		h.t.Fatalf("session: %v", err)
	}
	tenant := &storage.Tenant{Name: name, ZitadelOrgID: orgID}
	if err := db.Create(tenant).Error; err != nil {
		_ = commit()
		h.t.Fatalf("create tenant: %v", err)
	}
	if err := commit(); err != nil {
		h.t.Fatalf("commit: %v", err)
	}
	return tenant
}

// noRedirectClient returns an http.Client that never auto-follows redirects,
// so each hop is observable in the test.
func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// -----------------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------------

func TestPortal_LoginRedirectsToIssuer(t *testing.T) {
	h := newPortalHarness(t)
	tenant := h.seedTenant("acme", "zorg-acme")

	resp, err := noRedirectClient().Get(h.server.URL + "/t/" + tenant.PublicID + "/auth/login?return_to=/portal/home")
	if err != nil {
		t.Fatalf("login GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status: got %d want 302", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse location: %v", err)
	}
	if !strings.HasPrefix(loc.String(), h.stub.URL()+"/authorize") {
		t.Fatalf("location: got %q want prefix %q", loc, h.stub.URL()+"/authorize")
	}
	if got := loc.Query().Get("client_id"); got != "limen-portal" {
		t.Fatalf("client_id: got %q", got)
	}
	if got := loc.Query().Get("redirect_uri"); got != h.server.URL+"/auth/callback" {
		t.Fatalf("redirect_uri: got %q", got)
	}
	scopes := loc.Query().Get("scope")
	if !strings.Contains(scopes, "openid") {
		t.Fatalf("scope: got %q, want openid", scopes)
	}
	// State cookie must be scoped to the callback path.
	state := findCookie(resp.Cookies(), "limen_state")
	if state == nil {
		t.Fatal("missing limen_state cookie")
	}
	if state.Path != "/auth/callback" {
		t.Fatalf("state cookie path: got %q want /auth/callback", state.Path)
	}
	if !state.HttpOnly || state.SameSite != http.SameSiteLaxMode {
		t.Fatalf("state cookie attrs wrong: %+v", state)
	}
}

func TestPortal_LoginUnknownTenant_404(t *testing.T) {
	h := newPortalHarness(t)
	// no seed
	resp, err := noRedirectClient().Get(h.server.URL + "/t/tnt_0000000000000000000000000Z/auth/login")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: got %d want 404", resp.StatusCode)
	}
}

func TestPortal_CallbackHappyPath(t *testing.T) {
	h := newPortalHarness(t)
	tenant := h.seedTenant("acme", "zorg-acme")

	cookies, state := h.driveLogin(tenant.PublicID, "")

	// Exchange code at /auth/callback.
	resp := h.callback(cookies, state)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		body := readBody(resp)
		t.Fatalf("callback status: got %d want 302; body=%s", resp.StatusCode, body)
	}
	if loc := resp.Header.Get("Location"); loc != "/t/"+tenant.PublicID+"/portal" {
		t.Fatalf("post-callback location: got %q want /t/%s/portal", loc, tenant.PublicID)
	}
	portal := findCookie(resp.Cookies(), "limen_portal")
	if portal == nil {
		t.Fatal("missing limen_portal cookie")
	}
	if portal.Path != "/t/"+tenant.PublicID {
		t.Fatalf("portal cookie path: got %q want /t/%s", portal.Path, tenant.PublicID)
	}
	if !portal.HttpOnly || portal.SameSite != http.SameSiteLaxMode {
		t.Fatalf("portal cookie attrs wrong: %+v", portal)
	}
	if portal.Value == "" {
		t.Fatal("portal cookie empty")
	}

	// User must have been upserted under this tenant.
	assertUser(t, h.store, tenant.ID, h.stub.claims.sub, h.stub.claims.email, h.stub.claims.name)

	// Second login is idempotent — same row, possibly updated fields.
	h.stub.claims.email = "alice2@acme.test"
	h.stub.claims.name = "Alice Two"
	cookies2, state2 := h.driveLogin(tenant.PublicID, "")
	resp2 := h.callback(cookies2, state2)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusFound {
		t.Fatalf("second callback: got %d", resp2.StatusCode)
	}
	assertUser(t, h.store, tenant.ID, h.stub.claims.sub, "alice2@acme.test", "Alice Two")
	assertUserCount(t, h.store, tenant.ID, 1)
}

func TestPortal_CallbackOrgMismatch_403(t *testing.T) {
	h := newPortalHarness(t)
	tenant := h.seedTenant("acme", "zorg-acme")
	// Stub will return a token bound to a different org.
	h.stub.claims.orgID = "zorg-evil"

	cookies, state := h.driveLogin(tenant.PublicID, "")
	resp := h.callback(cookies, state)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status: got %d want 403", resp.StatusCode)
	}
	if cookie := findCookie(resp.Cookies(), "limen_portal"); cookie != nil && cookie.MaxAge > 0 {
		t.Fatalf("portal cookie should not be set on org mismatch: %+v", cookie)
	}
}

func TestPortal_CallbackTamperedState_400(t *testing.T) {
	h := newPortalHarness(t)
	tenant := h.seedTenant("acme", "zorg-acme")

	cookies, state := h.driveLogin(tenant.PublicID, "")
	// Flip a char in the signed state.
	tampered := flipLastChar(state)

	// Send the cookie-state intact but the URL-state tampered: handler must
	// reject because cookie.value != query.state.
	resp := h.callbackWith(cookies, tampered, "stub-code")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", resp.StatusCode)
	}
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// driveLogin issues GET /t/{tenant}/auth/login, follows the authorize hop to
// the stub, and returns the cookies + state needed to drive /auth/callback.
func (h *portalHarness) driveLogin(tenant, returnTo string) ([]*http.Cookie, string) {
	h.t.Helper()
	u := h.server.URL + "/t/" + tenant + "/auth/login"
	if returnTo != "" {
		u += "?return_to=" + url.QueryEscape(returnTo)
	}
	resp, err := noRedirectClient().Get(u)
	if err != nil {
		h.t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		h.t.Fatalf("login status: %d", resp.StatusCode)
	}
	cookies := resp.Cookies()
	loc := resp.Header.Get("Location")

	// Follow the redirect to the stub /authorize, which redirects back to
	// /auth/callback?code=...&state=...
	resp2, err := noRedirectClient().Get(loc)
	if err != nil {
		h.t.Fatalf("authorize follow: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusFound {
		h.t.Fatalf("authorize status: %d", resp2.StatusCode)
	}
	callbackLoc, err := url.Parse(resp2.Header.Get("Location"))
	if err != nil {
		h.t.Fatalf("parse callback loc: %v", err)
	}
	return cookies, callbackLoc.Query().Get("state")
}

func (h *portalHarness) callback(cookies []*http.Cookie, state string) *http.Response {
	return h.callbackWith(cookies, state, "stub-code")
}

func (h *portalHarness) callbackWith(cookies []*http.Cookie, state, code string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.server.URL+"/auth/callback?code="+code+"&state="+url.QueryEscape(state), nil)
	if err != nil {
		h.t.Fatalf("new req: %v", err)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		h.t.Fatalf("callback do: %v", err)
	}
	return resp
}

func findCookie(cs []*http.Cookie, name string) *http.Cookie {
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func assertUser(t *testing.T, store *storage.Store, tenantID int64, sub, email, name string) {
	t.Helper()
	ctx := storage.WithSuperuser(context.Background())
	db, commit, err := store.Session(ctx)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer func() { _ = commit() }()
	var u storage.User
	if err := db.Where("tenant_id = ? AND zitadel_subject = ?", tenantID, sub).First(&u).Error; err != nil {
		t.Fatalf("user lookup: %v", err)
	}
	if u.Email != email {
		t.Fatalf("email: got %q want %q", u.Email, email)
	}
	if u.Name != name {
		t.Fatalf("name: got %q want %q", u.Name, name)
	}
}

func assertUserCount(t *testing.T, store *storage.Store, tenantID int64, want int) {
	t.Helper()
	ctx := storage.WithSuperuser(context.Background())
	db, commit, err := store.Session(ctx)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer func() { _ = commit() }()
	var n int64
	if err := db.Model(&storage.User{}).Where("tenant_id = ?", tenantID).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if int(n) != want {
		t.Fatalf("user count: got %d want %d", n, want)
	}
}

func readBody(resp *http.Response) string {
	buf := make([]byte, 1024)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n])
}

func flipLastChar(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[len(b)-1] == 'A' {
		b[len(b)-1] = 'B'
	} else {
		b[len(b)-1] = 'A'
	}
	return string(b)
}

var _ = fmt.Sprintf // keep fmt in import set even when unused after refactors
