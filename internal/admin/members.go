package admin

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/belphemur/limen/internal/admin/adminv1"
	"github.com/belphemur/limen/internal/session"
	"github.com/belphemur/limen/internal/tenancy"
	"github.com/belphemur/limen/internal/zitadel"
	"go.uber.org/zap"
)

// MemberDirectory is the slice of the Zitadel client the admin
// Service uses to read+write tenant members. Defined here (SOLID/ISP)
// so the MCP gateway hot path never transitively links the Zitadel
// SDK.
type MemberDirectory interface {
	ListOrgUsers(ctx context.Context, orgID, search string) ([]zitadel.OrgUser, error)
	ListUserGrants(ctx context.Context, orgID, userID string) ([]zitadel.UserGrant, error)
	AddHumanUser(ctx context.Context, in zitadel.HumanUser) (string, error)
	CreateInviteCode(ctx context.Context, userID string) error
	AddUserGrant(ctx context.Context, orgID, userID string, roleKeys []string) (string, error)
	UpdateUserGrant(ctx context.Context, grantID string, roleKeys []string) error
	DeleteUserGrant(ctx context.Context, grantID string) error
	DeleteUser(ctx context.Context, userID string) error
	// AddOrgRoles grants Zitadel org-level administrator roles. Idempotent.
	AddOrgRoles(ctx context.Context, orgID, userID string, roles []string) error
	// RemoveOrgRoles removes ALL Zitadel org-level administrator roles for
	// the user on the given org. Idempotent.
	RemoveOrgRoles(ctx context.Context, orgID, userID string) error
}



// roleKeyFromProto returns the Zitadel role key for the wire enum.
// ok is false when role is UNSPECIFIED (callers reject the request).
func roleKeyFromProto(r adminv1.MemberRole) (string, bool) {
	switch r {
	case adminv1.MemberRole_MEMBER_ROLE_OWNER:
		return zitadel.RoleKeyOwner, true
	case adminv1.MemberRole_MEMBER_ROLE_ADMIN:
		return zitadel.RoleKeyAdmin, true
	case adminv1.MemberRole_MEMBER_ROLE_MEMBER:
		return zitadel.RoleKeyMember, true
	default:
		return "", false
	}
}

// pickHighestRoleProto collapses the set of role keys attached to a
// user-grant into the single wire-level role Limen surfaces. Owner
// dominates admin dominates member. Unknown keys (e.g. super_admin)
// are ignored on purpose — super_admin is intentionally not
// representable on the wire.
func pickHighestRoleProto(keys []string) adminv1.MemberRole {
	best := adminv1.MemberRole_MEMBER_ROLE_UNSPECIFIED
	for _, k := range keys {
		switch k {
		case zitadel.RoleKeyOwner:
			return adminv1.MemberRole_MEMBER_ROLE_OWNER
		case zitadel.RoleKeyAdmin:
			if best < adminv1.MemberRole_MEMBER_ROLE_ADMIN {
				best = adminv1.MemberRole_MEMBER_ROLE_ADMIN
			}
		case zitadel.RoleKeyMember:
			if best < adminv1.MemberRole_MEMBER_ROLE_MEMBER {
				best = adminv1.MemberRole_MEMBER_ROLE_MEMBER
			}
		}
	}
	return best
}

// mapZitadelStateToProto maps the lowercased Zitadel UserState short
// form to the Limen wire enum.
func mapZitadelStateToProto(s string) adminv1.MemberState {
	switch s {
	case "active":
		return adminv1.MemberState_MEMBER_STATE_ACTIVE
	case "inactive":
		return adminv1.MemberState_MEMBER_STATE_INACTIVE
	case "locked":
		return adminv1.MemberState_MEMBER_STATE_LOCKED
	case "initial":
		return adminv1.MemberState_MEMBER_STATE_INITIAL
	default:
		return adminv1.MemberState_MEMBER_STATE_UNSPECIFIED
	}
}

// memberDisplayName picks the best display value: explicit display
// name, then "given family", then preferred login, then username.
func memberDisplayName(u zitadel.OrgUser) string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	if u.GivenName != "" || u.FamilyName != "" {
		return strings.TrimSpace(u.GivenName + " " + u.FamilyName)
	}
	if u.PreferredLoginName != "" {
		return u.PreferredLoginName
	}
	return u.Username
}

