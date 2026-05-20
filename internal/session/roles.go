package session

import sessionv1 "github.com/belphemur/limen/internal/session/sessionv1"

// Role enumerates the access tiers the role interceptor recognises.
// Sourced from the Zitadel project-roles claim — Limen does not keep a
// role table. There is no RoleAny: every Connect handler in Limen runs
// behind the session interceptor and therefore has an authenticated
// user; the role table only encodes "which authenticated users".
type Role string

const (
	RoleMember Role = "member"
	RoleAdmin  Role = "admin"
	RoleOwner  Role = "owner"
)

// roleRank orders the tiers so the same table satisfies "needs member,
// user is owner" without enumerating the cartesian product.
var roleRank = map[Role]int{
	RoleMember: 1,
	RoleAdmin:  2,
	RoleOwner:  3,
}

// Satisfies reports whether a user holding userRoles is allowed to
// invoke a procedure whose minimum is need. Members of higher tiers
// also satisfy lower tiers via roleRank.
func Satisfies(userRoles []string, need Role) bool {
	wantRank, ok := roleRank[need]
	if !ok {
		return false
	}
	for _, r := range userRoles {
		gotRank, ok := roleRank[Role(r)]
		if !ok {
			continue
		}
		if gotRank >= wantRank {
			return true
		}
	}
	return false
}

// HighestRole picks the strongest tier present in userRoles for the
// session response. Returns ROLE_UNSPECIFIED if the user holds no
// recognised tier (e.g. an outside contractor with a Zitadel account
// but no project membership).
func HighestRole(userRoles []string) sessionv1.Role {
	best := sessionv1.Role_ROLE_UNSPECIFIED
	bestRank := 0
	for _, r := range userRoles {
		switch Role(r) {
		case RoleOwner:
			if 3 > bestRank {
				best, bestRank = sessionv1.Role_ROLE_OWNER, 3
			}
		case RoleAdmin:
			if 2 > bestRank {
				best, bestRank = sessionv1.Role_ROLE_ADMIN, 2
			}
		case RoleMember:
			if 1 > bestRank {
				best, bestRank = sessionv1.Role_ROLE_MEMBER, 1
			}
		}
	}
	return best
}
