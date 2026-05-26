package admin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	"go.uber.org/zap"
	"gorm.io/gorm"

	adminv1 "github.com/belphemur/limen/internal/admin/adminv1"
	"github.com/belphemur/limen/internal/crypto"
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
		return roleKeyMember, true
	case adminv1.ServiceAccountRole_SERVICE_ACCOUNT_ROLE_ADMIN:
		return roleKeyAdmin, true
	default:
		return "", false
	}
}

func saRoleToProto(roleKey string) adminv1.ServiceAccountRole {
	switch roleKey {
	case roleKeyMember:
		return adminv1.ServiceAccountRole_SERVICE_ACCOUNT_ROLE_MEMBER
	case roleKeyAdmin:
		return adminv1.ServiceAccountRole_SERVICE_ACCOUNT_ROLE_ADMIN
	default:
		return adminv1.ServiceAccountRole_SERVICE_ACCOUNT_ROLE_UNSPECIFIED
	}
}

func pickHighestSARole(keys []string) string {
	best := ""
	for _, k := range keys {
		switch k {
		case roleKeyAdmin:
			return roleKeyAdmin
		case roleKeyMember:
			if best != roleKeyAdmin {
				best = roleKeyMember
			}
		}
	}
	return best
}

func saModelToProto(sa *storage.ServiceAccount) *adminv1.ServiceAccount {
	return &adminv1.ServiceAccount{
		PublicId:    sa.PublicID,
		Name:        sa.Name,
		Description: sa.Description,
		Role:        saRoleToProto(sa.Role),
		CreatedAt:   sa.CreatedAt.UTC().Format(time.RFC3339),
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

	sa := &storage.ServiceAccount{
		TenantID:      t.ID,
		Name:          name,
		Description:   msg.GetDescription(),
		ZitadelUserID: zitadelUserID,
		CreatedByID:   creator.ID,
		Role:          roleKey,
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
	if err := db.Find(&sas).Error; err != nil {
		return nil, s.internal("list service accounts", err)
	}

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

	_, token, err := s.serviceAccounts.AddPersonalAccessToken(ctx, sa.ZitadelUserID, expiry)
	if err != nil {
		return nil, s.internal("create new PAT", err)
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

	return connect.NewResponse(&adminv1.RegenerateServiceAccountTokenResponse{Token: token}), nil
}

// ImpersonateServiceAccount starts an impersonation session for the
// caller, scoped to the requested service account. The response carries
// a Set-Cookie header with the impersonation session cookie.
func (s *Service) ImpersonateServiceAccount(ctx context.Context, req *connect.Request[adminv1.ImpersonateServiceAccountRequest]) (*connect.Response[adminv1.ImpersonateServiceAccountResponse], error) {
	if s.serviceAccounts == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("admin: service account directory not wired"))
	}
	if s.zitadelDomain == "" {
		return nil, connect.NewError(connect.CodeInternal, errors.New("admin: zitadel domain not configured"))
	}

	publicID := strings.TrimSpace(req.Msg.GetPublicId())
	if publicID == "" {
		return nil, s.invalidArg("public_id", "required")
	}

	t := tenancy.MustTenant(ctx)

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

	if sa.DeletedAt.Valid {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("admin: service account not found"))
	}

	accessToken, ok := session.AccessTokenFromContext(ctx)
	if !ok || accessToken == "" {
		return nil, s.internal("no access token", errors.New("admin: access token missing from session"))
	}

	patID, pat, err := s.serviceAccounts.AddPersonalAccessToken(ctx, sa.ZitadelUserID, nil)
	if err != nil {
		return nil, s.internal("create temporary PAT", err)
	}

	exchangeURL := strings.TrimSuffix(s.zitadelDomain, "/") + "/oauth/v2/token"
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:token-exchange")
	form.Set("subject_token", pat)
	form.Set("subject_token_type", "urn:ietf:params:oauth:token-type:access_token")
	form.Set("actor_token", accessToken)
	form.Set("actor_token_type", "urn:ietf:params:oauth:token-type:access_token")
	form.Set("scope", "openid profile email offline_access urn:zitadel:iam:org:project:id:"+s.zitadelProjectID+":aud")

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, exchangeURL, strings.NewReader(form.Encode()))
	if err != nil {
		_ = s.serviceAccounts.RemovePersonalAccessToken(ctx, sa.ZitadelUserID, patID)
		return nil, s.internal("build token exchange request", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	httpClient := &http.Client{Timeout: 10 * time.Second}
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		_ = s.serviceAccounts.RemovePersonalAccessToken(ctx, sa.ZitadelUserID, patID)
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("admin: token exchange unavailable"))
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		_ = s.serviceAccounts.RemovePersonalAccessToken(ctx, sa.ZitadelUserID, patID)
		return nil, s.internal("read token exchange response", err)
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		_ = s.serviceAccounts.RemovePersonalAccessToken(ctx, sa.ZitadelUserID, patID)
		switch {
		case httpResp.StatusCode == 400 || httpResp.StatusCode == 401:
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("admin: token exchange denied"))
		case httpResp.StatusCode >= 500:
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("admin: token exchange unavailable"))
		default:
			return nil, connect.NewError(connect.CodeInternal, errors.New("admin: token exchange failed"))
		}
	}

	var exchange struct {
		AccessToken  string `json:"access_token"`
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &exchange); err != nil {
		_ = s.serviceAccounts.RemovePersonalAccessToken(ctx, sa.ZitadelUserID, patID)
		return nil, s.internal("parse token exchange response", err)
	}

	if rmErr := s.serviceAccounts.RemovePersonalAccessToken(ctx, sa.ZitadelUserID, patID); rmErr != nil {
		s.logger.Warn("impersonate: remove temporary PAT failed", zap.String("zitadel_user_id", sa.ZitadelUserID), zap.Error(rmErr))
	}

	expiresAt := time.Now().Add(time.Duration(exchange.ExpiresIn) * time.Second)
	if exchange.ExpiresIn == 0 {
		expiresAt = time.Now().Add(24 * time.Hour)
	}

	cookieValue, err := s.buildImpersonationCookieValue(t.PublicID, exchange.IDToken, exchange.AccessToken, exchange.RefreshToken, expiresAt)
	if err != nil {
		return nil, s.internal("build impersonation cookie", err)
	}

	cookie := &http.Cookie{
		Name:     "limen_portal_impersonate",
		Value:    cookieValue,
		Path:     "/t/" + t.PublicID,
		HttpOnly: true,
		Secure:   s.secureCookie,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400,
	}

	resp := connect.NewResponse(&adminv1.ImpersonateServiceAccountResponse{})
	resp.Header().Set("Set-Cookie", cookie.String())
	return resp, nil
}

