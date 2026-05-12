package zitadel

import (
	"context"
	"fmt"

	"github.com/zitadel/zitadel-go/v3/pkg/client/middleware"
	objectV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/object/v2"
	userV1 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user"
	userV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"

	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/management"
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

	req := &userV2.AddHumanUserRequest{
		Organization: &objectV2.Organization{
			Org: &objectV2.Organization_OrgId{OrgId: u.OrgID},
		},
		Profile: &userV2.SetHumanProfile{
			GivenName:  u.GivenName,
			FamilyName: u.FamilyName,
		},
		Email: &userV2.SetHumanEmail{Email: u.Email},
	}
	if u.Username != "" {
		req.Username = &u.Username
	}

	resp, err := c.api.UserServiceV2().AddHumanUser(ctx, req)
	if err != nil {
		return "", fmt.Errorf("zitadel: add human user %q: %w", u.Email, err)
	}
	return resp.GetUserId(), nil
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
	ctx = middleware.SetOrgID(ctx, orgID)
	req := &management.AddUserGrantRequest{
		UserId:    userID,
		ProjectId: c.projectID,
		RoleKeys:  roleKeys,
	}
	resp, err := c.api.ManagementService().AddUserGrant(ctx, req)
	if err != nil {
		return "", fmt.Errorf("zitadel: add user grant (user=%q org=%q): %w", userID, orgID, err)
	}
	return resp.GetUserGrantId(), nil
}

// ListUserGrants returns all grants on the configured project, optionally
// filtered by userID. orgID scopes the call to one tenant org.
func (c *Client) ListUserGrants(ctx context.Context, orgID, userID string) ([]UserGrant, error) {
	if orgID == "" {
		return nil, fmt.Errorf("zitadel: ListUserGrants: orgID is required")
	}
	ctx = middleware.SetOrgID(ctx, orgID)

	queries := []*userV1.UserGrantQuery{{
		Query: &userV1.UserGrantQuery_ProjectIdQuery{
			ProjectIdQuery: &userV1.UserGrantProjectIDQuery{ProjectId: c.projectID},
		},
	}}
	if userID != "" {
		queries = append(queries, &userV1.UserGrantQuery{
			Query: &userV1.UserGrantQuery_UserIdQuery{
				UserIdQuery: &userV1.UserGrantUserIDQuery{UserId: userID},
			},
		})
	}

	resp, err := c.api.ManagementService().ListUserGrants(ctx, &management.ListUserGrantRequest{Queries: queries})
	if err != nil {
		return nil, fmt.Errorf("zitadel: list user grants (org=%q user=%q): %w", orgID, userID, err)
	}
	out := make([]UserGrant, 0, len(resp.GetResult()))
	for _, g := range resp.GetResult() {
		out = append(out, UserGrant{
			ID:        g.GetId(),
			UserID:    g.GetUserId(),
			ProjectID: g.GetProjectId(),
			OrgID:     g.GetOrgId(),
			RoleKeys:  g.GetRoleKeys(),
		})
	}
	return out, nil
}
