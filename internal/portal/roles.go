package portal

import "github.com/belphemur/limen/internal/session"

// requiredRole maps the RPC procedure name (the leaf method name as
// returned by session.ProcedureMethod) to the minimum role required to
// invoke it. Unknown procedures default-deny in
// session.RoleInterceptor; new RPCs MUST be added here before they
// ship.
//
// PortalService is a user-facing service: every entry is at least
// RoleMember. The "who am I?" RPC lives on SessionService, NOT here.
var requiredRole = map[string]session.Role{
	"ListUpstreams":          session.RoleMember,
	"StartConnect":           session.RoleMember,
	"SubmitUpstreamAPIKey":   session.RoleMember,
	"SetUpstreamLinkEnabled": session.RoleMember,
	"Disconnect":             session.RoleMember,
	"ListMCPClients":         session.RoleMember,
	"RevokeMCPClient":        session.RoleMember,
}
