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

type rowScanner interface{ Scan(...any) error }
type queryExecutor interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
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
