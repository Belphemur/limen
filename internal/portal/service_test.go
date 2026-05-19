package portal

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/zitadel/oidc/v3/pkg/oidc"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/portal/portalv1"
	"github.com/belphemur/limen/internal/portal/portalv1/portalv1connect"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
)

// fakeResolver lets each test pin a canned outcome onto the Connect
// session interceptor without standing up a real Zitadel.
type fakeResolver struct {
	sess      *UserSession
	setCookie *http.Cookie
	err       error
	calls     int
}

func (f *fakeResolver) resolve(_ context.Context, _ http.Header, _ string) (*UserSession, *http.Cookie, error) {
	f.calls++
	return f.sess, f.setCookie, f.err
}

// mountFixture builds a Connect server with the portal interceptor stack
// wired against a fake resolver and a single hard-coded tenant pinned
// to ctx via tenancy.RequireTenant's equivalent (we use a chi-less
// middleware to set ctx).
func mountFixture(t *testing.T, resolver SessionResolver, tenantPublicID string) *httptest.Server {
	t.Helper()
	svc := &Service{
		store:    nil, // not exercised — none of these tests touch the DB.
		resolver: resolver,
		logger:   zap.NewNop(),
	}
	_, h := svc.Handler()

	// Wrap the Connect handler so we can pin a fake tenant on ctx —
	// production has tenancy.RequireTenant doing this via the chi route.
	tenant := &storage.Tenant{Base: storage.Base{PublicID: tenantPublicID}}
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(tenancy.WithTenant(r.Context(), tenant))
		h.ServeHTTP(w, r)
	})
	return httptest.NewServer(wrapped)
}

func newClient(t *testing.T, srv *httptest.Server) portalv1connect.PortalServiceClient {
	t.Helper()
	return portalv1connect.NewPortalServiceClient(srv.Client(), srv.URL)
}

func TestGetSession_NoCookie_ReturnsLoginURL(t *testing.T) {
	r := &fakeResolver{err: errors.New("no cookie")}
	srv := mountFixture(t, r.resolve, "tnt_test")
	defer srv.Close()

	resp, err := newClient(t, srv).GetSession(context.Background(), connect.NewRequest(&portalv1.GetSessionRequest{}))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if resp.Msg.Authenticated {
		t.Fatalf("expected authenticated=false, got true")
	}
	if !strings.Contains(resp.Msg.LoginUrl, "/t/tnt_test/auth/login") {
		t.Fatalf("login_url missing tenant: %q", resp.Msg.LoginUrl)
	}
}

func TestGetSession_WithSession_ReturnsUser(t *testing.T) {
	r := &fakeResolver{sess: &UserSession{Subject: "user-1", Email: "u@example.com", Name: "U", Roles: []string{"member"}}}
	srv := mountFixture(t, r.resolve, "tnt_test")
	defer srv.Close()

	resp, err := newClient(t, srv).GetSession(context.Background(), connect.NewRequest(&portalv1.GetSessionRequest{}))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !resp.Msg.Authenticated {
		t.Fatalf("expected authenticated=true")
	}
	if resp.Msg.User == nil || resp.Msg.User.Subject != "user-1" {
		t.Fatalf("unexpected user: %+v", resp.Msg.User)
	}
}

func TestAuthenticatedRPC_RejectsMissingSession(t *testing.T) {
	r := &fakeResolver{err: errors.New("no cookie")}
	srv := mountFixture(t, r.resolve, "tnt_test")
	defer srv.Close()

	_, err := newClient(t, srv).ListUpstreams(context.Background(), connect.NewRequest(&portalv1.ListUpstreamsRequest{}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := connect.CodeOf(err); got != connect.CodeUnauthenticated {
		t.Fatalf("want CodeUnauthenticated, got %v", got)
	}
}

func TestAuthenticatedRPC_RejectsInsufficientRole(t *testing.T) {
	// No "member" role — only "viewer", which is not in the role table
	// and therefore satisfies nothing.
	r := &fakeResolver{sess: &UserSession{Subject: "u", Roles: []string{"viewer"}}}
	srv := mountFixture(t, r.resolve, "tnt_test")
	defer srv.Close()

	_, err := newClient(t, srv).ListUpstreams(context.Background(), connect.NewRequest(&portalv1.ListUpstreamsRequest{}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := connect.CodeOf(err); got != connect.CodePermissionDenied {
		t.Fatalf("want CodePermissionDenied, got %v", got)
	}
}

func TestAuthenticatedRPC_MemberPassesInterceptors(t *testing.T) {
	r := &fakeResolver{sess: &UserSession{Subject: "u", Roles: []string{"member"}}}
	srv := mountFixture(t, r.resolve, "tnt_test")
	defer srv.Close()

	// ListMCPClients still stubs Unimplemented in slice 3 — that's the
	// proof the interceptor stack let the call through (the upstream
	// RPCs now reach the upstream service and would panic on the nil
	// dep, so we use a still-unimplemented RPC as the canary).
	_, err := newClient(t, srv).ListMCPClients(context.Background(), connect.NewRequest(&portalv1.ListMCPClientsRequest{}))
	if err == nil {
		t.Fatal("expected Unimplemented error from stub, got nil")
	}
	if got := connect.CodeOf(err); got != connect.CodeUnimplemented {
		t.Fatalf("want CodeUnimplemented (proves interceptors passed), got %v", got)
	}
}

func TestTenancyInterceptor_RejectsMissingTenant(t *testing.T) {
	// Bypass mountFixture's tenant pinning by hitting the Connect
	// handler directly without it.
	r := &fakeResolver{}
	svc := &Service{resolver: r.resolve, logger: zap.NewNop()}
	_, h := svc.Handler()
	srv := httptest.NewServer(h)
	defer srv.Close()

	_, err := newClient(t, srv).GetSession(context.Background(), connect.NewRequest(&portalv1.GetSessionRequest{}))
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
		{"empty satisfies any", nil, RoleAny, true},
		{"empty fails member", nil, RoleMember, false},
		{"member fails owner", []string{"member"}, RoleOwner, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := satisfies(tc.roles, tc.need); got != tc.want {
				t.Errorf("satisfies(%v, %v) = %v, want %v", tc.roles, tc.need, got, tc.want)
			}
		})
	}
}

func TestProcedureMethod(t *testing.T) {
	cases := map[string]string{
		"/limen.portal.v1.PortalService/GetSession":    "GetSession",
		"/limen.portal.v1.PortalService/ListUpstreams": "ListUpstreams",
		"GetSession": "GetSession",
	}
	for in, want := range cases {
		if got := procedureMethod(in); got != want {
			t.Errorf("procedureMethod(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClaimsToSession_BuildsNameFromGivenFamily(t *testing.T) {
	c := &oidc.IDTokenClaims{}
	c.GivenName = "Ada"
	c.FamilyName = "Lovelace"
	c.Email = "ada@example.com"
	s := claimsToSession(c)
	if s == nil || s.Name != "Ada Lovelace" {
		t.Fatalf("name mismatch: %+v", s)
	}
}

func TestClaimsToSession_Nil(t *testing.T) {
	if s := claimsToSession(nil); s != nil {
		t.Fatalf("expected nil session, got %+v", s)
	}
}
