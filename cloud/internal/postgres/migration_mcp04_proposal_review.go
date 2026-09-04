package postgres

import (
	"context"
	"database/sql"
)

// MigrateMCP04ProposalReview creates workflow/audit authority only. It does
// not duplicate ServiceConfiguration or source-repository authority.
func MigrateMCP04ProposalReview(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS proposal_reviews (
			id TEXT PRIMARY KEY,
			org_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
			application_id TEXT NOT NULL REFERENCES control_services(id) ON DELETE CASCADE,
			kind TEXT NOT NULL CHECK (kind IN ('dependency','service_configuration','source_patch')),
			status TEXT NOT NULL CHECK (status IN ('review_required','approved','rejected','stale','expired','applied','apply_failed')),
			proposal_hash TEXT NOT NULL,
			analysis_inputs_hash TEXT NOT NULL,
			source_commit TEXT NOT NULL DEFAULT '',
			application_root TEXT NOT NULL DEFAULT '',
			normalized_payload JSONB NOT NULL,
			reviewed_payload_hash TEXT NOT NULL,
			expected_configuration_revision BIGINT NOT NULL DEFAULT 0,
			expected_configuration_state_hash TEXT NOT NULL DEFAULT '',
			created_by TEXT,
			created_at TIMESTAMPTZ NOT NULL,
			expires_at TIMESTAMPTZ NOT NULL,
			approved_by TEXT,
			approved_at TIMESTAMPTZ,
			rejected_by TEXT,
			rejected_at TIMESTAMPTZ,
			applied_at TIMESTAMPTZ,
			resulting_configuration_revision BIGINT,
			apply_idempotency_key TEXT,
			failure_code TEXT NOT NULL DEFAULT ''
		)`,
		// Expand the persisted kind boundary before new mcp-04 writers emit
		// service_configuration. Keep dependency during one rollout window so an
		// older Cloud replica can coexist safely; registry no longer creates it.
		`DO $$
		BEGIN
			IF NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conrelid = 'proposal_reviews'::regclass
				  AND conname = 'proposal_reviews_kind_check'
				  AND pg_get_constraintdef(oid) LIKE '%service_configuration%'
			) THEN
				ALTER TABLE proposal_reviews DROP CONSTRAINT IF EXISTS proposal_reviews_kind_check;
				ALTER TABLE proposal_reviews ADD CONSTRAINT proposal_reviews_kind_check CHECK (kind IN ('dependency','service_configuration','source_patch'));
			END IF;
		END $$`,
		`CREATE INDEX IF NOT EXISTS proposal_reviews_application_created_idx ON proposal_reviews(project_id, application_id, created_at DESC)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS proposal_reviews_apply_key_idx ON proposal_reviews(project_id, apply_idempotency_key) WHERE apply_idempotency_key IS NOT NULL`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
