package postgres

import (
	"context"
	"database/sql"
)

const BootstrapCheckpointMigrationID = "p04_bootstrap_checkpoint_v1"

var bootstrapCheckpointMigrationStatements = []string{
	`ALTER TABLE bootstrap_sessions ADD COLUMN IF NOT EXISTS checkpoint_schema_version INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE bootstrap_sessions ADD COLUMN IF NOT EXISTS checkpoint_plan_version TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE bootstrap_sessions ADD COLUMN IF NOT EXISTS checkpoint_plan_fingerprint TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE bootstrap_sessions ADD COLUMN IF NOT EXISTS checkpoint_next_step_index INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE bootstrap_sessions ADD COLUMN IF NOT EXISTS checkpoint_last_completed_step TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE bootstrap_sessions ADD COLUMN IF NOT EXISTS checkpoint_updated_at TIMESTAMPTZ NULL`,
	`DO $$
	BEGIN
		IF NOT EXISTS (
			SELECT 1 FROM pg_constraint
			WHERE conname = 'bootstrap_sessions_checkpoint_schema_version_nonnegative'
			  AND conrelid = 'bootstrap_sessions'::regclass
		) THEN
			ALTER TABLE bootstrap_sessions
				ADD CONSTRAINT bootstrap_sessions_checkpoint_schema_version_nonnegative
				CHECK (checkpoint_schema_version >= 0);
		END IF;
	END
	$$`,
	`DO $$
	BEGIN
		IF NOT EXISTS (
			SELECT 1 FROM pg_constraint
			WHERE conname = 'bootstrap_sessions_checkpoint_next_step_index_nonnegative'
			  AND conrelid = 'bootstrap_sessions'::regclass
		) THEN
			ALTER TABLE bootstrap_sessions
				ADD CONSTRAINT bootstrap_sessions_checkpoint_next_step_index_nonnegative
				CHECK (checkpoint_next_step_index >= 0);
		END IF;
	END
	$$`,
}

type checkpointMigrationExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func MigrateBootstrapCheckpoint(ctx context.Context, db checkpointMigrationExecer) error {
	for _, statement := range bootstrapCheckpointMigrationStatements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
