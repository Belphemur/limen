package auth

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zitadel/oidc/v3/pkg/oidc"
)

func TestOIDCConfig_Validate(t *testing.T) {
	base := OIDCConfig{
		Issuer:      "https://auth.example.com",
		ClientID:    "portal-client",
		RedirectURI: "https://gw.example.com/auth/callback",
		Scopes:      []string{oidc.ScopeOpenID, "profile"},
	}
	tests := []struct {
		name    string
		mutate  func(*OIDCConfig)
		wantErr string
	}{
		{"ok", func(*OIDCConfig) {}, ""},
		{"missing_issuer", func(c *OIDCConfig) { c.Issuer = " " }, "Issuer"},
		{"missing_client_id", func(c *OIDCConfig) { c.ClientID = "" }, "ClientID"},
		{"missing_redirect_uri", func(c *OIDCConfig) { c.RedirectURI = "" }, "RedirectURI"},
		{"missing_scopes", func(c *OIDCConfig) { c.Scopes = nil }, "Scopes"},
		{"scopes_missing_openid", func(c *OIDCConfig) { c.Scopes = []string{"email"} }, "openid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := base
			tt.mutate(&c)
			err := c.validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validate: unexpected error %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validate: want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestExtractRoles(t *testing.T) {
	t.Run("nil_claims", func(t *testing.T) {
		if got := ExtractRoles(nil); got != nil {
			t.Fatalf("want nil, got %v", got)
		}
	})
	t.Run("nil_map", func(t *testing.T) {
		c := &oidc.IDTokenClaims{}
		if got := ExtractRoles(c); got != nil {
			t.Fatalf("want nil, got %v", got)
		}
	})
	t.Run("absent_roles_claim", func(t *testing.T) {
		c := &oidc.IDTokenClaims{Claims: map[string]any{"sub": "u1"}}
		if got := ExtractRoles(c); got != nil {
			t.Fatalf("want nil, got %v", got)
		}
	})
	t.Run("populated", func(t *testing.T) {
		c := &oidc.IDTokenClaims{Claims: map[string]any{
			rolesClaim: map[string]any{
				"owner":  map[string]any{"zorg-1": "Acme"},
				"member": map[string]any{"zorg-1": "Acme"},
			},
		}}
		got := ExtractRoles(c)
		if len(got) != 2 {
			t.Fatalf("want 2 roles, got %v", got)
		}
		seen := map[string]bool{}
		for _, r := range got {
			seen[r] = true
		}
		if !seen["owner"] || !seen["member"] {
			t.Fatalf("missing role: %v", got)
		}
	})
	t.Run("wrong_shape_returns_nil", func(t *testing.T) {
		c := &oidc.IDTokenClaims{Claims: map[string]any{rolesClaim: "not-a-map"}}
		if got := ExtractRoles(c); got != nil {
			t.Fatalf("want nil for non-map roles claim, got %v", got)
		}
	})
}

func TestSafeReturnTo(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "/portal"},
		{"/portal", "/portal"},
		{"/portal/users", "/portal/users"},
		{"no-leading-slash", "/portal"},
		{"//evil.example.com/path", "/portal"},
		{"/portal?q=1", "/portal?q=1"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := safeReturnTo(tt.in); got != tt.want {
				t.Errorf("safeReturnTo(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTrimTenantPrefix(t *testing.T) {
	tests := []struct {
		req, slug, want string
	}{
		{"/t/acme", "acme", "/"},
		{"/t/acme/", "acme", "/"},
		{"/t/acme/portal", "acme", "/portal"},
		{"/t/acme/portal/users?x=1", "acme", "/portal/users?x=1"},
		{"/t/other/portal", "acme", "/"},
		{"/", "acme", "/"},
	}
	for _, tt := range tests {
		t.Run(tt.req, func(t *testing.T) {
			if got := trimTenantPrefix(tt.req, tt.slug); got != tt.want {
				t.Errorf("trimTenantPrefix(%q,%q) = %q, want %q", tt.req, tt.slug, got, tt.want)
			}
		})
	}
}

func TestClaimsCtx_RoundTrip(t *testing.T) {
	if _, ok := ClaimsFromContext(context.Background()); ok {
		t.Fatal("empty ctx must not carry claims")
	}
	c := &oidc.IDTokenClaims{TokenClaims: oidc.TokenClaims{Subject: "u1"}}
	ctx := WithClaims(context.Background(), c)
	got, ok := ClaimsFromContext(ctx)
	if !ok || got.Subject != "u1" {
		t.Fatalf("claims roundtrip failed: %+v", got)
	}
	if MustClaims(ctx).Subject != "u1" {
		t.Fatal("MustClaims returned wrong claims")
	}
}

func TestMustClaims_PanicsWithoutMiddleware(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	MustClaims(context.Background())
}

func TestClearCookie_SetsExpiredHeader(t *testing.T) {
	w := httptest.NewRecorder()
	clearCookie(w, "limen_portal", "/t/acme", true)
	got := w.Result().Cookies()
	if len(got) != 1 {
		t.Fatalf("want 1 cookie, got %d", len(got))
	}
	c := got[0]
	if c.Name != "limen_portal" || c.Path != "/t/acme" {
		t.Errorf("wrong cookie shape: %+v", c)
	}
	if c.MaxAge != -1 {
		t.Errorf("want MaxAge=-1, got %d", c.MaxAge)
	}
	if !c.Secure || !c.HttpOnly {
		t.Errorf("want HttpOnly+Secure, got %+v", c)
	}
}
