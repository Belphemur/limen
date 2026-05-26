package main

// Package main implements the Zitadel bootstrap for Limen dev environments.
//
// It is idempotent: re-running it is safe. It creates a dedicated 'limen'
// organization that owns the Limen Gateway project, the Portal (OIDC/PKCE)
// and MCP RS (API) apps, the project roles (member/admin/owner/super_admin),
// a sample tenant org, and the staff org with a super_admin user. The
// Zitadel instance default organization is intentionally left untouched so
// it stays clean. All work is done through the official zitadel-go/v3 SDK,
// preferring v2 services. The only v1 endpoint we touch is
// ManagementService.AddOrgMember — see the "API surface" note below.
//
// Connection topology (dev):
//   - gRPC dial address: zitadel-api:8080 (internal docker DNS)
//   - gRPC :authority   : localhost (Zitadel rejects mismatched hosts)
//   - Issuer / Origin   : http://localhost:8081 (only used by JWT-profile
//     auth, not by PAT — irrelevant here)
//
// API surface: v2 services exclusively for everything Zitadel has shipped
// in v2. The single exception is ManagementService.AddOrgMember (v1) —
// Zitadel has not migrated org-member CRUD to v2 yet, and Console deep-
// links from Limen require the seed user to be ORG_OWNER to self-serve
// invites / roles / IdP / branding from `<issuer>/ui/console`.

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	zsdk "github.com/zitadel/zitadel-go/v3/pkg/client"
	applicationV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/application/v2"
	authorizationV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/authorization/v2"
	filterV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/filter/v2"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/management"
	objectV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/object/v2"
	orgV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	projectV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/project/v2"
	userV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
	"github.com/zitadel/zitadel-go/v3/pkg/zitadel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// alreadyExists reports whether err is an idempotent "already exists" /
// "duplicate" condition from Zitadel.
func alreadyExists(err error) bool {
	if err == nil {
		return false
	}
	if s, ok := status.FromError(err); ok {
		switch s.Code() {
		case codes.AlreadyExists:
			return true
		case codes.FailedPrecondition, codes.Internal:
			low := strings.ToLower(s.Message())
			if strings.Contains(low, "already exists") || strings.Contains(low, "duplicate") {
				return true
			}
		}
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "already exists") || strings.Contains(low, "alreadyexists")
}

type bootstrap struct {
	api *zsdk.Client
}

func (b *bootstrap) ensureProject(ctx context.Context, orgID, name string) (string, error) {
	list, err := b.api.ProjectServiceV2().ListProjects(ctx, &projectV2.ListProjectsRequest{
		Filters: []*projectV2.ProjectSearchFilter{
			{Filter: &projectV2.ProjectSearchFilter_ProjectNameFilter{
				ProjectNameFilter: &projectV2.ProjectNameFilter{
					ProjectName: name,
					Method:      filterV2.TextFilterMethod_TEXT_FILTER_METHOD_EQUALS,
				},
			}},
		},
	})
	if err == nil {
		for _, p := range list.GetProjects() {
			if p.GetName() == name {
				return p.GetProjectId(), nil
			}
		}
	}
	resp, err := b.api.ProjectServiceV2().CreateProject(ctx, &projectV2.CreateProjectRequest{
		OrganizationId:       orgID,
		Name:                 name,
		ProjectRoleAssertion: true,
	})
	if err != nil {
		return "", fmt.Errorf("create project %q: %w", name, err)
	}
	return resp.GetProjectId(), nil
}

func (b *bootstrap) ensureRole(ctx context.Context, projectID, key, displayName string) error {
	_, err := b.api.ProjectServiceV2().AddProjectRole(ctx, &projectV2.AddProjectRoleRequest{
		ProjectId:   projectID,
		RoleKey:     key,
		DisplayName: displayName,
	})
	if err != nil && !alreadyExists(err) {
		return fmt.Errorf("add project role %q: %w", key, err)
	}
	return nil
}

