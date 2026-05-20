package portal

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/portal/portalv1"
	"github.com/belphemur/limen/internal/portal/portalv1/portalv1connect"
	"github.com/belphemur/limen/internal/session"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
)

// fakeResolver lets each test pin a canned outcome onto the session
// interceptor without standing up a real Zitadel.
type fakeResolver struct {
	sess      *session.UserSession
	setCookie *http.Cookie
	err       error
	calls     int
}

func (f *fakeResolver) resolve(_ context.Context, _ http.Header, _ string) (*session.UserSession, *http.Cookie, error) {
	f.calls++
	return f.sess, f.setCookie, f.err
}

// fakeAppManager satisfies portal.AppManager for tests that need to
// reach RevokeMCPClient but don't want to call a real Zitadel.
type fakeAppManager struct{ err error }

func (f fakeAppManager) DeleteOIDCApp(_ context.Context, _, _, _ string) error {
	return f.err
}

// mountFixture builds a Connect server with the portal interceptor
// stack wired against a fake resolver and a single hard-coded tenant
// pinned to ctx via tenancy.RequireTenant's equivalent.
func mountFixture(t *testing.T, resolver session.Resolver, tenantPublicID string) *httptest.Server {
	t.Helper()
	svc := &Service{
		store:    nil, // not exercised — none of these tests touch the DB.
		apps:     fakeAppManager{},
		resolver: resolver,
		logger:   zap.NewNop(),
	}
	_, h := svc.Handler()

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
	// "viewer" is not in the role table and therefore satisfies nothing.
	r := &fakeResolver{sess: &session.UserSession{Subject: "u", Roles: []string{"viewer"}}}
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
	r := &fakeResolver{sess: &session.UserSession{Subject: "u", Roles: []string{"member"}}}
	srv := mountFixture(t, r.resolve, "tnt_test")
	defer srv.Close()

	// RevokeMCPClient with an empty public_id short-circuits in the
	// handler before touching store/apps — InvalidArgument is the
	// signal the interceptor stack let the call through.
	_, err := newClient(t, srv).RevokeMCPClient(context.Background(), connect.NewRequest(&portalv1.RevokeMCPClientRequest{}))
	if err == nil {
		t.Fatal("expected InvalidArgument error from handler, got nil")
	}
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("want CodeInvalidArgument (proves interceptors passed), got %v", got)
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

	_, err := newClient(t, srv).ListUpstreams(context.Background(), connect.NewRequest(&portalv1.ListUpstreamsRequest{}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Fatalf("want CodeNotFound, got %v", got)
	}
}
