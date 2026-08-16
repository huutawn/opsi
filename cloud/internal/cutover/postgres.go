package cutover

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	cutoverv1 "github.com/opsi-dev/opsi/contracts/go/cutoverv1"
)

type PostgresStore struct{ DB *sql.DB }

func (s PostgresStore) CreateReview(ctx context.Context, v cutoverv1.ApplicationCutoverReview, key, payload string) (cutoverv1.ApplicationCutoverReview, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return cutoverv1.ApplicationCutoverReview{}, false, err
	}
	defer tx.Rollback()

	if key != "" {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, v.ProjectID+"\x1f"+key); err != nil {
			return cutoverv1.ApplicationCutoverReview{}, false, err
		}
		var id, oldPayload string
		err = tx.QueryRowContext(ctx, `SELECT review_id, payload_hash FROM application_cutover_review_idempotency WHERE project_id=$1 AND idempotency_key=$2`, v.ProjectID, key).Scan(&id, &oldPayload)
		if err == nil {
			if oldPayload != payload {
				return cutoverv1.ApplicationCutoverReview{}, false, invalid(cutoverv1.FailureIdempotencyConflict, "idempotency key was used with another review request")
			}
			loaded, getErr := getReview(ctx, tx, v.ProjectID, id)
			return loaded, true, getErr
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return cutoverv1.ApplicationCutoverReview{}, false, err
		}
	}

	authority, _ := json.Marshal(v)
	_, err = tx.ExecContext(ctx, `INSERT INTO application_cutover_reviews(id,project_id,environment_id,application_id,source_binding_id,source_resource_id,target_resource_id,target_binding_id,target_node_id,lifecycle,authority,requested_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12)`, v.ID, v.ProjectID, v.EnvironmentID, v.ApplicationID, v.SourceBindingID, v.SourceResourceID, v.TargetResourceID, v.TargetBindingID, v.TargetNodeID, v.Lifecycle, authority, v.RequestedAt)
	if err != nil {
		return cutoverv1.ApplicationCutoverReview{}, false, err
	}

	if key != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO application_cutover_review_idempotency(project_id,idempotency_key,payload_hash,review_id,created_at) VALUES($1,$2,$3,$4,$5)`, v.ProjectID, key, payload, v.ID, v.RequestedAt); err != nil {
			return cutoverv1.ApplicationCutoverReview{}, false, err
		}
	}
	return v, false, tx.Commit()
}

func (s PostgresStore) GetReview(ctx context.Context, projectID, id string) (cutoverv1.ApplicationCutoverReview, error) {
	return getReview(ctx, s.DB, projectID, id)
}

func (s PostgresStore) ListReviews(ctx context.Context, projectID, applicationID string) ([]cutoverv1.ApplicationCutoverReview, error) {
	query, args := `SELECT authority,lifecycle,attempt_count,lease_token,lease_expires_at FROM application_cutover_reviews WHERE project_id=$1`, []any{projectID}
	if applicationID != "" {
		query += ` AND application_id=$2`
		args = append(args, applicationID)
	}
	query += ` ORDER BY requested_at,id`
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []cutoverv1.ApplicationCutoverReview{}
	for rows.Next() {
		v, scanErr := scanReview(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s PostgresStore) ClaimReview(ctx context.Context, projectID, nodeID, token string, now, expires time.Time) (cutoverv1.ApplicationCutoverReview, bool, error) {
	query := `WITH candidate AS (
		SELECT id FROM application_cutover_reviews
		WHERE project_id=$1`
	args := []any{projectID}
	n := 2
	if nodeID != "" {
		query += ` AND (target_node_id=$` + strconv.Itoa(n) + ` OR target_node_id='')`
		args = append(args, nodeID)
		n++
	}
	query += ` AND lifecycle IN ('queued','leased')
		AND (lifecycle='queued' OR lease_expires_at<=$` + strconv.Itoa(n) + `)
		ORDER BY requested_at,id FOR UPDATE SKIP LOCKED LIMIT 1
	) UPDATE application_cutover_reviews cr SET lifecycle='leased',lease_token=$` + strconv.Itoa(n+1) + `,lease_expires_at=$` + strconv.Itoa(n+2) + `,attempt_count=cr.attempt_count+1
	FROM candidate c WHERE cr.id=c.id
	RETURNING cr.authority,cr.lifecycle,cr.attempt_count,cr.lease_token,cr.lease_expires_at`
	args = append(args, now, token, expires)

	row := s.DB.QueryRowContext(ctx, query, args...)
	v, err := scanReview(row)
	if errors.Is(err, sql.ErrNoRows) {
		return cutoverv1.ApplicationCutoverReview{}, false, nil
	}
	return v, err == nil, err
}

func (s PostgresStore) UpdateReviewClaimed(ctx context.Context, v cutoverv1.ApplicationCutoverReview, token string) (cutoverv1.ApplicationCutoverReview, error) {
	terminal := v.Lifecycle == cutoverv1.ReviewSucceeded || v.Lifecycle == cutoverv1.ReviewFailed
	authority, _ := json.Marshal(v)
	row := s.DB.QueryRowContext(ctx, `UPDATE application_cutover_reviews
		SET lifecycle=$1, authority=$2::jsonb,
		    reviewed_at=CASE WHEN $3 THEN $4::timestamptz ELSE reviewed_at END,
		    lease_token=CASE WHEN $3 THEN NULL ELSE lease_token END,
		    lease_expires_at=CASE WHEN $3 THEN NULL::timestamptz ELSE $5 END
		WHERE project_id=$6 AND id=$7 AND (lease_token=$8 OR $8='')
		RETURNING authority,lifecycle,attempt_count,lease_token,lease_expires_at`,
		v.Lifecycle, authority, terminal, v.ReviewedAt, v.LeaseExpiresAt, v.ProjectID, v.ID, token)
	updated, err := scanReview(row)
	if errors.Is(err, sql.ErrNoRows) {
		return cutoverv1.ApplicationCutoverReview{}, invalid(cutoverv1.FailureLeaseLost, "cutover review lease is invalid")
	}
	return updated, err
}

func (s PostgresStore) HasActive(ctx context.Context, projectID, applicationID string) (bool, error) {
	var exists bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM application_cutover_reviews WHERE project_id=$1 AND application_id=$2 AND lifecycle IN ('queued','leased'))`, projectID, applicationID).Scan(&exists)
	return exists, err
}

