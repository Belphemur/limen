package portal

// Role enumerates the access tiers the role interceptor recognises.
// Sourced from the Zitadel project-roles claim — Limen does not keep a
// role table.
type Role string

const (
	RoleAny    Role = "*"
	RoleMember Role = "member"
	RoleAdmin  Role = "admin"
	RoleOwner  Role = "owner"
)

// requiredRole maps the RPC procedure name (no leading slash, no
// package — just the method name as exposed by connect.AnyRequest.Spec())
// to the minimum role required to invoke it. Unknown procedures
// default-deny in the role interceptor; new RPCs MUST be added here
// before they ship.
var requiredRole = map[string]Role{
	"GetSession":             RoleAny,
	"ListUpstreams":          RoleMember,
	"StartConnect":           RoleMember,
	"SubmitUpstreamAPIKey":   RoleMember,
	"SetUpstreamLinkEnabled": RoleMember,
	"Disconnect":             RoleMember,
	"ListMCPClients":         RoleMember,
	"RevokeMCPClient":        RoleMember,
}

// satisfies reports whether a user holding userRoles is allowed to
// invoke a procedure whose minimum is need. RoleAny lets anyone through
// (used by GetSession and the unauthenticated-by-design flow). Members
// of higher tiers also satisfy lower tiers via roleRank.
func satisfies(userRoles []string, need Role) bool {
	if need == RoleAny {
		return true
	}
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

// roleRank orders the tiers so the same table satisfies "needs member,
// user is owner" without enumerating the cartesian product.
var roleRank = map[Role]int{
	RoleMember: 1,
	RoleAdmin:  2,
	RoleOwner:  3,
}
