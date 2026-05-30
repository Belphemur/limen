package metrics

// SQL query constants shared between the consumer and fallback recorder.
// Centralised here to eliminate the DRY violation.

const (
	// UpsertActiveUserMonthSQL increments call_count and updates last_seen_at
	// for an active-user-month row, inserting if absent.
	UpsertActiveUserMonthSQL = `
		INSERT INTO active_user_months (tenant_id, month_start, user_id, service_account_id, first_seen_at, last_seen_at, call_count, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, NOW(), NOW())
		ON CONFLICT (tenant_id, month_start, user_id, service_account_id) WHERE deleted_at IS NULL
		DO UPDATE SET
			call_count = active_user_months.call_count + 1,
			last_seen_at = GREATEST(active_user_months.last_seen_at, EXCLUDED.last_seen_at),
			updated_at = NOW()
	`

	// InsertSAConnectionSnapshotSQL inserts a new SA connection snapshot,
	// computing the concurrent count via a subquery.
	InsertSAConnectionSnapshotSQL = `
		INSERT INTO sa_connection_snapshots (tenant_id, service_account_id, connected_at, concurrent_count, created_at, updated_at)
		VALUES (?, ?, ?, (SELECT COUNT(*) + 1 FROM sa_connection_snapshots WHERE tenant_id = ? AND disconnected_at IS NULL AND deleted_at IS NULL), NOW(), NOW())
	`

	// UpdateSAConnectionSnapshotDisconnectSQL closes the most recent open
	// connection for a given service account.
	UpdateSAConnectionSnapshotDisconnectSQL = `
		UPDATE sa_connection_snapshots
		SET disconnected_at = ?, updated_at = NOW()
		WHERE id = (
			SELECT id FROM sa_connection_snapshots
			WHERE tenant_id = ? AND service_account_id = ? AND disconnected_at IS NULL AND deleted_at IS NULL
			ORDER BY connected_at DESC LIMIT 1
		)
	`
)
