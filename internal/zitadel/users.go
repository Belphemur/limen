package zitadel

import (
	"context"
	"fmt"

	authorizationV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/authorization/v2"
	filterV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/filter/v2"
	userV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
)

// HumanUser is the input for AddHumanUser.
type HumanUser struct {
	Email      string
	GivenName  string
	FamilyName string
	// Username is optional; Zitadel uses Email when empty.
	Username string
	// OrgID scopes the new user to a tenant organization. Required.
	OrgID string
}

// UserGrant is the Limen-shaped projection of a Zitadel user grant.
type UserGrant struct {
	ID        string
	UserID    string
	ProjectID string
	OrgID     string
	RoleKeys  []string
}

// AddHumanUser creates a new human user in the given Zitadel organization.
// Returns the new user's id. Zitadel emails the user an initialization
// link unless the instance is configured otherwise.
func (c *Client) AddHumanUser(ctx context.Context, u HumanUser) (string, error) {
	if u.OrgID == "" {
		return "", fmt.Errorf("zitadel: AddHumanUser: OrgID is required")
	}
	if u.Email == "" {
		return "", fmt.Errorf("zitadel: AddHumanUser: Email is required")
	}

	req := &userV2.CreateUserRequest{
		OrganizationId: u.OrgID,
		UserType: &userV2.CreateUserRequest_Human_{
			Human: &userV2.CreateUserRequest_Human{
				Profile: &userV2.SetHumanProfile{
					GivenName:  u.GivenName,
					FamilyName: u.FamilyName,
				},
				Email: &userV2.SetHumanEmail{Email: u.Email},
			},
		},
	}
	if u.Username != "" {
		req.Username = &u.Username
	}

	resp, err := c.api.UserServiceV2().CreateUser(ctx, req)
	if err != nil {
		return "", fmt.Errorf("zitadel: create user %q: %w", u.Email, err)
	}
	return resp.GetId(), nil
}

// CreateInviteCode triggers Zitadel to send an invite email to userID using
// the default URL configured on the instance. Limen does not store the
// generated code.
func (c *Client) CreateInviteCode(ctx context.Context, userID string) error {
	req := &userV2.CreateInviteCodeRequest{
		UserId: userID,
		Verification: &userV2.CreateInviteCodeRequest_SendCode{
			SendCode: &userV2.SendInviteCode{},
		},
	}
	if _, err := c.api.UserServiceV2().CreateInviteCode(ctx, req); err != nil {
		return fmt.Errorf("zitadel: create invite code for %q: %w", userID, err)
	}
	return nil
}

// AddUserGrant assigns role keys to userID on the configured Limen project,
// scoped to orgID. Idempotent at the Zitadel level — re-adding an existing
// grant returns an ALREADY_EXISTS error which the caller may ignore.
func (c *Client) AddUserGrant(ctx context.Context, orgID, userID string, roleKeys []string) (string, error) {
	if orgID == "" {
		return "", fmt.Errorf("zitadel: AddUserGrant: orgID is required")
	}
	resp, err := c.api.AuthorizationServiceV2().CreateAuthorization(ctx, &authorizationV2.CreateAuthorizationRequest{
		UserId:         userID,
		ProjectId:      c.projectID,
		OrganizationId: orgID,
		RoleKeys:       roleKeys,
	})
	if err != nil {
		return "", fmt.Errorf("zitadel: create authorization (user=%q org=%q): %w", userID, orgID, err)
	}
	return resp.GetId(), nil
}

// ListUserGrants returns all grants on the configured project, optionally
// filtered by userID. orgID scopes the call to one tenant org.
func (c *Client) ListUserGrants(ctx context.Context, orgID, userID string) ([]UserGrant, error) {
	if orgID == "" {
		return nil, fmt.Errorf("zitadel: ListUserGrants: orgID is required")
	}

	filters := []*authorizationV2.AuthorizationsSearchFilter{
		{
			Filter: &authorizationV2.AuthorizationsSearchFilter_OrganizationId{
				OrganizationId: &filterV2.IDFilter{Id: orgID},
			},
		},
		{
			Filter: &authorizationV2.AuthorizationsSearchFilter_ProjectId{
				ProjectId: &filterV2.IDFilter{Id: c.projectID},
			},
		},
	}
	if userID != "" {
		filters = append(filters, &authorizationV2.AuthorizationsSearchFilter{
			Filter: &authorizationV2.AuthorizationsSearchFilter_InUserIds{
				InUserIds: &filterV2.InIDsFilter{Ids: []string{userID}},
			},
		})
	}

	resp, err := c.api.AuthorizationServiceV2().ListAuthorizations(ctx, &authorizationV2.ListAuthorizationsRequest{
		Filters: filters,
	})
	if err != nil {
		return nil, fmt.Errorf("zitadel: list authorizations (org=%q user=%q): %w", orgID, userID, err)
	}
	authzs := resp.GetAuthorizations()
	out := make([]UserGrant, 0, len(authzs))
	for _, a := range authzs {
		roles := a.GetRoles()
		keys := make([]string, 0, len(roles))
		for _, r := range roles {
			keys = append(keys, r.GetKey())
		}
		out = append(out, UserGrant{
			ID:        a.GetId(),
			UserID:    a.GetUser().GetId(),
			ProjectID: a.GetProject().GetId(),
			OrgID:     a.GetOrganization().GetId(),
			RoleKeys:  keys,
		})
	}
	return out, nil
}
