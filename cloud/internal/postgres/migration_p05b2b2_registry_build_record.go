package postgres

import (
	"context"
	"database/sql"
)

func MigrateP05B2B2RegistryBuildRecord(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`ALTER TABLE build_records ADD COLUMN IF NOT EXISTS build_job_id TEXT REFERENCES build_jobs(id)`,
		`ALTER TABLE build_records ADD COLUMN IF NOT EXISTS build_strategy TEXT`,
		`ALTER TABLE build_records ADD COLUMN IF NOT EXISTS builder_identity TEXT`,
		`ALTER TABLE build_records ADD COLUMN IF NOT EXISTS builder_version TEXT`,
		`ALTER TABLE build_records ADD COLUMN IF NOT EXISTS media_type TEXT`,
		`CREATE UNIQUE INDEX IF NOT EXISTS build_records_build_job_uidx ON build_records(build_job_id) WHERE build_job_id IS NOT NULL`,
		`ALTER TABLE build_jobs ADD COLUMN IF NOT EXISTS build_record_id TEXT REFERENCES build_records(id)`,
		`ALTER TABLE build_jobs ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ`,
		`CREATE UNIQUE INDEX IF NOT EXISTS build_jobs_build_record_uidx ON build_jobs(build_record_id) WHERE build_record_id IS NOT NULL`,
		`ALTER TABLE build_executor_attempts DROP CONSTRAINT IF EXISTS build_executor_attempts_last_state_check`,
		`ALTER TABLE build_executor_attempts ADD CONSTRAINT build_executor_attempts_last_state_check CHECK (last_state IN ('dispatching','dispatched','dispatch_rejected','claimed','succeeded','failed'))`,
		`DO $$ DECLARE constraint_name TEXT; BEGIN
			FOR constraint_name IN SELECT conname FROM pg_constraint WHERE conrelid='build_executor_attempts'::regclass AND contype='c' AND pg_get_constraintdef(oid) LIKE '%dispatch_rejected%' AND pg_get_constraintdef(oid) LIKE '%failure_code%'
			LOOP EXECUTE format('ALTER TABLE build_executor_attempts DROP CONSTRAINT %I', constraint_name); END LOOP;
		END $$`,
		`ALTER TABLE build_executor_attempts ADD CONSTRAINT build_executor_attempts_failure_state_check CHECK ((last_state IN ('dispatch_rejected','failed'))=(failure_code IS NOT NULL))`,
		`DO $$ DECLARE constraint_name TEXT; BEGIN
			FOR constraint_name IN SELECT conname FROM pg_constraint WHERE conrelid='build_executor_attempts'::regclass AND contype='c' AND pg_get_constraintdef(oid) LIKE '%claimed_at%' AND pg_get_constraintdef(oid) LIKE '%lease_token_hash%'
			LOOP EXECUTE format('ALTER TABLE build_executor_attempts DROP CONSTRAINT %I', constraint_name); END LOOP;
		END $$`,
		`ALTER TABLE build_executor_attempts ADD CONSTRAINT build_executor_attempts_claimed_fields_check CHECK ((last_state IN ('claimed','succeeded','failed'))=(claimed_at IS NOT NULL AND github_run_id IS NOT NULL AND github_run_attempt IS NOT NULL AND lease_token_hash IS NOT NULL AND octet_length(lease_token_hash)=32 AND lease_expires_at IS NOT NULL))`,
		`ALTER TABLE build_records DROP CONSTRAINT IF EXISTS build_records_build_job_metadata_check`,
		`ALTER TABLE build_records ADD CONSTRAINT build_records_build_job_metadata_check CHECK (
			build_job_id IS NULL OR (build_strategy='dockerfile' AND builder_identity<>'' AND builder_version<>'' AND media_type<>'')
		)`,
		`ALTER TABLE build_jobs DROP CONSTRAINT IF EXISTS build_jobs_success_record_check`,
		`ALTER TABLE build_jobs ADD CONSTRAINT build_jobs_success_record_check CHECK (
			(status='succeeded')=(build_record_id IS NOT NULL AND completed_at IS NOT NULL)
		)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
