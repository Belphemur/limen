package zitadel

import (
	"context"
	"fmt"
	"time"

	authorizationV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/authorization/v2"
	filterV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/filter/v2"
	objectV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/object/v2"
	userV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
)

// textContainsCI is the Zitadel text-query method Limen uses for
// admin-side substring search (e.g. ListOrgUsers).
const textContainsCI = objectV2.TextQueryMethod_TEXT_QUERY_METHOD_CONTAINS_IGNORE_CASE

// OrgUser is the Limen-shaped projection of a Zitadel User in an org.
type OrgUser struct {
	ID                 string
	Email              string
	GivenName          string
	FamilyName         string
	DisplayName        string
	Username           string
	PreferredLoginName string
	// State is the lowercased Zitadel UserState short form:
	// "active" / "inactive" / "locked" / "initial". Empty when Zitadel
	// returns USER_STATE_UNSPECIFIED.
	State string
	// LastLogin is best-effort: Zitadel reports the user's most recent
	// change date (which covers logins) via Details.ChangeDate. Zero
	// when unavailable.
	LastLogin time.Time
}

// HumanUser is the input for AddHumanUser.
type HumanUser struct {
	Email      string
	GivenName  string
	FamilyName string
	// Username is optional; Zitadel uses Email when empty.
	Username string
	// OrgID scopes the new user to a tenant organization. Required.
	OrgID string
	// EmailVerified pre-flags the email as already verified. Set this
	// when Limen has already proven ownership out-of-band (e.g. the
	// Phase 9h signup wizard's verification email) so Zitadel does
	// not send its own verification message.
	EmailVerified bool
	// Password, when non-empty, is set on the user at creation time
	// (used by the Phase 9h signup wizard which collects the owner's
	// password in the SPA before calling CompleteSignup). Limen does
	// not persist the plaintext — it is forwarded once to Zitadel.
	Password string
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
	if u.EmailVerified {
		req.GetHuman().Email.Verification = &userV2.SetHumanEmail_IsVerified{IsVerified: true}
	}
	if u.Password != "" {
		req.GetHuman().PasswordType = &userV2.CreateUserRequest_Human_Password{
			Password: &userV2.Password{Password: u.Password},
		}
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
		if IsAlreadyExists(err) {
			return "", nil
		}
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

// UserExistsByEmail returns true when at least one human user in the
// Zitadel instance has the given email address (case-insensitive
// exact match, no organisation filter). Used by the signup wizard to
// reject duplicate emails at the first form, before sending a
// verification email that the user could never complete.
func (c *Client) UserExistsByEmail(ctx context.Context, email string) (bool, error) {
	if email == "" {
		return false, fmt.Errorf("zitadel: UserExistsByEmail: email is required")
	}
	resp, err := c.api.UserServiceV2().ListUsers(ctx, &userV2.ListUsersRequest{
		Queries: []*userV2.SearchQuery{
			{Query: &userV2.SearchQuery_EmailQuery{
				EmailQuery: &userV2.EmailQuery{
					EmailAddress: email,
					Method:       objectV2.TextQueryMethod_TEXT_QUERY_METHOD_EQUALS_IGNORE_CASE,
				},
			}},
		},
	})
	if err != nil {
		return false, fmt.Errorf("zitadel: user exists by email %q: %w", email, err)
	}
	for _, u := range resp.GetResult() {
		if u.GetHuman() != nil {
			return true, nil
		}
	}
	return false, nil
}

// ListOrgUsers returns every human user in orgID. When search is
// non-empty it adds an OR filter across display name + email
// (case-insensitive contains). Machine users are not returned.
func (c *Client) ListOrgUsers(ctx context.Context, orgID, search string) ([]OrgUser, error) {
	if orgID == "" {
		return nil, fmt.Errorf("zitadel: ListOrgUsers: orgID is required")
	}

	queries := []*userV2.SearchQuery{
		{Query: &userV2.SearchQuery_OrganizationIdQuery{
			OrganizationIdQuery: &userV2.OrganizationIdQuery{OrganizationId: orgID},
		}},
	}
	if search != "" {
		queries = append(queries, &userV2.SearchQuery{
			Query: &userV2.SearchQuery_OrQuery{
				OrQuery: &userV2.OrQuery{
					Queries: []*userV2.SearchQuery{
						{Query: &userV2.SearchQuery_DisplayNameQuery{
							DisplayNameQuery: &userV2.DisplayNameQuery{
								DisplayName: search,
								Method:      textContainsCI,
							},
						}},
						{Query: &userV2.SearchQuery_EmailQuery{
							EmailQuery: &userV2.EmailQuery{
								EmailAddress: search,
								Method:       textContainsCI,
							},
						}},
					},
				},
			},
		})
	}

	resp, err := c.api.UserServiceV2().ListUsers(ctx, &userV2.ListUsersRequest{Queries: queries})
	if err != nil {
		return nil, fmt.Errorf("zitadel: list users (org=%q): %w", orgID, err)
	}
	users := resp.GetResult()
	out := make([]OrgUser, 0, len(users))
	for _, u := range users {
		human := u.GetHuman()
		if human == nil {
			continue
		}
		profile := human.GetProfile()
		email := ""
		if e := human.GetEmail(); e != nil {
			email = e.GetEmail()
		}
		last := time.Time{}
		if d := u.GetDetails(); d != nil {
			if ts := d.GetChangeDate(); ts != nil {
				last = ts.AsTime()
			}
		}
		out = append(out, OrgUser{
			ID:                 u.GetUserId(),
			Email:              email,
			GivenName:          profile.GetGivenName(),
			FamilyName:         profile.GetFamilyName(),
			DisplayName:        profile.GetDisplayName(),
			Username:           u.GetUsername(),
			PreferredLoginName: u.GetPreferredLoginName(),
			State:              userStateShort(u.GetState()),
			LastLogin:          last,
		})
	}
	return out, nil
}

// UpdateUserGrant replaces the role keys on the existing
// authorization (grant) identified by grantID. Zitadel treats RoleKeys
// as a full replacement set.
func (c *Client) UpdateUserGrant(ctx context.Context, grantID string, roleKeys []string) error {
	if grantID == "" {
		return fmt.Errorf("zitadel: UpdateUserGrant: grantID is required")
	}
	if _, err := c.api.AuthorizationServiceV2().UpdateAuthorization(ctx, &authorizationV2.UpdateAuthorizationRequest{
		Id:       grantID,
		RoleKeys: roleKeys,
	}); err != nil {
		return fmt.Errorf("zitadel: update authorization %q: %w", grantID, err)
	}
	return nil
}

// DeleteUserGrant removes the authorization (grant) identified by
// grantID. The underlying user is untouched.
func (c *Client) DeleteUserGrant(ctx context.Context, grantID string) error {
	if grantID == "" {
		return fmt.Errorf("zitadel: DeleteUserGrant: grantID is required")
	}
	if _, err := c.api.AuthorizationServiceV2().DeleteAuthorization(ctx, &authorizationV2.DeleteAuthorizationRequest{
		Id: grantID,
	}); err != nil {
		return fmt.Errorf("zitadel: delete authorization %q: %w", grantID, err)
	}
	return nil
}

// DeleteUser hard-deletes userID from Zitadel. All authorizations on
// that user cascade.
func (c *Client) DeleteUser(ctx context.Context, userID string) error {
	if userID == "" {
		return fmt.Errorf("zitadel: DeleteUser: userID is required")
	}
	if _, err := c.api.UserServiceV2().DeleteUser(ctx, &userV2.DeleteUserRequest{UserId: userID}); err != nil {
		return fmt.Errorf("zitadel: delete user %q: %w", userID, err)
	}
	return nil
}

// userStateShort lowercases the Zitadel UserState enum to the Limen
// wire form. USER_STATE_DELETED collapses to "inactive" because Limen
// only sees deleted-state users transiently between RemoveMember and
// the next ListMembers.
func userStateShort(s userV2.UserState) string {
	switch s {
	case userV2.UserState_USER_STATE_ACTIVE:
		return "active"
	case userV2.UserState_USER_STATE_INACTIVE, userV2.UserState_USER_STATE_DELETED:
		return "inactive"
	case userV2.UserState_USER_STATE_LOCKED:
		return "locked"
	case userV2.UserState_USER_STATE_INITIAL:
		return "initial"
	default:
		return ""
	}
}
