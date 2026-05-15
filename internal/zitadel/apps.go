package zitadel

import (
	"context"
	"fmt"

	applicationV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/application/v2"
)

// OIDCAppType captures the RFC 7591 `application_type` values Limen forwards
// to Zitadel. Strings match the DCR / OIDC client metadata spec so they
// round-trip cleanly through the proxy.
type OIDCAppType string

const (
	OIDCAppTypeNative OIDCAppType = "native"
	OIDCAppTypeWeb    OIDCAppType = "web"
)

// OIDCAuthMethod is the subset of `token_endpoint_auth_method` values
// Limen's DCR proxy accepts (see Phase 5). `none` = public PKCE client,
// `client_secret_basic` = confidential.
type OIDCAuthMethod string

const (
	OIDCAuthMethodNone  OIDCAuthMethod = "none"
	OIDCAuthMethodBasic OIDCAuthMethod = "client_secret_basic"
)

// AddOIDCAppInput is the Limen-shaped input for AddOIDCApp. It only exposes
// the fields the DCR proxy actually writes; Zitadel's other knobs (token
// type, role assertions, clock skew, ...) keep their defaults.
type AddOIDCAppInput struct {
	OrgID string
	// ProjectID picks the Zitadel project the app is created in. Empty
	// falls back to Client.projectID (Limen's shared gateway project),
	// used by the bootstrap path. DCR-created apps always pass a
	// per-client project id — see
	// docs/phases/phase-07b-dcr-per-client-project.md.
	ProjectID              string
	Name                   string
	RedirectURIs           []string
	PostLogoutRedirectURIs []string
	AppType                OIDCAppType
	AuthMethod             OIDCAuthMethod
}

// UpdateOIDCAppInput is the full-replace payload for UpdateOIDCApp,
// matching the semantics of RFC 7592 `PUT /register/{client_id}`.
type UpdateOIDCAppInput struct {
	OrgID string
	// ProjectID picks the Zitadel project hosting the app. Empty falls
	// back to Client.projectID.
	ProjectID              string
	AppID                  string
	RedirectURIs           []string
	PostLogoutRedirectURIs []string
	AppType                OIDCAppType
	AuthMethod             OIDCAuthMethod
}

// OIDCApp is the Limen projection of a Zitadel OIDC app. ClientSecret is
// only populated by AddOIDCApp for confidential clients; GetOIDCApp leaves
// it empty.
type OIDCApp struct {
	AppID                  string
	ClientID               string
	ClientSecret           string
	Name                   string
	RedirectURIs           []string
	PostLogoutRedirectURIs []string
	AppType                OIDCAppType
	AuthMethod             OIDCAuthMethod
}

