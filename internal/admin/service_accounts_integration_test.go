//go:build integration

package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	adminv1 "github.com/belphemur/limen/internal/admin/adminv1"
	"github.com/belphemur/limen/internal/admin/adminv1/adminv1connect"
	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/session"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/storage/storagetest"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/zitadel"
	userV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
)

// fakeServiceAccountDirectory is an in-memory implementation of
// ServiceAccountDirectory for integration tests.
type fakeServiceAccountDirectory struct {
	mu      sync.Mutex
	counter int
	users   map[string]zitadel.MachineUser
	tokens  map[string][]*userV2.PersonalAccessToken
	grants  []zitadel.UserGrant
	deleted map[string]bool
}

func newFakeServiceAccountDirectory() *fakeServiceAccountDirectory {
	return &fakeServiceAccountDirectory{
		users:   make(map[string]zitadel.MachineUser),
		tokens:  make(map[string][]*userV2.PersonalAccessToken),
		deleted: make(map[string]bool),
	}
}

func (f *fakeServiceAccountDirectory) cleanup() {}

func (f *fakeServiceAccountDirectory) CreateMachineUser(_ context.Context, in zitadel.MachineUser) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counter++
	id := fmt.Sprintf("user-%d", f.counter)
	f.users[id] = in
	return id, nil
}

func (f *fakeServiceAccountDirectory) GetMachineUser(_ context.Context, zitadelUserID string) (*userV2.GetUserByIDResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.users[zitadelUserID]; !ok {
		return nil, fmt.Errorf("user not found")
	}
	return &userV2.GetUserByIDResponse{}, nil
}

func (f *fakeServiceAccountDirectory) DeleteMachineUser(_ context.Context, zitadelUserID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted[zitadelUserID] = true
	delete(f.users, zitadelUserID)
	return nil
}

func (f *fakeServiceAccountDirectory) AddPersonalAccessToken(_ context.Context, zitadelUserID string, expiry *time.Time) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counter++
	tokenID := fmt.Sprintf("pat-%d", f.counter)
	token := fmt.Sprintf("token-%d", f.counter)
	f.tokens[zitadelUserID] = append(f.tokens[zitadelUserID], &userV2.PersonalAccessToken{Id: tokenID})
	return tokenID, token, nil
}

func (f *fakeServiceAccountDirectory) ListPersonalAccessTokens(_ context.Context, zitadelUserID string) ([]*userV2.PersonalAccessToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokens[zitadelUserID], nil
}

func (f *fakeServiceAccountDirectory) RemovePersonalAccessToken(_ context.Context, zitadelUserID, tokenID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	toks := f.tokens[zitadelUserID]
	for i, t := range toks {
		if t.GetId() == tokenID {
			f.tokens[zitadelUserID] = append(toks[:i], toks[i+1:]...)
			return nil
		}
	}
	return nil
}

func (f *fakeServiceAccountDirectory) AddUserGrant(_ context.Context, orgID, userID string, roleKeys []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counter++
	grantID := fmt.Sprintf("grant-%d", f.counter)
	f.grants = append(f.grants, zitadel.UserGrant{
		ID:       grantID,
		UserID:   userID,
		OrgID:    orgID,
		RoleKeys: roleKeys,
	})
	return grantID, nil
}

func (f *fakeServiceAccountDirectory) DeleteUserGrant(_ context.Context, grantID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, g := range f.grants {
		if g.ID == grantID {
			f.grants = append(f.grants[:i], f.grants[i+1:]...)
			return nil
		}
	}
	return nil
}

func (f *fakeServiceAccountDirectory) ListUserGrants(_ context.Context, orgID, userID string) ([]zitadel.UserGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []zitadel.UserGrant
	for _, g := range f.grants {
		if g.OrgID != orgID {
			continue
		}
		if userID != "" && g.UserID != userID {
			continue
		}
		out = append(out, g)
	}
	return out, nil
}

func mountRealSAWithConfig(t *testing.T, roles []string, zitadelDomain, zitadelProjectID string, cipher *crypto.Cipher) (adminv1connect.AdminServiceClient, *storage.Tenant, *storage.User, *fakeServiceAccountDirectory) {
	t.Helper()
	store := storagetest.OpenMigrated(t)

	ctx := context.Background()
	tx, commit, err := store.Session(storage.WithSuperuser(ctx))
	if err != nil {
		t.Fatalf("super session: %v", err)
	}
	tenant := &storage.Tenant{Name: "AcmeSA", ZitadelOrgID: "z-sa-test"}
	if err := tx.Create(tenant).Error; err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	user := &storage.User{TenantID: tenant.ID, Email: "owner@example.com", Name: "owner", ZitadelSubject: "sub-sa-test"}
	if err := tx.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	resolver := func(_ context.Context, _ http.Header, _ string) (*session.UserSession, *http.Cookie, error) {
		return &session.UserSession{Subject: "sub-sa-test", Email: "owner@example.com", Roles: roles, AccessToken: "access-token-123"}, nil, nil
	}

	fakeSA := newFakeServiceAccountDirectory()
	t.Cleanup(fakeSA.cleanup)

	svc := NewService(store, nil, nil, resolver, nil, nil, fakeSA, zitadelDomain, zitadelProjectID, OIDCCredentials{}, cipher, false, zap.NewNop())
	_, h := svc.Handler()
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := tenancy.WithTenant(r.Context(), tenant)
		ctx = storage.WithTenant(ctx, tenant.ID)
		r = r.WithContext(ctx)
		h.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(wrapped)
	t.Cleanup(srv.Close)

	return adminv1connect.NewAdminServiceClient(srv.Client(), srv.URL), tenant, user, fakeSA
}