// findOIDCClientID returns the client_id of an existing OIDC app on
// projectID with the given name, or "" if not found.
func (b *bootstrap) findApp(ctx context.Context, projectID, name string) (string, error) {
	app, err := b.findAppRaw(ctx, projectID, name)
	if err != nil || app == nil {
		return "", err
	}
	if oidc := app.GetOidcConfiguration(); oidc != nil {
		return oidc.GetClientId(), nil
	}
	if api := app.GetApiConfiguration(); api != nil {
		return api.GetClientId(), nil
	}
	return "", nil
}

// findAppRaw returns the full Application record (or nil) matching name.
func (b *bootstrap) findAppRaw(ctx context.Context, projectID, name string) (*applicationV2.Application, error) {
	list, err := b.api.ApplicationServiceV2().ListApplications(ctx, &applicationV2.ListApplicationsRequest{
		Filters: []*applicationV2.ApplicationSearchFilter{
			{Filter: &applicationV2.ApplicationSearchFilter_ProjectIdFilter{
				ProjectIdFilter: &applicationV2.ProjectIDFilter{ProjectId: projectID},
			}},
			{Filter: &applicationV2.ApplicationSearchFilter_NameFilter{
				NameFilter: &applicationV2.ApplicationNameFilter{
					Name:   name,
					Method: filterV2.TextFilterMethod_TEXT_FILTER_METHOD_EQUALS,
				},
			}},
		},
	})
	if err != nil {
		return nil, err
	}
	for _, a := range list.GetApplications() {
		if a.GetName() == name {
			return a, nil
		}
	}
	return nil, nil
}

