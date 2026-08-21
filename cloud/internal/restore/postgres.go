package restore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	restorev1 "github.com/opsi-dev/opsi/contracts/go/restorev1"
)

type PostgresStore struct{ DB *sql.DB }

func (s PostgresStore) CreateReview(ctx context.Context, v restorev1.Review) (restorev1.Review, error) {
	authority, _ := json.Marshal(v)
	_, err := s.DB.ExecContext(ctx, `INSERT INTO restore_reviews(id,project_id,environment_id,backup_id,target_resource_id,target_node_id,lifecycle,authority,requested_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9)`, v.ID, v.ProjectID, v.EnvironmentID, v.BackupID, v.TargetResourceID, v.TargetNodeID, v.Lifecycle, authority, v.RequestedAt)
	return v, err
}
func (s PostgresStore) GetReview(ctx context.Context, projectID, id string) (restorev1.Review, error) {
	v, err := scanReview(s.DB.QueryRowContext(ctx, `SELECT authority,lifecycle,attempt_count,lease_token,lease_expires_at FROM restore_reviews WHERE project_id=$1 AND id=$2`, projectID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return restorev1.Review{}, ErrNotFound
	}
	return v, err
}
func (s PostgresStore) ClaimReview(ctx context.Context, projectID, nodeID, token string, now, expires time.Time) (restorev1.Review, bool, error) {
	row := s.DB.QueryRowContext(ctx, `WITH candidate AS (SELECT id FROM restore_reviews WHERE project_id=$1 AND target_node_id=$2 AND lifecycle IN ('queued','leased') AND (lifecycle='queued' OR lease_expires_at<=$3) ORDER BY requested_at,id FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE restore_reviews rr SET lifecycle='leased',lease_token=$4,lease_expires_at=$5,attempt_count=rr.attempt_count+1 FROM candidate c WHERE rr.id=c.id RETURNING rr.authority,rr.lifecycle,rr.attempt_count,rr.lease_token,rr.lease_expires_at`, projectID, nodeID, now, token, expires)
	v, err := scanReview(row)
	if errors.Is(err, sql.ErrNoRows) {
		return restorev1.Review{}, false, nil
	}
	return v, err == nil, err
}
func (s PostgresStore) UpdateReviewClaimed(ctx context.Context, v restorev1.Review, token string) (restorev1.Review, error) {
	terminal := v.Lifecycle == restorev1.ReviewSucceeded || v.Lifecycle == restorev1.ReviewFailed
	authority, _ := json.Marshal(v)
	row := s.DB.QueryRowContext(ctx, `UPDATE restore_reviews SET lifecycle=$1,authority=$2::jsonb,lease_token=CASE WHEN $3 THEN NULL ELSE lease_token END,lease_expires_at=CASE WHEN $3 THEN NULL::timestamptz ELSE $4 END WHERE project_id=$5 AND id=$6 AND lease_token=$7 RETURNING authority,lifecycle,attempt_count,lease_token,lease_expires_at`, v.Lifecycle, authority, terminal, v.LeaseExpiresAt, v.ProjectID, v.ID, token)
	updated, err := scanReview(row)
	if errors.Is(err, sql.ErrNoRows) {
		return restorev1.Review{}, invalid(restorev1.FailureLeaseLost, "restore review lease is invalid")
	}
	return updated, err
}

func (s PostgresStore) Create(ctx context.Context, v restorev1.Restore, key, payload string) (restorev1.Restore, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return restorev1.Restore{}, false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, v.ProjectID+"\x1f"+key); err != nil {
		return restorev1.Restore{}, false, err
	}
	var id, oldPayload string
	err = tx.QueryRowContext(ctx, `SELECT restore_id,payload_hash FROM restore_idempotency WHERE project_id=$1 AND idempotency_key=$2`, v.ProjectID, key).Scan(&id, &oldPayload)
	if err == nil {
		if oldPayload != payload {
			return restorev1.Restore{}, false, invalid("RESTORE_IDEMPOTENCY_CONFLICT", "idempotency key was used with another restore request")
		}
		loaded, getErr := getRestore(ctx, tx, v.ProjectID, id)
		return loaded, true, getErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return restorev1.Restore{}, false, err
	}
	authority, _ := json.Marshal(v)
	_, err = tx.ExecContext(ctx, `INSERT INTO restores(id,project_id,environment_id,backup_id,target_resource_id,target_node_id,lifecycle,authority,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9)`, v.ID, v.ProjectID, v.EnvironmentID, v.BackupID, v.TargetResourceID, v.TargetNodeID, v.Lifecycle, authority, v.CreatedAt)
	if err != nil {
		if constraint(err) == "restores_one_active_per_target_uidx" {
			return restorev1.Restore{}, false, invalid(restorev1.FailureAlreadyRunning, "a restore is already active for this target")
		}
		return restorev1.Restore{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO restore_idempotency(project_id,idempotency_key,payload_hash,restore_id,created_at) VALUES($1,$2,$3,$4,$5)`, v.ProjectID, key, payload, v.ID, v.CreatedAt); err != nil {
		return restorev1.Restore{}, false, err
	}
	return v, false, tx.Commit()
}
func (s PostgresStore) Get(ctx context.Context, projectID, id string) (restorev1.Restore, error) {
	return getRestore(ctx, s.DB, projectID, id)
}
func (s PostgresStore) List(ctx context.Context, projectID, backupID, targetID string) ([]restorev1.Restore, error) {
	query, args := `SELECT authority,lifecycle,attempt_count,lease_token,lease_expires_at FROM restores WHERE project_id=$1`, []any{projectID}
	n := 2
	if backupID != "" {
		query += ` AND backup_id=$` + strconv.Itoa(n)
		args = append(args, backupID)
		n++
	}
	if targetID != "" {
		query += ` AND target_resource_id=$` + strconv.Itoa(n)
		args = append(args, targetID)
	}
	query += ` ORDER BY created_at,id`
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []restorev1.Restore{}
	for rows.Next() {
		v, err := scanRestore(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s PostgresStore) Claim(ctx context.Context, projectID, nodeID, token string, now, expires time.Time) (restorev1.Restore, bool, error) {
	row := s.DB.QueryRowContext(ctx, `WITH candidate AS (SELECT id FROM restores WHERE project_id=$1 AND target_node_id=$2 AND lifecycle IN ('queued','leased','running','verifying') AND (lifecycle='queued' OR lease_expires_at<=$3) ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE restores rs SET lifecycle='leased',lease_token=$4,lease_expires_at=$5,attempt_count=rs.attempt_count+1 FROM candidate c WHERE rs.id=c.id RETURNING rs.authority,rs.lifecycle,rs.attempt_count,rs.lease_token,rs.lease_expires_at`, projectID, nodeID, now, token, expires)
	v, err := scanRestore(row)
	if errors.Is(err, sql.ErrNoRows) {
		return restorev1.Restore{}, false, nil
	}
	return v, err == nil, err
}
func (s PostgresStore) UpdateClaimed(ctx context.Context, v restorev1.Restore, token string) (restorev1.Restore, error) {
	terminal := v.Lifecycle == restorev1.LifecycleSucceeded || v.Lifecycle == restorev1.LifecycleFailed
	authority, _ := json.Marshal(v)
	row := s.DB.QueryRowContext(ctx, `UPDATE restores SET lifecycle=$1,authority=$2::jsonb,lease_token=CASE WHEN $3 THEN NULL ELSE lease_token END,lease_expires_at=CASE WHEN $3 THEN NULL::timestamptz ELSE $4 END WHERE project_id=$5 AND id=$6 AND lease_token=$7 AND lifecycle<>'succeeded' RETURNING authority,lifecycle,attempt_count,lease_token,lease_expires_at`, v.Lifecycle, authority, terminal, v.LeaseExpiresAt, v.ProjectID, v.ID, token)
	updated, err := scanRestore(row)
	if errors.Is(err, sql.ErrNoRows) {
		return restorev1.Restore{}, invalid(restorev1.FailureLeaseLost, "restore lease is invalid")
	}
	return updated, err
}
func (s PostgresStore) HasActive(ctx context.Context, projectID, targetID string) (bool, error) {
	var exists bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM restores WHERE project_id=$1 AND target_resource_id=$2 AND lifecycle IN ('queued','leased','running','verifying'))`, projectID, targetID).Scan(&exists)
	return exists, err
}

type rowScanner interface{ Scan(...any) error }

func scanReview(row rowScanner) (restorev1.Review, error) {
	var raw []byte
	var lifecycle string
	var attempts int
	var token sql.NullString
	var expires sql.NullTime
	if err := row.Scan(&raw, &lifecycle, &attempts, &token, &expires); err != nil {
		return restorev1.Review{}, err
	}
	var v restorev1.Review
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, err
	}
	v.Lifecycle, v.AttemptCount, v.LeaseToken = lifecycle, attempts, token.String
	if expires.Valid {
		v.LeaseExpiresAt = expires.Time
	}
	return v, nil
}
func scanRestore(row rowScanner) (restorev1.Restore, error) {
	var raw []byte
	var lifecycle string
	var attempts int
	var token sql.NullString
	var expires sql.NullTime
	if err := row.Scan(&raw, &lifecycle, &attempts, &token, &expires); err != nil {
		return restorev1.Restore{}, err
	}
	var v restorev1.Restore
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, err
	}
	v.Lifecycle, v.AttemptCount, v.LeaseToken = lifecycle, attempts, token.String
	if expires.Valid {
		v.LeaseExpiresAt = expires.Time
	}
	return v, nil
}
func getRestore(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, projectID, id string) (restorev1.Restore, error) {
	v, err := scanRestore(q.QueryRowContext(ctx, `SELECT authority,lifecycle,attempt_count,lease_token,lease_expires_at FROM restores WHERE project_id=$1 AND id=$2`, projectID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return restorev1.Restore{}, ErrNotFound
	}
	return v, err
}
func constraint(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}
