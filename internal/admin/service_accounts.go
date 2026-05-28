package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"go.uber.org/zap"
	"gorm.io/gorm"

	adminv1 "github.com/belphemur/limen/internal/admin/adminv1"
	"github.com/belphemur/limen/internal/session"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/zitadel"
	userV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
)

// ServiceAccountDirectory is the slice of the Zitadel client the admin
// Service uses to read+write service accounts. Defined here (SOLID/ISP)
// so the MCP gateway hot path never transitively links the Zitadel SDK.
type ServiceAccountDirectory interface {
	CreateMachineUser(ctx context.Context, in zitadel.MachineUser) (string, error)
	DeleteMachineUser(ctx context.Context, zitadelUserID string) error
	AddPersonalAccessToken(ctx context.Context, zitadelUserID string, expiry *time.Time) (tokenID, token string, err error)
	ListPersonalAccessTokens(ctx context.Context, zitadelUserID string) ([]*userV2.PersonalAccessToken, error)
	RemovePersonalAccessToken(ctx context.Context, zitadelUserID, tokenID string) error
	AddUserGrant(ctx context.Context, orgID, userID string, roleKeys []string) (string, error)
	DeleteUserGrant(ctx context.Context, grantID string) error
	ListUserGrants(ctx context.Context, orgID, userID string) ([]zitadel.UserGrant, error)
}

func saRoleKeyFromProto(r adminv1.ServiceAccountRole) (string, bool) {
	switch r {
	case adminv1.ServiceAccountRole_SERVICE_ACCOUNT_ROLE_MEMBER:
		return zitadel.RoleKeyMember, true
	case adminv1.ServiceAccountRole_SERVICE_ACCOUNT_ROLE_ADMIN:
		return zitadel.RoleKeyAdmin, true
	default:
		return "", false
	}
}

func saRoleToProto(roleKey string) adminv1.ServiceAccountRole {
	switch roleKey {
	case zitadel.RoleKeyMember:
		return adminv1.ServiceAccountRole_SERVICE_ACCOUNT_ROLE_MEMBER
	case zitadel.RoleKeyAdmin:
		return adminv1.ServiceAccountRole_SERVICE_ACCOUNT_ROLE_ADMIN
	default:
		return adminv1.ServiceAccountRole_SERVICE_ACCOUNT_ROLE_UNSPECIFIED
	}
}

func pickHighestSARole(keys []string) string {
	best := ""
	for _, k := range keys {
		switch k {
		case zitadel.RoleKeyAdmin:
			return zitadel.RoleKeyAdmin
		case zitadel.RoleKeyMember:
			if best != zitadel.RoleKeyAdmin {
				best = zitadel.RoleKeyMember
			}
		}
	}
	return best
}