func (b *bootstrap) ensureOIDCApp(ctx context.Context, projectID, name string, redirectURIs, postLogoutURIs []string) (string, error) {
	var expandedPostLogout []string
	for _, u := range postLogoutURIs {
		expandedPostLogout = append(expandedPostLogout, postLogoutURIVariants(u)...)
	}
	if existing, err := b.findAppRaw(ctx, projectID, name); err == nil && existing != nil {
		oidc := existing.GetOidcConfiguration()
		if oidc == nil {
			return "", fmt.Errorf("app %q exists but is not OIDC", name)
		}
		needsRedirect := !containsAll(oidc.GetRedirectUris(), redirectURIs)
		needsPostLogout := len(expandedPostLogout) > 0 && !containsAll(oidc.GetPostLogoutRedirectUris(), expandedPostLogout)
		if needsRedirect || needsPostLogout {
			cfg := &applicationV2.UpdateOIDCApplicationConfigurationRequest{}
			if needsRedirect {
				cfg.RedirectUris = mergeUnique(oidc.GetRedirectUris(), redirectURIs)
			}
			if needsPostLogout {
				cfg.PostLogoutRedirectUris = mergeUnique(oidc.GetPostLogoutRedirectUris(), expandedPostLogout)
			}
			if _, err := b.api.ApplicationServiceV2().UpdateApplication(ctx, &applicationV2.UpdateApplicationRequest{
				ProjectId:     projectID,
				ApplicationId: existing.GetApplicationId(),
				ApplicationType: &applicationV2.UpdateApplicationRequest_OidcConfiguration{
					OidcConfiguration: cfg,
				},
			}); err != nil {
				return "", fmt.Errorf("update OIDC app %q URIs: %w", name, err)
			}
			if needsRedirect {
				log.Printf("updated %s redirect URIs: %v", name, cfg.RedirectUris)
			}
			if needsPostLogout {
				log.Printf("updated %s post-logout URIs: %v", name, cfg.PostLogoutRedirectUris)
			}
		}
		return oidc.GetClientId(), nil
	}
	resp, err := b.api.ApplicationServiceV2().CreateApplication(ctx, &applicationV2.CreateApplicationRequest{
		ProjectId: projectID,
		Name:      name,
		ApplicationType: &applicationV2.CreateApplicationRequest_OidcConfiguration{
			OidcConfiguration: &applicationV2.CreateOIDCApplicationRequest{
				RedirectUris:             redirectURIs,
				PostLogoutRedirectUris:   expandedPostLogout,
				ResponseTypes:            []applicationV2.OIDCResponseType{applicationV2.OIDCResponseType_OIDC_RESPONSE_TYPE_CODE},
				GrantTypes:               []applicationV2.OIDCGrantType{applicationV2.OIDCGrantType_OIDC_GRANT_TYPE_AUTHORIZATION_CODE, applicationV2.OIDCGrantType_OIDC_GRANT_TYPE_REFRESH_TOKEN},
				ApplicationType:          applicationV2.OIDCApplicationType_OIDC_APP_TYPE_WEB,
				AuthMethodType:           applicationV2.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_NONE,
				DevelopmentMode:          true,
				AccessTokenType:          applicationV2.OIDCTokenType_OIDC_TOKEN_TYPE_JWT,
				AccessTokenRoleAssertion: true,
				IdTokenRoleAssertion:     true,
				IdTokenUserinfoAssertion: true,
			},
		},
	})
	if err != nil {
		if alreadyExists(err) {
			if cid, lookupErr := b.findApp(ctx, projectID, name); lookupErr == nil && cid != "" {
				return cid, nil
			}
		}
		return "", fmt.Errorf("create OIDC app %q: %w", name, err)
	}
	return resp.GetOidcConfiguration().GetClientId(), nil
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func containsAll(haystack, needles []string) bool {
	for _, n := range needles {
		if !containsString(haystack, n) {
			return false
		}
	}
	return true
}

// mergeUnique returns base + extras with duplicates removed, preserving
// insertion order so Zitadel keeps the original ordering of registered
// URIs across runs.
func mergeUnique(base, extras []string) []string {
	seen := make(map[string]struct{}, len(base)+len(extras))
	out := make([]string, 0, len(base)+len(extras))
	for _, v := range base {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	for _, v := range extras {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// postLogoutURIVariants returns both the with-trailing-slash and
// without-trailing-slash forms of u, so Zitadel's exact-match check
// accepts either form from the relying party. Empty input → nil.
func postLogoutURIVariants(u string) []string {
	if u == "" {
		return nil
	}
	withSlash := u
	withoutSlash := u
	if len(u) > 0 && u[len(u)-1] == '/' {
		withoutSlash = u[:len(u)-1]
	} else {
		withSlash = u + "/"
	}
	if withSlash == withoutSlash {
		return []string{u}
	}
	return []string{withoutSlash, withSlash}
}

func (b *bootstrap) ensureAPIApp(ctx context.Context, projectID, name string) (string, error) {
	if cid, err := b.findApp(ctx, projectID, name); err == nil && cid != "" {
		return cid, nil
	}
	resp, err := b.api.ApplicationServiceV2().CreateApplication(ctx, &applicationV2.CreateApplicationRequest{
		ProjectId: projectID,
		Name:      name,
		ApplicationType: &applicationV2.CreateApplicationRequest_ApiConfiguration{
			ApiConfiguration: &applicationV2.CreateAPIApplicationRequest{
				AuthMethodType: applicationV2.APIAuthMethodType_API_AUTH_METHOD_TYPE_PRIVATE_KEY_JWT,
			},
		},
	})
	if err != nil {
		if alreadyExists(err) {
			if cid, lookupErr := b.findApp(ctx, projectID, name); lookupErr == nil && cid != "" {
				return cid, nil
			}
		}
		return "", fmt.Errorf("create API app %q: %w", name, err)
	}
	return resp.GetApiConfiguration().GetClientId(), nil
}

func (b *bootstrap) ensureOrg(ctx context.Context, name string) (string, error) {
	list, err := b.api.OrganizationServiceV2().ListOrganizations(ctx, &orgV2.ListOrganizationsRequest{
		Queries: []*orgV2.SearchQuery{
			{Query: &orgV2.SearchQuery_NameQuery{NameQuery: &orgV2.OrganizationNameQuery{
				Name:   name,
				Method: objectV2.TextQueryMethod_TEXT_QUERY_METHOD_EQUALS,
			}}},
		},
	})
	if err == nil {
		for _, o := range list.GetResult() {
			if o.GetName() == name {
				return o.GetId(), nil
			}
		}
	}
	resp, err := b.api.OrganizationServiceV2().AddOrganization(ctx, &orgV2.AddOrganizationRequest{Name: name})
	if err != nil {
		return "", fmt.Errorf("add organization %q: %w", name, err)
	}
	return resp.GetOrganizationId(), nil
}

func (b *bootstrap) ensureHumanUser(ctx context.Context, orgID, email, given, family string) (string, error) {
	return b.ensureHumanUserWithPassword(ctx, orgID, email, given, family, "")
}

// ensureHumanUserWithPassword is the password-bearing variant. When
// password is non-empty the user is created with a pre-set, already-
// verified credential so dev seed users (e.g. test@test.com) can log in
// without waiting on the Mailpit invite link. When password is empty
// behaviour is identical to ensureHumanUser — Zitadel emails an init
// link.
func (b *bootstrap) ensureHumanUserWithPassword(ctx context.Context, orgID, email, given, family, password string) (string, error) {
	list, err := b.api.UserServiceV2().ListUsers(ctx, &userV2.ListUsersRequest{
		Queries: []*userV2.SearchQuery{
			{Query: &userV2.SearchQuery_OrganizationIdQuery{OrganizationIdQuery: &userV2.OrganizationIdQuery{
				OrganizationId: orgID,
			}}},
			{Query: &userV2.SearchQuery_LoginNameQuery{LoginNameQuery: &userV2.LoginNameQuery{
				LoginName: email,
				Method:    objectV2.TextQueryMethod_TEXT_QUERY_METHOD_EQUALS,
			}}},
		},
	})
	if err == nil {
		for _, u := range list.GetResult() {
			if id := u.GetUserId(); id != "" {
				return id, nil
			}
		}
	}
	username := email
	req := &userV2.AddHumanUserRequest{
		Organization: &objectV2.Organization{Org: &objectV2.Organization_OrgId{OrgId: orgID}},
		Username:     &username,
		Profile: &userV2.SetHumanProfile{
			GivenName:         given,
			FamilyName:        family,
			PreferredLanguage: ptr("en"),
		},
		Email: &userV2.SetHumanEmail{Email: email},
	}
	if password != "" {
		req.PasswordType = &userV2.AddHumanUserRequest_Password{
			Password: &userV2.Password{Password: password, ChangeRequired: false},
		}
		req.Email.Verification = &userV2.SetHumanEmail_IsVerified{IsVerified: true}
	}
	resp, err := b.api.UserServiceV2().AddHumanUser(ctx, req)
	if err != nil {
		if alreadyExists(err) {
			// Re-search once: the conflict implies the user is on this org.
			list2, lookupErr := b.api.UserServiceV2().ListUsers(ctx, &userV2.ListUsersRequest{
				Queries: []*userV2.SearchQuery{
					{Query: &userV2.SearchQuery_OrganizationIdQuery{OrganizationIdQuery: &userV2.OrganizationIdQuery{OrganizationId: orgID}}},
					{Query: &userV2.SearchQuery_LoginNameQuery{LoginNameQuery: &userV2.LoginNameQuery{LoginName: email, Method: objectV2.TextQueryMethod_TEXT_QUERY_METHOD_EQUALS}}},
				},
			})
			if lookupErr == nil {
				for _, u := range list2.GetResult() {
					if id := u.GetUserId(); id != "" {
						return id, nil
					}
				}
			}
		}
		return "", fmt.Errorf("add human user %q: %w", email, err)
	}
	return resp.GetUserId(), nil
}

func (b *bootstrap) ensureAuthorization(ctx context.Context, userID, projectID, orgID string, roleKeys []string) error {
	_, err := b.api.AuthorizationServiceV2().CreateAuthorization(ctx, &authorizationV2.CreateAuthorizationRequest{
		UserId:         userID,
		ProjectId:      projectID,
		OrganizationId: orgID,
		RoleKeys:       roleKeys,
	})
	if err != nil && !alreadyExists(err) {
		return fmt.Errorf("create authorization (user=%s project=%s org=%s): %w", userID, projectID, orgID, err)
	}
	return nil
}

// ensureProjectGrant grants the Limen Gateway project to grantedOrgID with
// the given roleKeys. Required before any authorization can be created for
// users in a non-owning organization (tenant orgs and staff org).
func (b *bootstrap) ensureProjectGrant(ctx context.Context, projectID, grantedOrgID string, roleKeys []string) error {
	_, err := b.api.ProjectServiceV2().CreateProjectGrant(ctx, &projectV2.CreateProjectGrantRequest{
		ProjectId:             projectID,
		GrantedOrganizationId: grantedOrgID,
		RoleKeys:              roleKeys,
	})
	if err != nil && !alreadyExists(err) {
		return fmt.Errorf("create project grant (project=%s org=%s): %w", projectID, grantedOrgID, err)
	}
	return nil
}

// ensureOrgOwnerMembership makes userID an ORG_OWNER of orgID via the
// v1 ManagementService.AddOrgMember RPC. Zitadel has not exposed
// org-member CRUD on the v2 surface yet; the org context is selected by
// the x-zitadel-orgid header rather than a request field. ORG_OWNER is
// the Zitadel-side role that lets the user self-serve invites, role
// changes, IdP federation, and branding from `<issuer>/ui/console` —
// the deep-link targets the admin SPA renders.
func (b *bootstrap) ensureOrgOwnerMembership(ctx context.Context, orgID, userID string) error {
	ctx = metadata.AppendToOutgoingContext(ctx, zsdk.OrgHeader, orgID)
	_, err := b.api.ManagementService().AddOrgMember(ctx, &management.AddOrgMemberRequest{
		UserId: userID,
		Roles:  []string{"ORG_OWNER"},
	})
	if err != nil && !alreadyExists(err) {
		return fmt.Errorf("add ORG_OWNER membership (user=%s org=%s): %w", userID, orgID, err)
	}
	return nil
}

// ensureUserRegistrationDisabled disables self-registration on the given org.
// It reads the current login policy, then either creates or updates the
// custom policy with AllowRegister set to false while preserving all other
// settings. This uses the v1 ManagementService (org-scoped via
// x-zitadel-orgid), following the same header pattern as
// ensureOrgOwnerMembership.
func (b *bootstrap) ensureUserRegistrationDisabled(ctx context.Context, orgID string) error {
	ctx = metadata.AppendToOutgoingContext(ctx, zsdk.OrgHeader, orgID)

	p, err := b.api.ManagementService().GetLoginPolicy(ctx, &management.GetLoginPolicyRequest{})
	if err != nil {
		return fmt.Errorf("get login policy (org=%s): %w", orgID, err)
	}
	cur := p.GetPolicy()
	if !cur.GetAllowRegister() {
		log.Printf("user registration already disabled for org %s", orgID)
		return nil
	}

	if p.GetIsDefault() {
		// No custom policy yet — create one with current values + AllowRegister=false.
		_, err = b.api.ManagementService().AddCustomLoginPolicy(ctx, &management.AddCustomLoginPolicyRequest{
			AllowUsernamePassword:      cur.GetAllowUsernamePassword(),
			AllowRegister:              false,
			AllowExternalIdp:           cur.GetAllowExternalIdp(),
			ForceMfa:                   cur.GetForceMfa(),
			ForceMfaLocalOnly:          cur.GetForceMfaLocalOnly(),
			PasswordlessType:           cur.GetPasswordlessType(),
			HidePasswordReset:          cur.GetHidePasswordReset(),
			IgnoreUnknownUsernames:     cur.GetIgnoreUnknownUsernames(),
			AllowDomainDiscovery:       cur.GetAllowDomainDiscovery(),
			DisableLoginWithEmail:      cur.GetDisableLoginWithEmail(),
			DisableLoginWithPhone:      cur.GetDisableLoginWithPhone(),
			DefaultRedirectUri:         cur.GetDefaultRedirectUri(),
			PasswordCheckLifetime:      cur.GetPasswordCheckLifetime(),
			ExternalLoginCheckLifetime: cur.GetExternalLoginCheckLifetime(),
			MfaInitSkipLifetime:        cur.GetMfaInitSkipLifetime(),
			SecondFactorCheckLifetime:  cur.GetSecondFactorCheckLifetime(),
			MultiFactorCheckLifetime:   cur.GetMultiFactorCheckLifetime(),
			SecondFactors:              cur.GetSecondFactors(),
			MultiFactors:               cur.GetMultiFactors(),
		})
		if err != nil {
			return fmt.Errorf("add custom login policy (org=%s): %w", orgID, err)
		}
	} else {
		// Custom policy exists — update AllowRegister only.
		_, err = b.api.ManagementService().UpdateCustomLoginPolicy(ctx, &management.UpdateCustomLoginPolicyRequest{
			AllowUsernamePassword:      cur.GetAllowUsernamePassword(),
			AllowRegister:              false,
			AllowExternalIdp:           cur.GetAllowExternalIdp(),
			ForceMfa:                   cur.GetForceMfa(),
			ForceMfaLocalOnly:          cur.GetForceMfaLocalOnly(),
			PasswordlessType:           cur.GetPasswordlessType(),
			HidePasswordReset:          cur.GetHidePasswordReset(),
			IgnoreUnknownUsernames:     cur.GetIgnoreUnknownUsernames(),
			AllowDomainDiscovery:       cur.GetAllowDomainDiscovery(),
			DisableLoginWithEmail:      cur.GetDisableLoginWithEmail(),
			DisableLoginWithPhone:      cur.GetDisableLoginWithPhone(),
			DefaultRedirectUri:         cur.GetDefaultRedirectUri(),
			PasswordCheckLifetime:      cur.GetPasswordCheckLifetime(),
			ExternalLoginCheckLifetime: cur.GetExternalLoginCheckLifetime(),
			MfaInitSkipLifetime:        cur.GetMfaInitSkipLifetime(),
			SecondFactorCheckLifetime:  cur.GetSecondFactorCheckLifetime(),
			MultiFactorCheckLifetime:   cur.GetMultiFactorCheckLifetime(),
		})
		if err != nil {
			return fmt.Errorf("update custom login policy (org=%s): %w", orgID, err)
		}
	}
	log.Printf("disabled user registration for org %s", orgID)
	return nil
}

func ptr[T any](v T) *T { return &v }

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	apiHost := getenvDefault("ZITADEL_API_HOST", "zitadel-api")
	apiPort := getenvDefault("ZITADEL_API_PORT", "8080")
	authority := getenvDefault("ZITADEL_HOST", "localhost") // matches Zitadel ExternalDomain

	patFile := os.Getenv("ZITADEL_PAT_FILE")
	if patFile == "" {
		log.Fatal("ZITADEL_PAT_FILE not set")
	}
	pat, err := readPAT(patFile)
	if err != nil {
		log.Fatalf("read PAT: %v", err)
	}

	log.Printf("bootstrapping Zitadel via gRPC %s:%s (authority=%s)", apiHost, apiPort, authority)

	api, err := zsdk.New(ctx,
		zitadel.New(apiHost, zitadel.WithInsecure(apiPort)),
		zsdk.WithAuth(zsdk.PAT(pat)),
		zsdk.WithGRPCDialOptions(grpc.WithAuthority(authority)),
	)
	if err != nil {
		log.Fatalf("build SDK client: %v", err)
	}
	b := &bootstrap{api: api}

	// The Limen Gateway lives in its own organization (default 'limen') so
	// the Zitadel instance default org stays free of Limen-specific objects.
	gatewayOrgName := getenvDefault("LIMEN_GATEWAY_ORG_NAME", "limen")
	gatewayOrgID, err := b.ensureOrg(ctx, gatewayOrgName)
	if err != nil {
		log.Fatalf("ensure gateway org %q: %v", gatewayOrgName, err)
	}
	log.Printf("gateway org %q: %s", gatewayOrgName, gatewayOrgID)

	projectID, err := b.ensureProject(ctx, gatewayOrgID, "Limen Gateway")
	if err != nil {
		log.Fatalf("ensure project: %v", err)
	}
	log.Printf("project: %s", projectID)

	// Defaults cover both Vite dev origins: the portal SPA on :5173
	// and the tenant-admin SPA on :5174. The browser stays on a single
	// origin throughout login/logout so the portal cookie is same-
	// origin and the SPA picks up the session on the next render. Vite
	// proxies /auth/* to Limen; the /signed-out route is a tenant-
	// agnostic SPA page that Zitadel bounces back to after end-session.
	//
	// Override via LIMEN_PORTAL_REDIRECT / LIMEN_PORTAL_POST_LOGOUT
	// (primary entry, must match the Limen config redirect_uri) and
	// LIMEN_EXTRA_REDIRECTS / LIMEN_EXTRA_POST_LOGOUTS (comma-separated
	// additional entries that must appear in Limen's
	// oidc.allowed_redirect_uris).
	portalRedirect := getenvDefault("LIMEN_PORTAL_REDIRECT", "http://localhost:5173/auth/callback")
	portalPostLogout := getenvDefault("LIMEN_PORTAL_POST_LOGOUT", "http://localhost:5173/signed-out")
	extraRedirects := splitCSV(getenvDefault("LIMEN_EXTRA_REDIRECTS", "http://localhost:5174/auth/callback"))
	extraPostLogouts := splitCSV(getenvDefault("LIMEN_EXTRA_POST_LOGOUTS", "http://localhost:5174/signed-out"))
	redirectURIs := mergeUnique([]string{portalRedirect}, extraRedirects)
	postLogoutURIs := mergeUnique([]string{portalPostLogout}, extraPostLogouts)
	portalClientID, err := b.ensureOIDCApp(ctx, projectID, "Limen Portal", redirectURIs, postLogoutURIs)
	if err != nil {
		log.Fatalf("ensure portal app: %v", err)
	}
	log.Printf("portal client_id: %s", portalClientID)

	mcpClientID, err := b.ensureAPIApp(ctx, projectID, "Limen MCP RS")
	if err != nil {
		log.Fatalf("ensure MCP RS app: %v", err)
	}
	log.Printf("mcp RS client_id: %s", mcpClientID)

	for _, r := range []struct{ key, display string }{
		{"member", "Member"},
		{"admin", "Admin"},
		{"owner", "Owner"},
		{"super_admin", "Super Admin"},
	} {
		if err := b.ensureRole(ctx, projectID, r.key, r.display); err != nil {
			log.Fatalf("ensure role %s: %v", r.key, err)
		}
	}
	log.Printf("roles: member, admin, owner, super_admin")

	sampleName := getenvDefault("LIMEN_SAMPLE_TENANT_NAME", "acme")
	orgID, err := b.ensureOrg(ctx, sampleName)
	if err != nil {
		log.Fatalf("ensure sample org: %v", err)
	}
	log.Printf("sample org %q: %s", sampleName, orgID)

	allRoles := []string{"member", "admin", "owner", "super_admin"}
	if err := b.ensureProjectGrant(ctx, projectID, orgID, allRoles); err != nil {
		log.Fatalf("ensure sample project grant: %v", err)
	}
	log.Printf("project granted to sample org")

	// Seed owner for the sample tenant. The credential is fixed so dev
	// flows (Playwright e2e, manual smoke tests) can log straight in
	// without waiting on the Mailpit invite link. Override via
	// LIMEN_SAMPLE_OWNER_{EMAIL,PASSWORD} when you want a different
	// identity (e.g. CI's per-run unique email).
	sampleOwnerEmail := getenvDefault("LIMEN_SAMPLE_OWNER_EMAIL", "test@test.com")
	sampleOwnerPassword := getenvDefault("LIMEN_SAMPLE_OWNER_PASSWORD", "Password1!")
	sampleOwnerUserID, err := b.ensureHumanUserWithPassword(ctx, orgID, sampleOwnerEmail, "Test", "Owner", sampleOwnerPassword)
	if err != nil {
		log.Fatalf("ensure sample owner %q: %v", sampleOwnerEmail, err)
	}
	log.Printf("sample owner %q: %s", sampleOwnerEmail, sampleOwnerUserID)

	if err := b.ensureAuthorization(ctx, sampleOwnerUserID, projectID, orgID, []string{"owner"}); err != nil {
		log.Fatalf("ensure sample owner authorization: %v", err)
	}
	log.Printf("granted owner to %s in sample org", sampleOwnerEmail)

	// ORG_OWNER (Zitadel side) lets the seed user self-serve invites,
	// roles, IdP federation, and branding from Console — the surface
	// the admin SPA's Members page deep-links into.
	if err := b.ensureOrgOwnerMembership(ctx, orgID, sampleOwnerUserID); err != nil {
		log.Fatalf("ensure sample owner ORG_OWNER membership: %v", err)
	}
	log.Printf("granted ORG_OWNER to %s in sample org", sampleOwnerEmail)

	if err := b.ensureUserRegistrationDisabled(ctx, orgID); err != nil {
		log.Fatalf("ensure user registration disabled (sample org): %v", err)
	}

	// Staff (operator) org — see docs/phases/phase-12-staff-backoffice.md.
	staffOrgName := getenvDefault("LIMEN_STAFF_ORG_NAME", "limen-staff")
	staffOrgID, err := b.ensureOrg(ctx, staffOrgName)
	if err != nil {
		log.Fatalf("ensure staff org: %v", err)
	}
	log.Printf("staff org %q: %s", staffOrgName, staffOrgID)

	if err := b.ensureProjectGrant(ctx, projectID, staffOrgID, allRoles); err != nil {
		log.Fatalf("ensure staff project grant: %v", err)
	}
	log.Printf("project granted to staff org")

	staffEmail := getenvDefault("LIMEN_STAFF_BOOTSTRAP_EMAIL", "staff@limen.dev")
	staffUserID, err := b.ensureHumanUser(ctx, staffOrgID, staffEmail, "Limen", "Staff")
	if err != nil {
		log.Fatalf("ensure staff user %q: %v", staffEmail, err)
	}
	log.Printf("staff user %q: %s", staffEmail, staffUserID)

	if err := b.ensureAuthorization(ctx, staffUserID, projectID, staffOrgID, []string{"super_admin"}); err != nil {
		log.Fatalf("ensure staff authorization: %v", err)
	}
	log.Printf("granted super_admin to %s in staff org", staffEmail)

	if err := b.ensureUserRegistrationDisabled(ctx, staffOrgID); err != nil {
		log.Fatalf("ensure user registration disabled (staff org): %v", err)
	}

	out := map[string]string{
		"LIMEN_OIDC_PORTAL_CLIENT_ID": portalClientID,
		"LIMEN_OIDC_MCP_RS_CLIENT_ID": mcpClientID,
		"LIMEN_OIDC_PROJECT_ID":       projectID,
		"LIMEN_GATEWAY_ORG_ID":        gatewayOrgID,
		"LIMEN_GATEWAY_ORG_NAME":      gatewayOrgName,
		"LIMEN_SAMPLE_TENANT_ORG_ID":  orgID,
		"LIMEN_SAMPLE_TENANT_NAME":    sampleName,
		"LIMEN_SAMPLE_OWNER_USER_ID":  sampleOwnerUserID,
		"LIMEN_SAMPLE_OWNER_EMAIL":    sampleOwnerEmail,
		"LIMEN_SAMPLE_OWNER_PASSWORD": sampleOwnerPassword,
		"LIMEN_STAFF_ZITADEL_ORG_ID":  staffOrgID,
		"LIMEN_STAFF_BOOTSTRAP_EMAIL": staffEmail,
	}
	if path := os.Getenv("LIMEN_BOOTSTRAP_OUT"); path != "" {
		if err := writeEnvFile(path, out); err != nil {
			log.Fatalf("write %s: %v", path, err)
		}
	}
	fmt.Println("\n--- bootstrap output (copy into .env) ---")
	for k, v := range out {
		fmt.Printf("%s=%s\n", k, v)
	}

}

func readPAT(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func writeEnvFile(path string, kv map[string]string) error {
	var b strings.Builder
	for k, v := range kv {
		fmt.Fprintf(&b, "%s=%s\n", k, v)
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func getenvDefault(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// splitCSV trims and drops empty entries from a comma-separated list.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
