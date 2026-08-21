package postgres

import (
	"context"
	"database/sql"
)

func MigrateP07B3C2ARestore(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS restore_reviews (
			id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			environment_id TEXT NOT NULL, backup_id TEXT NOT NULL REFERENCES backups(id), target_resource_id TEXT NOT NULL,
			target_node_id TEXT NOT NULL DEFAULT '', lifecycle TEXT NOT NULL CHECK (lifecycle IN ('queued','leased','succeeded','failed')),
			authority JSONB NOT NULL, requested_at TIMESTAMPTZ NOT NULL, attempt_count INTEGER NOT NULL DEFAULT 0,
			lease_token TEXT, lease_expires_at TIMESTAMPTZ,
			FOREIGN KEY(environment_id,project_id) REFERENCES environments(id,project_id) ON DELETE CASCADE)`,
		`CREATE INDEX IF NOT EXISTS restore_reviews_lease_idx ON restore_reviews(project_id,lifecycle,lease_expires_at,requested_at,id)`,
		`CREATE TABLE IF NOT EXISTS restores (
			id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			environment_id TEXT NOT NULL, backup_id TEXT NOT NULL REFERENCES backups(id), target_resource_id TEXT NOT NULL,
			target_node_id TEXT NOT NULL DEFAULT '', lifecycle TEXT NOT NULL CHECK (lifecycle IN ('queued','leased','running','verifying','succeeded','failed')),
			authority JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL, attempt_count INTEGER NOT NULL DEFAULT 0,
			lease_token TEXT, lease_expires_at TIMESTAMPTZ,
			FOREIGN KEY(environment_id,project_id) REFERENCES environments(id,project_id) ON DELETE CASCADE)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS restores_one_active_per_target_uidx ON restores(project_id,target_resource_id) WHERE lifecycle IN ('queued','leased','running','verifying')`,
		`CREATE INDEX IF NOT EXISTS restores_project_filter_idx ON restores(project_id,backup_id,target_resource_id,created_at,id)`,
		`CREATE INDEX IF NOT EXISTS restores_lease_idx ON restores(project_id,lifecycle,lease_expires_at,created_at,id)`,
		`CREATE TABLE IF NOT EXISTS restore_idempotency (project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE, idempotency_key TEXT NOT NULL, payload_hash TEXT NOT NULL CHECK(payload_hash ~ '^[0-9a-f]{64}$'), restore_id TEXT NOT NULL REFERENCES restores(id) ON DELETE CASCADE, created_at TIMESTAMPTZ NOT NULL, PRIMARY KEY(project_id,idempotency_key))`,
		`CREATE OR REPLACE FUNCTION prevent_succeeded_restore_mutation() RETURNS trigger AS $$ BEGIN IF OLD.lifecycle='succeeded' AND OLD IS DISTINCT FROM NEW THEN RAISE EXCEPTION 'Succeeded Restore authority is immutable'; END IF; RETURN NEW; END; $$ LANGUAGE plpgsql`,
		`DROP TRIGGER IF EXISTS restores_succeeded_immutable ON restores`,
		`CREATE TRIGGER restores_succeeded_immutable BEFORE UPDATE ON restores FOR EACH ROW EXECUTE FUNCTION prevent_succeeded_restore_mutation()`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
