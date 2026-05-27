package zitadel

// RoleKeyOwner / Admin / Member are the Zitadel project-role keys the
// Limen project ships. They MUST match the keys seeded by the
// bootstrap (see scripts/zitadel-bootstrap).
const (
	RoleKeyOwner  = "owner"
	RoleKeyAdmin  = "admin"
	RoleKeyMember = "member"
)

// OrgRolesForLimenRole returns the Zitadel org-level administrator
// roles that correspond to a Limen project role key.
// Owners receive the 3-role set that matches what the Zitadel console
// assigns via ManagementService.UpdateOrgMember:
//
//	ORG_OWNER_VIEWER, ORG_SETTINGS_MANAGER, ORG_USER_MANAGER
//
// Admins receive:
//
//	ORG_USER_MANAGER
//
// Members receive no org-level roles.
func OrgRolesForLimenRole(roleKey string) []string {
	switch roleKey {
	case RoleKeyOwner:
		return []string{"ORG_OWNER_VIEWER", "ORG_SETTINGS_MANAGER", "ORG_USER_MANAGER"}
	case RoleKeyAdmin:
		return []string{"ORG_USER_MANAGER"}
	default:
		return nil
	}
}
