package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
)

// newTestCipher builds a Cipher from a deterministic 32-byte key so tests
// stay reproducible without touching the process-wide cipher.
func newTestCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	var k crypto.Key
	copy(k[:], []byte("test-master-key-aes-siv-32bytes!"))
	c, err := crypto.NewCipher(k)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	return c
}

func newTestManager(t *testing.T) *SessionManager {
	t.Helper()
	sm, err := NewSessionManager(SessionConfig{
		Cipher:            newTestCipher(t),
		CookieName:        "limen_portal",
		Lifetime:          time.Hour,
		SkipLivenessCheck: true,
	})
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	return sm
}

func TestSessionConfig_Validate(t *testing.T) {
	cases := []struct {
		name string
		cfg  SessionConfig
		ok   bool
	}{
		{"missing cipher", SessionConfig{CookieName: "c", Lifetime: time.Hour, SkipLivenessCheck: true}, false},
		{"missing cookie name", SessionConfig{Cipher: newTestCipher(t), Lifetime: time.Hour, SkipLivenessCheck: true}, false},
		{"missing lifetime", SessionConfig{Cipher: newTestCipher(t), CookieName: "c", SkipLivenessCheck: true}, false},
		{"missing zitadel when liveness required", SessionConfig{Cipher: newTestCipher(t), CookieName: "c", Lifetime: time.Hour}, false},
		{"ok skip liveness", SessionConfig{Cipher: newTestCipher(t), CookieName: "c", Lifetime: time.Hour, SkipLivenessCheck: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewSessionManager(tc.cfg)
			if (err == nil) != tc.ok {
				t.Fatalf("validate ok=%v, err=%v", tc.ok, err)
			}
		})
	}
}

func TestSession_IssueLoadRoundtrip(t *testing.T) {
	sm := newTestManager(t)
	w := httptest.NewRecorder()

	data := SessionData{
		ZitadelSID:   "sid-1",
		ZitadelToken: "tok",
		Subject:      "user-1",
		LocalUserID:  42,
		Email:        "user@example.com",
		Roles:        []string{"tenant_admin", "tenant_user"},
	}
	if err := sm.Issue(w, "acme", data); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	resp := w.Result()
	defer resp.Body.Close()
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("want 1 cookie, got %d", len(cookies))
	}
	c := cookies[0]
	if c.Path != "/t/acme" {
		t.Errorf("cookie path = %q, want /t/acme", c.Path)
	}
	if !c.HttpOnly {
		t.Error("cookie not HttpOnly")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie SameSite = %v, want Lax", c.SameSite)
	}

	r := httptest.NewRequest(http.MethodGet, "/t/acme/", nil)
	r.AddCookie(c)
	got, err := sm.Load(r, "acme")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ZitadelSID != data.ZitadelSID || got.Subject != data.Subject || got.LocalUserID != data.LocalUserID {
		t.Errorf("decoded session mismatch: got=%+v want=%+v", got, data)
	}
	if len(got.Roles) != 2 || got.Roles[0] != "tenant_admin" {
		t.Errorf("roles round-trip failed: %v", got.Roles)
	}
}

