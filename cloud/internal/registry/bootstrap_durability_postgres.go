package registry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

func (s PostgresService) RenewBootstrapLease(projectID, sessionID, workerID, rawLeaseToken string, now time.Time, leaseDuration time.Duration) (BootstrapSession, error) {
	if leaseDuration <= 0 {
		return BootstrapSession{}, errors.New("bootstrap lease duration must be positive")
	}
	now = now.UTC()
	expiresAt := now.Add(leaseDuration)
	sum := sha256.Sum256([]byte(rawLeaseToken))
	tokenHash := hex.EncodeToString(sum[:])
	ctx := context.Background()
	updated, err := scanBootstrapSession(s.DB.QueryRowContext(ctx, `WITH renewed AS (
		UPDATE bootstrap_sessions
		SET lease_heartbeat_at=$1, lease_expires_at=$2, updated_at=$1
		WHERE project_id=$3 AND id=$4 AND lease_owner=$5 AND lease_token_hash=$6
		  AND lease_expires_at > $1
		  AND expires_at > $1
		  AND status IN ('preflight','validating','connecting','installing','installing_k3s','installing_agent','registering_agent','waiting_agent','verifying_agent','verifying')
		RETURNING *
	) SELECT id, org_id, project_id, environment_id, runtime_id, COALESCE(node_id,''), COALESCE(created_by,''), role, status, idempotency_key, COALESCE(public_host,''), COALESCE(ssh_port,0), COALESCE(ssh_username,''), COALESCE(auth_method,''), expires_at, started_at, finished_at, COALESCE(lease_owner,''), COALESCE(lease_token_hash,''), lease_expires_at, leased_at, COALESCE(attempt_count,0), COALESCE(max_attempts,3), next_attempt_at, lease_heartbeat_at, COALESCE(last_failure_code,''), COALESCE(last_failure_message_redacted,''), dead_lettered_at, created_at, updated_at FROM renewed`, now, expiresAt, projectID, sessionID, workerID, tokenHash))
	if err == nil {
		return updated, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return BootstrapSession{}, err
	}
	session, getErr := scanBootstrapSession(s.DB.QueryRowContext(ctx, bootstrapSelectSQL+` WHERE project_id=$1 AND id=$2`, projectID, sessionID))
	if errors.Is(getErr, sql.ErrNoRows) {
		return BootstrapSession{}, ErrNotFound
	}
	if getErr != nil {
		return BootstrapSession{}, getErr
	}
	if validateErr := validateBootstrapLease(session, workerID, rawLeaseToken, now); validateErr != nil {
		return BootstrapSession{}, validateErr
	}
	return BootstrapSession{}, APIError{Status: 409, Code: "BOOTSTRAP_LEASE_INACTIVE", Message: "bootstrap lease could not be renewed"}
}

func (s PostgresService) RecoverExpiredBootstrapLeases(now time.Time) (BootstrapRecoverySummary, error) {
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return BootstrapRecoverySummary{}, err
	}
	defer tx.Rollback()
	summary, err := recoverExpiredBootstrapLeasesPostgres(ctx, tx, now.UTC())
	if err != nil {
		return BootstrapRecoverySummary{}, err
	}
	return summary, tx.Commit()
}

