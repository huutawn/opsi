package postgres

import (
	"context"
	"database/sql"
)

func MigrateP07B3B2RetainedStorage(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS retained_storages (
			id TEXT PRIMARY KEY,
			original_resource_id TEXT NOT NULL,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			environment_id TEXT NOT NULL,
			resource_type TEXT NOT NULL CHECK (resource_type='postgres'),
			resource_name TEXT NOT NULL,
			namespace TEXT NOT NULL,
			pvc_name TEXT NOT NULL,
			pvc_uid TEXT NOT NULL,
			pv_name TEXT NOT NULL,
			pv_uid TEXT,
			storage_class TEXT NOT NULL,
			reclaim_policy TEXT NOT NULL,
			requested_bytes BIGINT NOT NULL CHECK (requested_bytes>0),
			actual_size TEXT NOT NULL,
			storage_hash TEXT NOT NULL,
			assignment JSONB NOT NULL,
			lifecycle TEXT NOT NULL CHECK (lifecycle IN ('retained','destroying','destroyed','destroy_failed','unknown')),
			revision BIGINT NOT NULL CHECK (revision>0),
			original_created_by TEXT,
			retained_by TEXT,
			retained_at TIMESTAMPTZ NOT NULL,
			review_token TEXT,
			reviewed_by TEXT,
			reviewed_at TIMESTAMPTZ,
			destroy_requested_by TEXT,
			destroy_requested_at TIMESTAMPTZ,
			destroyed_at TIMESTAMPTZ,
			failure_code TEXT,
			failure_message_redacted TEXT,
			lease_token TEXT,
			lease_expires_at TIMESTAMPTZ,
			UNIQUE(project_id,original_resource_id),
			UNIQUE(project_id,pvc_uid),
			FOREIGN KEY(environment_id,project_id) REFERENCES environments(id,project_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS retained_storages_project_environment_idx ON retained_storages(project_id,environment_id,retained_at,id)`,
		`CREATE INDEX IF NOT EXISTS retained_storages_destroy_lease_idx ON retained_storages(project_id,lifecycle,lease_expires_at)`,
		`CREATE TABLE IF NOT EXISTS retained_storage_destroy_intents (
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			idempotency_key TEXT NOT NULL,
			payload_hash TEXT NOT NULL,
			retained_storage_id TEXT NOT NULL REFERENCES retained_storages(id),
			created_at TIMESTAMPTZ NOT NULL,
			PRIMARY KEY(project_id,idempotency_key)
		)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
