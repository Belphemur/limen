package zitadel

import (
	"context"
	"fmt"
	"time"

	filterV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/filter/v2"
	userV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// MachineUser is the input for creating a Zitadel machine user (service account).
type MachineUser struct {
	Name            string
	Description     string
	OrgID           string
	AccessTokenType userV2.AccessTokenType
}

// CreateMachineUser creates a machine user (service account) in the given
// Zitadel organization via the UserServiceV2.CreateUser API. Returns the
// new machine user's Zitadel ID.
func (c *Client) CreateMachineUser(ctx context.Context, u MachineUser) (string, error) {
	if u.OrgID == "" {
		return "", fmt.Errorf("zitadel: CreateMachineUser: OrgID is required")
	}
	if u.Name == "" {
		return "", fmt.Errorf("zitadel: CreateMachineUser: Name is required")
	}

	req := &userV2.CreateUserRequest{
		OrganizationId: u.OrgID,
		UserType: &userV2.CreateUserRequest_Machine_{
			Machine: &userV2.CreateUserRequest_Machine{
				Name:            u.Name,
				AccessTokenType: u.AccessTokenType,
			},
		},
	}
	if u.Description != "" {
		req.GetMachine().Description = &u.Description
	}

	resp, err := c.api.UserServiceV2().CreateUser(ctx, req)
	if err != nil {
		return "", fmt.Errorf("zitadel: create machine user %q: %w", u.Name, err)
	}
	return resp.GetId(), nil
}

// GetMachineUser retrieves a machine user by Zitadel user ID. Returns the
// full user response including the Machine projection.
func (c *Client) GetMachineUser(ctx context.Context, zitadelUserID string) (*userV2.GetUserByIDResponse, error) {
	if zitadelUserID == "" {
		return nil, fmt.Errorf("zitadel: GetMachineUser: zitadelUserID is required")
	}

	resp, err := c.api.UserServiceV2().GetUserByID(ctx, &userV2.GetUserByIDRequest{
		UserId: zitadelUserID,
	})
	if err != nil {
		return nil, fmt.Errorf("zitadel: get machine user %q: %w", zitadelUserID, err)
	}
	return resp, nil
}

// DeleteMachineUser removes a machine user from Zitadel.
func (c *Client) DeleteMachineUser(ctx context.Context, zitadelUserID string) error {
	if zitadelUserID == "" {
		return fmt.Errorf("zitadel: DeleteMachineUser: zitadelUserID is required")
	}

	_, err := c.api.UserServiceV2().DeleteUser(ctx, &userV2.DeleteUserRequest{
		UserId: zitadelUserID,
	})
	if err != nil {
		return fmt.Errorf("zitadel: delete machine user %q: %w", zitadelUserID, err)
	}
	return nil
}

// AddPersonalAccessToken creates a PAT for a Zitadel user. When expiry is
// nil no expiration is set; Zitadel treats this as "never expires".
// Returns the token ID and the token value. The token is only returned once.
func (c *Client) AddPersonalAccessToken(ctx context.Context, zitadelUserID string, expiry *time.Time) (string, string, error) {
	if zitadelUserID == "" {
		return "", "", fmt.Errorf("zitadel: AddPersonalAccessToken: zitadelUserID is required")
	}

	req := &userV2.AddPersonalAccessTokenRequest{
		UserId: zitadelUserID,
	}
	if expiry != nil {
		req.ExpirationDate = timestamppb.New(*expiry)
	}

	resp, err := c.api.UserServiceV2().AddPersonalAccessToken(ctx, req)
	if err != nil {
		return "", "", fmt.Errorf("zitadel: add personal access token (user=%q): %w", zitadelUserID, err)
	}
	return resp.GetTokenId(), resp.GetToken(), nil
}

// ListPersonalAccessTokens returns all PATs for a Zitadel user.
func (c *Client) ListPersonalAccessTokens(ctx context.Context, zitadelUserID string) ([]*userV2.PersonalAccessToken, error) {
	if zitadelUserID == "" {
		return nil, fmt.Errorf("zitadel: ListPersonalAccessTokens: zitadelUserID is required")
	}

	resp, err := c.api.UserServiceV2().ListPersonalAccessTokens(ctx, &userV2.ListPersonalAccessTokensRequest{
		Filters: []*userV2.PersonalAccessTokensSearchFilter{
			{
				Filter: &userV2.PersonalAccessTokensSearchFilter_UserIdFilter{
					UserIdFilter: &filterV2.IDFilter{Id: zitadelUserID},
				},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("zitadel: list personal access tokens (user=%q): %w", zitadelUserID, err)
	}
	return resp.GetResult(), nil
}

// RemovePersonalAccessToken removes a specific PAT from a Zitadel user.
func (c *Client) RemovePersonalAccessToken(ctx context.Context, zitadelUserID, tokenID string) error {
	if zitadelUserID == "" {
		return fmt.Errorf("zitadel: RemovePersonalAccessToken: zitadelUserID is required")
	}
	if tokenID == "" {
		return fmt.Errorf("zitadel: RemovePersonalAccessToken: tokenID is required")
	}

	_, err := c.api.UserServiceV2().RemovePersonalAccessToken(ctx, &userV2.RemovePersonalAccessTokenRequest{
		UserId:  zitadelUserID,
		TokenId: tokenID,
	})
	if err != nil {
		return fmt.Errorf("zitadel: remove personal access token (user=%q token=%q): %w", zitadelUserID, tokenID, err)
	}
	return nil
}