func (s PostgresStore) CreateCutover(ctx context.Context, v cutoverv1.ApplicationCutover, key, payload string) (cutoverv1.ApplicationCutover, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return cutoverv1.ApplicationCutover{}, false, err
	}
	defer tx.Rollback()

	if key != "" {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, v.ProjectID+"\x1f"+key); err != nil {
			return cutoverv1.ApplicationCutover{}, false, err
		}
		var id, oldPayload string
		err = tx.QueryRowContext(ctx, `SELECT cutover_id, payload_hash FROM application_cutover_idempotency WHERE project_id=$1 AND idempotency_key=$2`, v.ProjectID, key).Scan(&id, &oldPayload)
		if err == nil {
			if oldPayload != payload {
				return cutoverv1.ApplicationCutover{}, false, invalid(cutoverv1.FailureIdempotencyConflict, "idempotency key was used with another cutover request")
			}
			loaded, getErr := getCutover(ctx, tx, v.ProjectID, id)
			return loaded, true, getErr
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return cutoverv1.ApplicationCutover{}, false, err
		}
	}

	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM application_cutovers WHERE project_id=$1 AND application_id=$2 AND lifecycle IN ('queued','validating','applying','deploying','verifying'))`, v.ProjectID, v.ApplicationID).Scan(&active); err != nil {
		return cutoverv1.ApplicationCutover{}, false, err
	}
	if active {
		return cutoverv1.ApplicationCutover{}, false, conflict(cutoverv1.FailureCutoverAlreadyRunning, "an active cutover is already running for this application")
	}

	authority, _ := json.Marshal(v)
	_, err = tx.ExecContext(ctx, `INSERT INTO application_cutovers(
		id,project_id,environment_id,application_id,cutover_review_id,
		source_binding_id,source_resource_id,target_resource_id,target_binding_id,target_node_id,
		deployment_job_id,lifecycle,authority,requested_by,requested_at,applied_at,completed_at,updated_at,
		failure_code,failure_message
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb,$14,$15,$16,$17,$18,$19,$20)`,
		v.ID, v.ProjectID, v.EnvironmentID, v.ApplicationID, v.CutoverReviewID,
		v.SourceBindingID, v.SourceResourceID, v.TargetResourceID, v.TargetBindingID, v.TargetNodeID,
		v.DeploymentJobID, v.Lifecycle, authority, v.RequestedBy, v.RequestedAt, v.AppliedAt, v.CompletedAt, v.UpdatedAt,
		v.FailureCode, v.FailureMessageRedacted)
	if err != nil {
		return cutoverv1.ApplicationCutover{}, false, err
	}

	if key != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO application_cutover_idempotency(project_id,idempotency_key,payload_hash,cutover_id,created_at) VALUES($1,$2,$3,$4,$5)`, v.ProjectID, key, payload, v.ID, v.RequestedAt); err != nil {
			return cutoverv1.ApplicationCutover{}, false, err
		}
	}
	return v, false, tx.Commit()
}

