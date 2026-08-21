package postgres

import (
	"context"
	"database/sql"
)

func MigrateP07B3C2B2ACutover(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	statements := []string{
		`DO $$
		DECLARE
			r RECORD;
		BEGIN
			FOR r IN (SELECT conname FROM pg_constraint WHERE conrelid = 'resource_bindings'::regclass AND contype = 'u' AND conname NOT LIKE '%pkey%' AND conname NOT LIKE '%credential%' AND conname NOT LIKE '%role%') LOOP
				EXECUTE 'ALTER TABLE resource_bindings DROP CONSTRAINT IF EXISTS ' || quote_ident(r.conname);
			END LOOP;
		END $$`,
		`CREATE TABLE IF NOT EXISTS application_cutover_reviews (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			environment_id TEXT NOT NULL,
			application_id TEXT NOT NULL,
			source_binding_id TEXT NOT NULL,
			source_resource_id TEXT NOT NULL,
			target_resource_id TEXT NOT NULL,
			target_binding_id TEXT NOT NULL,
			target_node_id TEXT NOT NULL DEFAULT '',
			lifecycle TEXT NOT NULL CHECK (lifecycle IN ('queued','leased','succeeded','failed')),
			authority JSONB NOT NULL,
			requested_at TIMESTAMPTZ NOT NULL,
			reviewed_at TIMESTAMPTZ,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			lease_token TEXT,
			lease_expires_at TIMESTAMPTZ,
			FOREIGN KEY(environment_id,project_id) REFERENCES environments(id,project_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS application_cutover_reviews_lease_idx ON application_cutover_reviews(project_id,lifecycle,lease_expires_at,requested_at,id)`,
		`CREATE INDEX IF NOT EXISTS application_cutover_reviews_app_idx ON application_cutover_reviews(project_id,application_id,requested_at,id)`,
		`CREATE TABLE IF NOT EXISTS application_cutover_review_idempotency (
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			idempotency_key TEXT NOT NULL,
			payload_hash TEXT NOT NULL CHECK(payload_hash ~ '^[0-9a-f]{64}$'),
			review_id TEXT NOT NULL REFERENCES application_cutover_reviews(id) ON DELETE CASCADE,
			created_at TIMESTAMPTZ NOT NULL,
			PRIMARY KEY(project_id,idempotency_key)
		)`,
		`CREATE OR REPLACE FUNCTION prevent_succeeded_cutover_review_mutation() RETURNS trigger AS $$
		BEGIN
			IF OLD.lifecycle='succeeded' AND OLD IS DISTINCT FROM NEW THEN
				RAISE EXCEPTION 'Succeeded Cutover Review authority is immutable';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql`,
		`DROP TRIGGER IF EXISTS cutover_reviews_succeeded_immutable ON application_cutover_reviews`,
		`CREATE TRIGGER cutover_reviews_succeeded_immutable BEFORE UPDATE ON application_cutover_reviews FOR EACH ROW EXECUTE FUNCTION prevent_succeeded_cutover_review_mutation()`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
