package postgres

import (
	"context"
	"database/sql"
)

func MigrateP07B3C2B2CCutoverFinalize(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS application_cutover_finalizations (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			environment_id TEXT NOT NULL,
			application_id TEXT NOT NULL,
			cutover_id TEXT NOT NULL REFERENCES application_cutovers(id) ON DELETE RESTRICT,
			source_binding_id TEXT NOT NULL,
			source_resource_id TEXT NOT NULL,
			target_resource_id TEXT NOT NULL,
			target_binding_id TEXT NOT NULL,
			target_node_id TEXT NOT NULL DEFAULT '',
			lifecycle TEXT NOT NULL CHECK (lifecycle IN ('queued','validating','revoking_source_binding','verifying','succeeded','failed')),
			authority JSONB NOT NULL,
			requested_by TEXT NOT NULL DEFAULT '',
			requested_at TIMESTAMPTZ NOT NULL,
			completed_at TIMESTAMPTZ,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			attempt_count INTEGER NOT NULL DEFAULT 0,
			lease_token TEXT,
			lease_expires_at TIMESTAMPTZ,
			failure_code TEXT,
			failure_message TEXT,
			FOREIGN KEY(environment_id,project_id) REFERENCES environments(id,project_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS application_cutover_finalizations_app_lifecycle_idx ON application_cutover_finalizations(project_id,application_id,lifecycle,requested_at,id)`,
		`CREATE INDEX IF NOT EXISTS application_cutover_finalizations_cutover_idx ON application_cutover_finalizations(project_id,cutover_id,lifecycle)`,
		`CREATE INDEX IF NOT EXISTS application_cutover_finalizations_lease_idx ON application_cutover_finalizations(project_id,lifecycle,lease_expires_at,requested_at,id)`,
		`CREATE TABLE IF NOT EXISTS application_cutover_finalization_idempotency (
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			idempotency_key TEXT NOT NULL,
			payload_hash TEXT NOT NULL CHECK(payload_hash ~ '^[0-9a-f]{64}$'),
			finalization_id TEXT NOT NULL REFERENCES application_cutover_finalizations(id) ON DELETE CASCADE,
			created_at TIMESTAMPTZ NOT NULL,
			PRIMARY KEY(project_id,idempotency_key)
		)`,
		`CREATE OR REPLACE FUNCTION prevent_succeeded_cutover_finalization_mutation() RETURNS trigger AS $$
		BEGIN
			IF OLD.lifecycle='succeeded' AND OLD IS DISTINCT FROM NEW THEN
				RAISE EXCEPTION 'Succeeded Cutover Finalization authority is immutable';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql`,
		`DROP TRIGGER IF EXISTS cutover_finalizations_succeeded_immutable ON application_cutover_finalizations`,
		`CREATE TRIGGER cutover_finalizations_succeeded_immutable BEFORE UPDATE ON application_cutover_finalizations FOR EACH ROW EXECUTE FUNCTION prevent_succeeded_cutover_finalization_mutation()`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