// formatLastLogin renders t as RFC3339 or "" when zero.
func formatLastLogin(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ListMembers lists every user in the tenant's Zitadel org, joined to
// their Limen-project grant (if any). When role_filter is set, only
// rows whose joined role exactly matches are returned (UNSPECIFIED
// row is dropped on any role_filter).
func (s *Service) ListMembers(ctx context.Context, req *connect.Request[adminv1.ListMembersRequest]) (*connect.Response[adminv1.ListMembersResponse], error) {
	if s.members == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("admin: member directory not wired"))
	}
	t := tenancy.MustTenant(ctx)
	orgID := t.ZitadelOrgID

	users, err := s.members.ListOrgUsers(ctx, orgID, strings.TrimSpace(req.Msg.GetSearch()))
	if err != nil {
		return nil, s.internal("list org users", err)
	}
	grants, err := s.members.ListUserGrants(ctx, orgID, "")
	if err != nil {
		return nil, s.internal("list user grants", err)
	}

	rolesByUser := make(map[string][]string, len(grants))
	for _, g := range grants {
		rolesByUser[g.UserID] = append(rolesByUser[g.UserID], g.RoleKeys...)
	}

	filter := req.Msg.GetRoleFilter()
	out := make([]*adminv1.Member, 0, len(users))
	for _, u := range users {
		role := pickHighestRoleProto(rolesByUser[u.ID])
		if filter != adminv1.MemberRole_MEMBER_ROLE_UNSPECIFIED && role != filter {
			continue
		}
		out = append(out, &adminv1.Member{
			UserId:      u.ID,
			Email:       u.Email,
			DisplayName: memberDisplayName(u),
			Role:        role,
			State:       mapZitadelStateToProto(u.State),
			LastLogin:   formatLastLogin(u.LastLogin),
		})
	}
	return connect.NewResponse(&adminv1.ListMembersResponse{Members: out}), nil
}

// InviteMember creates a human user in the tenant's Zitadel org,
// grants them the requested role on the Limen project, and triggers
// an invite email. The user's initial state will be "initial" until
// they accept the invite.
func (s *Service) InviteMember(ctx context.Context, req *connect.Request[adminv1.InviteMemberRequest]) (*connect.Response[adminv1.InviteMemberResponse], error) {
	if s.members == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("admin: member directory not wired"))
	}
	msg := req.Msg
	email := strings.TrimSpace(msg.GetEmail())
	if email == "" {
		return nil, s.invalidArg("email", "email must not be empty")
	}
	roleKey, ok := roleKeyFromProto(msg.GetRole())
	if !ok {
		return nil, s.invalidArg("role", "role must be MEMBER, ADMIN, or OWNER")
	}

	t := tenancy.MustTenant(ctx)
	orgID := t.ZitadelOrgID

	givenName := strings.TrimSpace(msg.GetGivenName())
	familyName := strings.TrimSpace(msg.GetFamilyName())

	userID, err := s.members.AddHumanUser(ctx, zitadel.HumanUser{
		OrgID:      orgID,
		Email:      email,
		GivenName:  givenName,
		FamilyName: familyName,
	})
	if err != nil {
		return nil, s.internal("add human user", err)
	}
	if _, err := s.members.AddUserGrant(ctx, orgID, userID, []string{roleKey}); err != nil {
		return nil, s.internal("add user grant", err)
	}
	if err := s.members.CreateInviteCode(ctx, userID); err != nil {
		return nil, s.internal("create invite code", err)
	}

	// Org roles are add-only here because the user was just created
	// (they have no prior org roles). UpdateMemberRole uses remove-first
	// because it handles role transitions where stale roles may exist.
	// Sync Zitadel org-level roles for admin and owner.
	if roles := zitadel.OrgRolesForLimenRole(roleKey); len(roles) > 0 {
		if err := s.members.AddOrgRoles(ctx, orgID, userID, roles); err != nil {
			s.logger.Warn("invite: failed to sync org roles, continuing",
				zap.String("user_id", userID), zap.String("role_key", roleKey), zap.Error(err))
		}
	}

	display := strings.TrimSpace(givenName + " " + familyName)
	if display == "" {
		display = email
	}
	return connect.NewResponse(&adminv1.InviteMemberResponse{
		Member: &adminv1.Member{
			UserId:      userID,
			Email:       email,
			DisplayName: display,
			Role:        msg.GetRole(),
			State:       adminv1.MemberState_MEMBER_STATE_INITIAL,
		},
	}), nil
}

