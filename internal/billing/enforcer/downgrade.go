package enforcer

import (
	"errors"
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
// Idempotent. If the tenant is already on the Developer plan, the
// SELECT happens but nothing is written. A missing tenant_billing row
// is also a no-op — there is nothing to downgrade (the middleware
// short-circuits before reaching here in that case, but the helper
// stays safe for any future caller).
func DowngradeToDeveloper(tx *gorm.DB, tenantID int64) error {
	var billing storage.TenantBilling
	if err := tx.Where("tenant_id = ?", tenantID).First(&billing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("enforcer: load tenant_billing for downgrade: %w", err)
	}
	if billing.Plan == DeveloperPlan {
		return nil
	}

	billing.Plan = DeveloperPlan
	billing.GraceUntil = nil
	if err := tx.Where("tenant_id = ?", tenantID).Save(&billing).Error; err != nil {
		return fmt.Errorf("enforcer: save tenant_billing downgrade: %w", err)
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