func formatTimeOpt(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func saModelToProto(sa *storage.ServiceAccount) *adminv1.ServiceAccount {
	createdById := ""
	if sa.CreatedBy != nil {
		createdById = sa.CreatedBy.PublicID
	}
	return &adminv1.ServiceAccount{
		PublicId:         sa.PublicID,
		Name:             sa.Name,
		Description:      sa.Description,
		Role:             saRoleToProto(sa.Role),
		CreatedById:      createdById,
		CreatedAt:        sa.CreatedAt.UTC().Format(time.RFC3339),
		TokenGeneratedAt: formatTimeOpt(sa.TokenGeneratedAt),
		LastUsedAt:       formatTimeOpt(sa.LastUsedAt),
	}
}

// CreateServiceAccount creates a Zitadel machine user and stores a
// local mirror row. Returns the service account and a one-time PAT.
func (s *Service) CreateServiceAccount(ctx context.Context, req *connect.Request[adminv1.CreateServiceAccountRequest]) (*connect.Response[adminv1.CreateServiceAccountResponse], error) {
	if s.serviceAccounts == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("admin: service account directory not wired"))
	}
	msg := req.Msg
	name := strings.TrimSpace(msg.GetName())
	if name == "" {
		return nil, s.invalidArg("name", "name must not be empty")
	}
	roleKey, ok := saRoleKeyFromProto(msg.GetRole())
	if !ok {
		return nil, s.invalidArg("role", "role must be MEMBER or ADMIN")
	}

	expiryDays := msg.GetExpiryDays()
	if expiryDays == 0 {
		expiryDays = 365
	}
	var expiry *time.Time
	if expiryDays > 0 {
		t := time.Now().Add(time.Duration(expiryDays) * 24 * time.Hour)
		expiry = &t
	}

	t := tenancy.MustTenant(ctx)
	orgID := t.ZitadelOrgID

	zitadelUserID, err := s.serviceAccounts.CreateMachineUser(ctx, zitadel.MachineUser{
		OrgID:           orgID,
		Name:            name,
		Description:     msg.GetDescription(),
		AccessTokenType: userV2.AccessTokenType_ACCESS_TOKEN_TYPE_JWT,
	})
	if err != nil {
		return nil, s.internal("create machine user", err)
	}

	if _, err := s.serviceAccounts.AddUserGrant(ctx, orgID, zitadelUserID, []string{roleKey}); err != nil {
		if delErr := s.serviceAccounts.DeleteMachineUser(ctx, zitadelUserID); delErr != nil {
			s.logger.Warn("create service account rollback: delete machine user failed", zap.String("zitadel_user_id", zitadelUserID), zap.Error(delErr))
		}
		return nil, s.internal("add user grant", err)
	}

	tokenID, token, err := s.serviceAccounts.AddPersonalAccessToken(ctx, zitadelUserID, expiry)
	if err != nil {
		grants, _ := s.serviceAccounts.ListUserGrants(ctx, orgID, zitadelUserID)
		for _, g := range grants {
			_ = s.serviceAccounts.DeleteUserGrant(ctx, g.ID)
		}
		if delErr := s.serviceAccounts.DeleteMachineUser(ctx, zitadelUserID); delErr != nil {
			s.logger.Warn("create service account rollback: delete machine user failed", zap.String("zitadel_user_id", zitadelUserID), zap.Error(delErr))
		}
		return nil, s.internal("create PAT", err)
	}

	db, commit, err := s.store.Session(ctx)
	if err != nil {
		return nil, s.internal("db session", err)
	}
	defer func() { _ = commit() }()

	createdBySubject := session.MustUser(ctx).Subject
	var creator storage.User
	if err := db.Where("zitadel_subject = ?", createdBySubject).First(&creator).Error; err != nil {
		return nil, s.internal("find creator user", err)
	}

	now := time.Now()
	sa := &storage.ServiceAccount{
		TenantID:         t.ID,
		Name:             name,
		Description:      msg.GetDescription(),
		ZitadelUserID:    zitadelUserID,
		CreatedByID:      creator.ID,
		Role:             roleKey,
		TokenGeneratedAt: &now,
	}
	if err := db.Create(sa).Error; err != nil {
		s.logger.Error("create service account local row failed, rolling back Zitadel", zap.String("zitadel_user_id", zitadelUserID), zap.Error(err))
		if rmErr := s.serviceAccounts.RemovePersonalAccessToken(ctx, zitadelUserID, tokenID); rmErr != nil {
			s.logger.Warn("create service account rollback: remove PAT failed", zap.String("zitadel_user_id", zitadelUserID), zap.String("token_id", tokenID), zap.Error(rmErr))
		}
		grants, _ := s.serviceAccounts.ListUserGrants(ctx, orgID, zitadelUserID)
		for _, g := range grants {
			_ = s.serviceAccounts.DeleteUserGrant(ctx, g.ID)
		}
		if delErr := s.serviceAccounts.DeleteMachineUser(ctx, zitadelUserID); delErr != nil {
			s.logger.Warn("create service account rollback: delete machine user failed", zap.String("zitadel_user_id", zitadelUserID), zap.Error(delErr))
		}
		return nil, s.internal("create local service account", err)
	}

	return connect.NewResponse(&adminv1.CreateServiceAccountResponse{
		ServiceAccount: saModelToProto(sa),
		Token:          token,
	}), nil
}

