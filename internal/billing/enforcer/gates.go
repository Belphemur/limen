package enforcer

import "github.com/belphemur/limen/internal/billing/entitlements"

// CheckMaxUsers returns nil if allowed. limit=-1 means unlimited.
func CheckMaxUsers(e entitlements.PlanEntitlements, currentCount int32) error {
	if e.MaxActiveUsers == -1 {
		return nil
	}
	if currentCount >= e.MaxActiveUsers {
		return NewFeatureLockedError("max-users", e.MaxActiveUsers, currentCount,
			"max active users reached")
	}
	return nil
}

// CheckMaxServiceAccounts returns nil if allowed.
func CheckMaxServiceAccounts(e entitlements.PlanEntitlements, currentCount int32) error {
	if e.MaxServiceAccounts == -1 {
		return nil
	}
	if currentCount >= e.MaxServiceAccounts {
		return NewFeatureLockedError("max-service-accounts", e.MaxServiceAccounts, currentCount,
			"max service accounts reached")
	}
	return nil
}

// CheckSAConnectionLimit returns nil if allowed, or an ErrSAConnectionLimit
// if the tenant has exceeded MaxSAConnections. This is intentionally a
// distinct error type so MCP callers can surface it as an in-band tool error.
func CheckSAConnectionLimit(e entitlements.PlanEntitlements, currentCount int32) error {
	if e.MaxSAConnections == -1 {
		return nil
	}
	if currentCount >= e.MaxSAConnections {
		return NewSAConnectionLimitError(e.MaxSAConnections, currentCount)
	}
	return nil
}

// CheckMaxSAConnections returns nil if allowed.
func CheckMaxSAConnections(e entitlements.PlanEntitlements, currentCount int32) error {
	if e.MaxSAConnections == -1 {
		return nil
	}
	if currentCount >= e.MaxSAConnections {
		return NewFeatureLockedError("max-sa-connections", e.MaxSAConnections, currentCount,
			"max service account connections reached")
	}
	return nil
}

// CheckMaxProjects returns nil if allowed.
func CheckMaxProjects(e entitlements.PlanEntitlements, currentCount int32) error {
	if e.MaxProjects == -1 {
		return nil
	}
	if currentCount >= e.MaxProjects {
		return NewFeatureLockedError("max-projects", e.MaxProjects, currentCount,
			"max projects reached")
	}
	return nil
}

// CheckStorageLimit returns nil if allowed. limit=-1 means unlimited.
func CheckStorageLimit(e entitlements.PlanEntitlements, currentUsageMB int32) error {
	if e.StorageLimitMB == -1 {
		return nil
	}
	if currentUsageMB >= e.StorageLimitMB {
		return NewFeatureLockedError("max-storage", e.StorageLimitMB, currentUsageMB,
			"storage limit reached")
	}
	return nil
}

// CheckFeature returns nil if the boolean feature is enabled.
func CheckFeature(e entitlements.PlanEntitlements, featureKey string, enabled bool) error {
	if !enabled {
		return NewFeatureLockedError(featureKey, -1, -1,
			"feature not available on current plan")
	}
	return nil
}
