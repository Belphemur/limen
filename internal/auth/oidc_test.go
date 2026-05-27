package auth

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		{"", "/"},
		{"/portal", "/portal"},
		{"/portal/users", "/portal/users"},
		{"no-leading-slash", "/"},
		{"//evil.example.com/path", "/"},
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
		req, tenant, want string
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
			if got := trimTenantPrefix(tt.req, tt.tenant); got != tt.want {
				t.Errorf("trimTenantPrefix(%q,%q) = %q, want %q", tt.req, tt.tenant, got, tt.want)
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

func TestPackPortalCookie_RoundTrip(t *testing.T) {
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	original := PortalCookieValue{
		IDToken:      "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.fakesig",
		RefreshToken: "rt_secret_refresh_token_value_12345",
		AccessToken:  "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJhdWQiOiJtcGNfcmVzb3VyY2UifQ.fakesig2",
		ExpiresAt:    now,
	}

	packed := PackPortalCookie(original)

	got, err := UnpackPortalCookie(packed)
	if err != nil {
		t.Fatalf("UnpackPortalCookie: unexpected error: %v", err)
	}

	if got.IDToken != original.IDToken {
		t.Errorf("IDToken mismatch: got %q, want %q", got.IDToken, original.IDToken)
	}
	if got.RefreshToken != original.RefreshToken {
		t.Errorf("RefreshToken mismatch: got %q, want %q", got.RefreshToken, original.RefreshToken)
	}
	if got.AccessToken != original.AccessToken {
		t.Errorf("AccessToken mismatch: got %q, want %q", got.AccessToken, original.AccessToken)
	}
	if !got.ExpiresAt.Equal(now) {
		t.Errorf("ExpiresAt mismatch: got %v, want %v", got.ExpiresAt, now)
	}
}

func TestPackPortalCookie_EmptyFields(t *testing.T) {
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	original := PortalCookieValue{
		IDToken:      "",
		RefreshToken: "rt_nonempty",
		AccessToken:  "",
		ExpiresAt:    now,
	}

	packed := PackPortalCookie(original)

	got, err := UnpackPortalCookie(packed)
	if err != nil {
		t.Fatalf("UnpackPortalCookie: unexpected error: %v", err)
	}

	if got.IDToken != "" {
		t.Errorf("IDToken should be empty, got %q", got.IDToken)
	}
	if got.RefreshToken != "rt_nonempty" {
		t.Errorf("RefreshToken mismatch: got %q", got.RefreshToken)
	}
	if got.AccessToken != "" {
		t.Errorf("AccessToken should be empty, got %q", got.AccessToken)
	}
}

func TestPackPortalCookie_SizeSmallerThanJSON(t *testing.T) {
	// Simulate realistic JWT token sizes
	jwtHeaderPayload := "eyJhbGciOiJSUzI1NiJ9."
	jwtClaims := "eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiZW1haWwiOiJqb2huQGV4YW1wbGUuY29tIn0."
	jwtSig := "NHVaYe26MbtOYhSKkoKYdFVomY4ir3eoPrzA-_Z5J6A"

	// Build a realistic ~800 byte JWT
	idToken := jwtHeaderPayload + jwtClaims + strings.Repeat("a", 700) + jwtSig
	accessToken := jwtHeaderPayload + strings.Repeat("x", 600) + jwtSig

	v := PortalCookieValue{
		IDToken:      idToken,
		RefreshToken: "rt_",
		AccessToken:  accessToken,
		ExpiresAt:    time.Now(),
	}

	packed := PackPortalCookie(v)

	// Binary packed should be roughly sum of string lengths + 14 bytes framing
	expectedMinSize := len(v.IDToken) + len(v.RefreshToken) + len(v.AccessToken) + 14
	if len(packed) < expectedMinSize || len(packed) > expectedMinSize+5 {
		t.Errorf("packed size %d, expected ~%d", len(packed), expectedMinSize)
	}

	// Verify round-trip
	got, err := UnpackPortalCookie(packed)
	if err != nil {
		t.Fatalf("round-trip failed: %v", err)
	}
	if got.IDToken != idToken {
		t.Error("IDToken round-trip mismatch")
	}
	if got.AccessToken != accessToken {
		t.Error("AccessToken round-trip mismatch")
	}
}

func TestUnPackPortalCookie_TruncatedData(t *testing.T) {
	// Only a partial uint16 — not enough bytes to read the first length prefix
	_, err := UnpackPortalCookie([]byte{0x01})
	if err == nil {
		t.Fatal("expected error for truncated data")
	}
}

func TestPackPortalCookie_Deterministic(t *testing.T) {
	v := PortalCookieValue{
		IDToken:      "id1",
		RefreshToken: "rt1",
		AccessToken:  "at1",
		ExpiresAt:    time.Unix(1718400000, 0),
	}

	packed1 := PackPortalCookie(v)
	packed2 := PackPortalCookie(v)

	if len(packed1) != len(packed2) {
		t.Fatal("PackPortalCookie is not deterministic")
	}
	for i := range packed1 {
		if packed1[i] != packed2[i] {
			t.Fatalf("PackPortalCookie is not deterministic at byte %d", i)
		}
	}
}

func TestPackCookieV2_RoundTrip(t *testing.T) {
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	original := CookiePayloadV2{
		Version:        CookieVersionV2,
		AccessToken:    "at_v2_abc123",
		Subject:        "sub_sa_001",
		Email:          "sa@example.com",
		FirstName:      "Service",
		LastName:       "Account",
		Roles:          []string{"admin", "member"},
		ActorUserID:    "actor_001",
		ActorEmail:     "actor@example.com",
		ActorFirstName: "Alice",
		ActorLastName:  "Admin",
		Reason:         "debugging",
		UserType:       ImpersonatedUserTypeServiceAccount,
		Impersonated:   true,
		ExpiresAt:      now,
	}

	packed := PackCookieV2(original)
	got, err := UnpackCookieV2(packed)
	if err != nil {
		t.Fatalf("UnpackCookieV2: unexpected error: %v", err)
	}

	if got.Version != original.Version {
		t.Errorf("Version mismatch: got %d, want %d", got.Version, original.Version)
	}
	if got.AccessToken != original.AccessToken {
		t.Errorf("AccessToken mismatch: got %q, want %q", got.AccessToken, original.AccessToken)
	}
	if got.Subject != original.Subject {
		t.Errorf("Subject mismatch: got %q, want %q", got.Subject, original.Subject)
	}
	if got.Email != original.Email {
		t.Errorf("Email mismatch: got %q, want %q", got.Email, original.Email)
	}
	if got.FirstName != original.FirstName {
		t.Errorf("FirstName mismatch: got %q, want %q", got.FirstName, original.FirstName)
	}
	if got.LastName != original.LastName {
		t.Errorf("LastName mismatch: got %q, want %q", got.LastName, original.LastName)
	}
	if len(got.Roles) != len(original.Roles) || got.Roles[0] != original.Roles[0] || got.Roles[1] != original.Roles[1] {
		t.Errorf("Roles mismatch: got %v, want %v", got.Roles, original.Roles)
	}
	if got.ActorUserID != original.ActorUserID {
		t.Errorf("ActorUserID mismatch: got %q, want %q", got.ActorUserID, original.ActorUserID)
	}
	if got.ActorEmail != original.ActorEmail {
		t.Errorf("ActorEmail mismatch: got %q, want %q", got.ActorEmail, original.ActorEmail)
	}
	if got.ActorFirstName != original.ActorFirstName {
		t.Errorf("ActorFirstName mismatch: got %q, want %q", got.ActorFirstName, original.ActorFirstName)
	}
	if got.ActorLastName != original.ActorLastName {
		t.Errorf("ActorLastName mismatch: got %q, want %q", got.ActorLastName, original.ActorLastName)
	}
	if got.Reason != original.Reason {
		t.Errorf("Reason mismatch: got %q, want %q", got.Reason, original.Reason)
	}
	if got.UserType != original.UserType {
		t.Errorf("UserType mismatch: got %d, want %d", got.UserType, original.UserType)
	}
	if got.Impersonated != original.Impersonated {
		t.Errorf("Impersonated mismatch: got %v, want %v", got.Impersonated, original.Impersonated)
	}
	if !got.ExpiresAt.Equal(now) {
		t.Errorf("ExpiresAt mismatch: got %v, want %v", got.ExpiresAt, now)
	}
}

func TestPackCookieV2_EmptyFields(t *testing.T) {
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	original := CookiePayloadV2{
		Version:      CookieVersionV2,
		AccessToken:  "",
		Subject:      "",
		Email:        "",
		FirstName:    "",
		LastName:     "",
		Roles:        nil,
		UserType:     ImpersonatedUserTypeUser,
		Impersonated: false,
		ExpiresAt:    now,
	}

	packed := PackCookieV2(original)
	got, err := UnpackCookieV2(packed)
	if err != nil {
		t.Fatalf("UnpackCookieV2: unexpected error: %v", err)
	}

	if got.AccessToken != "" {
		t.Errorf("AccessToken should be empty, got %q", got.AccessToken)
	}
	if got.Subject != "" {
		t.Errorf("Subject should be empty, got %q", got.Subject)
	}
	if got.Email != "" {
		t.Errorf("Email should be empty, got %q", got.Email)
	}
	if len(got.Roles) != 0 {
		t.Errorf("Roles should be empty, got %v", got.Roles)
	}
	if got.Impersonated {
		t.Error("Impersonated should be false")
	}
	if !got.ExpiresAt.Equal(now) {
		t.Errorf("ExpiresAt mismatch: got %v, want %v", got.ExpiresAt, now)
	}
}

func TestUnpackCookieV2_UnknownVersion(t *testing.T) {
	data := []byte{0x02} // version 2 is not supported
	_, err := UnpackCookieV2(data)
	if err == nil {
		t.Fatal("expected error for unknown version")
	}
	if !strings.Contains(err.Error(), "unsupported cookie version") {
		t.Fatalf("expected 'unsupported cookie version' error, got: %v", err)
	}
}

func TestUnpackCookieV2_Truncated(t *testing.T) {
	// Only version byte — not enough to read the first length prefix
	data := []byte{CookieVersionV2, 0x01}
	_, err := UnpackCookieV2(data)
	if err == nil {
		t.Fatal("expected error for truncated data")
	}
}

func TestPackCookieV2_Deterministic(t *testing.T) {
	v := CookiePayloadV2{
		Version:     CookieVersionV2,
		AccessToken: "at1",
		Subject:     "sub1",
		Email:       "u@example.com",
		FirstName:   "Alice",
		LastName:    "Admin",
		Roles:       []string{"admin", "member"},
		ExpiresAt:   time.Unix(1718400000, 0),
	}

	packed1 := PackCookieV2(v)
	packed2 := PackCookieV2(v)

	if len(packed1) != len(packed2) {
		t.Fatal("PackCookieV2 is not deterministic")
	}
	for i := range packed1 {
		if packed1[i] != packed2[i] {
			t.Fatalf("PackCookieV2 is not deterministic at byte %d", i)
		}
	}
}