// ListServiceAccounts returns every service account for the tenant.
func (s *Service) ListServiceAccounts(ctx context.Context, req *connect.Request[adminv1.ListServiceAccountsRequest]) (*connect.Response[adminv1.ListServiceAccountsResponse], error) {
	if s.serviceAccounts == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("admin: service account directory not wired"))
	}
	t := tenancy.MustTenant(ctx)

	db, commit, err := s.store.Session(ctx)
	if err != nil {
		return nil, s.internal("db session", err)
	}
	defer func() { _ = commit() }()

	var sas []storage.ServiceAccount
	if err := db.Preload("CreatedBy").Find(&sas).Error; err != nil {
		return nil, s.internal("list service accounts", err)
	}

	// Fetch all grants for the org+project. For orgs with many members this
	// is a full scan; optimize with userID filter if latency becomes a concern.
	grants, err := s.serviceAccounts.ListUserGrants(ctx, t.ZitadelOrgID, "")
	if err != nil {
		return nil, s.internal("list user grants", err)
	}

	rolesByUser := make(map[string][]string, len(grants))
	for _, g := range grants {
		rolesByUser[g.UserID] = append(rolesByUser[g.UserID], g.RoleKeys...)
	}

	out := make([]*adminv1.ServiceAccount, 0, len(sas))
	for _, sa := range sas {
		roleKey := pickHighestSARole(rolesByUser[sa.ZitadelUserID])
		protoSA := saModelToProto(&sa)
		protoSA.Role = saRoleToProto(roleKey)
		out = append(out, protoSA)
	}

	return connect.NewResponse(&adminv1.ListServiceAccountsResponse{ServiceAccounts: out}), nil
}

// DeleteServiceAccount removes the Zitadel machine user and the local
// mirror row. All tokens are revoked.
func (s *Service) DeleteServiceAccount(ctx context.Context, req *connect.Request[adminv1.DeleteServiceAccountRequest]) (*connect.Response[adminv1.DeleteServiceAccountResponse], error) {
	if s.serviceAccounts == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("admin: service account directory not wired"))
	}
	publicID := strings.TrimSpace(req.Msg.GetPublicId())
	if publicID == "" {
		return nil, s.invalidArg("public_id", "required")
	}

	db, commit, err := s.store.Session(ctx)
	if err != nil {
		return nil, s.internal("db session", err)
	}
	defer func() { _ = commit() }()

	var sa storage.ServiceAccount
	if err := db.Where("public_id = ?", publicID).First(&sa).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("admin: service account not found"))
		}
		return nil, s.internal("find service account", err)
	}

	if err := db.Delete(&sa).Error; err != nil {
		return nil, s.internal("delete service account", err)
	}

	tokens, err := s.serviceAccounts.ListPersonalAccessTokens(ctx, sa.ZitadelUserID)
	if err != nil {
		s.logger.Warn("delete service account: list PATs failed", zap.String("zitadel_user_id", sa.ZitadelUserID), zap.Error(err))
	} else {
		for _, tok := range tokens {
			if rmErr := s.serviceAccounts.RemovePersonalAccessToken(ctx, sa.ZitadelUserID, tok.GetId()); rmErr != nil {
				s.logger.Warn("delete service account: remove PAT failed", zap.String("zitadel_user_id", sa.ZitadelUserID), zap.String("token_id", tok.GetId()), zap.Error(rmErr))
			}
		}
	}

	if delErr := s.serviceAccounts.DeleteMachineUser(ctx, sa.ZitadelUserID); delErr != nil {
		s.logger.Warn("delete service account: delete machine user failed", zap.String("zitadel_user_id", sa.ZitadelUserID), zap.Error(delErr))
	}

	return connect.NewResponse(&adminv1.DeleteServiceAccountResponse{}), nil
}

