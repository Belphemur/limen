package portal

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	"github.com/belphemur/limen/internal/portal/portalv1"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/storage/storagetest"
	"github.com/belphemur/limen/internal/tenancy"
)

// recordingAppManager captures the (org, project, app) tuple the
// handler asked Zitadel to delete, and optionally returns an error.
type recordingAppManager struct {
	lastOrg, lastProject, lastApp string
	calls                         int
	err                           error
}

func (r *recordingAppManager) DeleteOIDCApp(_ context.Context, orgID, projectID, appID string) error {
	r.calls++
	r.lastOrg, r.lastProject, r.lastApp = orgID, projectID, appID
	return r.err
}

func newPortalServiceForTest(t *testing.T, store *storage.Store, apps AppManager) *Service {
	t.Helper()
	return &Service{
		store:    store,
		apps:     apps,
		resolver: nil,
		logger:   zap.NewNop(),
	}
}

func seedMCPClient(t *testing.T, store *storage.Store, tenantID int64, publicID, zitadelAppID, zitadelProjectID string) {
	t.Helper()
	tx, commit, err := store.Session(storage.WithSuperuser(context.Background()))
	if err != nil {
		t.Fatalf("super session: %v", err)
	}
	row := &storage.ZitadelApp{
		Base:             storage.Base{PublicID: publicID},
		TenantID:         tenantID,
		ZitadelAppID:     zitadelAppID,
		ZitadelProjectID: zitadelProjectID,
		ClientID:         "client-" + publicID,
		Name:             "client-" + publicID,
	}
	if err := tx.Create(row).Error; err != nil {
		t.Fatalf("seed app: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestListMCPClients_TenantScoped(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	store := storagetest.OpenMigrated(t)
	apps := &recordingAppManager{}
	svc := newPortalServiceForTest(t, store, apps)

	ctx := context.Background()
	adminTx, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		t.Fatalf("super session: %v", err)
	}
	tA := &storage.Tenant{Name: "tenant-a", ZitadelOrgID: "org-A"}
	tB := &storage.Tenant{Name: "tenant-b", ZitadelOrgID: "org-B"}
	if err := adminTx.Create(tA).Error; err != nil {
		t.Fatalf("create tenant A: %v", err)
	}
	if err := adminTx.Create(tB).Error; err != nil {
		t.Fatalf("create tenant B: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	seedMCPClient(t, store, tA.ID, "app_A1", "zid-A1", "proj-A")
	seedMCPClient(t, store, tA.ID, "app_A2", "zid-A2", "proj-A")
	seedMCPClient(t, store, tB.ID, "app_B1", "zid-B1", "proj-B")

	ctxA := tenancy.WithTenant(ctx, tA)
	resp, err := svc.ListMCPClients(ctxA, connect.NewRequest(&portalv1.ListMCPClientsRequest{}))
	if err != nil {
		t.Fatalf("ListMCPClients(A): %v", err)
	}
	if got := len(resp.Msg.Clients); got != 2 {
		t.Fatalf("tenant A should see 2 clients, got %d", got)
	}
	ids := map[string]bool{}
	for _, c := range resp.Msg.Clients {
		ids[c.PublicId] = true
	}
	if !ids["app_A1"] || !ids["app_A2"] {
		t.Fatalf("tenant A client set wrong: %#v", ids)
	}
	if ids["app_B1"] {
		t.Fatal("tenant A leaked tenant B's client")
	}
}

func TestRevokeMCPClient_HappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	store := storagetest.OpenMigrated(t)
	apps := &recordingAppManager{}
	svc := newPortalServiceForTest(t, store, apps)

	ctx := context.Background()
	adminTx, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		t.Fatalf("super session: %v", err)
	}
	tA := &storage.Tenant{Name: "tenant-a", ZitadelOrgID: "org-A"}
	if err := adminTx.Create(tA).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	seedMCPClient(t, store, tA.ID, "app_keep", "zid-keep", "proj-A")
	seedMCPClient(t, store, tA.ID, "app_kill", "zid-kill", "proj-A")

	ctxA := tenancy.WithTenant(ctx, tA)
	if _, err := svc.RevokeMCPClient(ctxA, connect.NewRequest(&portalv1.RevokeMCPClientRequest{PublicId: "app_kill"})); err != nil {
		t.Fatalf("RevokeMCPClient: %v", err)
	}
	if apps.calls != 1 {
		t.Fatalf("Zitadel DeleteOIDCApp calls: want 1, got %d", apps.calls)
	}
	if apps.lastOrg != "org-A" || apps.lastProject != "proj-A" || apps.lastApp != "zid-kill" {
		t.Fatalf("Zitadel delete dispatched with wrong tuple: org=%q proj=%q app=%q", apps.lastOrg, apps.lastProject, apps.lastApp)
	}
	resp, err := svc.ListMCPClients(ctxA, connect.NewRequest(&portalv1.ListMCPClientsRequest{}))
	if err != nil {
		t.Fatalf("ListMCPClients: %v", err)
	}
	if len(resp.Msg.Clients) != 1 || resp.Msg.Clients[0].PublicId != "app_keep" {
		t.Fatalf("post-revoke list wrong: %#v", resp.Msg.Clients)
	}
}

func TestRevokeMCPClient_CrossTenantReturnsNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	store := storagetest.OpenMigrated(t)
	apps := &recordingAppManager{}
	svc := newPortalServiceForTest(t, store, apps)

	ctx := context.Background()
	adminTx, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		t.Fatalf("super session: %v", err)
	}
	tA := &storage.Tenant{Name: "tenant-a", ZitadelOrgID: "org-A"}
	tB := &storage.Tenant{Name: "tenant-b", ZitadelOrgID: "org-B"}
	if err := adminTx.Create(tA).Error; err != nil {
		t.Fatalf("create tenant A: %v", err)
	}
	if err := adminTx.Create(tB).Error; err != nil {
		t.Fatalf("create tenant B: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	seedMCPClient(t, store, tB.ID, "app_B1", "zid-B1", "proj-B")

	// Tenant A tries to revoke tenant B's client by forged public_id.
	ctxA := tenancy.WithTenant(ctx, tA)
	_, err = svc.RevokeMCPClient(ctxA, connect.NewRequest(&portalv1.RevokeMCPClientRequest{PublicId: "app_B1"}))
	if err == nil {
		t.Fatal("expected error, got nil — cross-tenant revoke succeeded!")
	}
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Fatalf("want CodeNotFound, got %v (err=%v)", got, err)
	}
	if apps.calls != 0 {
		t.Fatalf("Zitadel must NOT be called on a cross-tenant attempt, got %d calls", apps.calls)
	}
}

