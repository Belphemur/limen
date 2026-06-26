package entitlements

import (
	"testing"

	"github.com/belphemur/limen/internal/storage"
)

func TestDeveloperEntitlements(t *testing.T) {
	d := DeveloperEntitlements()
	if d.MaxActiveUsers != 1 {
		t.Errorf("MaxActiveUsers = %d, want 1", d.MaxActiveUsers)
	}
	if d.MaxSAConnections != 1 {
		t.Errorf("MaxSAConnections = %d, want 1", d.MaxSAConnections)
	}
	if d.MaxProjects != 5 {
		t.Errorf("MaxProjects = %d, want 5", d.MaxProjects)
	}
	if !d.CodeMode {
		t.Error("CodeMode = false, want true")
	}
	if d.AdvancedAI {
		t.Error("AdvancedAI = true, want false")
	}
	if d.AuditLogs {
		t.Error("AuditLogs = true, want false")
	}
	if d.SSOSAML {
		t.Error("SSOSAML = true, want false")
	}
	if d.IDEPresets {
		t.Error("IDEPresets = true, want false")
	}
	if d.CustomUpstreams {
		t.Error("CustomUpstreams = true, want false")
	}
	if d.PrioritySupport {
		t.Error("PrioritySupport = true, want false")
	}
	if !d.CommunitySupport {
		t.Error("CommunitySupport = false, want true")
	}
	if d.StorageLimitMB != 1024 {
		t.Errorf("StorageLimitMB = %d, want 1024", d.StorageLimitMB)
	}
}

func TestEntitlementsFromRows_EmptyReturnsDeveloper(t *testing.T) {
	e := EntitlementsFromRows(nil)
	d := DeveloperEntitlements()
	if e != d {
		t.Errorf("EntitlementsFromRows(nil) != DeveloperEntitlements(): %+v vs %+v", e, d)
	}
}

func TestEntitlementsFromRows_TeamPlan(t *testing.T) {
	rows := []storage.TenantEntitlement{
		{Feature: "max-user_unlimited", LimitValue: -1},
		{Feature: "max-sa-connection_unlimited", LimitValue: -1},
		{Feature: "advanced-ai", LimitValue: 1},
		{Feature: "audit-logs", LimitValue: 1},
		{Feature: "sso", LimitValue: 1},
		{Feature: "ide-preset", LimitValue: 1},
		{Feature: "custom-upstream", LimitValue: 1},
		{Feature: "priority-support", LimitValue: 1},
		{Feature: "community-support", LimitValue: 1},
		{Feature: "code-mode", LimitValue: 1},
		{Feature: "max-storage_10gb", LimitValue: 10240},
	}
	e := EntitlementsFromRows(rows)
	if e.MaxActiveUsers != -1 {
		t.Errorf("MaxActiveUsers = %d, want -1", e.MaxActiveUsers)
	}
	if e.MaxSAConnections != -1 {
		t.Errorf("MaxSAConnections = %d, want -1", e.MaxSAConnections)
	}
	if e.MaxProjects != 5 {
		t.Errorf("MaxProjects = %d, want 5", e.MaxProjects)
	}
	if !e.CodeMode {
		t.Error("CodeMode = false, want true")
	}
	if !e.AdvancedAI {
		t.Error("AdvancedAI = false, want true")
	}
	if !e.AuditLogs {
		t.Error("AuditLogs = false, want true")
	}
	if !e.SSOSAML {
		t.Error("SSOSAML = false, want true")
	}
	if !e.IDEPresets {
		t.Error("IDEPresets = false, want true")
	}
	if !e.CustomUpstreams {
		t.Error("CustomUpstreams = false, want true")
	}
	if !e.PrioritySupport {
		t.Error("PrioritySupport = false, want true")
	}
	if !e.CommunitySupport {
		t.Error("CommunitySupport = false, want true")
	}
	if e.StorageLimitMB != 10240 {
		t.Errorf("StorageLimitMB = %d, want 10240", e.StorageLimitMB)
	}
}

func TestEntitlementsFromRows_UnknownFeatureIgnored(t *testing.T) {
	rows := []storage.TenantEntitlement{
		{Feature: "unknown-feature", LimitValue: 999},
	}
	e := EntitlementsFromRows(rows)
	d := DeveloperEntitlements()
	if e != d {
		t.Errorf("unknown feature should not change entitlements: %+v", e)
	}
}

func TestEntitlementLimitFromLookupKey_Unlimited(t *testing.T) {
	if v := EntitlementLimitFromLookupKey("max-user_unlimited", true); v != -1 {
		t.Errorf("max-user_unlimited = %d, want -1", v)
	}
	if v := EntitlementLimitFromLookupKey("max-project_unlimited", true); v != -1 {
		t.Errorf("max-project_unlimited = %d, want -1", v)
	}
	if v := EntitlementLimitFromLookupKey("max-storage_unlimited", true); v != -1 {
		t.Errorf("max-storage_unlimited = %d, want -1", v)
	}
}

func TestEntitlementLimitFromLookupKey_Numeric(t *testing.T) {
	tests := []struct {
		key  string
		want int32
	}{
		{"max-user_1", 1},
		{"max-sa-connection_1", 1},
		{"max-project_5", 5},
		{"max-storage_1gb", 1024},
		{"max-storage_10gb", 10240},
	}
	for _, tt := range tests {
		if v := EntitlementLimitFromLookupKey(tt.key, true); v != tt.want {
			t.Errorf("%s = %d, want %d", tt.key, v, tt.want)
		}
	}
}

func TestEntitlementLimitFromLookupKey_BooleanFeatures(t *testing.T) {
	features := []string{"code-mode", "advanced-ai", "audit-logs", "sso", "ide-preset", "custom-upstream", "priority-support", "community-support"}
	for _, f := range features {
		if v := EntitlementLimitFromLookupKey(f, true); v != 1 {
			t.Errorf("%s enabled = %d, want 1", f, v)
		}
		if v := EntitlementLimitFromLookupKey(f, false); v != 0 {
			t.Errorf("%s disabled = %d, want 0", f, v)
		}
	}
}

func TestEntitlementLimitFromLookupKey_DisabledReturnsZero(t *testing.T) {
	if v := EntitlementLimitFromLookupKey("max-user_unlimited", false); v != 0 {
		t.Errorf("want 0, got %d", v)
	}
	if v := EntitlementLimitFromLookupKey("code-mode", false); v != 0 {
		t.Errorf("want 0, got %d", v)
	}
	if v := EntitlementLimitFromLookupKey("max-project_5", false); v != 0 {
		t.Errorf("want 0, got %d", v)
	}
}

func TestEntitlementLimitFromLookupKey_UnknownReturnsZero(t *testing.T) {
	if v := EntitlementLimitFromLookupKey("nonexistent", true); v != 0 {
		t.Errorf("want 0, got %d", v)
	}
	if v := EntitlementLimitFromLookupKey("", true); v != 0 {
		t.Errorf("want 0, got %d", v)
	}
}
