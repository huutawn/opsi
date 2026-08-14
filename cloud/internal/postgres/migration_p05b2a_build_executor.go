package postgres

import (
	"context"
	"database/sql"
)

func MigrateP05B2ABuildExecutor(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS build_executor_attempts (
			attempt_id TEXT PRIMARY KEY,
			build_job_id TEXT NOT NULL REFERENCES build_jobs(id) ON DELETE CASCADE,
			provider TEXT NOT NULL CHECK (provider='github_actions'),
			workflow_path TEXT NOT NULL,
			workflow_ref TEXT NOT NULL,
			executor_ref TEXT NOT NULL,
			github_run_id BIGINT CHECK (github_run_id>0),
			github_run_attempt INTEGER CHECK (github_run_attempt>0),
			github_run_url TEXT,
			dispatched_at TIMESTAMPTZ NOT NULL,
			claimed_at TIMESTAMPTZ,
			completed_at TIMESTAMPTZ,
			last_state TEXT NOT NULL CHECK (last_state IN ('dispatching','dispatched','dispatch_rejected','claimed')),
			failure_code TEXT,
			lease_token_hash BYTEA,
			lease_expires_at TIMESTAMPTZ,
			updated_at TIMESTAMPTZ NOT NULL,
			CHECK (attempt_id<>'' AND build_job_id<>'' AND workflow_path<>'' AND workflow_ref<>'' AND executor_ref<>''),
			CHECK ((last_state='dispatch_rejected')=(failure_code IS NOT NULL)),
			CHECK ((last_state='claimed')=(claimed_at IS NOT NULL AND github_run_id IS NOT NULL AND github_run_attempt IS NOT NULL AND lease_token_hash IS NOT NULL AND octet_length(lease_token_hash)=32 AND lease_expires_at IS NOT NULL)),
			CHECK (lease_expires_at IS NULL OR lease_expires_at>claimed_at),
			CHECK (claimed_at IS NULL OR claimed_at>=dispatched_at),
			CHECK (completed_at IS NULL OR completed_at>=dispatched_at)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS build_executor_attempts_active_job_uidx ON build_executor_attempts(build_job_id) WHERE last_state IN ('dispatching','dispatched','claimed')`,
		`CREATE UNIQUE INDEX IF NOT EXISTS build_executor_attempts_lease_hash_uidx ON build_executor_attempts(lease_token_hash) WHERE lease_token_hash IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS build_executor_attempts_run_idx ON build_executor_attempts(github_run_id) WHERE github_run_id IS NOT NULL`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
