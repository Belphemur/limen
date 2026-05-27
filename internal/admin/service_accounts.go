package admin

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/klauspost/compress/zstd"
	"go.uber.org/zap"
	"gorm.io/gorm"

	adminv1 "github.com/belphemur/limen/internal/admin/adminv1"
	"github.com/belphemur/limen/internal/auth"
	"github.com/belphemur/limen/internal/crypto"
	"github.com/belphemur/limen/internal/session"
	"github.com/belphemur/limen/internal/storage"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/zitadel"
	userV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
)

var zstdEnc, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))

func compressZstd(src []byte) ([]byte, error) {
	return zstdEnc.EncodeAll(src, nil), nil
}

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

func saModelToProto(sa *storage.ServiceAccount) *adminv1.ServiceAccount {
	return &adminv1.ServiceAccount{
		PublicId:    sa.PublicID,
		Name:        sa.Name,
		Description: sa.Description,
		Role:        saRoleToProto(sa.Role),
		CreatedAt:   sa.CreatedAt.UTC().Format(time.RFC3339),
	}
}

type exchangeTokenClaims struct {
	Subject   string
	Email     string
	FirstName string
	LastName  string
	Roles     []string
}

func parseExchangeTokenClaims(idToken string) (exchangeTokenClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return exchangeTokenClaims{}, fmt.Errorf("invalid JWT format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return exchangeTokenClaims{}, fmt.Errorf("decode JWT payload: %w", err)
	}
	var raw struct {
		Sub        string `json:"sub"`
		Email      string `json:"email"`
		GivenName  string `json:"given_name"`
		FamilyName string `json:"family_name"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return exchangeTokenClaims{}, fmt.Errorf("parse JWT payload: %w", err)
	}
	var rawRoles struct {
		Roles map[string]map[string]string `json:"urn:zitadel:iam:org:project:roles"`
	}
	_ = json.Unmarshal(payload, &rawRoles) // ignore error — roles may not exist
	var roles []string
	for role := range rawRoles.Roles {
		roles = append(roles, role)
	}
	return exchangeTokenClaims{
		Subject:   raw.Sub,
		Email:     raw.Email,
		FirstName: raw.GivenName,
		LastName:  raw.FamilyName,
		Roles:     roles,
	}, nil
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
	if s.oidc.TokenExchangeClientID == "" || s.oidc.TokenExchangeClientSecret == "" {
		return nil, connect.NewError(connect.CodeInternal, errors.New("admin: token exchange credentials not configured"))
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

	// Defense-in-depth: verify the SA belongs to the requesting tenant.
	// RLS covers this, but an explicit check makes the security boundary visible.
	if sa.TenantID != t.ID {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("admin: service account not found"))
	}

	accessToken, ok := session.AccessTokenFromContext(ctx)
	if !ok || accessToken == "" {
		return nil, s.internal("no access token", errors.New("admin: access token missing from session"))
	}

	// Per Zitadel token exchange docs, the SA's Zitadel user ID is accepted
	// directly with subject_token_type=urn:zitadel:params:oauth:token-type:user_id.
	// No temporary PAT is needed.
	exchangeURL := strings.TrimSuffix(s.zitadelDomain, "/") + "/oauth/v2/token"
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:token-exchange")
	form.Set("subject_token", sa.ZitadelUserID)
	form.Set("subject_token_type", "urn:zitadel:params:oauth:token-type:user_id")
	form.Set("actor_token", accessToken)
	form.Set("actor_token_type", "urn:ietf:params:oauth:token-type:access_token")
	form.Set("requested_token_type", "urn:ietf:params:oauth:token-type:jwt")
	form.Set("scope", "openid profile email offline_access urn:zitadel:iam:org:project:id:"+s.zitadelProjectID+":aud")

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, exchangeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, s.internal("build token exchange request", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.SetBasicAuth(s.oidc.TokenExchangeClientID, s.oidc.TokenExchangeClientSecret)

	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("admin: token exchange unavailable"))
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, s.internal("read token exchange response", err)
	}

	bodyPreviewLen := int(math.Min(200, float64(len(body))))
	bodyPreview := string(body[:bodyPreviewLen])
	if !utf8.ValidString(bodyPreview) {
		bodyPreview = hex.EncodeToString(body[:bodyPreviewLen])
	}
	s.logger.Debug("token exchange response",
		zap.Int("status", httpResp.StatusCode),
		zap.String("content_type", httpResp.Header.Get("Content-Type")),
		zap.String("content_length", httpResp.Header.Get("Content-Length")),
		zap.String("location", httpResp.Header.Get("Location")),
		zap.String("exchange_url", exchangeURL),
		zap.Int("body_len", len(body)),
		zap.String("body_preview", bodyPreview),
	)

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		var errBody struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		if jsonErr := json.Unmarshal(body, &errBody); jsonErr == nil {
			s.logger.Warn("token exchange denied",
				zap.String("error", errBody.Error),
				zap.String("error_description", errBody.ErrorDescription),
			)
		} else {
			s.logger.Warn("token exchange failed",
				zap.Int("status", httpResp.StatusCode),
			)
		}
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
		return nil, s.internal("parse token exchange response", err)
	}

	claims, err := parseExchangeTokenClaims(exchange.IDToken)
	if err != nil {
		return nil, s.internal("parse exchange token claims", err)
	}

	adminSession := session.MustUser(ctx)

	expiresAt := time.Now().Add(time.Duration(exchange.ExpiresIn) * time.Second)
	maxAge := 12 * time.Hour
	if time.Until(expiresAt) > maxAge {
		expiresAt = time.Now().Add(maxAge)
	}
	if exchange.ExpiresIn == 0 {
		expiresAt = time.Now().Add(maxAge)
	}

	payload := auth.CookiePayloadV2{
		Version:        auth.CookieVersionV2,
		AccessToken:    exchange.AccessToken,
		Subject:        claims.Subject,
		Email:          claims.Email,
		FirstName:      claims.FirstName,
		LastName:       claims.LastName,
		Roles:          claims.Roles,
		ActorUserID:    adminSession.Subject,
		ActorEmail:     adminSession.Email,
		ActorFirstName: adminSession.FirstName,
		ActorLastName:  adminSession.LastName,
		Reason:         "",
		UserType:       auth.ImpersonatedUserTypeServiceAccount,
		Impersonated:   true,
		ExpiresAt:      expiresAt,
	}

	cookieValue, err := s.buildImpersonationCookieV2(t.PublicID, payload)
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
		MaxAge:   int(time.Until(expiresAt).Seconds()),
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

func (s *Service) buildImpersonationCookieV2(tenantID string, payload auth.CookiePayloadV2) (string, error) {
	if s.cipher == nil {
		return "", errors.New("admin: cipher not configured")
	}

	packed := auth.PackCookieV2(payload)

	compressed, err := compressZstd(packed)
	if err != nil {
		return "", fmt.Errorf("compress impersonation cookie: %w", err)
	}

	sealed, err := s.cipher.Encrypt(compressed, crypto.AAD{TenantID: tenantID, Kind: "portal.impersonate"})
	if err != nil {
		return "", fmt.Errorf("encrypt impersonation cookie: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(sealed), nil
}
