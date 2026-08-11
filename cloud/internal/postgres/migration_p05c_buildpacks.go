package postgres

import (
	"context"
	"database/sql"
)

func MigrateP05CBuildpacks(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`ALTER TABLE build_jobs DROP CONSTRAINT IF EXISTS build_jobs_resolved_build_strategy_check`,
		`ALTER TABLE build_jobs DISABLE TRIGGER build_jobs_source_immutable`,
		`UPDATE build_jobs SET resolved_build_strategy='buildpack' WHERE resolved_build_strategy='buildpack_required'`,
		`ALTER TABLE build_jobs ENABLE TRIGGER build_jobs_source_immutable`,
		`ALTER TABLE build_jobs ADD CONSTRAINT build_jobs_resolved_build_strategy_check CHECK (resolved_build_strategy IN ('dockerfile','buildpack'))`,
		`ALTER TABLE build_records ADD COLUMN IF NOT EXISTS builder_metadata JSONB NOT NULL DEFAULT '{}'::jsonb`,
		`ALTER TABLE build_records DROP CONSTRAINT IF EXISTS build_records_build_job_metadata_check`,
		`ALTER TABLE build_records ADD CONSTRAINT build_records_build_job_metadata_check CHECK (
			build_job_id IS NULL OR (
				build_strategy IN ('dockerfile','buildpack') AND builder_identity<>'' AND builder_version<>'' AND media_type<>'' AND
				(build_strategy='dockerfile' OR (
					COALESCE(builder_metadata->>'pack_version','')<>'' AND
					COALESCE(builder_metadata->>'builder_image_digest','')<>'' AND
					COALESCE(builder_metadata->>'run_image_digest','')<>'' AND
					COALESCE(builder_metadata->>'lifecycle_version','')<>''
				))
			)
		)`,
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}