func TestRevokeMCPClient_IdempotentOnZitadelNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	store := storagetest.OpenMigrated(t)
	apps := &recordingAppManager{err: errors.New("zitadel: delete app: NotFound: app does not exist")}
	svc := newPortalServiceForTest(t, store, apps)

	ctx := context.Background()
	adminTx, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		t.Fatalf("super session: %v", err)
	}
	tA := &storage.Tenant{Name: "tenant-a", ZitadelOrgID: "org-A"}
	if err := adminTx.Create(tA).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	seedMCPClient(t, store, tA.ID, "app_orphan", "zid-orphan", "proj-A")

	ctxA := tenancy.WithTenant(ctx, tA)
	if _, err := svc.RevokeMCPClient(ctxA, connect.NewRequest(&portalv1.RevokeMCPClientRequest{PublicId: "app_orphan"})); err != nil {
		t.Fatalf("RevokeMCPClient should swallow NotFound, got: %v", err)
	}
	// Mirror is soft-deleted.
	resp, err := svc.ListMCPClients(ctxA, connect.NewRequest(&portalv1.ListMCPClientsRequest{}))
	if err != nil {
		t.Fatalf("ListMCPClients: %v", err)
	}
	if len(resp.Msg.Clients) != 0 {
		t.Fatalf("mirror not cleaned up: %#v", resp.Msg.Clients)
	}
}

func TestRevokeMCPClient_RejectsEmptyPublicID(t *testing.T) {
	store := storagetest.OpenMigrated(t)
	svc := newPortalServiceForTest(t, store, &recordingAppManager{})
	ctxA := tenancy.WithTenant(context.Background(), &storage.Tenant{Base: storage.Base{PublicID: "tnt"}, Name: "t"})
	_, err := svc.RevokeMCPClient(ctxA, connect.NewRequest(&portalv1.RevokeMCPClientRequest{PublicId: "   "}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("want CodeInvalidArgument, got %v", got)
	}
	if !strings.Contains(err.Error(), "public_id") {
		t.Fatalf("error should mention public_id: %v", err)
	}
}
