// Package entitlements provides the canonical mapping from Stripe
// entitlement lookup_keys to Limen feature limits. It is the single
// source of truth for what features exist and how they translate
// into concrete limits.
package entitlements

import (
	"github.com/belphemur/limen/internal/storage"
)

// PlanEntitlements holds per-plan feature limits derived from Stripe
// entitlements.
type PlanEntitlements struct {
	MaxActiveUsers   int32
	MaxSAConnections int32
	MaxProjects      int32
	CodeMode         bool
	AdvancedAI       bool
	AuditLogs        bool
	SSOSAML          bool
	IDEPresets       bool
	CustomUpstreams  bool
	PrioritySupport  bool
	CommunitySupport bool
	StorageLimitMB   int32
}

// DeveloperEntitlements returns the hardcoded entitlements for the
// free Developer plan. Used as a fallback when no Stripe subscription
// exists.
func DeveloperEntitlements() PlanEntitlements {
	return PlanEntitlements{
		MaxActiveUsers:   1,
		MaxSAConnections: 1,
		MaxProjects:      5,
		CodeMode:         true,
		CommunitySupport: true,
		StorageLimitMB:   1024,
	}
}

// EntitlementLimitFromLookupKey returns the limit value for a Stripe
// entitlement lookup_key. Returns 0 for disabled/unknown features.
func EntitlementLimitFromLookupKey(lookupKey string, isEnabled bool) int32 {
	if !isEnabled {
		return 0
	}
	switch lookupKey {
	case "max-user_unlimited", "max-sa-connection_unlimited", "max-project_unlimited", "max-storage_unlimited":
		return -1
	case "max-user_1", "max-sa-connection_1":
		return 1
	case "max-project_5":
		return 5
	case "max-storage_1gb":
		return 1024
	case "max-storage_10gb":
		return 10240
	case "code-mode", "advanced-ai", "audit-logs", "sso-saml", "ide-presets", "custom-upstreams", "priority-support", "community-support":
		return 1 // boolean features → enabled = 1
	default:
		return 0
	}
}

// EntitlementsFromRows resolves a PlanEntitlements from tenant_entitlements
// rows. Starts from Developer defaults, then overlays any features present
// in the rows. Callers should pass rows filtered to one tenant.
//
//nolint:gocyclo // mapping all known lookup_keys requires a long switch
func EntitlementsFromRows(rows []storage.TenantEntitlement) PlanEntitlements {
	e := DeveloperEntitlements()

	for _, row := range rows {
		switch row.Feature {
		case "max-user_unlimited":
			e.MaxActiveUsers = -1
		case "max-user_1":
			// Already set by Developer default; no-op but explicit.
		case "max-sa-connection_unlimited":
			e.MaxSAConnections = -1
		case "max-sa-connection_1":
			// Already set by Developer default.
		case "code-mode":
			e.CodeMode = true
		case "advanced-ai":
			e.AdvancedAI = true
		case "audit-logs":
			e.AuditLogs = true
		case "sso-saml":
			e.SSOSAML = true
		case "ide-presets":
			e.IDEPresets = true
		case "custom-upstreams":
			e.CustomUpstreams = true
		case "priority-support":
			e.PrioritySupport = true
		case "community-support":
			e.CommunitySupport = true
		case "max-project_5":
			if row.LimitValue != 0 {
				e.MaxProjects = row.LimitValue
			}
		case "max-project_unlimited":
			e.MaxProjects = -1
		case "max-storage_1gb":
			e.StorageLimitMB = 1024
		case "max-storage_10gb":
			e.StorageLimitMB = 10240
		case "max-storage_unlimited":
			e.StorageLimitMB = -1
		}
	}

	return e
}
