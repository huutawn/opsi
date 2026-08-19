package postgres

import (
	"context"
	"database/sql"
)

// MigrateR5014SourceRisk adds persistence for ADC-05 source risk reports and post-deploy dependency verification runs.
func MigrateR5014SourceRisk(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS source_risk_reports (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			application_id TEXT NOT NULL,
			repository_id BIGINT NOT NULL,
			resolved_commit_sha TEXT NOT NULL,
			application_root TEXT NOT NULL,
			scanner_version TEXT NOT NULL,
			build_job_id TEXT,
			analysis_status TEXT NOT NULL,
			files_scanned INT NOT NULL DEFAULT 0,
			bytes_scanned BIGINT NOT NULL DEFAULT 0,
			truncated BOOLEAN NOT NULL DEFAULT FALSE,
			findings JSONB NOT NULL DEFAULT '[]'::jsonb,
			env_references JSONB NOT NULL DEFAULT '[]'::jsonb,
			report_hash TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS source_risk_reports_identity_idx
			ON source_risk_reports(project_id, application_id, repository_id, resolved_commit_sha, application_root, scanner_version)`,
		`CREATE INDEX IF NOT EXISTS source_risk_reports_build_job_idx
			ON source_risk_reports(build_job_id) WHERE build_job_id IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS source_risk_reports_project_app_idx
			ON source_risk_reports(project_id, application_id)`,

		`CREATE TABLE IF NOT EXISTS dependency_verification_runs (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			environment_id TEXT NOT NULL,
			consumer_application_id TEXT NOT NULL,
			dependency_logical_name TEXT NOT NULL,
			deployment_job_id TEXT NOT NULL,
			config_revision BIGINT NOT NULL,
			target_binding_id TEXT,
			source_commit_sha TEXT,
			staleness_hash TEXT NOT NULL,
			provider_health JSONB NOT NULL DEFAULT '{}'::jsonb,
			contract_resolution JSONB NOT NULL DEFAULT '{}'::jsonb,
			connection JSONB NOT NULL DEFAULT '{}'::jsonb,
			consumer_health JSONB NOT NULL DEFAULT '{}'::jsonb,
			consumer_assertion JSONB NOT NULL DEFAULT '{}'::jsonb,
			overall_status TEXT NOT NULL,
			failure_code TEXT,
			triggered_by TEXT NOT NULL,
			started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			completed_at TIMESTAMPTZ
		)`,
		`CREATE INDEX IF NOT EXISTS dep_verification_runs_project_idx
			ON dependency_verification_runs(project_id, environment_id, consumer_application_id, dependency_logical_name)`,
		`CREATE INDEX IF NOT EXISTS dep_verification_runs_deployment_idx
			ON dependency_verification_runs(deployment_job_id)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
