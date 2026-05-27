package session

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"go.uber.org/zap"

	sessionv1 "github.com/belphemur/limen/internal/session/sessionv1"
	"github.com/belphemur/limen/internal/session/sessionv1/sessionv1connect"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
)

type fakeResolver struct {
	sess      *UserSession
	setCookie *http.Cookie
	err       error
}

func (f *fakeResolver) resolve(_ context.Context, _ http.Header, _ string) (*UserSession, *http.Cookie, error) {
	return f.sess, f.setCookie, f.err
}

func mountFixture(t *testing.T, r Resolver, tenant *storage.Tenant) *httptest.Server {
	t.Helper()
	svc := NewService(r, nil, zap.NewNop())
	_, h := svc.Handler()
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if tenant != nil {
			req = req.WithContext(tenancy.WithTenant(req.Context(), tenant))
		}
		h.ServeHTTP(w, req)
	})
	return httptest.NewServer(wrapped)
}

func client(srv *httptest.Server) sessionv1connect.SessionServiceClient {
	return sessionv1connect.NewSessionServiceClient(srv.Client(), srv.URL)
}

func TestGetSession_ReturnsTenantUserRole(t *testing.T) {
	tenant := &storage.Tenant{Base: storage.Base{PublicID: "tnt_test"}, Name: "Acme"}
	res := &fakeResolver{sess: &UserSession{
		Subject:   "user-1",
		Email:     "ada@example.com",
		FirstName: "Ada",
		LastName:  "Lovelace",
		Roles:     []string{"member", "owner"},
	}}
	srv := mountFixture(t, res.resolve, tenant)
	defer srv.Close()

	resp, err := client(srv).GetSession(context.Background(), connect.NewRequest(&sessionv1.GetSessionRequest{}))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if resp.Msg.Tenant.GetPublicId() != "tnt_test" || resp.Msg.Tenant.GetName() != "Acme" {
		t.Fatalf("tenant mismatch: %+v", resp.Msg.Tenant)
	}
	if resp.Msg.User.GetId() != "user-1" || resp.Msg.User.GetEmail() != "ada@example.com" {
		t.Fatalf("user mismatch: %+v", resp.Msg.User)
	}
	if resp.Msg.User.GetFirstName() != "Ada" || resp.Msg.User.GetLastName() != "Lovelace" {
		t.Fatalf("name mismatch: %+v", resp.Msg.User)
	}
	if resp.Msg.GetRole() != sessionv1.Role_ROLE_OWNER {
		t.Fatalf("want ROLE_OWNER (highest), got %v", resp.Msg.GetRole())
	}
}

func TestGetSession_NoCookie_Unauthenticated(t *testing.T) {
	tenant := &storage.Tenant{Base: storage.Base{PublicID: "tnt_test"}}
	res := &fakeResolver{err: errors.New("no cookie")}
	srv := mountFixture(t, res.resolve, tenant)
	defer srv.Close()

	_, err := client(srv).GetSession(context.Background(), connect.NewRequest(&sessionv1.GetSessionRequest{}))
	if err == nil {
		t.Fatal("expected unauthenticated error, got nil")
	}
	if got := connect.CodeOf(err); got != connect.CodeUnauthenticated {
		t.Fatalf("want CodeUnauthenticated, got %v", got)
	}
}

func TestGetSession_MissingTenant_NotFound(t *testing.T) {
	res := &fakeResolver{sess: &UserSession{Subject: "u"}}
	srv := mountFixture(t, res.resolve, nil)
	defer srv.Close()

	_, err := client(srv).GetSession(context.Background(), connect.NewRequest(&sessionv1.GetSessionRequest{}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Fatalf("want CodeNotFound, got %v", got)
	}
}

func TestSatisfies_RoleHierarchy(t *testing.T) {
	cases := []struct {
		name  string
		roles []string
		need  Role
		want  bool
	}{
		{"member satisfies member", []string{"member"}, RoleMember, true},
		{"owner satisfies member", []string{"owner"}, RoleMember, true},
		{"admin satisfies member", []string{"admin"}, RoleMember, true},
		{"viewer satisfies none", []string{"viewer"}, RoleMember, false},
		{"empty fails member", nil, RoleMember, false},
		{"member fails owner", []string{"member"}, RoleOwner, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Satisfies(tc.roles, tc.need); got != tc.want {
				t.Errorf("Satisfies(%v, %v) = %v, want %v", tc.roles, tc.need, got, tc.want)
			}
		})
	}
}

func TestHighestRole(t *testing.T) {
	cases := map[string]struct {
		roles []string
		want  sessionv1.Role
	}{
		"none":    {nil, sessionv1.Role_ROLE_UNSPECIFIED},
		"member":  {[]string{"member"}, sessionv1.Role_ROLE_MEMBER},
		"admin":   {[]string{"member", "admin"}, sessionv1.Role_ROLE_ADMIN},
		"owner":   {[]string{"member", "owner"}, sessionv1.Role_ROLE_OWNER},
		"unknown": {[]string{"viewer"}, sessionv1.Role_ROLE_UNSPECIFIED},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := HighestRole(tc.roles); got != tc.want {
				t.Errorf("HighestRole(%v) = %v, want %v", tc.roles, got, tc.want)
			}
		})
	}
}

func TestProcedureMethod(t *testing.T) {
	cases := map[string]string{
		"/limen.session.v1.SessionService/GetSession":  "GetSession",
		"/limen.portal.v1.PortalService/ListUpstreams": "ListUpstreams",
		"GetSession": "GetSession",
	}
	for in, want := range cases {
		if got := ProcedureMethod(in); got != want {
			t.Errorf("ProcedureMethod(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClaimsToSession_BuildsFirstLastFromGivenFamily(t *testing.T) {
	c := &oidc.IDTokenClaims{}
	c.GivenName = "Ada"
	c.FamilyName = "Lovelace"
	c.Email = "ada@example.com"
	s := claimsToSession(c)
	if s == nil || s.FirstName != "Ada" || s.LastName != "Lovelace" {
		t.Fatalf("name mismatch: %+v", s)
	}
}

func TestClaimsToSession_SplitsCombinedName(t *testing.T) {
	c := &oidc.IDTokenClaims{}
	c.Name = "Grace Hopper"
	s := claimsToSession(c)
	if s == nil || s.FirstName != "Grace" || s.LastName != "Hopper" {
		t.Fatalf("name split mismatch: %+v", s)
	}
}

func TestClaimsToSession_Nil(t *testing.T) {
	if s := claimsToSession(nil); s != nil {
		t.Fatalf("expected nil session, got %+v", s)
	}
}

func TestExtractRolesFromClaims_WithSyntheticRoleMap(t *testing.T) {
	c := &oidc.IDTokenClaims{
		Claims: map[string]any{
			"urn:zitadel:iam:org:project:roles": map[string]any{
				"admin":  map[string]any{},
				"member": map[string]any{},
			},
		},
	}
	got := extractRolesFromClaims(c)
	if len(got) != 2 {
		t.Fatalf("want 2 roles, got %v", got)
	}
	seen := map[string]bool{}
	for _, r := range got {
		seen[r] = true
	}
	if !seen["admin"] || !seen["member"] {
		t.Fatalf("missing role: %v", got)
	}
}