// ExitImpersonation ends the current impersonation session. The
// response carries a Set-Cookie header clearing the impersonation cookie.
func (s *Service) ExitImpersonation(ctx context.Context, req *connect.Request[adminv1.ExitImpersonationRequest]) (*connect.Response[adminv1.ExitImpersonationResponse], error) {
	t := tenancy.MustTenant(ctx)

	clearCookie := &http.Cookie{
		Name:     "limen_portal_impersonate",
		Value:    "",
		Path:     "/t/" + t.PublicID,
		HttpOnly: true,
		Secure:   s.secureCookie,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	}

	resp := connect.NewResponse(&adminv1.ExitImpersonationResponse{})
	resp.Header().Set("Set-Cookie", clearCookie.String())
	return resp, nil
}

func (s *Service) buildImpersonationCookieValue(tenantID, idToken, accessToken, refreshToken string, expiresAt time.Time) (string, error) {
	if s.cipher == nil {
		return "", errors.New("admin: cipher not configured")
	}

	payload, err := json.Marshal(map[string]any{
		"id_token":      idToken,
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"expires_at":    expiresAt.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return "", fmt.Errorf("marshal impersonation payload: %w", err)
	}

	sealed, err := s.cipher.Encrypt(payload, crypto.AAD{TenantID: tenantID, Kind: "portal.impersonate"})
	if err != nil {
		return "", fmt.Errorf("encrypt impersonation payload: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(sealed), nil
}
