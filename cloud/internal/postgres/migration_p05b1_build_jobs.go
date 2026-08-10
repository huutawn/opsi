package postgres

import (
	"context"
	"database/sql"
)

func MigrateP05B1BuildJobs(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS build_jobs (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			environment_id TEXT NOT NULL,
			application_id TEXT NOT NULL,
			source_binding_id TEXT NOT NULL,
			source_binding_updated_at TIMESTAMPTZ NOT NULL,
			github_installation_id BIGINT NOT NULL CHECK (github_installation_id > 0),
			repository_id BIGINT NOT NULL CHECK (repository_id > 0),
			repository_owner_id BIGINT NOT NULL CHECK (repository_owner_id > 0),
			repository_full_name TEXT NOT NULL,
			selected_ref TEXT NOT NULL,
			resolved_commit_sha TEXT NOT NULL CHECK (resolved_commit_sha ~ '^[0-9a-f]{40}$'),
			application_root TEXT NOT NULL,
			build_context TEXT NOT NULL,
			requested_build_strategy TEXT NOT NULL CHECK (requested_build_strategy IN ('auto','dockerfile','buildpack')),
			resolved_build_strategy TEXT NOT NULL CHECK (resolved_build_strategy IN ('dockerfile','buildpack_required')),
			dockerfile_path TEXT,
			status TEXT NOT NULL CHECK (status IN ('pending','ready','running','succeeded','failed','cancelled')),
			failure_code TEXT,
			failure_message_redacted TEXT,
			failure_cause TEXT,
			created_by TEXT NOT NULL,
			idempotency_key TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL,
			UNIQUE (project_id, application_id, idempotency_key),
			CHECK (id<>'' AND project_id<>'' AND environment_id<>'' AND application_id<>'' AND source_binding_id<>'' AND created_by<>''),
			CHECK (idempotency_key<>'' AND length(idempotency_key)<=128 AND idempotency_key=btrim(idempotency_key) AND idempotency_key !~ '[[:space:][:cntrl:]]'),
			CHECK (length(repository_full_name)<=201 AND repository_full_name ~ '^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$'),
			CHECK (selected_ref<>'' AND length(selected_ref)<=1024 AND selected_ref=btrim(selected_ref) AND selected_ref !~ '[[:cntrl:]]'),
			CHECK (length(application_root)<=1024 AND length(build_context)<=1024 AND (dockerfile_path IS NULL OR length(dockerfile_path)<=1024)),
			CHECK ((application_root='.' OR (application_root<>'' AND application_root !~ '^/' AND position(E'\\\\' in application_root)=0 AND application_root !~ '(^|/)\\.\\.?(/|$)' AND application_root !~ '//|/$' AND application_root !~ '[[:cntrl:]]'))),
			CHECK ((build_context='.' OR (build_context<>'' AND build_context !~ '^/' AND position(E'\\\\' in build_context)=0 AND build_context !~ '(^|/)\\.\\.?(/|$)' AND build_context !~ '//|/$' AND build_context !~ '[[:cntrl:]]'))),
			CHECK (build_context='.' OR application_root=build_context OR left(application_root,length(build_context)+1)=build_context||'/'),
			CHECK (dockerfile_path IS NULL OR (dockerfile_path<>'' AND dockerfile_path<>'.' AND dockerfile_path !~ '^/' AND position(E'\\\\' in dockerfile_path)=0 AND dockerfile_path !~ '(^|/)\\.\\.?(/|$)' AND dockerfile_path !~ '//|/$' AND dockerfile_path !~ '[[:cntrl:]]')),
			CHECK ((resolved_build_strategy='dockerfile') = (dockerfile_path IS NOT NULL)),
			CHECK ((status='failed') = (failure_code IS NOT NULL AND failure_message_redacted IS NOT NULL AND failure_cause IS NOT NULL)),
			CHECK (updated_at>=created_at)
		)`,
		`CREATE INDEX IF NOT EXISTS build_jobs_application_status_created_idx ON build_jobs(project_id,application_id,status,created_at DESC)`,
		`CREATE OR REPLACE FUNCTION prevent_build_job_source_mutation() RETURNS trigger AS $$
		BEGIN
			IF ROW(OLD.project_id,OLD.environment_id,OLD.application_id,OLD.source_binding_id,OLD.source_binding_updated_at,OLD.github_installation_id,OLD.repository_id,OLD.repository_owner_id,OLD.repository_full_name,OLD.selected_ref,OLD.resolved_commit_sha,OLD.application_root,OLD.build_context,OLD.requested_build_strategy,OLD.resolved_build_strategy,OLD.dockerfile_path,OLD.created_by,OLD.idempotency_key,OLD.created_at)
			 IS DISTINCT FROM ROW(NEW.project_id,NEW.environment_id,NEW.application_id,NEW.source_binding_id,NEW.source_binding_updated_at,NEW.github_installation_id,NEW.repository_id,NEW.repository_owner_id,NEW.repository_full_name,NEW.selected_ref,NEW.resolved_commit_sha,NEW.application_root,NEW.build_context,NEW.requested_build_strategy,NEW.resolved_build_strategy,NEW.dockerfile_path,NEW.created_by,NEW.idempotency_key,NEW.created_at) THEN
				RAISE EXCEPTION 'BuildJob source snapshot is immutable';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql`,
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname='build_jobs_source_immutable' AND tgrelid='build_jobs'::regclass) THEN
				CREATE TRIGGER build_jobs_source_immutable BEFORE UPDATE ON build_jobs FOR EACH ROW EXECUTE FUNCTION prevent_build_job_source_mutation();
			END IF;
		END $$`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