// RegenerateServiceAccountToken revokes existing tokens and issues a
// new one-time PAT for the service account.
func (s *Service) RegenerateServiceAccountToken(ctx context.Context, req *connect.Request[adminv1.RegenerateServiceAccountTokenRequest]) (*connect.Response[adminv1.RegenerateServiceAccountTokenResponse], error) {
	if s.serviceAccounts == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("admin: service account directory not wired"))
	}
	publicID := strings.TrimSpace(req.Msg.GetPublicId())
	if publicID == "" {
		return nil, s.invalidArg("public_id", "required")
	}

	db, commit, err := s.store.Session(ctx)
	if err != nil {
		return nil, s.internal("db session", err)
	}
	defer func() { _ = commit() }()

	var sa storage.ServiceAccount
	if err := db.Where("public_id = ?", publicID).First(&sa).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("admin: service account not found"))
		}
		return nil, s.internal("find service account", err)
	}

	expiryDays := req.Msg.GetExpiryDays()
	if expiryDays == 0 {
		expiryDays = 365
	}
	var expiry *time.Time
	if expiryDays > 0 {
		t := time.Now().Add(time.Duration(expiryDays) * 24 * time.Hour)
		expiry = &t
	}

	existingTokens, err := s.serviceAccounts.ListPersonalAccessTokens(ctx, sa.ZitadelUserID)
	if err != nil {
		s.logger.Warn("regenerate token: list existing PATs failed", zap.String("zitadel_user_id", sa.ZitadelUserID), zap.Error(err))
	} else {
		for _, tok := range existingTokens {
			if rmErr := s.serviceAccounts.RemovePersonalAccessToken(ctx, sa.ZitadelUserID, tok.GetId()); rmErr != nil {
				s.logger.Warn("regenerate token: remove PAT failed", zap.String("zitadel_user_id", sa.ZitadelUserID), zap.String("token_id", tok.GetId()), zap.Error(rmErr))
			}
		}
	}

	_, token, err := s.serviceAccounts.AddPersonalAccessToken(ctx, sa.ZitadelUserID, expiry)
	if err != nil {
		return nil, s.internal("create new PAT", err)
	}

	// Update the token_generated_at timestamp locally.
	now := time.Now()
	if err := db.Model(&sa).Update("token_generated_at", now).Error; err != nil {
		s.logger.Error("regenerate token: update token_generated_at failed", zap.String("zitadel_user_id", sa.ZitadelUserID), zap.Error(err))
	}

	return connect.NewResponse(&adminv1.RegenerateServiceAccountTokenResponse{Token: token}), nil
}

// GetServiceAccount returns a single service account by public ID.
func (s *Service) GetServiceAccount(ctx context.Context, req *connect.Request[adminv1.GetServiceAccountRequest]) (*connect.Response[adminv1.GetServiceAccountResponse], error) {
	if s.serviceAccounts == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("admin: service account directory not wired"))
	}
	publicID := strings.TrimSpace(req.Msg.GetPublicId())
	if publicID == "" {
		return nil, s.invalidArg("public_id", "required")
	}

	t := tenancy.MustTenant(ctx)

	sa, err := s.store.GetServiceAccount(ctx, t.ID, publicID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("service account %q not found", publicID))
		}
		return nil, s.internal("get service account", err)
	}

	grants, err := s.serviceAccounts.ListUserGrants(ctx, t.ZitadelOrgID, sa.ZitadelUserID)
	if err != nil {
		return nil, s.internal("list user grants", err)
	}
	roleKey := pickHighestSARole(grantRoleKeys(grants))
	protoSA := saModelToProto(sa)
	protoSA.Role = saRoleToProto(roleKey)

	return connect.NewResponse(&adminv1.GetServiceAccountResponse{
		ServiceAccount: protoSA,
	}), nil
}

// UpdateServiceAccount updates the name and/or description of a service account.
func (s *Service) UpdateServiceAccount(ctx context.Context, req *connect.Request[adminv1.UpdateServiceAccountRequest]) (*connect.Response[adminv1.UpdateServiceAccountResponse], error) {
	publicID := strings.TrimSpace(req.Msg.GetPublicId())
	if publicID == "" {
		return nil, s.invalidArg("public_id", "required")
	}

	t := tenancy.MustTenant(ctx)

	sa, err := s.store.GetServiceAccount(ctx, t.ID, publicID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("service account %q not found", publicID))
		}
		return nil, s.internal("get service account", err)
	}

	changed := false
	if req.Msg.Name != nil {
		sa.Name = strings.TrimSpace(*req.Msg.Name)
		if sa.Name == "" {
			return nil, s.invalidArg("name", "name must not be empty")
		}
		changed = true
	}
	if req.Msg.Description != nil {
		sa.Description = *req.Msg.Description
		changed = true
	}
	if !changed {
		return connect.NewResponse(&adminv1.UpdateServiceAccountResponse{
			ServiceAccount: saModelToProto(sa),
		}), nil
	}

	if err := s.store.UpdateServiceAccount(ctx, sa); err != nil {
		return nil, s.internal("update service account", err)
	}

	return connect.NewResponse(&adminv1.UpdateServiceAccountResponse{
		ServiceAccount: saModelToProto(sa),
	}), nil
}

func grantRoleKeys(grants []zitadel.UserGrant) []string {
	var keys []string
	for _, g := range grants {
		keys = append(keys, g.RoleKeys...)
	}
	return keys
}