func (s PostgresStore) GetCutover(ctx context.Context, projectID, id string) (cutoverv1.ApplicationCutover, error) {
	return getCutover(ctx, s.DB, projectID, id)
}

func (s PostgresStore) ListCutovers(ctx context.Context, projectID, applicationID string) ([]cutoverv1.ApplicationCutover, error) {
	query, args := `SELECT authority,lifecycle,deployment_job_id,applied_at,completed_at,updated_at,failure_code,failure_message FROM application_cutovers WHERE project_id=$1`, []any{projectID}
	if applicationID != "" {
		query += ` AND application_id=$2`
		args = append(args, applicationID)
	}
	query += ` ORDER BY requested_at,id`
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []cutoverv1.ApplicationCutover{}
	for rows.Next() {
		v, scanErr := scanCutover(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s PostgresStore) UpdateCutover(ctx context.Context, v cutoverv1.ApplicationCutover) (cutoverv1.ApplicationCutover, error) {
	authority, _ := json.Marshal(v)
	row := s.DB.QueryRowContext(ctx, `UPDATE application_cutovers
		SET lifecycle=$1, authority=$2::jsonb,
		    applied_at=$3, completed_at=$4, updated_at=now(),
		    deployment_job_id=$5, failure_code=$6, failure_message=$7
		WHERE project_id=$8 AND id=$9
		RETURNING authority,lifecycle,deployment_job_id,applied_at,completed_at,updated_at,failure_code,failure_message`,
		v.Lifecycle, authority, v.AppliedAt, v.CompletedAt, v.DeploymentJobID, v.FailureCode, v.FailureMessageRedacted, v.ProjectID, v.ID)
	updated, err := scanCutover(row)
	if errors.Is(err, sql.ErrNoRows) {
		return cutoverv1.ApplicationCutover{}, ErrNotFound
	}
	return updated, err
}

func (s PostgresStore) HasActiveCutover(ctx context.Context, projectID, applicationID string) (bool, error) {
	var exists bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM application_cutovers WHERE project_id=$1 AND application_id=$2 AND lifecycle IN ('queued','validating','applying','deploying','verifying'))`, projectID, applicationID).Scan(&exists)
	return exists, err
}

type rowScanner interface{ Scan(...any) error }
type queryExecutor interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s PostgresStore) CreateRollback(ctx context.Context, v cutoverv1.ApplicationCutoverRollback, key, payload string) (cutoverv1.ApplicationCutoverRollback, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return cutoverv1.ApplicationCutoverRollback{}, false, err
	}
	defer tx.Rollback()

	if key != "" {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, v.ProjectID+"\x1f"+key); err != nil {
			return cutoverv1.ApplicationCutoverRollback{}, false, err
		}
		var id, oldPayload string
		err = tx.QueryRowContext(ctx, `SELECT rollback_id, payload_hash FROM application_cutover_rollback_idempotency WHERE project_id=$1 AND idempotency_key=$2`, v.ProjectID, key).Scan(&id, &oldPayload)
		if err == nil {
			if oldPayload != payload {
				return cutoverv1.ApplicationCutoverRollback{}, false, invalid(cutoverv1.FailureIdempotencyConflict, "idempotency key was used with another rollback request")
			}
			loaded, getErr := getRollback(ctx, tx, v.ProjectID, id)
			return loaded, true, getErr
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return cutoverv1.ApplicationCutoverRollback{}, false, err
		}
	}

	var activeCount int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM application_cutover_rollbacks WHERE project_id=$1 AND application_id=$2 AND lifecycle IN ('queued','validating','applying','deploying','verifying')`, v.ProjectID, v.ApplicationID).Scan(&activeCount)
	if err != nil {
		return cutoverv1.ApplicationCutoverRollback{}, false, err
	}
	if activeCount > 0 {
		return cutoverv1.ApplicationCutoverRollback{}, false, conflict(cutoverv1.FailureRollbackAlreadyRunning, "another cutover rollback is already active for this application")
	}

	authority, _ := json.Marshal(v)
	_, err = tx.ExecContext(ctx, `INSERT INTO application_cutover_rollbacks(
		id,project_id,environment_id,application_id,cutover_id,
		source_binding_id,source_resource_id,target_resource_id,target_binding_id,target_node_id,
		deployment_job_id,lifecycle,authority,requested_by,requested_at,applied_at,completed_at,updated_at,failure_code,failure_message)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb,$14,$15,$16,$17,now(),$18,$19)`,
		v.ID, v.ProjectID, v.EnvironmentID, v.ApplicationID, v.CutoverID,
		v.SourceBindingID, v.SourceResourceID, v.TargetResourceID, v.TargetBindingID, v.TargetNodeID,
		v.DeploymentJobID, v.Lifecycle, authority, v.RequestedBy, v.RequestedAt, v.AppliedAt, v.CompletedAt, v.FailureCode, v.FailureMessageRedacted)
	if err != nil {
		return cutoverv1.ApplicationCutoverRollback{}, false, err
	}

	if key != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO application_cutover_rollback_idempotency(project_id,idempotency_key,payload_hash,rollback_id,created_at) VALUES($1,$2,$3,$4,$5)`, v.ProjectID, key, payload, v.ID, v.RequestedAt); err != nil {
			return cutoverv1.ApplicationCutoverRollback{}, false, err
		}
	}
	return v, false, tx.Commit()
}

func (s PostgresStore) GetRollback(ctx context.Context, projectID, id string) (cutoverv1.ApplicationCutoverRollback, error) {
	return getRollback(ctx, s.DB, projectID, id)
}

func (s PostgresStore) ListRollbacks(ctx context.Context, projectID, applicationID string) ([]cutoverv1.ApplicationCutoverRollback, error) {
	query, args := `SELECT authority,lifecycle,deployment_job_id,applied_at,completed_at,updated_at,failure_code,failure_message FROM application_cutover_rollbacks WHERE project_id=$1`, []any{projectID}
	if applicationID != "" {
		query += ` AND application_id=$2`
		args = append(args, applicationID)
	}
	query += ` ORDER BY requested_at,id`
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []cutoverv1.ApplicationCutoverRollback{}
	for rows.Next() {
		v, scanErr := scanRollback(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s PostgresStore) UpdateRollback(ctx context.Context, v cutoverv1.ApplicationCutoverRollback) (cutoverv1.ApplicationCutoverRollback, error) {
	authority, _ := json.Marshal(v)
	row := s.DB.QueryRowContext(ctx, `UPDATE application_cutover_rollbacks
		SET lifecycle=$1, authority=$2::jsonb,
		    applied_at=$3, completed_at=$4, updated_at=now(),
		    deployment_job_id=$5, failure_code=$6, failure_message=$7
		WHERE project_id=$8 AND id=$9
		RETURNING authority,lifecycle,deployment_job_id,applied_at,completed_at,updated_at,failure_code,failure_message`,
		v.Lifecycle, authority, v.AppliedAt, v.CompletedAt, v.DeploymentJobID, v.FailureCode, v.FailureMessageRedacted, v.ProjectID, v.ID)
	updated, err := scanRollback(row)
	if errors.Is(err, sql.ErrNoRows) {
		return cutoverv1.ApplicationCutoverRollback{}, ErrNotFound
	}
	return updated, err
}

func (s PostgresStore) HasActiveRollback(ctx context.Context, projectID, applicationID string) (bool, error) {
	var exists bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM application_cutover_rollbacks WHERE project_id=$1 AND application_id=$2 AND lifecycle IN ('queued','validating','applying','deploying','verifying'))`, projectID, applicationID).Scan(&exists)
	return exists, err
}

