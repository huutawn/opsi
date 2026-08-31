package postgres

import (
	"context"
	"database/sql"
)

func MigrateRepositoryDeploymentWorkflow(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS workload_secret_metadata (
			id TEXT PRIMARY KEY REFERENCES managed_resource_credentials(id) ON DELETE CASCADE,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			service_id TEXT NOT NULL,
			logical_name TEXT NOT NULL,
			revision BIGINT NOT NULL,
			status TEXT NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL,
			UNIQUE(project_id,service_id,logical_name)
		)`,
		`CREATE TABLE IF NOT EXISTS workload_secret_upserts (
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			idempotency_key TEXT NOT NULL,
			payload_hash TEXT NOT NULL,
			secret_id TEXT NOT NULL REFERENCES workload_secret_metadata(id) ON DELETE CASCADE,
			revision BIGINT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL,
			PRIMARY KEY(project_id,idempotency_key)
		)`,
		`CREATE TABLE IF NOT EXISTS deployment_runs (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			idempotency_key TEXT NOT NULL,
			state TEXT NOT NULL,
			revision BIGINT NOT NULL,
			run_data JSONB NOT NULL,
			lease_owner TEXT,
			lease_expires_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL,
			UNIQUE(project_id,idempotency_key)
		)`,
		`CREATE INDEX IF NOT EXISTS deployment_runs_project_created_idx ON deployment_runs(project_id,created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS deployment_runs_runnable_v2_idx ON deployment_runs(state,updated_at) WHERE state IN ('provisioning','building','preflighting','deploying','verifying','rolling_back','cleaning_up')`,
		`DROP INDEX IF EXISTS deployment_runs_runnable_idx`,
		`CREATE INDEX IF NOT EXISTS deployment_runs_lease_idx ON deployment_runs(lease_expires_at) WHERE lease_expires_at IS NOT NULL`,
		`CREATE TABLE IF NOT EXISTS deployment_run_events (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			run_id TEXT NOT NULL REFERENCES deployment_runs(id) ON DELETE CASCADE,
			state TEXT NOT NULL,
			level TEXT NOT NULL,
			message TEXT NOT NULL,
			metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS deployment_run_events_timeline_idx ON deployment_run_events(run_id,created_at,id)`,
		`UPDATE deployment_runs
		 SET state='stale',
		     run_data=(run_data || '{"state":"stale","approval":null,"warning_acknowledgement":null}'::jsonb),
		     updated_at=NOW()
		 WHERE run_data #>> '{plan,schema_version}'='opsi.deployment_plan/v1'
		   AND state NOT IN ('succeeded','stale','failed','rolled_back','cancelled')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
