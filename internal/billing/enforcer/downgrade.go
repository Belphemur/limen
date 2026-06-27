package enforcer

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/belphemur/limen/internal/storage"
)

// DowngradeToDeveloper resets a tenant's billing row to the free
// Developer plan and deletes the tenant_entitlements rows so the
// enforcer resolves DeveloperEntitlements() on the next load.
//
// Used by the lifecycle middleware when a cancelled, past-due-expired,
// or unpaid-expired tenant hits a request — we auto-downgrade so the
// request proceeds with developer limits rather than being 402'd.
//
// The caller must wrap tx in a Session() / commit() cycle (the
// middleware does) and call Enforcer.Invalidate after a successful
// commit so the next read bypasses the stale cache.
//
// Idempotent and atomic. The `plan != developer` guard in the WHERE
// clause collapses the "load → check → save" sequence into a single
// UPDATE; if the row is already on the Developer plan RowsAffected
// is 0 and we skip the entitlement delete (there is nothing to
// free — the loader will resolve the Developer defaults either way).
// A missing tenant_billing row is also a no-op: the WHERE matches
// nothing, RowsAffected is 0, and we return. The middleware
// short-circuits before reaching here in that case, but the helper
// stays safe for any future caller.
func DowngradeToDeveloper(tx *gorm.DB, tenantID int64) error {
	res := tx.Model(&storage.TenantBilling{}).
		Where("tenant_id = ? AND plan != ?", tenantID, DeveloperPlan).
		Updates(map[string]any{"plan": DeveloperPlan, "grace_until": nil})
	if res.Error != nil {
		return fmt.Errorf("enforcer: downgrade tenant_billing: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil // already developer, or no billing row
	}

	// Hard-delete the entitlement rows so EntitlementsFromRows
	// resolves the Developer defaults on the next cache miss.
	// Soft-delete would leave rows behind with deleted_at set;
	// the loader currently has no Unscoped, so a soft-delete would
	// also work — but a hard delete is unambiguous about intent and
	// matches what the webhook does in handleEntitlementsUpdated.
	if err := tx.Unscoped().Delete(&storage.TenantEntitlement{}, "tenant_id = ?", tenantID).Error; err != nil {
		return fmt.Errorf("enforcer: delete tenant_entitlements on downgrade: %w", err)
	}

	return nil
}