func getReview(ctx context.Context, db queryExecutor, projectID, id string) (cutoverv1.ApplicationCutoverReview, error) {
	v, err := scanReview(db.QueryRowContext(ctx, `SELECT authority,lifecycle,attempt_count,lease_token,lease_expires_at FROM application_cutover_reviews WHERE project_id=$1 AND id=$2`, projectID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return cutoverv1.ApplicationCutoverReview{}, ErrNotFound
	}
	return v, err
}

func scanReview(row rowScanner) (cutoverv1.ApplicationCutoverReview, error) {
	var raw []byte
	var lifecycle string
	var attempts int
	var token sql.NullString
	var expires sql.NullTime
	if err := row.Scan(&raw, &lifecycle, &attempts, &token, &expires); err != nil {
		return cutoverv1.ApplicationCutoverReview{}, err
	}
	var v cutoverv1.ApplicationCutoverReview
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, err
	}
	v.Lifecycle = lifecycle
	v.AttemptCount = attempts
	v.LeaseToken = token.String
	if expires.Valid {
		v.LeaseExpiresAt = expires.Time
	}
	return v, nil
}

func getCutover(ctx context.Context, db queryExecutor, projectID, id string) (cutoverv1.ApplicationCutover, error) {
	v, err := scanCutover(db.QueryRowContext(ctx, `SELECT authority,lifecycle,deployment_job_id,applied_at,completed_at,updated_at,failure_code,failure_message FROM application_cutovers WHERE project_id=$1 AND id=$2`, projectID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return cutoverv1.ApplicationCutover{}, ErrNotFound
	}
	return v, err
}

func scanCutover(row rowScanner) (cutoverv1.ApplicationCutover, error) {
	var raw []byte
	var lifecycle string
	var deploymentJobID sql.NullString
	var appliedAt sql.NullTime
	var completedAt sql.NullTime
	var updatedAt time.Time
	var failureCode sql.NullString
	var failureMessage sql.NullString
	if err := row.Scan(&raw, &lifecycle, &deploymentJobID, &appliedAt, &completedAt, &updatedAt, &failureCode, &failureMessage); err != nil {
		return cutoverv1.ApplicationCutover{}, err
	}
	var v cutoverv1.ApplicationCutover
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, err
	}
	v.Lifecycle = lifecycle
	v.DeploymentJobID = deploymentJobID.String
	if appliedAt.Valid {
		v.AppliedAt = &appliedAt.Time
	}
	if completedAt.Valid {
		v.CompletedAt = &completedAt.Time
	}
	v.UpdatedAt = updatedAt
	v.FailureCode = failureCode.String
	v.FailureMessageRedacted = failureMessage.String
	return v, nil
}

