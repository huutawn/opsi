package postgres

import (
	"context"
	"database/sql"
)

// MigrateR5010Deployment extends the existing deployment job tables. The
// migration is append-only so legacy development jobs remain readable while
// immutable-image jobs carry their complete authority snapshot.
func MigrateR5010Deployment(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	statements := []string{
		`ALTER TABLE deployment_jobs ADD COLUMN IF NOT EXISTS schema_version TEXT`,
		`ALTER TABLE deployment_jobs ADD COLUMN IF NOT EXISTS mode TEXT`,
		`ALTER TABLE deployment_jobs ADD COLUMN IF NOT EXISTS snapshot_json JSONB`,
		`ALTER TABLE deployment_jobs ADD COLUMN IF NOT EXISTS spec_hash TEXT`,
		`ALTER TABLE deployment_jobs ADD COLUMN IF NOT EXISTS payload_hash TEXT`,
		`ALTER TABLE deployment_jobs ADD COLUMN IF NOT EXISTS terminal_result_json JSONB`,
		`ALTER TABLE deployment_jobs ADD COLUMN IF NOT EXISTS retry_after TIMESTAMPTZ`,
		`ALTER TABLE deployment_events ADD COLUMN IF NOT EXISTS schema_version TEXT`,
		`ALTER TABLE deployment_events ADD COLUMN IF NOT EXISTS attempt INTEGER NOT NULL DEFAULT 0`,
		`CREATE INDEX IF NOT EXISTS deployment_jobs_target_queue_idx ON deployment_jobs(project_id, node_id, status, created_at)`,
		`CREATE INDEX IF NOT EXISTS deployment_jobs_retry_after_idx ON deployment_jobs(project_id, status, retry_after)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
