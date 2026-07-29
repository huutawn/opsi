package postgres

import (
	"context"
	"database/sql"
)

func MigrateBuildRecords(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	statements := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS github_repositories_build_record_owner_uidx ON github_repositories(repository_id, owner_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS github_service_bindings_build_record_identity_uidx ON github_service_bindings(id, project_id, service_id, repository_id, service_key)`,
		`CREATE TABLE IF NOT EXISTS build_records (
			id TEXT PRIMARY KEY,
			schema_version TEXT NOT NULL CHECK (schema_version = 'opsi.build_record/v1'),
			project_id TEXT NOT NULL REFERENCES projects(id),
			repository_id BIGINT NOT NULL CHECK (repository_id > 0),
			repository_owner_id BIGINT NOT NULL CHECK (repository_owner_id > 0),
			active_binding_id TEXT NOT NULL,
			service_id TEXT NOT NULL,
			service_key TEXT NOT NULL,
			issuer TEXT NOT NULL,
			subject TEXT NOT NULL,
			ref TEXT NOT NULL,
			sha TEXT NOT NULL CHECK (sha ~ '^[0-9a-f]{40}$'),
			event_name TEXT NOT NULL,
			workflow TEXT NOT NULL,
			workflow_ref TEXT NOT NULL,
			job_workflow_ref TEXT,
			run_id BIGINT NOT NULL CHECK (run_id > 0),
			run_attempt INTEGER NOT NULL CHECK (run_attempt > 0),
			config_hash TEXT NOT NULL CHECK (config_hash ~ '^[0-9a-f]{64}$'),
			plan_hash TEXT CHECK (plan_hash IS NULL OR plan_hash ~ '^[0-9a-f]{64}$'),
			platform TEXT NOT NULL,
			oci_repository TEXT NOT NULL,
			oci_digest TEXT NOT NULL CHECK (oci_digest ~ '^sha256:[0-9a-f]{64}$'),
			provenance_digest TEXT CHECK (provenance_digest IS NULL OR provenance_digest ~ '^sha256:[0-9a-f]{64}$'),
			build_status TEXT NOT NULL CHECK (build_status = 'succeeded'),
			payload_hash TEXT NOT NULL CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
			created_at TIMESTAMPTZ NOT NULL,
			UNIQUE (repository_id, run_id, run_attempt, service_key),
			FOREIGN KEY (repository_id, repository_owner_id) REFERENCES github_repositories(repository_id, owner_id),
			FOREIGN KEY (active_binding_id, project_id, service_id, repository_id, service_key) REFERENCES github_service_bindings(id, project_id, service_id, repository_id, service_key)
		)`,
		`CREATE INDEX IF NOT EXISTS build_records_project_created_idx ON build_records(project_id, created_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS build_records_project_service_idx ON build_records(project_id, service_key, created_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS build_records_project_sha_idx ON build_records(project_id, sha, created_at DESC, id DESC)`,
		`CREATE OR REPLACE FUNCTION prevent_build_record_mutation() RETURNS trigger AS $$
		BEGIN RAISE EXCEPTION 'build_records is append-only'; END; $$ LANGUAGE plpgsql`,
		`DROP TRIGGER IF EXISTS build_records_no_update ON build_records`,
		`DROP TRIGGER IF EXISTS build_records_no_delete ON build_records`,
		`CREATE TRIGGER build_records_no_update BEFORE UPDATE ON build_records FOR EACH ROW EXECUTE FUNCTION prevent_build_record_mutation()`,
		`CREATE TRIGGER build_records_no_delete BEFORE DELETE ON build_records FOR EACH ROW EXECUTE FUNCTION prevent_build_record_mutation()`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
