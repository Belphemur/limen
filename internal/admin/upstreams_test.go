package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	adminv1 "github.com/belphemur/limen/internal/admin/adminv1"
	"github.com/belphemur/limen/internal/admin/adminv1/adminv1connect"
	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/session"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/storage/storagetest"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/upstream"
	"github.com/belphemur/limen/internal/upstream/none"
	"github.com/belphemur/limen/internal/upstream/statichdr"
)

func newAdminCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	var key crypto.Key
	for i := range key {
		key[i] = byte(i + 7)
	}
	c, err := crypto.NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	return c
}

// mountReal stands up a Postgres-backed AdminService whose handler is
// reachable via the returned client. The supplied roles drive
// RoleInterceptor; subject is fixed to "sub-admin-test" so the
// resolver lines up with the seeded storage.User row.
func mountReal(t *testing.T, roles []string) (adminv1connect.AdminServiceClient, *storage.Tenant, *storage.User) {
	t.Helper()
	store := storagetest.OpenMigrated(t)
	cipher := newAdminCipher(t)
	crypto.SetCipher(cipher)
	t.Cleanup(func() { crypto.SetCipher(nil) })

	ctx := context.Background()
	tx, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		t.Fatalf("super session: %v", err)
	}
	tenant := &storage.Tenant{Name: "Acme", ZitadelOrgID: "z-admin-test"}
	if err := tx.Create(tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	user := &storage.User{TenantID: tenant.ID, Email: "owner@example.com", Name: "owner", ZitadelSubject: "sub-admin-test"}
	if err := tx.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	registry := upstream.NewRegistry()
	registry.Register(none.New(nil))
	registry.Register(statichdr.New(store, cipher, nil))
	upstreamSvc := upstream.NewService(store, registry)

	resolver := func(_ context.Context, _ http.Header, _ string) (*session.UserSession, *http.Cookie, error) {
		return &session.UserSession{Subject: "sub-admin-test", Email: "owner@example.com", Roles: roles}, nil, nil
	}
	svc := NewService(store, upstreamSvc, resolver, zap.NewNop())
	_, h := svc.Handler()
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(tenancy.WithTenant(r.Context(), tenant))
		h.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(wrapped)
	t.Cleanup(srv.Close)
	return adminv1connect.NewAdminServiceClient(srv.Client(), srv.URL), tenant, user
}

func TestAdmin_CreateUpstream_Succeeds(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	c, _, _ := mountReal(t, []string{"admin"})
	resp, err := c.CreateUpstream(context.Background(), connect.NewRequest(&adminv1.CreateUpstreamRequest{
		Name:         "u1",
		DisplayName:  "U1",
		McpUrl:       "https://example.com/mcp",
		StrategyType: string(upstream.StrategyNone),
	}))
	if err != nil {
		t.Fatalf("CreateUpstream: %v", err)
	}
	if got := resp.Msg.GetUpstream().GetName(); got != "u1" {
		t.Errorf("name = %q, want u1", got)
	}
	if resp.Msg.GetRequiresAdminLink() {
		t.Errorf("requires_admin_link = true, want false for none strategy")
	}
}

func TestAdmin_CreateUpstream_DuplicateName_AlreadyExists(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	c, _, _ := mountReal(t, []string{"admin"})
	req := &adminv1.CreateUpstreamRequest{
		Name:         "dup",
		McpUrl:       "https://example.com/mcp",
		StrategyType: string(upstream.StrategyNone),
	}
	if _, err := c.CreateUpstream(context.Background(), connect.NewRequest(req)); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := c.CreateUpstream(context.Background(), connect.NewRequest(req))
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if got := connect.CodeOf(err); got != connect.CodeAlreadyExists {
		t.Fatalf("code = %v, want AlreadyExists", got)
	}
}

func TestAdmin_CreateUpstream_BadDefaults_InvalidArgumentWithFieldPath(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	c, _, _ := mountReal(t, []string{"admin"})
	_, err := c.CreateUpstream(context.Background(), connect.NewRequest(&adminv1.CreateUpstreamRequest{
		Name:         "bad",
		McpUrl:       "https://example.com/mcp",
		StrategyType: string(upstream.StrategyNone),
		DefaultsJson: `["nope"]`,
	}))
	if err == nil {
		t.Fatal("want error, got nil")
	}
	cErr := new(connect.Error)
	if !errorsAs(err, &cErr) {
		t.Fatalf("want *connect.Error, got %T", err)
	}
	if cErr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", cErr.Code())
	}
	var sawDetail bool
	for _, d := range cErr.Details() {
		v, err := d.Value()
		if err != nil {
			continue
		}
		if s, ok := asStringMap(v); ok && s["path"] == "defaults_json" {
			sawDetail = true
		}
	}
	if !sawDetail {
		t.Errorf("missing field_path detail for defaults_json")
	}
}

func TestAdmin_UpdateUpstream_Patches(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	c, _, _ := mountReal(t, []string{"admin"})
	created, err := c.CreateUpstream(context.Background(), connect.NewRequest(&adminv1.CreateUpstreamRequest{
		Name:         "u2",
		DisplayName:  "Old",
		McpUrl:       "https://example.com/mcp",
		StrategyType: string(upstream.StrategyNone),
	}))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	resp, err := c.UpdateUpstream(context.Background(), connect.NewRequest(&adminv1.UpdateUpstreamRequest{
		PublicId:    created.Msg.GetUpstream().GetPublicId(),
		DisplayName: "New",
	}))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := resp.Msg.GetUpstream().GetDisplayName(); got != "New" {
		t.Errorf("display_name = %q, want New", got)
	}
}

func TestAdmin_DeleteUpstream_NotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	c, _, _ := mountReal(t, []string{"admin"})
	_, err := c.DeleteUpstream(context.Background(), connect.NewRequest(&adminv1.DeleteUpstreamRequest{
		PublicId: "ups_doesnotexist",
	}))
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Fatalf("code = %v, want NotFound", got)
	}
}

func TestAdmin_ReindexCatalog_UnknownUpstream_NotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	c, _, _ := mountReal(t, []string{"admin"})
	_, err := c.ReindexUpstreamCatalog(context.Background(), connect.NewRequest(&adminv1.ReindexUpstreamCatalogRequest{
		PublicId: "ups_unknown",
	}))
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Fatalf("code = %v, want NotFound", got)
	}
}

// errorsAs is a tiny local errors.As shim so we don't drag in the
// dependency just for one cast. Mirrors stdlib semantics for our
// concrete *connect.Error case.
func errorsAs(err error, target **connect.Error) bool {
	for err != nil {
		if ce, ok := err.(*connect.Error); ok {
			*target = ce
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// asStringMap pulls a flat string->string view out of a
// google.protobuf.Struct value attached as a Connect error detail.
func asStringMap(v any) (map[string]string, bool) {
	type structLike interface {
		AsMap() map[string]any
	}
	sl, ok := v.(structLike)
	if !ok {
		return nil, false
	}
	out := map[string]string{}
	for k, raw := range sl.AsMap() {
		if s, ok := raw.(string); ok {
			out[k] = s
		}
	}
	return out, true
}
