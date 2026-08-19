package postgres

import (
	"context"
	"database/sql"
)

// MigrateR5013BuildDependency ensures build_jobs retains immutable build dependency state and environment.
func MigrateR5013BuildDependency(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	statements := []string{
		`ALTER TABLE build_jobs ADD COLUMN IF NOT EXISTS build_dependency_state TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE build_jobs ADD COLUMN IF NOT EXISTS build_environment JSONB NOT NULL DEFAULT '{}'::jsonb`,
		`CREATE OR REPLACE FUNCTION prevent_build_job_source_mutation() RETURNS trigger AS $$
		BEGIN
			IF ROW(OLD.project_id,OLD.environment_id,OLD.application_id,OLD.source_binding_id,OLD.source_binding_updated_at,OLD.github_installation_id,OLD.repository_id,OLD.repository_owner_id,OLD.repository_full_name,OLD.selected_ref,OLD.resolved_commit_sha,OLD.application_root,OLD.build_context,OLD.requested_build_strategy,OLD.resolved_build_strategy,OLD.dockerfile_path,OLD.build_dependency_state,OLD.build_environment,OLD.created_by,OLD.idempotency_key,OLD.created_at)
			 IS DISTINCT FROM ROW(NEW.project_id,NEW.environment_id,NEW.application_id,NEW.source_binding_id,NEW.source_binding_updated_at,NEW.github_installation_id,NEW.repository_id,NEW.repository_owner_id,NEW.repository_full_name,NEW.selected_ref,NEW.resolved_commit_sha,NEW.application_root,NEW.build_context,NEW.requested_build_strategy,NEW.resolved_build_strategy,NEW.dockerfile_path,NEW.build_dependency_state,NEW.build_environment,NEW.created_by,NEW.idempotency_key,NEW.created_at) THEN
				RAISE EXCEPTION 'BuildJob source snapshot is immutable';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
