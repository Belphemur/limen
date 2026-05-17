package transport_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	jose "github.com/go-jose/go-jose/v4"
	"go.uber.org/zap/zaptest"

	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/gateway"
	"github.com/belphemur/limen/internal/mcprs"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/transport"
	"github.com/belphemur/limen/internal/upstream"
)

// mcprsHarness wires a full MCP RS pipeline (tenant resolver → metadata →
// MCPAuth → SSE/Message) backed by a stub Zitadel-style issuer and a
// real Postgres testcontainer.
type mcprsHarness struct {
	t        *testing.T
	server   *httptest.Server
	issuer   *stubAS
	store    *storage.Store
	tenant   *storage.Tenant
	audience string
}

const mcprsAudience = "limen-mcp"

func newMCPRSHarness(t *testing.T) *mcprsHarness {
	t.Helper()
	store := openMigrated(t)
	logger := zaptest.NewLogger(t)

	issuer := newStubAS(t)

	r := chi.NewRouter()
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	metadata, err := mcprs.NewHandler(mcprs.MetadataConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("mcprs.NewHandler: %v", err)
	}

	mcpAuth, err := auth.NewMCPAuth(context.Background(), auth.MCPAuthConfig{
		Issuer:   issuer.URL(),
		Audience: mcprsAudience,
	}, metadata, store, logger)
	if err != nil {
		t.Fatalf("auth.NewMCPAuth: %v", err)
	}

	// MCP routes are mounted but the test never exercises the
	// codemode tools — a minimal Manager (empty registry, real store)
	// is enough to satisfy the constructor.
	registry := upstream.NewRegistry()
	mgr, err := gateway.NewManager(gateway.ManagerOptions{
		Store:    store,
		Service:  upstream.NewService(store, registry),
		Registry: registry,
		Logger:   logger,
	})
	if err != nil {
		t.Fatalf("gateway.NewManager: %v", err)
	}
	cm := gateway.NewCodeModeHandler(mgr, gateway.CodeModeConfig{ScriptTimeout: 5 * time.Second}, logger)
	mcpServer := transport.NewMCPServer(mgr, cm, logger)

	if err := transport.MountMCPRS(r, transport.MCPRSDeps{
		Store:     store,
		MCPServer: mcpServer,
		MCPAuth:   mcpAuth,
		Metadata:  metadata,
		Logger:    logger,
	}); err != nil {
		t.Fatalf("MountMCPRS: %v", err)
	}

	// Seed a tenant bound to the issuer's org.
	ctx := storage.WithSuperuser(context.Background())
	db, commit, err := store.Session(ctx)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	tenant := &storage.Tenant{Name: "acme", ZitadelOrgID: "zorg-acme"}
	if err := db.Create(tenant).Error; err != nil {
		_ = commit()
		t.Fatalf("create tenant: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	return &mcprsHarness{
		t:        t,
		server:   server,
		issuer:   issuer,
		store:    store,
		tenant:   tenant,
		audience: mcprsAudience,
	}
}

func (h *mcprsHarness) seedUser(zsub string) {
	h.t.Helper()
	ctx := storage.WithTenant(context.Background(), h.tenant.ID)
	db, commit, err := h.store.Session(ctx)
	if err != nil {
		h.t.Fatalf("session: %v", err)
	}
	u := &storage.User{
		TenantID:       h.tenant.ID,
		Email:          "user@acme.test",
		Name:           "Alice",
		ZitadelSubject: zsub,
	}
	if err := db.Create(u).Error; err != nil {
		_ = commit()
		h.t.Fatalf("create user: %v", err)
	}
	if err := commit(); err != nil {
		h.t.Fatalf("commit: %v", err)
	}
}

// stubAS is a minimal Zitadel-style issuer surfacing discovery + JWKS
// and signing access tokens with RS256.
type stubAS struct {
	server *httptest.Server
	priv   *rsa.PrivateKey
}

const stubASKeyID = "stub-as-1"

func newStubAS(t *testing.T) *stubAS {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa gen: %v", err)
	}
	s := &stubAS{priv: priv}
	mux := http.NewServeMux()
	s.server = httptest.NewServer(mux)
	t.Cleanup(s.server.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		doc := map[string]any{
			"issuer":                                s.server.URL,
			"authorization_endpoint":                s.server.URL + "/authorize",
			"token_endpoint":                        s.server.URL + "/token",
			"jwks_uri":                              s.server.URL + "/keys",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		jwks := jose.JSONWebKeySet{
			Keys: []jose.JSONWebKey{{
				Key:       &priv.PublicKey,
				KeyID:     stubASKeyID,
				Algorithm: "RS256",
				Use:       "sig",
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	})
	return s
}

func (s *stubAS) URL() string { return s.server.URL }

// mintAccessToken signs an access token with the given claim overrides.
// Defaults: 1h expiry, RS256, aud=mcprsAudience, iss=stubAS.URL().
type tokenOpts struct {
	Subject  string
	Audience []string
	OrgID    string
	Issuer   string // override iss
	Signer   *rsa.PrivateKey
	KeyID    string
	Expired  bool
}

func (s *stubAS) mintAccessToken(t *testing.T, opts tokenOpts) string {
	t.Helper()
	now := time.Now()
	exp := now.Add(time.Hour).Unix()
	if opts.Expired {
		exp = now.Add(-time.Minute).Unix()
	}
	iss := s.server.URL
	if opts.Issuer != "" {
		iss = opts.Issuer
	}
	aud := opts.Audience
	if aud == nil {
		aud = []string{mcprsAudience}
	}
	claims := map[string]any{
		"iss":                                   iss,
		"sub":                                   opts.Subject,
		"aud":                                   aud,
		"iat":                                   now.Unix(),
		"exp":                                   exp,
		"jti":                                   "jti-" + opts.Subject,
		"urn:zitadel:iam:user:resourceowner:id": opts.OrgID,
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	signKey := opts.Signer
	if signKey == nil {
		signKey = s.priv
	}
	kid := opts.KeyID
	if kid == "" {
		kid = stubASKeyID
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: signKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader(jose.HeaderKey("kid"), kid),
	)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	obj, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	tok, err := obj.CompactSerialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return tok
}

func TestMCPRS_PRMIsPublic(t *testing.T) {
	h := newMCPRSHarness(t)

	url := h.server.URL + "/t/" + h.tenant.PublicID + "/mcp/.well-known/oauth-protected-resource"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["resource"] != h.server.URL+"/t/"+h.tenant.PublicID+"/mcp" {
		t.Errorf("resource: %v", body["resource"])
	}
	servers, _ := body["authorization_servers"].([]any)
	if len(servers) != 1 || servers[0] != h.server.URL+"/t/"+h.tenant.PublicID+"/oauth" {
		t.Errorf("authorization_servers: %v", body["authorization_servers"])
	}
}

func TestMCPRS_NoBearerReturns401Challenge(t *testing.T) {
	h := newMCPRSHarness(t)

	url := h.server.URL + "/t/" + h.tenant.PublicID + "/mcp/sse"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	ch := resp.Header.Get("WWW-Authenticate")
	if !strings.Contains(ch, `Bearer realm="mcp"`) {
		t.Errorf("WWW-Authenticate: %q", ch)
	}
	want := `resource_metadata="` + h.server.URL + "/t/" + h.tenant.PublicID + `/mcp/.well-known/oauth-protected-resource"`
	if !strings.Contains(ch, want) {
		t.Errorf("missing resource_metadata: %q", ch)
	}
}

func TestMCPRS_WrongAudienceReturns401(t *testing.T) {
	h := newMCPRSHarness(t)
	h.seedUser("user-1")
	tok := h.issuer.mintAccessToken(t, tokenOpts{
		Subject:  "user-1",
		Audience: []string{"some-other-app"},
		OrgID:    h.tenant.ZitadelOrgID,
	})
	resp := h.callSSE(t, tok)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Errorf("WWW-Authenticate: %q", resp.Header.Get("WWW-Authenticate"))
	}
}

func TestMCPRS_CrossTenantReturns403(t *testing.T) {
	h := newMCPRSHarness(t)
	h.seedUser("user-1")
	tok := h.issuer.mintAccessToken(t, tokenOpts{
		Subject: "user-1",
		OrgID:   "zorg-other", // not h.tenant.ZitadelOrgID
	})
	resp := h.callSSE(t, tok)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("WWW-Authenticate"), `error="cross_tenant_denied"`) {
		t.Errorf("WWW-Authenticate: %q", resp.Header.Get("WWW-Authenticate"))
	}
}

func TestMCPRS_UnprovisionedUserReturns401(t *testing.T) {
	h := newMCPRSHarness(t)
	tok := h.issuer.mintAccessToken(t, tokenOpts{
		Subject: "unknown-user",
		OrgID:   h.tenant.ZitadelOrgID,
	})
	resp := h.callSSE(t, tok)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestMCPRS_BadSignatureReturns401(t *testing.T) {
	h := newMCPRSHarness(t)
	h.seedUser("user-1")
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa gen: %v", err)
	}
	tok := h.issuer.mintAccessToken(t, tokenOpts{
		Subject: "user-1",
		OrgID:   h.tenant.ZitadelOrgID,
		Signer:  other,
	})
	resp := h.callSSE(t, tok)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestMCPRS_BadIssuerReturns401(t *testing.T) {
	h := newMCPRSHarness(t)
	h.seedUser("user-1")
	tok := h.issuer.mintAccessToken(t, tokenOpts{
		Subject: "user-1",
		OrgID:   h.tenant.ZitadelOrgID,
		Issuer:  "https://attacker.example.com",
	})
	resp := h.callSSE(t, tok)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestMCPRS_HappyPathReachesSSE(t *testing.T) {
	h := newMCPRSHarness(t)
	h.seedUser("user-1")
	tok := h.issuer.mintAccessToken(t, tokenOpts{
		Subject: "user-1",
		OrgID:   h.tenant.ZitadelOrgID,
	})

	// The SSE handler keeps the connection open; do a short-deadline request
	// and assert the auth pipeline passes (we receive a 200 + text/event-stream).
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		h.server.URL+"/t/"+h.tenant.PublicID+"/mcp/sse", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Context deadline is expected once the stream is open; check status separately.
		if resp == nil {
			t.Fatalf("Do: %v", err)
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d body=%s", resp.StatusCode, readBodyMCPRS(resp))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type: %q", ct)
	}
}

func (h *mcprsHarness) callSSE(t *testing.T, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet,
		h.server.URL+"/t/"+h.tenant.PublicID+"/mcp/sse", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// Short timeout so failed-auth requests don't get blocked by the SSE stream.
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

// TestMCPRS_McpSessionIdAloneDoesNotAuthenticate pins the invariant that
// the MCP transport's session-id header is opaque to the RS: a request
// carrying only `Mcp-Session-Id` (no `Authorization`) must still be
// rejected with the standard 401 + PRM challenge. Identity is taken
// only from the bearer token, re-validated on every request.
func TestMCPRS_McpSessionIdAloneDoesNotAuthenticate(t *testing.T) {
	h := newMCPRSHarness(t)

	req, err := http.NewRequest(http.MethodGet,
		h.server.URL+"/t/"+h.tenant.PublicID+"/mcp/sse", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Mcp-Session-Id", "session-from-some-other-user")

	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	ch := resp.Header.Get("WWW-Authenticate")
	if !strings.Contains(ch, `Bearer realm="mcp"`) ||
		!strings.Contains(ch, `resource_metadata="`) {
		t.Errorf("missing PRM challenge: %q", ch)
	}
}

// TestMCPRS_DiscoveryChain walks the MCP-spec cold-start chain that a
// real client follows:
//
//  1. GET /mcp/sse with no auth → 401, parse resource_metadata from
//     WWW-Authenticate.
//  2. GET that PRM URL → JSON with `resource` + `authorization_servers`.
//  3. Use authorization_servers[0] to build the per-tenant AS URL the
//     client would discover next (Phase 5 mounts the AS metadata; here
//     we just assert the link the RS publishes is well-formed).
//  4. Simulate the token the AS would mint at the end of /authorize +
//     /token and re-hit /mcp/sse → 200.
func TestMCPRS_DiscoveryChain(t *testing.T) {
	h := newMCPRSHarness(t)
	h.seedUser("user-1")

	// Step 1 — unauthenticated probe.
	probe, err := http.Get(h.server.URL + "/t/" + h.tenant.PublicID + "/mcp/sse")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	probe.Body.Close()
	if probe.StatusCode != http.StatusUnauthorized {
		t.Fatalf("probe status: %d", probe.StatusCode)
	}

	prmURL := extractResourceMetadataURL(t, probe.Header.Get("WWW-Authenticate"))

	// Step 2 — fetch PRM (public, no auth).
	prmResp, err := http.Get(prmURL)
	if err != nil {
		t.Fatalf("PRM fetch: %v", err)
	}
	defer prmResp.Body.Close()
	if prmResp.StatusCode != http.StatusOK {
		t.Fatalf("PRM status: %d", prmResp.StatusCode)
	}
	var prm struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if err := json.NewDecoder(prmResp.Body).Decode(&prm); err != nil {
		t.Fatalf("PRM decode: %v", err)
	}
	wantResource := h.server.URL + "/t/" + h.tenant.PublicID + "/mcp"
	if prm.Resource != wantResource {
		t.Errorf("PRM resource = %q, want %q", prm.Resource, wantResource)
	}

	// Step 3 — authorization_servers must point at this tenant's AS wrapper.
	if len(prm.AuthorizationServers) != 1 {
		t.Fatalf("authorization_servers: %v", prm.AuthorizationServers)
	}
	wantAS := h.server.URL + "/t/" + h.tenant.PublicID + "/oauth"
	if prm.AuthorizationServers[0] != wantAS {
		t.Errorf("AS URL = %q, want %q", prm.AuthorizationServers[0], wantAS)
	}

	// Step 4 — stand in for the AS-issued access token and re-hit the RS.
	tok := h.issuer.mintAccessToken(t, tokenOpts{
		Subject: "user-1",
		OrgID:   h.tenant.ZitadelOrgID,
	})
	resp := h.callSSE(t, tok)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post-discovery status: %d body=%s", resp.StatusCode, readBodyMCPRS(resp))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type: %q", ct)
	}
}

var resourceMetadataRe = regexp.MustCompile(`resource_metadata="([^"]+)"`)

func extractResourceMetadataURL(t *testing.T, header string) string {
	t.Helper()
	m := resourceMetadataRe.FindStringSubmatch(header)
	if len(m) != 2 {
		t.Fatalf("resource_metadata not in WWW-Authenticate: %q", header)
	}
	return m[1]
}

func readBodyMCPRS(resp *http.Response) string {
	b := make([]byte, 256)
	n, _ := resp.Body.Read(b)
	return string(b[:n])
}
