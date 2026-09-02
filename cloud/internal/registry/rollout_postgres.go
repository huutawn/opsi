package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
)

type rolloutQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s PostgresService) PreviewExposure(projectID, actorUserID string, request deploymentv1.ExposureMutationRequest) (deploymentv1.ExposurePreview, error) {
	ctx := context.Background()
	base, err := s.GetDeployment(projectID, request.BaseDeploymentJobID)
	if err != nil || base.Snapshot == nil {
		return deploymentv1.ExposurePreview{}, ErrNotFound
	}
	return s.previewExposure(ctx, s.DB, base, request, s.clock())
}

func (s PostgresService) previewExposure(ctx context.Context, queryer rolloutQueryer, base DeploymentJob, request deploymentv1.ExposureMutationRequest, now time.Time) (deploymentv1.ExposurePreview, error) {
	if request.SchemaVersion != deploymentv1.ExposureMutationVersion {
		return deploymentv1.ExposurePreview{}, APIError{Status: 400, Code: "EXPOSURE_REQUEST_INVALID", Message: "unsupported exposure mutation schema"}
	}
	if base.Status != deploymentv1.StateSucceeded && base.Status != deploymentv1.RolloutStateRolledBack {
		return deploymentv1.ExposurePreview{}, APIError{Status: 409, Code: "EXPOSURE_BASE_NOT_READY", Message: "base deployment must have a terminal factual runtime result"}
	}
	desired, err := request.Exposure.Canonicalize()
	if err != nil {
		return deploymentv1.ExposurePreview{}, APIError{Status: 400, Code: "EXPOSURE_SPEC_INVALID", Message: err.Error()}
	}
	if desired.ProjectID != base.ProjectID || desired.EnvironmentID != base.EnvironmentID || desired.RuntimeID != base.RuntimeID || desired.ServiceKey != base.Snapshot.Workload.ServiceKey || desired.ServicePort != base.Snapshot.Workload.ContainerPort {
		return deploymentv1.ExposurePreview{}, APIError{Status: 409, Code: "EXPOSURE_TARGET_MISMATCH", Message: "ExposureSpec does not match the base deployment target"}
	}
	current, err := latestPostgresExposure(ctx, queryer, base.ProjectID, base.EnvironmentID, base.RuntimeID, base.ServiceID)
	if err != nil {
		return deploymentv1.ExposurePreview{}, err
	}
	preview := deploymentv1.ExposurePreview{SchemaVersion: deploymentv1.ExposurePreviewVersion, BaseDeploymentJobID: base.ID, Current: current, Desired: desired, Changes: exposureChanges(current, desired), Eligible: true, DecisionCode: "EXPOSURE_READY", Message: "exposure rollout is ready for Agent ownership preflight", ResolvedAt: now}
	preview.StateHash = hashJSON(map[string]any{"base_deployment_id": base.ID, "base_payload_hash": base.PayloadHash, "current": current, "desired": desired})
	if request.ExpectedStateHash != "" && request.ExpectedStateHash != preview.StateHash {
		return deploymentv1.ExposurePreview{}, APIError{Status: 409, Code: "EXPOSURE_STATE_CONFLICT", Message: "exposure state changed after preview"}
	}
	rows, err := queryer.QueryContext(ctx, `SELECT DISTINCT ON (environment_id,runtime_id,service_id) exposure_spec_json::text, environment_id, runtime_id, service_id FROM deployment_jobs WHERE project_id=$1 AND exposure_spec_json IS NOT NULL AND status NOT IN ($2,$3) AND rollout_state NOT IN ($4,$5,$6,$7) ORDER BY environment_id,runtime_id,service_id,created_at DESC`, base.ProjectID, deploymentv1.StateCancelled, deploymentv1.StateFailed, deploymentv1.RolloutStateFailed, deploymentv1.RolloutStateRolledBack, deploymentv1.RolloutStateRollbackFailed, deploymentv1.RolloutStateCleaned)
	if err != nil {
		return deploymentv1.ExposurePreview{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw, environmentID, runtimeID, serviceID string
		if err := rows.Scan(&raw, &environmentID, &runtimeID, &serviceID); err != nil {
			return deploymentv1.ExposurePreview{}, err
		}
		if environmentID == desired.EnvironmentID && runtimeID == desired.RuntimeID && serviceID == base.ServiceID {
			continue
		}
		var other exposurev1.ExposureSpec
		if json.Unmarshal([]byte(raw), &other) == nil && other.Hostname == desired.Hostname && exposurev1.ManagedPathsConflict(other.Path, desired.Path) {
			preview.Eligible = false
			preview.DecisionCode = "EXPOSURE_ROUTE_CONFLICT"
			preview.Message = "hostname and path overlap another Opsi desired exposure"
			break
		}
	}
	return preview, rows.Err()
}

func (s PostgresService) StartExposureRollout(projectID, actorUserID, key, requestID string, request deploymentv1.ExposureMutationRequest) (DeploymentJob, bool, error) {
	if !validDeploymentIdempotencyKey(key) {
		return DeploymentJob{}, false, APIError{Status: 400, Code: "IDEMPOTENCY_KEY_INVALID", Message: "deployment idempotency key is invalid", RequestID: requestID}
	}
	ctx := context.Background()
	payloadHash := hashJSON(request)
	scope := "exposure-rollout:v1:" + projectID
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DeploymentJob{}, false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, scope+":"+key); err != nil {
		return DeploymentJob{}, false, err
	}
	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT resource_id FROM idempotency_keys WHERE scope=$1 AND key=$2`, scope, key).Scan(&existingID)
	if err == nil {
		job, scanErr := scanDeployment(tx.QueryRowContext(ctx, deploymentSelectSQL+` WHERE id=$1 AND project_id=$2`, existingID, projectID))
		if scanErr != nil {
			return DeploymentJob{}, false, scanErr
		}
		if job.PayloadHash != payloadHash {
			return DeploymentJob{}, false, APIError{Status: 409, Code: "IDEMPOTENCY_CONFLICT", Message: "idempotency key was used with a different exposure payload", RequestID: requestID}
		}
		job.Reused = true
		return job, true, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return DeploymentJob{}, false, err
	}
	base, err := scanDeployment(tx.QueryRowContext(ctx, deploymentSelectSQL+` WHERE id=$1 AND project_id=$2 FOR UPDATE`, request.BaseDeploymentJobID, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return DeploymentJob{}, false, ErrNotFound
	}
	if err != nil {
		return DeploymentJob{}, false, err
	}
	preview, err := s.previewExposure(ctx, tx, base, request, s.clock())
	if err != nil {
		return DeploymentJob{}, false, err
	}
	if !preview.Eligible {
		return DeploymentJob{}, false, APIError{Status: 409, Code: preview.DecisionCode, Message: preview.Message, RequestID: requestID}
	}
	if len(preview.Changes) == 1 && preview.Changes[0] == "unchanged" {
		return DeploymentJob{}, false, APIError{Status: 409, Code: "EXPOSURE_UNCHANGED", Message: "exposure already matches the requested route", RequestID: requestID}
	}
	var collision int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployment_jobs WHERE id=$1`, preview.Desired.DeploymentJobID).Scan(&collision); err != nil {
		return DeploymentJob{}, false, err
	}
	if collision != 0 {
		return DeploymentJob{}, false, APIError{Status: 409, Code: "DEPLOYMENT_ID_CONFLICT", Message: "deployment_job_id already exists", RequestID: requestID}
	}
	previousID, previousHash, previousDigest, err := latestPostgresKnownGood(ctx, tx, projectID, base.EnvironmentID, base.RuntimeID, base.ServiceID, base.NodeID, base.AgentID)
	if err != nil {
		return DeploymentJob{}, false, err
	}
	now := s.clock()
	intent, err := buildRolloutIntent(base, &preview.Desired, previousID, previousHash, previousDigest, "", "", deploymentv1.RolloutOperationApply, now)
	if err != nil {
		return DeploymentJob{}, false, APIError{Status: 400, Code: "ROLLOUT_INTENT_INVALID", Message: err.Error(), RequestID: requestID}
	}
	job := rolloutDeploymentJob(base, intent, &preview.Desired, actorUserID, key, payloadHash, now)
	if err := insertDeployment(ctx, tx, job); err != nil {
		return DeploymentJob{}, false, err
	}
	if err := acquireDeploymentLock(ctx, tx, projectID, job.ServiceID, job.ID, now, requestID); err != nil {
		return DeploymentJob{}, false, err
	}
	if err := insertDeploymentEvent(ctx, tx, rolloutEvent(job, deploymentv1.RolloutStatePrepared, "durable exposure rollout prepared", 0, requestID, now, "")); err != nil {
		return DeploymentJob{}, false, err
	}
	if err := insertIdempotency(ctx, tx, scope, key, "deployment_job", job.ID); err != nil {
		return DeploymentJob{}, false, err
	}
	return job, false, tx.Commit()
}

