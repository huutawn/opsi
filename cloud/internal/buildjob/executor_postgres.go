package buildjob

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
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

func (s PostgresStore) CompleteRunner(ctx context.Context, completion Completion, registry RegistryConfig, executor ExecutorConfig) (CompletionResult, error) {
	if s.DB == nil {
		return CompletionResult{}, unavailable()
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return CompletionResult{}, unavailable()
	}
	defer tx.Rollback()
	attempt, err := scanAttempt(tx.QueryRowContext(ctx, `SELECT `+selectAttemptColumns+` FROM build_executor_attempts WHERE lease_token_hash=$1 FOR UPDATE`, completion.LeaseHash))
	if errors.Is(err, sql.ErrNoRows) {
		return CompletionResult{}, Error{Code: "RUNNER_LEASE_INVALID", Status: 401, Message: "Runner lease is invalid.", Cause: "runner_lease"}
	}
	if err != nil {
		return CompletionResult{}, unavailable()
	}
	result := completion.Result
	if attempt.BuildJobID != result.BuildJobID || attempt.AttemptID != result.AttemptID {
		return CompletionResult{}, Error{Code: "RUNNER_LEASE_SCOPE_MISMATCH", Status: 403, Message: "Runner lease cannot complete this BuildJob attempt.", Cause: "runner_lease_scope"}
	}
	if attempt.LeaseExpiresAt.IsZero() || !completion.Now.Before(attempt.LeaseExpiresAt) {
		return CompletionResult{}, Error{Code: "RUNNER_LEASE_EXPIRED", Status: 401, Message: "Runner lease has expired.", Cause: "runner_lease"}
	}
	job, err := scanJob(tx.QueryRowContext(ctx, selectJobColumns+` WHERE id=$1 FOR UPDATE`, result.BuildJobID))
	if errors.Is(err, sql.ErrNoRows) {
		return CompletionResult{}, Error{Code: "BUILD_JOB_NOT_FOUND", Status: 404, Message: "BuildJob was not found.", Cause: "build_job"}
	}
	if err != nil {
		return CompletionResult{}, unavailable()
	}
	if attempt.LastState == DispatchStateSucceeded && job.Status == StatusSucceeded {
		var digest string
		if job.BuildRecordID == "" || tx.QueryRowContext(ctx, `SELECT oci_digest FROM build_records WHERE id=$1 AND build_job_id=$2`, job.BuildRecordID, job.ID).Scan(&digest) != nil {
			return CompletionResult{}, unavailable()
		}
		if digest != result.Digest {
			return CompletionResult{}, Error{Code: "BUILD_RESULT_CONFLICT", Status: 409, Message: "BuildJob already completed with a different digest.", Cause: "build_result"}
		}
		if err := tx.Commit(); err != nil {
			return CompletionResult{}, unavailable()
		}
		return CompletionResult{BuildRecordID: job.BuildRecordID, Digest: digest, BuildJobState: StatusSucceeded, Reused: true}, nil
	}
	if attempt.LastState != DispatchStateClaimed || job.Status != StatusRunning {
		return CompletionResult{}, Error{Code: "RUNNER_LEASE_REVOKED", Status: 409, Message: "Runner lease is no longer valid for this BuildJob.", Cause: "build_job_status"}
	}
	if result.Executor.Strategy != job.ResolvedBuildStrategy || job.ResolvedBuildStrategy == StrategyDockerfile && job.DockerfilePath == "" || job.ResolvedBuildStrategy == StrategyBuildpack && job.DockerfilePath != "" || job.ResolvedBuildStrategy != StrategyDockerfile && job.ResolvedBuildStrategy != StrategyBuildpack {
		return CompletionResult{}, Error{Code: "BUILD_STRATEGY_MISMATCH", Status: 409, Message: "BuildJob strategy cannot accept this result.", Cause: "build_strategy"}
	}
	target := registry.Target(job.ApplicationID, job.ID)
	if result.RegistryReference != target.DigestReference(result.Digest) {
		return CompletionResult{}, Error{Code: "REGISTRY_TARGET_MISMATCH", Status: 409, Message: "Registry result is outside the canonical Opsi namespace.", Cause: "registry_target"}
	}
	var serviceKey, orgID string
	err = tx.QueryRowContext(ctx, `SELECT b.service_key,p.org_id FROM github_service_bindings b
		JOIN control_services s ON s.id=b.service_id AND s.project_id=b.project_id
		JOIN projects p ON p.id=b.project_id
		WHERE b.id=$1 AND b.project_id=$2 AND b.service_id=$3 AND b.repository_id=$4 AND b.installation_id=$5 AND b.updated_at=$6 AND b.status='active' AND s.status<>'deleted'`,
		job.Source.BindingID, job.ProjectID, job.ApplicationID, job.Source.RepositoryID, job.Source.InstallationID, job.Source.BindingUpdatedAt).Scan(&serviceKey, &orgID)
	if errors.Is(err, sql.ErrNoRows) {
		return CompletionResult{}, Error{Code: "SOURCE_BINDING_MISMATCH", Status: 409, Message: "The active source binding no longer matches the immutable BuildJob.", Cause: "source_binding"}
	}
	if err != nil {
		return CompletionResult{}, unavailable()
	}
	record := runnerBuildRecord(job, attempt, serviceKey, result, target, executor, completion.Now)
	payloadHash, err := buildRecordPayloadHash(record)
	if err != nil {
		return CompletionResult{}, unavailable()
	}
	builderMetadata, err := json.Marshal(record.Build.Builder)
	if err != nil {
		return CompletionResult{}, unavailable()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO build_records(
		id,schema_version,project_id,repository_id,repository_owner_id,active_binding_id,service_id,service_key,
		issuer,subject,ref,sha,event_name,workflow,workflow_ref,run_id,run_attempt,
		config_hash,platform,oci_repository,oci_digest,build_job_id,build_strategy,builder_identity,builder_version,builder_metadata,media_type,build_status,payload_hash,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30)`,
		record.ID, record.SchemaVersion, record.ProjectID, record.RepositoryID, record.RepositoryOwnerID, record.ActiveBindingID, record.ServiceID, record.ServiceKey,
		record.Workload.Issuer, record.Workload.Subject, record.Workload.Ref, record.Workload.SHA, record.Workload.EventName, record.Workload.Workflow, record.Workload.WorkflowRef, record.Workload.RunID, record.Workload.RunAttempt,
		record.Build.ConfigHash, record.Build.Platform, record.Build.OCIRepository, record.Build.OCIDigest, record.Build.BuildJobID, record.Build.BuildStrategy, record.Build.BuilderIdentity, record.Build.BuilderVersion, builderMetadata, record.Build.MediaType, record.Build.Status, payloadHash, record.CreatedAt)
	if err != nil {
		return CompletionResult{}, unavailable()
	}
	auditID := sha256.Sum256([]byte("build-finalization:" + job.ID))
	auditMetadata, _ := json.Marshal(map[string]any{"build_job_id": job.ID, "attempt_id": attempt.AttemptID, "repository": target.Repository, "digest": result.Digest})
	if _, err := tx.ExecContext(ctx, `INSERT INTO cloud_audit_events(id,org_id,project_id,actor_type,action,resource_type,resource_id,result,metadata_redacted,created_at) VALUES($1,$2,$3,'github_actions','BUILD_RECORD_FINALIZED','build_record',$4,'success',$5,$6)`, "aud-"+hex.EncodeToString(auditID[:16]), orgID, job.ProjectID, record.ID, auditMetadata, completion.Now); err != nil {
		return CompletionResult{}, unavailable()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE build_jobs SET status='succeeded',build_record_id=$2,completed_at=$3,updated_at=$3 WHERE id=$1 AND status='running'`, job.ID, record.ID, completion.Now); err != nil {
		return CompletionResult{}, unavailable()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE build_executor_attempts SET last_state='succeeded',completed_at=$2,updated_at=$2 WHERE attempt_id=$1 AND last_state='claimed'`, attempt.AttemptID, completion.Now); err != nil {
		return CompletionResult{}, unavailable()
	}
	if result.SourceRiskReport != nil {
		srr := *result.SourceRiskReport
		id := sha256.Sum256([]byte(job.ID + ":srr"))
		srrID := "srr-" + hex.EncodeToString(id[:12])
		findingsJSON, _ := json.Marshal(srr.Findings)
		envRefsJSON, _ := json.Marshal(srr.EnvReferences)
		_, _ = tx.ExecContext(ctx, `
			INSERT INTO source_risk_reports(
				id, project_id, application_id, repository_id, resolved_commit_sha,
				application_root, scanner_version, build_job_id, analysis_status,
				files_scanned, bytes_scanned, truncated, findings, env_references,
				report_hash, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
			ON CONFLICT (project_id, application_id, repository_id, resolved_commit_sha, application_root, scanner_version)
			DO UPDATE SET
				build_job_id = EXCLUDED.build_job_id,
				analysis_status = EXCLUDED.analysis_status,
				files_scanned = EXCLUDED.files_scanned,
				bytes_scanned = EXCLUDED.bytes_scanned,
				truncated = EXCLUDED.truncated,
				findings = EXCLUDED.findings,
				env_references = EXCLUDED.env_references,
				report_hash = EXCLUDED.report_hash
		`, srrID, job.ProjectID, job.ApplicationID, job.Source.RepositoryID, job.Source.ResolvedCommitSHA,
			job.Source.ApplicationRoot, srr.ScannerVersion, job.ID, srr.AnalysisStatus,
			srr.FilesScanned, srr.BytesScanned, srr.Truncated, findingsJSON, envRefsJSON,
			srr.ReportHash, completion.Now)
	}
	if err := tx.Commit(); err != nil {
		return CompletionResult{}, unavailable()
	}
	return CompletionResult{BuildRecordID: record.ID, Digest: result.Digest, BuildJobState: StatusSucceeded}, nil
}

func (s PostgresStore) FailRunner(ctx context.Context, failure RunnerFailure, leaseHash []byte, now time.Time) error {
	if s.DB == nil {
		return unavailable()
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return unavailable()
	}
	defer tx.Rollback()
	attempt, err := scanAttempt(tx.QueryRowContext(ctx, `SELECT `+selectAttemptColumns+` FROM build_executor_attempts WHERE lease_token_hash=$1 FOR UPDATE`, leaseHash))
	if errors.Is(err, sql.ErrNoRows) {
		return Error{Code: "RUNNER_LEASE_INVALID", Status: 401, Message: "Runner lease is invalid.", Cause: "runner_lease"}
	}
	if err != nil {
		return unavailable()
	}
	if attempt.BuildJobID != failure.BuildJobID || attempt.AttemptID != failure.AttemptID {
		return Error{Code: "RUNNER_LEASE_SCOPE_MISMATCH", Status: 403, Message: "Runner lease cannot fail this BuildJob attempt.", Cause: "runner_lease_scope"}
	}
	if attempt.LeaseExpiresAt.IsZero() || !now.Before(attempt.LeaseExpiresAt) {
		return Error{Code: "RUNNER_LEASE_EXPIRED", Status: 401, Message: "Runner lease has expired.", Cause: "runner_lease"}
	}
	job, err := scanJob(tx.QueryRowContext(ctx, selectJobColumns+` WHERE id=$1 FOR UPDATE`, failure.BuildJobID))
	if err != nil {
		return unavailable()
	}
	if attempt.LastState == DispatchStateFailed && job.Status == StatusFailed && job.FailureCode == failure.Code {
		return tx.Commit()
	}
	if attempt.LastState != DispatchStateClaimed || job.Status != StatusRunning {
		return Error{Code: "RUNNER_LEASE_REVOKED", Status: 409, Message: "Runner lease is no longer valid for this BuildJob.", Cause: "build_job_status"}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE build_jobs SET status='failed',failure_code=$2,failure_message_redacted=$3,failure_cause='executor',completed_at=$4,updated_at=$4 WHERE id=$1 AND status='running'`, job.ID, failure.Code, runnerFailureMessage(failure.Code), now); err != nil {
		return unavailable()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE build_executor_attempts SET last_state='failed',failure_code=$2,completed_at=$3,updated_at=$3 WHERE attempt_id=$1 AND last_state='claimed'`, attempt.AttemptID, failure.Code, now); err != nil {
		return unavailable()
	}
	if err := tx.Commit(); err != nil {
		return unavailable()
	}
	return nil
}

type BuildConfigInput struct {
	Commit        string `json:"commit"`
	Strategy      string `json:"strategy"`
	Dockerfile    string `json:"dockerfile,omitempty"`
	Context       string `json:"context"`
	Repository    string `json:"repository"`
	BuildDepState string `json:"build_dep_state,omitempty"`
}

func ComputeBuildConfigHash(commit, strategy, dockerfile, buildContext, repository, buildDepState string) string {
	data, _ := json.Marshal(BuildConfigInput{
		Commit:        commit,
		Strategy:      strategy,
		Dockerfile:    dockerfile,
		Context:       buildContext,
		Repository:    repository,
		BuildDepState: buildDepState,
	})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func runnerBuildRecord(job Job, attempt DispatchAttempt, serviceKey string, result RunnerResult, target PublicationTarget, executor ExecutorConfig, now time.Time) buildrecordv1.Record {
	idHash := sha256.Sum256([]byte(job.ID))
	configHash := ComputeBuildConfigHash(job.Source.ResolvedCommitSHA, job.ResolvedBuildStrategy, job.DockerfilePath, job.Source.BuildContext, target.Repository, job.Source.BuildDependencyState)
	builderVersion := result.Executor.BuildKitVersion + "/buildx-" + result.Executor.BuildxVersion
	if job.ResolvedBuildStrategy == StrategyBuildpack {
		builderVersion = "pack-" + result.Executor.Builder.PackVersion + "/lifecycle-" + result.Executor.Builder.LifecycleVersion
	}
	return buildrecordv1.Record{
		SchemaVersion: buildrecordv1.SchemaVersion, ID: "br-" + hex.EncodeToString(idHash[:16]), ProjectID: job.ProjectID,
		RepositoryID: uint64(job.Source.RepositoryID), RepositoryOwnerID: uint64(job.Source.RepositoryOwnerID), ActiveBindingID: job.Source.BindingID, ServiceID: job.ApplicationID, ServiceKey: serviceKey, CreatedAt: now,
		Workload: buildrecordv1.WorkloadIdentity{Issuer: "https://token.actions.githubusercontent.com", Subject: "repo:" + executor.RepositoryFullName() + ":ref:" + executor.Ref, RepositoryID: uint64(job.Source.RepositoryID), RepositoryOwnerID: uint64(job.Source.RepositoryOwnerID), Ref: executor.Ref, SHA: job.Source.ResolvedCommitSHA, EventName: "workflow_dispatch", Workflow: attempt.Workflow, WorkflowRef: attempt.WorkflowRef, RunID: attempt.RunID, RunAttempt: attempt.RunAttempt},
		Build:    buildrecordv1.BuildMetadata{ConfigHash: configHash, Platform: result.Executor.Platform, OCIRepository: target.Repository, OCIDigest: result.Digest, BuildJobID: job.ID, BuildStrategy: job.ResolvedBuildStrategy, BuilderIdentity: result.Executor.BuilderIdentity, BuilderVersion: builderVersion, Builder: result.Executor.Builder, MediaType: result.Executor.Remote.Descriptor.MediaType, Status: "succeeded"},
	}
}

func buildRecordPayloadHash(record buildrecordv1.Record) (string, error) {
	record.ID = ""
	record.CreatedAt = time.Time{}
	payload, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func runnerFailureMessage(code string) string {
	switch code {
	case "USER_BUILD_FAILED":
		return "Dockerfile build failed."
	case "BUILDPACK_DETECTION_FAILED":
		return "Buildpacks could not detect a deterministic build plan."
	case "BUILDPACK_BUILD_FAILED":
		return "Buildpacks build failed."
	case "BUILDPACK_RUN_IMAGE_UNAVAILABLE":
		return "Pinned Buildpacks run image is unavailable."
	case "BUILDPACK_BUILDER_UNAVAILABLE":
		return "Pinned Buildpacks builder is unavailable."
	case "BUILDPACK_MONOREPO_UNSUPPORTED":
		return "Buildpacks shared monorepo layout is unsupported."
	case "BUILDPACK_RESULT_INVALID":
		return "Buildpacks result metadata is invalid."
	case "REGISTRY_AUTH_FAILED":
		return "Registry authentication failed."
	case "REGISTRY_PUSH_FAILED":
		return "Registry publication failed."
	case "REGISTRY_DIGEST_MISMATCH":
		return "Registry digest verification failed."
	case "REGISTRY_ARTIFACT_NOT_FOUND":
		return "Published registry artifact was not found."
	default:
		return "Build executor infrastructure failed."
	}
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
