package buildjob

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s PostgresStore) ReserveDispatch(ctx context.Context, projectID, applicationID string, attempt DispatchAttempt) error {
	if s.DB == nil {
		return unavailable()
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return unavailable()
	}
	defer tx.Rollback()
	job, err := scanJob(tx.QueryRowContext(ctx, selectJobColumns+` WHERE project_id=$1 AND application_id=$2 AND id=$3 FOR UPDATE`, projectID, applicationID, attempt.BuildJobID))
	if errors.Is(err, sql.ErrNoRows) {
		return Error{Code: "BUILD_JOB_NOT_FOUND", Status: 404, Message: "BuildJob was not found.", Cause: "build_job"}
	}
	if err != nil {
		return unavailable()
	}
	if err := validateDispatchableJob(job); err != nil {
		return err
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM build_executor_attempts WHERE build_job_id=$1 AND last_state IN ('dispatching','dispatched','claimed'))`, job.ID).Scan(&active); err != nil {
		return unavailable()
	}
	if active {
		return Error{Code: "DUPLICATE_ACTIVE_DISPATCH", Status: 409, Message: "BuildJob already has an active dispatch attempt.", Cause: "executor_dispatch"}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO build_executor_attempts(attempt_id,build_job_id,provider,workflow_path,workflow_ref,executor_ref,dispatched_at,last_state,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$7)`, attempt.AttemptID, attempt.BuildJobID, attempt.Provider, attempt.Workflow, attempt.WorkflowRef, attempt.ExecutorRef, attempt.DispatchedAt, attempt.LastState); err != nil {
		return unavailable()
	}
	if err := tx.Commit(); err != nil {
		return unavailable()
	}
	return nil
}

func (s PostgresStore) CompleteDispatch(ctx context.Context, attemptID string, facts DispatchFacts, now time.Time) (DispatchAttempt, error) {
	if s.DB == nil {
		return DispatchAttempt{}, unavailable()
	}
	row := s.DB.QueryRowContext(ctx, `UPDATE build_executor_attempts SET last_state='dispatched',github_run_id=NULLIF($2,0),github_run_attempt=NULLIF($3,0),github_run_url=NULLIF($4,''),dispatched_at=$5,updated_at=$5 WHERE attempt_id=$1 AND last_state='dispatching' RETURNING `+selectAttemptColumns, attemptID, facts.RunID, facts.RunAttempt, facts.RunURL, now)
	attempt, err := scanAttempt(row)
	if err != nil {
		return DispatchAttempt{}, unavailable()
	}
	return attempt, nil
}

func (s PostgresStore) RejectDispatch(ctx context.Context, attemptID, code string, now time.Time) error {
	if s.DB == nil {
		return unavailable()
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE build_executor_attempts SET last_state='dispatch_rejected',failure_code=$2,completed_at=$3,updated_at=$3 WHERE attempt_id=$1 AND last_state='dispatching'`, attemptID, code, now)
	if err != nil {
		return unavailable()
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return unavailable()
	}
	return nil
}

func (s PostgresStore) ClaimDispatch(ctx context.Context, jobID, attemptID string, identity RunnerIdentity, leaseHash []byte, expiresAt, now time.Time) error {
	if s.DB == nil {
		return unavailable()
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return unavailable()
	}
	defer tx.Rollback()
	attempt, err := scanAttempt(tx.QueryRowContext(ctx, `SELECT `+selectAttemptColumns+` FROM build_executor_attempts WHERE attempt_id=$1 FOR UPDATE`, attemptID))
	if errors.Is(err, sql.ErrNoRows) || err == nil && attempt.BuildJobID != jobID {
		return Error{Code: "EXECUTOR_RUN_MISMATCH", Status: 409, Message: "Executor run does not match the dispatch attempt.", Cause: "executor_run"}
	}
	if err != nil {
		return unavailable()
	}
	if attempt.ClaimedAt != nil || attempt.LastState == DispatchStateClaimed {
		return Error{Code: "RUNNER_CLAIM_CONSUMED", Status: 409, Message: "Runner claim was already consumed.", Cause: "runner_claim"}
	}
	if attempt.LastState != DispatchStateDispatched || attempt.RunID != 0 && attempt.RunID != identity.RunID || attempt.RunAttempt != 0 && attempt.RunAttempt != identity.RunAttempt {
		return Error{Code: "EXECUTOR_RUN_MISMATCH", Status: 409, Message: "Executor run does not match the dispatch attempt.", Cause: "executor_run"}
	}
	job, err := scanJob(tx.QueryRowContext(ctx, selectJobColumns+` WHERE id=$1 FOR UPDATE`, jobID))
	if errors.Is(err, sql.ErrNoRows) || err == nil && job.Status != StatusReady {
		return Error{Code: "BUILD_JOB_NOT_READY", Status: 409, Message: "BuildJob is not ready for runner claim.", Cause: "build_job_status"}
	}
	if err != nil {
		return unavailable()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE build_jobs SET status='running',updated_at=$2 WHERE id=$1 AND status='ready'`, jobID, now); err != nil {
		return unavailable()
	}
	runURL := "https://github.com/" + identity.Repository + "/actions/runs/" + uintString(identity.RunID)
	if _, err := tx.ExecContext(ctx, `UPDATE build_executor_attempts SET last_state='claimed',github_run_id=$2,github_run_attempt=$3,github_run_url=$4,claimed_at=$5,lease_token_hash=$6,lease_expires_at=$7,updated_at=$5 WHERE attempt_id=$1`, attemptID, identity.RunID, identity.RunAttempt, runURL, now, leaseHash, expiresAt); err != nil {
		return unavailable()
	}
	if err := tx.Commit(); err != nil {
		return unavailable()
	}
	return nil
}