// AddOIDCApp creates a new OIDC application inside orgID's project. Limen
// pins response_types=[code] and grant_types=[authorization_code,
// refresh_token]; PKCE is required automatically by Zitadel when
// AuthMethod=none.
func (c *Client) AddOIDCApp(ctx context.Context, in AddOIDCAppInput) (*OIDCApp, error) {
	if in.OrgID == "" {
		return nil, fmt.Errorf("zitadel: AddOIDCApp: OrgID is required")
	}
	if in.Name == "" {
		return nil, fmt.Errorf("zitadel: AddOIDCApp: Name is required")
	}
	if len(in.RedirectURIs) == 0 {
		return nil, fmt.Errorf("zitadel: AddOIDCApp: RedirectURIs is required")
	}
	appType, err := encodeAppType(in.AppType)
	if err != nil {
		return nil, err
	}
	authMethod, err := encodeAuthMethod(in.AuthMethod)
	if err != nil {
		return nil, err
	}

	resp, err := c.api.ApplicationServiceV2().CreateApplication(ctx, &applicationV2.CreateApplicationRequest{
		ProjectId: c.projectIDOr(in.ProjectID),
		Name:      in.Name,
		ApplicationType: &applicationV2.CreateApplicationRequest_OidcConfiguration{
			OidcConfiguration: &applicationV2.CreateOIDCApplicationRequest{
				RedirectUris:           in.RedirectURIs,
				PostLogoutRedirectUris: in.PostLogoutRedirectURIs,
				ResponseTypes: []applicationV2.OIDCResponseType{
					applicationV2.OIDCResponseType_OIDC_RESPONSE_TYPE_CODE,
				},
				GrantTypes: []applicationV2.OIDCGrantType{
					applicationV2.OIDCGrantType_OIDC_GRANT_TYPE_AUTHORIZATION_CODE,
					applicationV2.OIDCGrantType_OIDC_GRANT_TYPE_REFRESH_TOKEN,
				},
				ApplicationType: appType,
				AuthMethodType:  authMethod,
				Version:         applicationV2.OIDCVersion_OIDC_VERSION_1_0,
				// JWT access tokens so the MCP resource server can verify
				// them locally against Zitadel's JWKS instead of running
				// an introspection round-trip per request.
				AccessTokenType: applicationV2.OIDCTokenType_OIDC_TOKEN_TYPE_JWT,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("zitadel: create OIDC app %q (org=%q): %w", in.Name, in.OrgID, err)
	}
	oidc := resp.GetOidcConfiguration()
	return &OIDCApp{
		AppID:                  resp.GetApplicationId(),
		ClientID:               oidc.GetClientId(),
		ClientSecret:           oidc.GetClientSecret(),
		Name:                   in.Name,
		RedirectURIs:           in.RedirectURIs,
		PostLogoutRedirectURIs: in.PostLogoutRedirectURIs,
		AppType:                in.AppType,
		AuthMethod:             in.AuthMethod,
	}, nil
}

// UpdateOIDCApp replaces the config of an existing OIDC app. Full-replace
// semantics: any field not set on UpdateOIDCAppInput is cleared.
func (c *Client) UpdateOIDCApp(ctx context.Context, in UpdateOIDCAppInput) error {
	if in.OrgID == "" {
		return fmt.Errorf("zitadel: UpdateOIDCApp: OrgID is required")
	}
	if in.AppID == "" {
		return fmt.Errorf("zitadel: UpdateOIDCApp: AppID is required")
	}
	if len(in.RedirectURIs) == 0 {
		return fmt.Errorf("zitadel: UpdateOIDCApp: RedirectURIs is required")
	}
	appType, err := encodeAppType(in.AppType)
	if err != nil {
		return err
	}
	authMethod, err := encodeAuthMethod(in.AuthMethod)
	if err != nil {
		return err
	}

	if _, err := c.api.ApplicationServiceV2().UpdateApplication(ctx, &applicationV2.UpdateApplicationRequest{
		ApplicationId: in.AppID,
		ProjectId:     c.projectIDOr(in.ProjectID),
		ApplicationType: &applicationV2.UpdateApplicationRequest_OidcConfiguration{
			OidcConfiguration: &applicationV2.UpdateOIDCApplicationConfigurationRequest{
				RedirectUris:           in.RedirectURIs,
				PostLogoutRedirectUris: in.PostLogoutRedirectURIs,
				ResponseTypes: []applicationV2.OIDCResponseType{
					applicationV2.OIDCResponseType_OIDC_RESPONSE_TYPE_CODE,
				},
				GrantTypes: []applicationV2.OIDCGrantType{
					applicationV2.OIDCGrantType_OIDC_GRANT_TYPE_AUTHORIZATION_CODE,
					applicationV2.OIDCGrantType_OIDC_GRANT_TYPE_REFRESH_TOKEN,
				},
				ApplicationType: &appType,
				AuthMethodType:  &authMethod,
			},
		},
	}); err != nil {
		return fmt.Errorf("zitadel: update OIDC app (app=%q org=%q): %w", in.AppID, in.OrgID, err)
	}
	return nil
}

// DeleteOIDCApp removes an OIDC app from a project. If projectID is empty
// the client falls back to Client.projectID (Limen's shared project).
func (c *Client) DeleteOIDCApp(ctx context.Context, orgID, projectID, appID string) error {
	if orgID == "" {
		return fmt.Errorf("zitadel: DeleteOIDCApp: orgID is required")
	}
	if appID == "" {
		return fmt.Errorf("zitadel: DeleteOIDCApp: appID is required")
	}
	if _, err := c.api.ApplicationServiceV2().DeleteApplication(ctx, &applicationV2.DeleteApplicationRequest{
		ApplicationId: appID,
		ProjectId:     c.projectIDOr(projectID),
	}); err != nil {
		return fmt.Errorf("zitadel: delete app (app=%q org=%q): %w", appID, orgID, err)
	}
	return nil
}

// GetOIDCApp fetches an OIDC app by id. Returns an error if the app exists
// but is not an OIDC application (e.g. API or SAML). projectID is accepted
// for API symmetry but the GetApplication call resolves the app by id
// alone — Zitadel verifies project scoping on its side.
func (c *Client) GetOIDCApp(ctx context.Context, orgID, projectID, appID string) (*OIDCApp, error) {
	_ = projectID
	if orgID == "" {
		return nil, fmt.Errorf("zitadel: GetOIDCApp: orgID is required")
	}
	if appID == "" {
		return nil, fmt.Errorf("zitadel: GetOIDCApp: appID is required")
	}
	resp, err := c.api.ApplicationServiceV2().GetApplication(ctx, &applicationV2.GetApplicationRequest{
		ApplicationId: appID,
	})
	if err != nil {
		return nil, fmt.Errorf("zitadel: get app (app=%q org=%q): %w", appID, orgID, err)
	}
	got := resp.GetApplication()
	cfg := got.GetOidcConfiguration()
	if cfg == nil {
		return nil, fmt.Errorf("zitadel: app %q is not an OIDC application", appID)
	}
	return &OIDCApp{
		AppID:                  got.GetApplicationId(),
		ClientID:               cfg.GetClientId(),
		Name:                   got.GetName(),
		RedirectURIs:           cfg.GetRedirectUris(),
		PostLogoutRedirectURIs: cfg.GetPostLogoutRedirectUris(),
		AppType:                decodeAppType(cfg.GetApplicationType()),
		AuthMethod:             decodeAuthMethod(cfg.GetAuthMethodType()),
	}, nil
}

func encodeAppType(t OIDCAppType) (applicationV2.OIDCApplicationType, error) {
	switch t {
	case "", OIDCAppTypeNative:
		return applicationV2.OIDCApplicationType_OIDC_APP_TYPE_NATIVE, nil
	case OIDCAppTypeWeb:
		return applicationV2.OIDCApplicationType_OIDC_APP_TYPE_WEB, nil
	default:
		return 0, fmt.Errorf("zitadel: unsupported OIDCAppType %q", t)
	}
}

func decodeAppType(t applicationV2.OIDCApplicationType) OIDCAppType {
	switch t {
	case applicationV2.OIDCApplicationType_OIDC_APP_TYPE_WEB:
		return OIDCAppTypeWeb
	default:
		return OIDCAppTypeNative
	}
}

func encodeAuthMethod(m OIDCAuthMethod) (applicationV2.OIDCAuthMethodType, error) {
	switch m {
	case "", OIDCAuthMethodNone:
		return applicationV2.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_NONE, nil
	case OIDCAuthMethodBasic:
		return applicationV2.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_BASIC, nil
	default:
		return 0, fmt.Errorf("zitadel: unsupported OIDCAuthMethod %q", m)
	}
}

func decodeAuthMethod(m applicationV2.OIDCAuthMethodType) OIDCAuthMethod {
	switch m {
	case applicationV2.OIDCAuthMethodType_OIDC_AUTH_METHOD_TYPE_BASIC:
		return OIDCAuthMethodBasic
	default:
		return OIDCAuthMethodNone
	}
}