func latestPostgresExposure(ctx context.Context, queryer rolloutQueryer, projectID, environmentID, runtimeID, serviceID string) (*exposurev1.ExposureSpec, error) {
	var raw string
	err := queryer.QueryRowContext(ctx, `SELECT exposure_spec_json::text FROM deployment_jobs WHERE project_id=$1 AND environment_id=$2 AND runtime_id=$3 AND service_id=$4 AND exposure_spec_json IS NOT NULL AND status=rollout_state AND rollout_state IN ($5,$6) ORDER BY created_at DESC LIMIT 1`, projectID, environmentID, runtimeID, serviceID, deploymentv1.RolloutStateSucceeded, deploymentv1.RolloutStateRolledBack).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var exposure exposurev1.ExposureSpec
	if err := json.Unmarshal([]byte(raw), &exposure); err != nil {
		return nil, err
	}
	return &exposure, nil
}

func latestPostgresKnownGood(ctx context.Context, tx *sql.Tx, projectID, environmentID, runtimeID, serviceID, nodeID, agentID string) (string, string, string, error) {
	rows, err := tx.QueryContext(ctx, deploymentSelectSQL+` WHERE project_id=$1 AND environment_id=$2 AND runtime_id=$3 AND service_id=$4 AND node_id=$5 AND agent_id=$6 AND schema_version=$7 AND mode=$8 AND snapshot_json IS NOT NULL AND snapshot_json <> '{}'::jsonb AND snapshot_json->>'schema_version'=$7 AND snapshot_json->'preview' IS NULL AND rollout_intent_json IS NOT NULL AND rollout_intent_json <> '{}'::jsonb AND rollout_intent_json->>'schema_version'=$9 AND rollout_intent_json->'target'->>'project_id'=project_id AND rollout_intent_json->'target'->>'environment_id'=environment_id AND rollout_intent_json->'target'->>'runtime_id'=runtime_id AND rollout_intent_json->'target'->>'service_key'=snapshot_json->'workload'->>'service_key' AND rollout_intent_json->'target'->>'node_id'=node_id AND rollout_intent_json->'target'->>'agent_id'=agent_id AND terminal_result_json IS NOT NULL AND terminal_result_json <> '{}'::jsonb AND terminal_result_json->>'schema_version'=$10 AND rollout_state IN ($11,$12) AND status=rollout_state AND terminal_result_json->>'rollout_state'=rollout_state AND terminal_result_json->>'status'=status AND terminal_result_json->>'known_good_id'=known_good_id AND terminal_result_json->>'known_good_hash'=known_good_hash AND terminal_result_json->>'current_digest'=current_digest AND terminal_result_json->>'desired_digest'=desired_digest AND COALESCE(terminal_result_json->>'previous_digest','')=COALESCE(previous_digest,'') ORDER BY updated_at DESC, id DESC LIMIT 100`, projectID, environmentID, runtimeID, serviceID, nodeID, agentID, deploymentv1.JobSchemaVersion, "rollout", deploymentv1.RolloutSchemaVersion, deploymentv1.ResultSchemaVersion, deploymentv1.RolloutStateSucceeded, deploymentv1.RolloutStateRolledBack)
	if err != nil {
		return "", "", "", err
	}
	defer rows.Close()
	for rows.Next() {
		job, err := scanDeployment(rows)
		if err != nil {
			return "", "", "", err
		}
		if validKnownGoodCandidate(job) {
			return job.KnownGoodID, job.KnownGoodHash, job.CurrentDigest, nil
		}
	}
	return "", "", "", rows.Err()
}