func TestSession_LoadNoCookie(t *testing.T) {
	sm := newTestManager(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, err := sm.Load(r, "acme"); err != ErrNoSession {
		t.Fatalf("want ErrNoSession, got %v", err)
	}
}

func TestSession_LoadExpired(t *testing.T) {
	sm := newTestManager(t)
	w := httptest.NewRecorder()
	if err := sm.Issue(w, "acme", SessionData{
		ZitadelSID: "s",
		Subject:    "u",
		IssuedAt:   time.Now().UTC().Add(-2 * time.Hour),
		ExpiresAt:  time.Now().UTC().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	c := w.Result().Cookies()[0]
	r := httptest.NewRequest(http.MethodGet, "/t/acme/", nil)
	r.AddCookie(c)
	if _, err := sm.Load(r, "acme"); err != ErrSessionExpired {
		t.Fatalf("want ErrSessionExpired, got %v", err)
	}
}

func TestSession_LoadRejectsCookieFromOtherTenant(t *testing.T) {
	sm := newTestManager(t)
	w := httptest.NewRecorder()
	if err := sm.Issue(w, "acme", SessionData{ZitadelSID: "s", Subject: "u"}); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	c := w.Result().Cookies()[0]
	// Replay cookie under a different tenant slug — AAD mismatch must fail.
	r := httptest.NewRequest(http.MethodGet, "/t/beta/", nil)
	r.AddCookie(c)
	if _, err := sm.Load(r, "beta"); err == nil {
		t.Fatal("want decrypt error when cookie is replayed across tenants")
	}
}

func TestSession_LoadRejectsTamperedCiphertext(t *testing.T) {
	sm := newTestManager(t)
	w := httptest.NewRecorder()
	_ = sm.Issue(w, "acme", SessionData{ZitadelSID: "s", Subject: "u"})
	c := w.Result().Cookies()[0]
	// Flip a character in the middle of the b64 ciphertext.
	mid := len(c.Value) / 2
	tampered := []byte(c.Value)
	if tampered[mid] == 'A' {
		tampered[mid] = 'B'
	} else {
		tampered[mid] = 'A'
	}
	c.Value = string(tampered)
	r := httptest.NewRequest(http.MethodGet, "/t/acme/", nil)
	r.AddCookie(c)
	if _, err := sm.Load(r, "acme"); err == nil {
		t.Fatal("want decrypt error on tampered cookie")
	}
}

func TestSession_ClearSetsExpiredCookie(t *testing.T) {
	sm := newTestManager(t)
	w := httptest.NewRecorder()
	sm.Clear(w, "acme")
	c := w.Result().Cookies()[0]
	if c.MaxAge >= 0 {
		t.Errorf("clear cookie MaxAge = %d, want negative", c.MaxAge)
	}
	if c.Path != "/t/acme" {
		t.Errorf("clear cookie path = %q, want /t/acme", c.Path)
	}
}

// Test the RequirePortalSession middleware: needs tenant in ctx; missing
// cookie redirects to /t/{slug}/auth/login; valid cookie populates ctx.
func TestRequirePortalSession_NoCookieRedirectsToLogin(t *testing.T) {
	sm := newTestManager(t)
	tenant := &storage.Tenant{Slug: "acme"}
	tenant.ID = 1
	mw := sm.RequirePortalSession(nil)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not run without a session")
	}))

	r := httptest.NewRequest(http.MethodGet, "/t/acme/dashboard?x=1", nil)
	r = r.WithContext(tenancy.WithTenant(r.Context(), tenant))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	resp := w.Result()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/t/acme/auth/login?return_to=") {
		t.Errorf("Location = %q, want /t/acme/auth/login?return_to=...", loc)
	}
}

func TestRequirePortalSession_ValidCookiePopulatesCtx(t *testing.T) {
	sm := newTestManager(t)
	tenant := &storage.Tenant{Slug: "acme"}
	tenant.ID = 1

	// Issue a cookie via the manager itself.
	rec := httptest.NewRecorder()
	if err := sm.Issue(rec, "acme", SessionData{
		ZitadelSID: "sid", Subject: "u-1", LocalUserID: 7,
	}); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	cookie := rec.Result().Cookies()[0]

	var seen *SessionData
	mw := sm.RequirePortalSession(nil)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = MustSession(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodGet, "/t/acme/", nil)
	r.AddCookie(cookie)
	r = r.WithContext(tenancy.WithTenant(r.Context(), tenant))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if seen == nil || seen.Subject != "u-1" || seen.LocalUserID != 7 {
		t.Errorf("session not populated: %+v", seen)
	}
}

func TestRequirePortalSession_MissingTenantIs500(t *testing.T) {
	sm := newTestManager(t)
	mw := sm.RequirePortalSession(nil)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestRequireRole(t *testing.T) {
	cases := []struct {
		name   string
		roles  []string
		have   []string
		status int
	}{
		{"match", []string{"tenant_admin"}, []string{"tenant_user", "tenant_admin"}, http.StatusOK},
		{"no match", []string{"tenant_admin"}, []string{"tenant_user"}, http.StatusForbidden},
		{"empty want rejects", nil, []string{"tenant_admin"}, http.StatusForbidden},
		{"no session", []string{"tenant_admin"}, nil, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := RequireRole(tc.roles...)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.have != nil {
				r = r.WithContext(WithSession(r.Context(), &SessionData{Roles: tc.have}))
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Errorf("status = %d, want %d", w.Code, tc.status)
			}
		})
	}
}

func TestSession_CtxHelpers(t *testing.T) {
	ctx := context.Background()
	if _, ok := SessionFromContext(ctx); ok {
		t.Error("empty ctx should not carry a session")
	}
	want := &SessionData{Subject: "abc"}
	ctx = WithSession(ctx, want)
	got, ok := SessionFromContext(ctx)
	if !ok || got != want {
		t.Errorf("ctx round-trip failed: got=%v ok=%v", got, ok)
	}
	if MustSession(ctx) != want {
		t.Error("MustSession returned wrong pointer")
	}
	defer func() {
		if r := recover(); r == nil {
			t.Error("MustSession on empty ctx should panic")
		}
	}()
	MustSession(context.Background())
}