func getRollback(ctx context.Context, db queryExecutor, projectID, id string) (cutoverv1.ApplicationCutoverRollback, error) {
	v, err := scanRollback(db.QueryRowContext(ctx, `SELECT authority,lifecycle,deployment_job_id,applied_at,completed_at,updated_at,failure_code,failure_message FROM application_cutover_rollbacks WHERE project_id=$1 AND id=$2`, projectID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return cutoverv1.ApplicationCutoverRollback{}, ErrNotFound
	}
	return v, err
}

func scanRollback(row rowScanner) (cutoverv1.ApplicationCutoverRollback, error) {
	var raw []byte
	var lifecycle string
	var deploymentJobID sql.NullString
	var appliedAt sql.NullTime
	var completedAt sql.NullTime
	var updatedAt time.Time
	var failureCode sql.NullString
	var failureMessage sql.NullString
	if err := row.Scan(&raw, &lifecycle, &deploymentJobID, &appliedAt, &completedAt, &updatedAt, &failureCode, &failureMessage); err != nil {
		return cutoverv1.ApplicationCutoverRollback{}, err
	}
	var v cutoverv1.ApplicationCutoverRollback
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, err
	}
	v.Lifecycle = lifecycle
	v.DeploymentJobID = deploymentJobID.String
	if appliedAt.Valid {
		v.AppliedAt = &appliedAt.Time
	}
	if completedAt.Valid {
		v.CompletedAt = &completedAt.Time
	}
	v.UpdatedAt = updatedAt
	v.FailureCode = failureCode.String
	v.FailureMessageRedacted = failureMessage.String
	return v, nil
}
