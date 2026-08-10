package buildjob

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type PostgresStore struct{ DB *sql.DB }

func (s PostgresStore) Create(ctx context.Context, job Job) (Job, bool, error) {
	if s.DB == nil {
		return Job{}, false, unavailable()
	}
	result, err := s.DB.ExecContext(ctx, `INSERT INTO build_jobs(
		id,project_id,environment_id,application_id,source_binding_id,source_binding_updated_at,github_installation_id,repository_id,repository_owner_id,
		repository_full_name,selected_ref,resolved_commit_sha,application_root,build_context,requested_build_strategy,
		resolved_build_strategy,dockerfile_path,status,failure_code,failure_message_redacted,failure_cause,created_by,idempotency_key,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,NULLIF($17,''),$18,NULLIF($19,''),NULLIF($20,''),NULLIF($21,''),$22,$23,$24,$25)
	ON CONFLICT (project_id,application_id,idempotency_key) DO NOTHING`,
		job.ID, job.ProjectID, job.EnvironmentID, job.ApplicationID, job.Source.BindingID, job.Source.BindingUpdatedAt, job.Source.InstallationID, job.Source.RepositoryID, job.Source.RepositoryOwnerID,
		job.Source.RepositoryFullName, job.Source.SelectedRef, job.Source.ResolvedCommitSHA, job.Source.ApplicationRoot, job.Source.BuildContext, job.RequestedBuildStrategy,
		job.ResolvedBuildStrategy, job.DockerfilePath, job.Status, job.FailureCode, job.FailureMessageRedacted, job.FailureCause, job.CreatedBy, job.IdempotencyKey, job.CreatedAt, job.UpdatedAt)
	if err != nil {
		return Job{}, false, unavailable()
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Job{}, false, unavailable()
	}
	if rows == 1 {
		return job, false, nil
	}
	current, ok, err := s.GetByIdempotency(ctx, job.ProjectID, job.ApplicationID, job.IdempotencyKey)
	if err != nil || !ok {
		return Job{}, false, unavailable()
	}
	return current, true, nil
}

func (s PostgresStore) Get(ctx context.Context, projectID, applicationID, jobID string) (Job, error) {
	if s.DB == nil {
		return Job{}, unavailable()
	}
	job, err := scanJob(s.DB.QueryRowContext(ctx, selectJobColumns+` WHERE project_id=$1 AND application_id=$2 AND id=$3`, projectID, applicationID, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, Error{Code: "BUILD_JOB_NOT_FOUND", Status: 404, Message: "BuildJob was not found.", Cause: "build_job"}
	}
	if err != nil {
		return Job{}, unavailable()
	}
	return job, nil
}

func (s PostgresStore) GetByIdempotency(ctx context.Context, projectID, applicationID, key string) (Job, bool, error) {
	if s.DB == nil {
		return Job{}, false, unavailable()
	}
	job, err := scanJob(s.DB.QueryRowContext(ctx, selectJobColumns+` WHERE project_id=$1 AND application_id=$2 AND idempotency_key=$3`, projectID, applicationID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, unavailable()
	}
	return job, true, nil
}

func (s PostgresStore) List(ctx context.Context, projectID, applicationID, status string, limit int) ([]Job, error) {
	if s.DB == nil {
		return nil, unavailable()
	}
	query := selectJobColumns + ` WHERE project_id=$1 AND application_id=$2`
	args := []any{projectID, applicationID}
	if status != "" {
		args = append(args, status)
		query += fmt.Sprintf(` AND status=$%d`, len(args))
	}
	args = append(args, limit)
	query += fmt.Sprintf(` ORDER BY created_at DESC,id DESC LIMIT $%d`, len(args))
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, unavailable()
	}
	defer rows.Close()
	jobs := make([]Job, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, unavailable()
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable()
	}
	return jobs, nil
}

const selectJobColumns = `SELECT id,project_id,environment_id,application_id,source_binding_id,source_binding_updated_at,github_installation_id,repository_id,repository_owner_id,repository_full_name,selected_ref,resolved_commit_sha,application_root,build_context,requested_build_strategy,resolved_build_strategy,COALESCE(dockerfile_path,''),status,COALESCE(failure_code,''),COALESCE(failure_message_redacted,''),COALESCE(failure_cause,''),created_by,idempotency_key,created_at,updated_at FROM build_jobs`

type scanner interface{ Scan(...any) error }

func scanJob(row scanner) (Job, error) {
	var job Job
	err := row.Scan(&job.ID, &job.ProjectID, &job.EnvironmentID, &job.ApplicationID, &job.Source.BindingID, &job.Source.BindingUpdatedAt, &job.Source.InstallationID, &job.Source.RepositoryID, &job.Source.RepositoryOwnerID, &job.Source.RepositoryFullName, &job.Source.SelectedRef, &job.Source.ResolvedCommitSHA, &job.Source.ApplicationRoot, &job.Source.BuildContext, &job.RequestedBuildStrategy, &job.ResolvedBuildStrategy, &job.DockerfilePath, &job.Status, &job.FailureCode, &job.FailureMessageRedacted, &job.FailureCause, &job.CreatedBy, &job.IdempotencyKey, &job.CreatedAt, &job.UpdatedAt)
	return job, err
}