func recoverExpiredBootstrapLeasesPostgres(ctx context.Context, tx *sql.Tx, now time.Time) (BootstrapRecoverySummary, error) {
	rows, err := tx.QueryContext(ctx, bootstrapSelectSQL+` WHERE lease_expires_at <= $1 AND status IN ('preflight','validating','connecting','installing','installing_k3s','installing_agent','registering_agent','waiting_agent','verifying_agent','verifying') FOR UPDATE SKIP LOCKED`, now)
	if err != nil {
		return BootstrapRecoverySummary{}, err
	}
	var expired []BootstrapSession
	for rows.Next() {
		session, scanErr := scanBootstrapSession(rows)
		if scanErr != nil {
			rows.Close()
			return BootstrapRecoverySummary{}, scanErr
		}
		expired = append(expired, session)
	}
	if err := rows.Close(); err != nil {
		return BootstrapRecoverySummary{}, err
	}
	var summary BootstrapRecoverySummary
	for _, session := range expired {
		status := BootstrapRetryWait
		var nextAttemptAt, deadLetteredAt, finishedAt any
		failureMessage := "bootstrap worker lease expired"
		level, step, eventMessage := "warn", "BOOTSTRAP_RETRY_SCHEDULED", failureMessage
		auditAction, auditResult := "BOOTSTRAP_RETRY_SCHEDULED", "success"
		if !now.Before(session.ExpiresAt) {
			status, finishedAt = "expired", now
			level, step, eventMessage = "warn", "BOOTSTRAP_LEASE_EXPIRED", "bootstrap session expired after worker lease loss"
			auditAction, auditResult = "BOOTSTRAP_LEASE_EXPIRED", "failure"
		} else if session.AttemptCount >= effectiveBootstrapMaxAttempts(session.MaxAttempts) {
			status, deadLetteredAt, finishedAt = BootstrapDeadLetter, now, now
			level, step = "error", "BOOTSTRAP_DEAD_LETTERED"
			auditAction, auditResult = "BOOTSTRAP_DEAD_LETTERED", "failure"
		} else {
			nextAttemptAt = now.Add(bootstrapRetryDelay(session.AttemptCount))
		}
		if _, err := tx.ExecContext(ctx, `UPDATE bootstrap_sessions SET status=$1, next_attempt_at=$2, dead_lettered_at=$3, finished_at=$4, lease_owner=NULL, lease_token_hash=NULL, lease_expires_at=NULL, lease_heartbeat_at=NULL, leased_at=NULL, last_failure_code='BOOTSTRAP_LEASE_EXPIRED', last_failure_message_redacted=$5, updated_at=$6 WHERE id=$7 AND lease_expires_at <= $6`, status, nextAttemptAt, deadLetteredAt, finishedAt, failureMessage, now, session.ID); err != nil {
			return BootstrapRecoverySummary{}, err
		}
		session.Status = status
		session.LastFailureCode = "BOOTSTRAP_LEASE_EXPIRED"
		session.LastFailureRedacted = failureMessage
		session.UpdatedAt = now
		clearBootstrapLease(&session)
		if t, ok := nextAttemptAt.(time.Time); ok {
			session.NextAttemptAt = &t
		}
		if status == BootstrapDeadLetter {
			t := now
			session.DeadLetteredAt, session.FinishedAt = &t, &t
			summary.DeadLettered = append(summary.DeadLettered, session)
		} else if status == "expired" {
			t := now
			session.FinishedAt = &t
			summary.Expired = append(summary.Expired, session)
		} else {
			summary.Recovered = append(summary.Recovered, session)
		}
		if err := insertBootstrapEvent(ctx, tx, session, level, step, eventMessage, now); err != nil {
			return BootstrapRecoverySummary{}, err
		}
		if err := insertCloudAudit(ctx, tx, session.OrgID, session.ProjectID, "worker", auditAction, "bootstrap_session", session.ID, auditResult, map[string]any{"attempt_count": session.AttemptCount, "max_attempts": effectiveBootstrapMaxAttempts(session.MaxAttempts), "failure_code": session.LastFailureCode, "next_attempt_at": session.NextAttemptAt}); err != nil {
			return BootstrapRecoverySummary{}, err
		}
	}
	rows, err = tx.QueryContext(ctx, bootstrapSelectSQL+` WHERE expires_at <= $1 AND lease_expires_at IS NULL AND status IN ('created','pending','retry_wait') FOR UPDATE SKIP LOCKED`, now)
	if err != nil {
		return BootstrapRecoverySummary{}, err
	}
	var ttlExpired []BootstrapSession
	for rows.Next() {
		session, scanErr := scanBootstrapSession(rows)
		if scanErr != nil {
			rows.Close()
			return BootstrapRecoverySummary{}, scanErr
		}
		ttlExpired = append(ttlExpired, session)
	}
	if err := rows.Close(); err != nil {
		return BootstrapRecoverySummary{}, err
	}
	for _, session := range ttlExpired {
		if _, err := tx.ExecContext(ctx, `UPDATE bootstrap_sessions SET status='expired', next_attempt_at=NULL, finished_at=$1, lease_owner=NULL, lease_token_hash=NULL, lease_expires_at=NULL, lease_heartbeat_at=NULL, leased_at=NULL, updated_at=$1 WHERE id=$2 AND status IN ('created','pending','retry_wait')`, now, session.ID); err != nil {
			return BootstrapRecoverySummary{}, err
		}
		session.Status = "expired"
		session.UpdatedAt = now
		session.FinishedAt = &now
		clearBootstrapLease(&session)
		summary.Expired = append(summary.Expired, session)
		if err := insertBootstrapEvent(ctx, tx, session, "warn", "expired", "bootstrap session expired", now); err != nil {
			return BootstrapRecoverySummary{}, err
		}
		if err := insertCloudAudit(ctx, tx, session.OrgID, session.ProjectID, "worker", "BOOTSTRAP_LEASE_EXPIRED", "bootstrap_session", session.ID, "failure", map[string]any{"failure_code": "BOOTSTRAP_SESSION_EXPIRED"}); err != nil {
			return BootstrapRecoverySummary{}, err
		}
	}
	return summary, nil
}