func mountRealSA(t *testing.T, roles []string) (adminv1connect.AdminServiceClient, *storage.Tenant, *storage.User, *fakeServiceAccountDirectory) {
	t.Helper()
	return mountRealSAWithConfig(t, roles, "", "", nil)
}

func TestCreateServiceAccount_Success(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	c, _, _, _ := mountRealSA(t, []string{"admin"})
	resp, err := c.CreateServiceAccount(context.Background(), connect.NewRequest(&adminv1.CreateServiceAccountRequest{
		Name: "test-sa",
		Role: adminv1.ServiceAccountRole_SERVICE_ACCOUNT_ROLE_ADMIN,
	}))
	if err != nil {
		t.Fatalf("CreateServiceAccount: %v", err)
	}
	if resp.Msg.GetServiceAccount() == nil {
		t.Fatal("expected service account in response")
	}
	if resp.Msg.GetServiceAccount().GetName() != "test-sa" {
		t.Errorf("name = %q, want test-sa", resp.Msg.GetServiceAccount().GetName())
	}
	if resp.Msg.GetToken() == "" {
		t.Error("expected token in response")
	}
}

func TestCreateServiceAccount_EmptyName(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	c, _, _, _ := mountRealSA(t, []string{"admin"})
	_, err := c.CreateServiceAccount(context.Background(), connect.NewRequest(&adminv1.CreateServiceAccountRequest{
		Name: "",
		Role: adminv1.ServiceAccountRole_SERVICE_ACCOUNT_ROLE_ADMIN,
	}))
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", got)
	}
}

func TestCreateServiceAccount_OwnerRole(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	c, _, _, _ := mountRealSA(t, []string{"admin"})
	_, err := c.CreateServiceAccount(context.Background(), connect.NewRequest(&adminv1.CreateServiceAccountRequest{
		Name: "owner-role-test",
		Role: adminv1.ServiceAccountRole_SERVICE_ACCOUNT_ROLE_UNSPECIFIED,
	}))
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", got)
	}
}

func TestListServiceAccounts_ShowsCreated(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	c, _, _, _ := mountRealSA(t, []string{"admin"})
	created, err := c.CreateServiceAccount(context.Background(), connect.NewRequest(&adminv1.CreateServiceAccountRequest{
		Name: "list-test",
		Role: adminv1.ServiceAccountRole_SERVICE_ACCOUNT_ROLE_MEMBER,
	}))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	listResp, err := c.ListServiceAccounts(context.Background(), connect.NewRequest(&adminv1.ListServiceAccountsRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	var found bool
	for _, sa := range listResp.Msg.GetServiceAccounts() {
		if sa.GetPublicId() == created.Msg.GetServiceAccount().GetPublicId() {
			found = true
			break
		}
	}
	if !found {
		t.Error("created service account not found in list")
	}
}

func TestDeleteServiceAccount_MarksDeleted(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	c, _, _, _ := mountRealSA(t, []string{"admin"})
	created, err := c.CreateServiceAccount(context.Background(), connect.NewRequest(&adminv1.CreateServiceAccountRequest{
		Name: "delete-test",
		Role: adminv1.ServiceAccountRole_SERVICE_ACCOUNT_ROLE_MEMBER,
	}))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err = c.DeleteServiceAccount(context.Background(), connect.NewRequest(&adminv1.DeleteServiceAccountRequest{
		PublicId: created.Msg.GetServiceAccount().GetPublicId(),
	}))
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	listResp, err := c.ListServiceAccounts(context.Background(), connect.NewRequest(&adminv1.ListServiceAccountsRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	for _, sa := range listResp.Msg.GetServiceAccounts() {
		if sa.GetPublicId() == created.Msg.GetServiceAccount().GetPublicId() {
			t.Error("deleted service account still in list")
		}
	}
}

func TestRegenerateServiceAccountToken_ReturnsNewToken(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres")
	}
	c, _, _, _ := mountRealSA(t, []string{"admin"})
	created, err := c.CreateServiceAccount(context.Background(), connect.NewRequest(&adminv1.CreateServiceAccountRequest{
		Name: "regen-test",
		Role: adminv1.ServiceAccountRole_SERVICE_ACCOUNT_ROLE_MEMBER,
	}))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	oldToken := created.Msg.GetToken()

	regenResp, err := c.RegenerateServiceAccountToken(context.Background(), connect.NewRequest(&adminv1.RegenerateServiceAccountTokenRequest{
		PublicId: created.Msg.GetServiceAccount().GetPublicId(),
	}))
	if err != nil {
		t.Fatalf("regenerate: %v", err)
	}

	if regenResp.Msg.GetToken() == "" {
		t.Error("expected new token")
	}
	if regenResp.Msg.GetToken() == oldToken {
		t.Error("new token should differ from old token")
	}
}
