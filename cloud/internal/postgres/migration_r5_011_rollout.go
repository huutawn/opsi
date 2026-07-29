package postgres

import (
	"context"
	"database/sql"
)

// MigrateR5011Rollout extends the canonical deployment tables without changing
// or deleting R5-010 jobs and events.
func MigrateR5011Rollout(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	statements := []string{
		`ALTER TABLE deployment_jobs ADD COLUMN IF NOT EXISTS base_deployment_id TEXT`,
		`ALTER TABLE deployment_jobs ADD COLUMN IF NOT EXISTS rollout_intent_json JSONB`,
		`ALTER TABLE deployment_jobs ADD COLUMN IF NOT EXISTS rollout_state TEXT`,
		`ALTER TABLE deployment_jobs ADD COLUMN IF NOT EXISTS rollout_state_hash TEXT`,
		`ALTER TABLE deployment_jobs ADD COLUMN IF NOT EXISTS rollout_version BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE deployment_jobs ADD COLUMN IF NOT EXISTS desired_digest TEXT`,
		`ALTER TABLE deployment_jobs ADD COLUMN IF NOT EXISTS current_digest TEXT`,
		`ALTER TABLE deployment_jobs ADD COLUMN IF NOT EXISTS previous_digest TEXT`,
		`ALTER TABLE deployment_jobs ADD COLUMN IF NOT EXISTS exposure_spec_json JSONB`,
		`ALTER TABLE deployment_jobs ADD COLUMN IF NOT EXISTS known_good_id TEXT`,
		`ALTER TABLE deployment_jobs ADD COLUMN IF NOT EXISTS known_good_hash TEXT`,
		`ALTER TABLE deployment_jobs ADD COLUMN IF NOT EXISTS readiness_evidence_hash TEXT`,
		`ALTER TABLE deployment_events ADD COLUMN IF NOT EXISTS rollout_id TEXT`,
		`ALTER TABLE deployment_events ADD COLUMN IF NOT EXISTS intent_hash TEXT`,
		`ALTER TABLE deployment_events ADD COLUMN IF NOT EXISTS state_hash TEXT`,
		`ALTER TABLE deployment_events ADD COLUMN IF NOT EXISTS readiness_evidence_hash TEXT`,
		`CREATE INDEX IF NOT EXISTS deployment_jobs_rollout_target_idx ON deployment_jobs(project_id, environment_id, runtime_id, service_id, created_at DESC) WHERE rollout_intent_json IS NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS deployment_jobs_rollout_id_idx ON deployment_jobs((rollout_intent_json->>'rollout_id')) WHERE rollout_intent_json IS NOT NULL`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
