package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	adminv1 "github.com/belphemur/limen/internal/admin/adminv1"
	"github.com/belphemur/limen/internal/admin/adminv1/adminv1connect"
	"github.com/belphemur/limen/internal/session"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
)

func resolverFor(roles []string) session.Resolver {
	return func(_ context.Context, _ http.Header, _ string) (*session.UserSession, *http.Cookie, error) {
		return &session.UserSession{Subject: "u1", Email: "u@example.com", Roles: roles}, nil, nil
	}
}

func mount(t *testing.T, roles []string) adminv1connect.AdminServiceClient {
	t.Helper()
	tenant := &storage.Tenant{Base: storage.Base{PublicID: "tnt_test"}, Name: "Acme"}
	svc := NewService(nil, nil, resolverFor(roles), zap.NewNop())
	_, h := svc.Handler()
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(tenancy.WithTenant(r.Context(), tenant))
		h.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(wrapped)
	t.Cleanup(srv.Close)
	return adminv1connect.NewAdminServiceClient(srv.Client(), srv.URL)
}

// callers exercises every AdminService RPC against the supplied client.
// Returns the per-method error map keyed on the leaf procedure name —
// matches session.ProcedureMethod, matches requiredRole keys.
func callers() map[string]func(context.Context, adminv1connect.AdminServiceClient) error {
	return map[string]func(context.Context, adminv1connect.AdminServiceClient) error{
		"CreateUpstream": func(ctx context.Context, c adminv1connect.AdminServiceClient) error {
			_, err := c.CreateUpstream(ctx, connect.NewRequest(&adminv1.CreateUpstreamRequest{}))
			return err
		},
		"UpdateUpstream": func(ctx context.Context, c adminv1connect.AdminServiceClient) error {
			_, err := c.UpdateUpstream(ctx, connect.NewRequest(&adminv1.UpdateUpstreamRequest{}))
			return err
		},
		"DeleteUpstream": func(ctx context.Context, c adminv1connect.AdminServiceClient) error {
			_, err := c.DeleteUpstream(ctx, connect.NewRequest(&adminv1.DeleteUpstreamRequest{}))
			return err
		},
		"ReindexUpstreamCatalog": func(ctx context.Context, c adminv1connect.AdminServiceClient) error {
			_, err := c.ReindexUpstreamCatalog(ctx, connect.NewRequest(&adminv1.ReindexUpstreamCatalogRequest{}))
			return err
		},
		"PreviewUpstreamContext": func(ctx context.Context, c adminv1connect.AdminServiceClient) error {
			_, err := c.PreviewUpstreamContext(ctx, connect.NewRequest(&adminv1.PreviewUpstreamContextRequest{}))
			return err
		},
		"UpdateTenantSettings": func(ctx context.Context, c adminv1connect.AdminServiceClient) error {
			_, err := c.UpdateTenantSettings(ctx, connect.NewRequest(&adminv1.UpdateTenantSettingsRequest{}))
			return err
		},
		"DeleteTenant": func(ctx context.Context, c adminv1connect.AdminServiceClient) error {
			_, err := c.DeleteTenant(ctx, connect.NewRequest(&adminv1.DeleteTenantRequest{}))
			return err
		},
	}
}

// TestRequiredRole_CoversEveryHandlerMethod is the load-bearing
// contract: the requiredRole map MUST list every method on the
// generated AdminServiceHandler interface. RoleInterceptor
// default-denies unknown procedures, so a missing entry is a 403 in
// production — caught here at build time instead.
func TestRequiredRole_CoversEveryHandlerMethod(t *testing.T) {
	iface := reflect.TypeOf((*adminv1connect.AdminServiceHandler)(nil)).Elem()
	for i := 0; i < iface.NumMethod(); i++ {
		name := iface.Method(i).Name
		if _, ok := requiredRole[name]; !ok {
			t.Errorf("requiredRole missing entry for AdminServiceHandler.%s", name)
		}
	}
	if got, want := len(requiredRole), iface.NumMethod(); got != want {
		t.Errorf("requiredRole has %d entries, AdminServiceHandler has %d methods", got, want)
	}
}

// TestOwner_ReachesHandler asserts an owner session passes every
// interceptor and the handler returns CodeUnimplemented. Once a slice
// implements an RPC for real this will start returning a different
// code for that method — flip the assertion at that time.
func TestOwner_ReachesHandler_AllRPCsUnimplemented(t *testing.T) {
	c := mount(t, []string{"owner"})
	for name, call := range callers() {
		t.Run(name, func(t *testing.T) {
			err := call(context.Background(), c)
			if err == nil {
				t.Fatalf("want CodeUnimplemented, got nil")
			}
			if got := connect.CodeOf(err); got != connect.CodeUnimplemented {
				t.Fatalf("want CodeUnimplemented, got %v: %v", got, err)
			}
		})
	}
}

// TestMember_DeniedOnEveryRPC: a member session never satisfies
// admin / owner, so every method must short-circuit at the role
// interceptor.
func TestMember_DeniedOnEveryRPC(t *testing.T) {
	c := mount(t, []string{"member"})
	for name, call := range callers() {
		t.Run(name, func(t *testing.T) {
			err := call(context.Background(), c)
			if err == nil {
				t.Fatalf("want CodePermissionDenied, got nil")
			}
			if got := connect.CodeOf(err); got != connect.CodePermissionDenied {
				t.Fatalf("want CodePermissionDenied, got %v: %v", got, err)
			}
		})
	}
}

// TestAdmin_DeniedOnDeleteTenant: DeleteTenant requires the owner
// tier; admin (which satisfies every other RPC) must be rejected.
func TestAdmin_DeniedOnDeleteTenant(t *testing.T) {
	c := mount(t, []string{"admin"})
	err := callers()["DeleteTenant"](context.Background(), c)
	if err == nil {
		t.Fatal("want CodePermissionDenied, got nil")
	}
	if got := connect.CodeOf(err); got != connect.CodePermissionDenied {
		t.Fatalf("want CodePermissionDenied, got %v: %v", got, err)
	}

	// Sanity: admin still reaches every non-owner RPC.
	for name, call := range callers() {
		if name == "DeleteTenant" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			err := call(context.Background(), c)
			if got := connect.CodeOf(err); got != connect.CodeUnimplemented {
				t.Fatalf("admin should reach handler for %s, got code %v: %v", name, got, err)
			}
		})
	}
}
