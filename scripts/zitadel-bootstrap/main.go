package main

// Package main implements the Zitadel bootstrap for Limen dev environments.
//
// It is idempotent: re-running it is safe. It creates the Limen Gateway
// project on the Zitadel default org, the Portal (OIDC/PKCE) and MCP RS
// (API) apps, the project roles (member/admin/owner/super_admin), a sample
// tenant org, and the staff org with a super_admin user. All work is done
// through the official zitadel-go/v3 SDK using v2 services exclusively —
// no v1 management endpoints, no hand-rolled HTTP.
//
// Connection topology (dev):
//   - gRPC dial address: zitadel-api:8080 (internal docker DNS)
//   - gRPC :authority   : localhost (Zitadel rejects mismatched hosts)
//   - Issuer / Origin   : http://localhost:8081 (only used by JWT-profile
//     auth, not by PAT — irrelevant here)

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	zsdk "github.com/zitadel/zitadel-go/v3/pkg/client"
	applicationV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/application/v2"
	authorizationV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/authorization/v2"
	filterV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/filter/v2"
	objectV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/object/v2"
	orgV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	projectV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/project/v2"
	userV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
	"github.com/zitadel/zitadel-go/v3/pkg/zitadel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
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

// defaultOrgID returns the ID of the Zitadel instance's default
// organization. The Limen Gateway project lives there.
func (b *bootstrap) defaultOrgID(ctx context.Context) (string, error) {
	resp, err := b.api.OrganizationServiceV2().ListOrganizations(ctx, &orgV2.ListOrganizationsRequest{
		Queries: []*orgV2.SearchQuery{
			{Query: &orgV2.SearchQuery_DefaultQuery{DefaultQuery: &orgV2.DefaultOrganizationQuery{}}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("list default org: %w", err)
	}
	for _, o := range resp.GetResult() {
		if id := o.GetId(); id != "" {
			return id, nil
		}
	}
	return "", errors.New("no default organization found")
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

func (b *bootstrap) ensureOIDCApp(ctx context.Context, projectID, name, redirectURI, postLogoutURI string) (string, error) {
	postLogoutURIs := postLogoutURIVariants(postLogoutURI)
	if existing, err := b.findAppRaw(ctx, projectID, name); err == nil && existing != nil {
		oidc := existing.GetOidcConfiguration()
		if oidc == nil {
			return "", fmt.Errorf("app %q exists but is not OIDC", name)
		}
		if len(postLogoutURIs) > 0 && !containsAll(oidc.GetPostLogoutRedirectUris(), postLogoutURIs) {
			if _, err := b.api.ApplicationServiceV2().UpdateApplication(ctx, &applicationV2.UpdateApplicationRequest{
				ProjectId:     projectID,
				ApplicationId: existing.GetApplicationId(),
				ApplicationType: &applicationV2.UpdateApplicationRequest_OidcConfiguration{
					OidcConfiguration: &applicationV2.UpdateOIDCApplicationConfigurationRequest{
						PostLogoutRedirectUris: postLogoutURIs,
					},
				},
			}); err != nil {
				return "", fmt.Errorf("update OIDC app %q post-logout URIs: %w", name, err)
			}
			log.Printf("updated %s post-logout URIs: %v", name, postLogoutURIs)
		}
		return oidc.GetClientId(), nil
	}
	resp, err := b.api.ApplicationServiceV2().CreateApplication(ctx, &applicationV2.CreateApplicationRequest{
		ProjectId: projectID,
		Name:      name,
		ApplicationType: &applicationV2.CreateApplicationRequest_OidcConfiguration{
			OidcConfiguration: &applicationV2.CreateOIDCApplicationRequest{
				RedirectUris:             []string{redirectURI},
				PostLogoutRedirectUris:   postLogoutURIs,
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
	resp, err := b.api.UserServiceV2().AddHumanUser(ctx, &userV2.AddHumanUserRequest{
		Organization: &objectV2.Organization{Org: &objectV2.Organization_OrgId{OrgId: orgID}},
		Username:     &username,
		Profile: &userV2.SetHumanProfile{
			GivenName:         given,
			FamilyName:        family,
			PreferredLanguage: ptr("en"),
		},
		Email: &userV2.SetHumanEmail{Email: email},
	})
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

	defaultOrg, err := b.defaultOrgID(ctx)
	if err != nil {
		log.Fatalf("resolve default org: %v", err)
	}
	log.Printf("default org: %s", defaultOrg)

	projectID, err := b.ensureProject(ctx, defaultOrg, "Limen Gateway")
	if err != nil {
		log.Fatalf("ensure project: %v", err)
	}
	log.Printf("project: %s", projectID)

	portalRedirect := getenvDefault("LIMEN_PORTAL_REDIRECT", "http://localhost:8080/auth/callback")
	portalPostLogout := getenvDefault("LIMEN_PORTAL_POST_LOGOUT", "http://localhost:8080/")
	portalClientID, err := b.ensureOIDCApp(ctx, projectID, "Limen Portal", portalRedirect, portalPostLogout)
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

	sampleSlug := getenvDefault("LIMEN_SAMPLE_TENANT_SLUG", "acme")
	orgID, err := b.ensureOrg(ctx, sampleSlug)
	if err != nil {
		log.Fatalf("ensure sample org: %v", err)
	}
	log.Printf("sample org %q: %s", sampleSlug, orgID)

	allRoles := []string{"member", "admin", "owner", "super_admin"}
	if err := b.ensureProjectGrant(ctx, projectID, orgID, allRoles); err != nil {
		log.Fatalf("ensure sample project grant: %v", err)
	}
	log.Printf("project granted to sample org")

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

	out := map[string]string{
		"LIMEN_OIDC_PORTAL_CLIENT_ID": portalClientID,
		"LIMEN_OIDC_MCP_RS_CLIENT_ID": mcpClientID,
		"LIMEN_OIDC_PROJECT_ID":       projectID,
		"LIMEN_SAMPLE_TENANT_ORG_ID":  orgID,
		"LIMEN_SAMPLE_TENANT_SLUG":    sampleSlug,
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