// UpdateMemberRole replaces the role keys on the user's grant for
// the Limen project. If the user has no grant yet (invited but never
// authorized) one is created. The last-owner invariant is enforced:
// the operation is rejected if it would leave the tenant with zero
// owners. Self-demotion is also rejected.
func (s *Service) UpdateMemberRole(ctx context.Context, req *connect.Request[adminv1.UpdateMemberRoleRequest]) (*connect.Response[adminv1.UpdateMemberRoleResponse], error) {
	if s.members == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("admin: member directory not wired"))
	}
	msg := req.Msg
	userID := strings.TrimSpace(msg.GetUserId())
	if userID == "" {
		return nil, s.invalidArg("user_id", "user_id must not be empty")
	}
	roleKey, ok := roleKeyFromProto(msg.GetRole())
	if !ok {
		return nil, s.invalidArg("role", "role must be MEMBER, ADMIN, or OWNER")
	}

	if me := session.MustUser(ctx); me.Subject == userID {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("admin: cannot change your own role"))
	}

	t := tenancy.MustTenant(ctx)
	orgID := t.ZitadelOrgID

	// Last-owner invariant: count current owners across the whole org
	// and reject if this change would drop the count to zero.
	allGrants, err := s.members.ListUserGrants(ctx, orgID, "")
	if err != nil {
		return nil, s.internal("list user grants", err)
	}
	owners := 0
	wasOwner := false
	var existingGrantID string
	for _, g := range allGrants {
		isOwner := slices.Contains(g.RoleKeys, zitadel.RoleKeyOwner)
		if isOwner {
			owners++
		}
		if g.UserID == userID {
			existingGrantID = g.ID
			wasOwner = isOwner
		}
	}
	if wasOwner && roleKey != zitadel.RoleKeyOwner && owners <= 1 {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("admin: cannot demote the last owner"))
	}

	if existingGrantID != "" {
		if err := s.members.UpdateUserGrant(ctx, existingGrantID, []string{roleKey}); err != nil {
			return nil, s.internal("update user grant", err)
		}
	} else {
		if _, err := s.members.AddUserGrant(ctx, orgID, userID, []string{roleKey}); err != nil {
			return nil, s.internal("add user grant", err)
		}
	}

	// Remove-first-then-add is not atomic: if RemoveOrgRoles succeeds
	// but AddOrgRoles fails, Zitadel org roles will be absent until a
	// subsequent retry or manual sync. Both failures are logged at WARN
	// and the RPC success is not affected — org role sync is best-effort.
	// Sync Zitadel org-level roles: reset to desired state.
	if roles := zitadel.OrgRolesForLimenRole(roleKey); len(roles) > 0 {
		// Going TO admin or owner: remove any existing roles first (clean slate),
		// then add the desired set.
		if err := s.members.RemoveOrgRoles(ctx, orgID, userID); err != nil {
			s.logger.Warn("update_role: failed to remove org roles, continuing",
				zap.String("user_id", userID), zap.String("role_key", roleKey), zap.Error(err))
		}
		if err := s.members.AddOrgRoles(ctx, orgID, userID, roles); err != nil {
			s.logger.Warn("update_role: failed to add org roles, continuing",
				zap.String("user_id", userID), zap.String("role_key", roleKey), zap.Error(err))
		}
	} else {
		// Going TO member: remove all org-level administrator roles.
		if err := s.members.RemoveOrgRoles(ctx, orgID, userID); err != nil {
			s.logger.Warn("update_role: failed to remove org roles, continuing",
				zap.String("user_id", userID), zap.Error(err))
		}
	}

	return connect.NewResponse(&adminv1.UpdateMemberRoleResponse{
		Member: &adminv1.Member{
			UserId: userID,
			Role:   msg.GetRole(),
		},
	}), nil
}

// RemoveMember deletes the user from Zitadel. All authorizations on
// that user cascade. Self-removal is rejected. The last-owner
// invariant is enforced.
func (s *Service) RemoveMember(ctx context.Context, req *connect.Request[adminv1.RemoveMemberRequest]) (*connect.Response[adminv1.RemoveMemberResponse], error) {
	if s.members == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("admin: member directory not wired"))
	}
	userID := strings.TrimSpace(req.Msg.GetUserId())
	if userID == "" {
		return nil, s.invalidArg("user_id", "user_id must not be empty")
	}
	if me := session.MustUser(ctx); me.Subject == userID {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("admin: cannot remove yourself"))
	}

	t := tenancy.MustTenant(ctx)
	orgID := t.ZitadelOrgID

	allGrants, err := s.members.ListUserGrants(ctx, orgID, "")
	if err != nil {
		return nil, s.internal("list user grants", err)
	}
	owners := 0
	targetIsOwner := false
	for _, g := range allGrants {
		isOwner := slices.Contains(g.RoleKeys, zitadel.RoleKeyOwner)
		if isOwner {
			owners++
			if g.UserID == userID {
				targetIsOwner = true
			}
		}
	}
	if targetIsOwner && owners <= 1 {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("admin: cannot remove the last owner"))
	}

	if err := s.members.DeleteUser(ctx, userID); err != nil {
		return nil, s.internal("delete user", err)
	}
	return connect.NewResponse(&adminv1.RemoveMemberResponse{}), nil
}