func (s PostgresStore) GetRunnerJob(ctx context.Context, access RunnerAccess, now time.Time) (Job, error) {
	if s.DB == nil {
		return Job{}, unavailable()
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, unavailable()
	}
	defer tx.Rollback()
	attempt, err := scanAttempt(tx.QueryRowContext(ctx, `SELECT `+selectAttemptColumns+` FROM build_executor_attempts WHERE lease_token_hash=$1 FOR UPDATE`, access.LeaseHash))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, Error{Code: "RUNNER_LEASE_INVALID", Status: 401, Message: "Runner lease is invalid.", Cause: "runner_lease"}
	}
	if err != nil {
		return Job{}, unavailable()
	}
	if attempt.BuildJobID != access.JobID || access.AttemptID != "" && attempt.AttemptID != access.AttemptID || access.RunID != 0 && attempt.RunID != access.RunID || access.RunAttempt != 0 && attempt.RunAttempt != access.RunAttempt {
		return Job{}, Error{Code: "RUNNER_LEASE_SCOPE_MISMATCH", Status: 403, Message: "Runner lease cannot access this BuildJob attempt.", Cause: "runner_lease_scope"}
	}
	if attempt.LeaseExpiresAt.IsZero() || !now.Before(attempt.LeaseExpiresAt) {
		return Job{}, Error{Code: "RUNNER_LEASE_EXPIRED", Status: 401, Message: "Runner lease has expired.", Cause: "runner_lease"}
	}
	if attempt.LastState != DispatchStateClaimed {
		return Job{}, Error{Code: "RUNNER_LEASE_REVOKED", Status: 409, Message: "Runner lease is no longer valid for this dispatch attempt.", Cause: "runner_lease"}
	}
	job, err := scanJob(tx.QueryRowContext(ctx, selectJobColumns+` WHERE id=$1 FOR UPDATE`, access.JobID))
	if errors.Is(err, sql.ErrNoRows) || err == nil && job.Status != StatusRunning {
		return Job{}, Error{Code: "RUNNER_LEASE_REVOKED", Status: 409, Message: "Runner lease is no longer valid for this BuildJob.", Cause: "build_job_status"}
	}
	if err != nil {
		return Job{}, unavailable()
	}
	if err := tx.Commit(); err != nil {
		return Job{}, unavailable()
	}
	return job, nil
}

const selectAttemptColumns = `provider,attempt_id,build_job_id,workflow_path,workflow_ref,executor_ref,COALESCE(github_run_id,0),COALESCE(github_run_attempt,0),COALESCE(github_run_url,''),dispatched_at,claimed_at,completed_at,last_state,COALESCE(failure_code,''),lease_expires_at,lease_token_hash`

func scanAttempt(row scanner) (DispatchAttempt, error) {
	var attempt DispatchAttempt
	var leaseExpiresAt sql.NullTime
	var leaseHash []byte
	err := row.Scan(&attempt.Provider, &attempt.AttemptID, &attempt.BuildJobID, &attempt.Workflow, &attempt.WorkflowRef, &attempt.ExecutorRef, &attempt.RunID, &attempt.RunAttempt, &attempt.RunURL, &attempt.DispatchedAt, &attempt.ClaimedAt, &attempt.CompletedAt, &attempt.LastState, &attempt.FailureCode, &leaseExpiresAt, &leaseHash)
	if leaseExpiresAt.Valid {
		attempt.LeaseExpiresAt = leaseExpiresAt.Time
	}
	attempt.LeaseHash = leaseHash
	return attempt, err
}