func (s PostgresService) FinishBootstrapSessionForLease(projectID, sessionID, workerID, leaseToken string, result BootstrapFinishResult, now time.Time) (BootstrapSession, error) {
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return BootstrapSession{}, err
	}
	defer tx.Rollback()
	session, err := scanBootstrapSession(tx.QueryRowContext(ctx, bootstrapSelectSQL+` WHERE project_id=$1 AND id=$2 FOR UPDATE`, projectID, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return BootstrapSession{}, ErrNotFound
	}
	if err != nil {
		return BootstrapSession{}, err
	}
	if err := validateBootstrapLease(session, workerID, leaseToken, now); err != nil {
		return BootstrapSession{}, err
	}
	now = now.UTC()
	status := result.Status
	var nextAttemptAt, deadLetteredAt, finishedAt any
	failureCode, failureMessage := "", ""
	level, step, eventMessage := "info", status, "bootstrap completed after verified Agent heartbeat"
	if status == "failed" {
		if result.FailureCode == "" {
			return BootstrapSession{}, APIError{Status: 400, Code: "INVALID_BOOTSTRAP_FINISH", Message: "failed bootstrap finish requires failure_code"}
		}
		failureCode = result.FailureCode
		failureMessage = RedactString(result.MessageRedacted)
		if failureMessage == "" {
			failureMessage = "bootstrap attempt failed"
		}
		if result.Retryable && session.AttemptCount < effectiveBootstrapMaxAttempts(session.MaxAttempts) && now.Before(session.ExpiresAt) {
			status = BootstrapRetryWait
			nextAttemptAt = now.Add(bootstrapRetryDelay(session.AttemptCount))
			level, step, eventMessage = "warn", "BOOTSTRAP_RETRY_SCHEDULED", failureMessage
		} else {
			status, deadLetteredAt, finishedAt = BootstrapDeadLetter, now, now
			level, step, eventMessage = "error", "BOOTSTRAP_DEAD_LETTERED", failureMessage
		}
	} else if status == "completed" || status == "succeeded" || status == "cancelled" {
		finishedAt = now
		if status == "cancelled" {
			level, eventMessage = "warn", RedactString(result.MessageRedacted)
		}
	} else {
		return BootstrapSession{}, APIError{Status: 400, Code: "INVALID_BOOTSTRAP_FINISH", Message: "bootstrap finish status is invalid"}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE bootstrap_sessions SET status=$1, next_attempt_at=$2, dead_lettered_at=$3, finished_at=$4, lease_owner=NULL, lease_token_hash=NULL, lease_expires_at=NULL, lease_heartbeat_at=NULL, leased_at=NULL, last_failure_code=$5, last_failure_message_redacted=$6, updated_at=$7 WHERE id=$8`, status, nextAttemptAt, deadLetteredAt, finishedAt, failureCode, failureMessage, now, sessionID); err != nil {
		return BootstrapSession{}, err
	}
	session.Status = status
	session.LastFailureCode = failureCode
	session.LastFailureRedacted = failureMessage
	session.UpdatedAt = now
	clearBootstrapLease(&session)
	if t, ok := nextAttemptAt.(time.Time); ok {
		session.NextAttemptAt = &t
	}
	if t, ok := deadLetteredAt.(time.Time); ok {
		session.DeadLetteredAt = &t
	}
	if t, ok := finishedAt.(time.Time); ok {
		session.FinishedAt = &t
	} else {
		session.FinishedAt = nil
	}
	if err := insertBootstrapEvent(ctx, tx, session, level, step, eventMessage, now); err != nil {
		return BootstrapSession{}, err
	}
	if err := tx.Commit(); err != nil {
		return BootstrapSession{}, err
	}
	return session, nil
}

func (s PostgresService) ManualRetryBootstrapSession(projectID, sessionID, idempotencyKey string, now time.Time) (BootstrapManualRetryResult, error) {
	if idempotencyKey == "" {
		return BootstrapManualRetryResult{}, APIError{Status: 400, Code: "IDEMPOTENCY_KEY_REQUIRED", Message: "Idempotency-Key is required"}
	}
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return BootstrapManualRetryResult{}, err
	}
	defer tx.Rollback()
	scope := "bootstrap-retry:" + projectID + ":" + sessionID
	var priorID string
	if err := tx.QueryRowContext(ctx, `SELECT resource_id FROM idempotency_keys WHERE scope=$1 AND key=$2`, scope, idempotencyKey).Scan(&priorID); err == nil {
		session, getErr := scanBootstrapSession(tx.QueryRowContext(ctx, bootstrapSelectSQL+` WHERE project_id=$1 AND id=$2`, projectID, priorID))
		return BootstrapManualRetryResult{Session: session}, getErr
	} else if !errors.Is(err, sql.ErrNoRows) {
		return BootstrapManualRetryResult{}, err
	}
	session, err := scanBootstrapSession(tx.QueryRowContext(ctx, bootstrapSelectSQL+` WHERE project_id=$1 AND id=$2 FOR UPDATE`, projectID, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return BootstrapManualRetryResult{}, ErrNotFound
	}
	if err != nil {
		return BootstrapManualRetryResult{}, err
	}
	if session.Status != BootstrapDeadLetter {
		return BootstrapManualRetryResult{}, APIError{Status: 409, Code: "BOOTSTRAP_NOT_DEAD_LETTER", Message: "bootstrap session is not dead-lettered"}
	}
	now = now.UTC()
	if !now.Before(session.ExpiresAt) {
		return BootstrapManualRetryResult{}, APIError{Status: 409, Code: "BOOTSTRAP_SESSION_EXPIRED", Message: "expired bootstrap session cannot be retried"}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE bootstrap_sessions SET status='pending', attempt_count=0, next_attempt_at=NULL, dead_lettered_at=NULL, finished_at=NULL, lease_owner=NULL, lease_token_hash=NULL, lease_expires_at=NULL, lease_heartbeat_at=NULL, leased_at=NULL, updated_at=$1 WHERE id=$2`, now, sessionID); err != nil {
		return BootstrapManualRetryResult{}, err
	}
	if err := insertIdempotency(ctx, tx, scope, idempotencyKey, "bootstrap_session", sessionID); err != nil {
		return BootstrapManualRetryResult{}, err
	}
	session.Status = BootstrapPending
	session.AttemptCount = 0
	session.NextAttemptAt = nil
	session.DeadLetteredAt = nil
	session.FinishedAt = nil
	session.UpdatedAt = now
	clearBootstrapLease(&session)
	if err := insertBootstrapEvent(ctx, tx, session, "info", "BOOTSTRAP_MANUAL_RETRY_REQUESTED", "bootstrap session manually returned to pending", now); err != nil {
		return BootstrapManualRetryResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return BootstrapManualRetryResult{}, err
	}
	return BootstrapManualRetryResult{Session: session, Applied: true}, nil
}

func insertBootstrapEvent(ctx context.Context, tx *sql.Tx, session BootstrapSession, level, step, message string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO bootstrap_events(id, org_id, project_id, session_id, node_id, level, step, message_redacted, progress_percent, created_at) VALUES($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9,$10)`, newID("evt"), session.OrgID, session.ProjectID, session.ID, session.NodeID, level, step, RedactString(message), bootstrapProgress(session.Status), now)
	return err
}
