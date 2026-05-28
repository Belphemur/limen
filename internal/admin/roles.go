package admin

import "github.com/belphemur/limen/internal/session"

// requiredRole maps the RPC procedure name (the leaf method as
// returned by session.ProcedureMethod) to the minimum role required
// to invoke it. Unknown procedures default-deny in
// session.RoleInterceptor; every new RPC MUST be added here before
// it ships.
//
// AdminService floor is RoleAdmin. DeleteTenant escalates to
// RoleOwner; owner naturally satisfies admin via session.Satisfies.
var requiredRole = map[string]session.Role{
	"CreateUpstream":                   session.RoleAdmin,
	"UpdateUpstream":                   session.RoleAdmin,
	"DeleteUpstream":                   session.RoleAdmin,
	"ReindexUpstreamCatalog":           session.RoleAdmin,
	"PreviewUpstreamContext":           session.RoleAdmin,
	"GetTenantSettings":                session.RoleAdmin,
	"UpdateTenantSettings":             session.RoleAdmin,
	"MarkIDEChoiceSkipped":             session.RoleAdmin,
	"ListIDEPresets":                   session.RoleAdmin,
	"ListAllowlistEntries":             session.RoleAdmin,
	"AddAllowlistEntry":                session.RoleAdmin,
	"UpdateAllowlistEntry":             session.RoleAdmin,
	"RemoveAllowlistEntry":             session.RoleAdmin,
	"ApplyIDEPreset":                   session.RoleAdmin,
	"RemoveIDEPreset":                  session.RoleAdmin,
	"ListMembers":                      session.RoleAdmin,
	"InviteMember":                     session.RoleAdmin,
	"UpdateMemberRole":                 session.RoleAdmin,
	"RemoveMember":                     session.RoleAdmin,
	"DeleteTenant":                     session.RoleOwner,
	"CreateServiceAccount":             session.RoleAdmin,
	"GetServiceAccount":                session.RoleAdmin,
	"UpdateServiceAccount":             session.RoleAdmin,
	"ListServiceAccounts":              session.RoleAdmin,
	"DeleteServiceAccount":             session.RoleAdmin,
	"RegenerateServiceAccountToken":    session.RoleAdmin,
	"ListServiceAccountUpstreamLinks":  session.RoleAdmin,
	"SetServiceAccountLinkEnabled":     session.RoleAdmin,
	"StartServiceAccountConnect":       session.RoleAdmin,
	"SubmitServiceAccountAPIKey":       session.RoleAdmin,
	"ClearServiceAccountOverride":      session.RoleAdmin,
	"DisconnectServiceAccountUpstream": session.RoleAdmin,
}
