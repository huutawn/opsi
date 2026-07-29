package actionplane

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
	_ "modernc.org/sqlite"
)

var (
	ErrNotFound        = errors.New("action not found")
	ErrReplayConflict  = errors.New("action replay conflicts with persisted approval")
	ErrNonceConsumed   = errors.New("action nonce was already consumed")
	ErrTargetLocked    = errors.New("action target is locked")
	ErrStaleTransition = errors.New("action state changed before persistence")
)

type ReservationOutcome string

const (
	ReservationAcquired       ReservationOutcome = "reservation_acquired"
	ReservationSameInProgress ReservationOutcome = "same_action_in_progress"
	ReservationExactReplay    ReservationOutcome = "exact_terminal_replay"
	ReservationReplayConflict ReservationOutcome = "replay_conflict"
	ReservationTargetLocked   ReservationOutcome = "target_locked"
)

type Reservation struct {
	Outcome ReservationOutcome
	Record  Record
}

type Record struct {
	Plan               actionv1.ActionPlan
	State              actionv1.CurrentState
	Challenge          actionv1.ApprovalChallenge
	Status             actionv1.ActionStatus
	NonceConsumed      bool
	GrantHash          string
	ApprovedBy         string
	DeviceID           string
	ExecutionStartedAt time.Time
	Result             actionv1.ActionResult
}

type ActionStore interface {
	Create(context.Context, actionv1.ActionPlan, actionv1.CurrentState, actionv1.ApprovalChallenge) error
	Load(context.Context, string, string) (Record, error)
	LoadAction(context.Context, string, string) (Record, error)
	ReserveExecution(context.Context, string, string, string, string, string) (Reservation, error)
	BeginExecution(context.Context, Record, time.Time) (Record, error)
	Complete(context.Context, Record, actionv1.ActionResult) (Record, error)
	Recoverable(context.Context) ([]Record, error)
	Close() error
}

type SQLiteStore struct{ db *sql.DB }

func OpenSQLiteStore(path string) (*SQLiteStore, error) {
	if path == "" {
		id, err := randomID()
		if err != nil {
			return nil, err
		}
		path = "file:actionplane-" + id + "?mode=memory&cache=shared"
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &SQLiteStore{db: db}
	if err := store.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

func (s *SQLiteStore) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS action_plane_actions (
  action_id TEXT PRIMARY KEY,
  challenge_id TEXT NOT NULL UNIQUE,
  project_id TEXT NOT NULL,
  target_key TEXT NOT NULL,
  plan_json TEXT NOT NULL,
  state_json TEXT NOT NULL,
  challenge_json TEXT NOT NULL,
  status TEXT NOT NULL,
  nonce_consumed INTEGER NOT NULL DEFAULT 0,
  grant_hash TEXT NOT NULL DEFAULT '',
  approved_by TEXT NOT NULL DEFAULT '',
  approved_device_id TEXT NOT NULL DEFAULT '',
  execution_started_at INTEGER,
  result_json TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS action_plane_project_idx ON action_plane_actions(project_id, updated_at, action_id);
CREATE TABLE IF NOT EXISTS action_plane_target_locks (
  target_key TEXT PRIMARY KEY,
  action_id TEXT NOT NULL UNIQUE,
  acquired_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS action_plane_events (
  id TEXT PRIMARY KEY,
  action_id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  status TEXT NOT NULL,
  failure_code TEXT NOT NULL DEFAULT '',
  actor TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS action_plane_event_action_idx ON action_plane_events(action_id, created_at, id);`)
	if err != nil {
		return err
	}
	if err := s.ensureActionColumns(ctx); err != nil {
		return err
	}
	// Historical releases could leave a lock beside a durable terminal result;
	// only those provably terminal rows are safe to release during migration.
	_, err = s.db.ExecContext(ctx, `DELETE FROM action_plane_target_locks WHERE action_id IN (SELECT action_id FROM action_plane_actions WHERE status IN (?,?,?))`, actionv1.StatusSucceeded, actionv1.StatusFailed, actionv1.StatusRejected)
	return err
}

func (s *SQLiteStore) ensureActionColumns(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(action_plane_actions)`)
	if err != nil {
		return err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, column := range []struct{ name, definition string }{
		{"approved_by", `approved_by TEXT NOT NULL DEFAULT ''`},
		{"approved_device_id", `approved_device_id TEXT NOT NULL DEFAULT ''`},
		{"execution_started_at", `execution_started_at INTEGER`},
	} {
		if columns[column.name] {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE action_plane_actions ADD COLUMN `+column.definition); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) Create(ctx context.Context, plan actionv1.ActionPlan, state actionv1.CurrentState, challenge actionv1.ApprovalChallenge) error {
	planJSON, err := actionv1.CanonicalJSON(plan)
	if err != nil {
		return err
	}
	stateJSON, err := actionv1.CanonicalJSON(state)
	if err != nil {
		return err
	}
	challengeJSON, err := actionv1.CanonicalJSON(challenge)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := plan.IssuedAt.UnixNano()
	if _, err := tx.ExecContext(ctx, `INSERT INTO action_plane_actions(action_id,challenge_id,project_id,target_key,plan_json,state_json,challenge_json,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, plan.ID, challenge.ID, plan.ProjectID, plan.Target.Key(), string(planJSON), string(stateJSON), string(challengeJSON), actionv1.StatusPlanned, now, now); err != nil {
		return err
	}
	if err := insertEvent(ctx, tx, plan.ID, plan.ProjectID, actionv1.StatusPlanned, "", plan.RequestedBy, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE action_plane_actions SET status=?,updated_at=? WHERE action_id=?`, actionv1.StatusPreflighted, now, plan.ID); err != nil {
		return err
	}
	if err := insertEvent(ctx, tx, plan.ID, plan.ProjectID, actionv1.StatusPreflighted, "", plan.RequestedBy, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) Load(ctx context.Context, projectID, challengeID string) (Record, error) {
	return scanRecord(s.db.QueryRowContext(ctx, recordSelect+` WHERE project_id=? AND challenge_id=?`, projectID, challengeID))
}

func (s *SQLiteStore) LoadAction(ctx context.Context, projectID, actionID string) (Record, error) {
	return scanRecord(s.db.QueryRowContext(ctx, recordSelect+` WHERE project_id=? AND action_id=?`, projectID, actionID))
}

func (s *SQLiteStore) ReserveExecution(ctx context.Context, projectID, challengeID, grantHash, approvedBy, deviceID string) (Reservation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Reservation{}, err
	}
	defer tx.Rollback()
	record, err := scanRecord(tx.QueryRowContext(ctx, recordSelect+` WHERE project_id=? AND challenge_id=?`, projectID, challengeID))
	if err != nil {
		return Reservation{}, err
	}
	if record.Status.Terminal() {
		if record.GrantHash == grantHash {
			return Reservation{Outcome: ReservationExactReplay, Record: record}, nil
		}
		return Reservation{Outcome: ReservationReplayConflict, Record: record}, nil
	}
	if record.GrantHash != "" && record.GrantHash != grantHash {
		return Reservation{Outcome: ReservationReplayConflict, Record: record}, nil
	}
	if record.Status == actionv1.StatusApproved || record.Status == actionv1.StatusExecuting {
		return Reservation{Outcome: ReservationSameInProgress, Record: record}, nil
	}
	if record.Status != actionv1.StatusPreflighted {
		return Reservation{}, ErrStaleTransition
	}
	var lockOwner string
	lockErr := tx.QueryRowContext(ctx, `SELECT action_id FROM action_plane_target_locks WHERE target_key=?`, record.Plan.Target.Key()).Scan(&lockOwner)
	if lockErr == nil {
		if lockOwner == record.Plan.ID {
			return Reservation{Outcome: ReservationSameInProgress, Record: record}, nil
		}
		return Reservation{Outcome: ReservationTargetLocked, Record: record}, nil
	}
	if !errors.Is(lockErr, sql.ErrNoRows) {
		return Reservation{}, lockErr
	}
	now := time.Now().UTC().UnixNano()
	if _, err := tx.ExecContext(ctx, `INSERT INTO action_plane_target_locks(target_key,action_id,acquired_at) VALUES(?,?,?)`, record.Plan.Target.Key(), record.Plan.ID, now); err != nil {
		return Reservation{}, err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE action_plane_actions SET status=?,grant_hash=?,approved_by=?,approved_device_id=?,updated_at=? WHERE action_id=? AND status=? AND grant_hash=''`, actionv1.StatusApproved, grantHash, approvedBy, deviceID, now, record.Plan.ID, actionv1.StatusPreflighted)
	if err != nil {
		return Reservation{}, err
	}
	rows, err := updated.RowsAffected()
	if err != nil || rows != 1 {
		return Reservation{}, firstStoreError(err, ErrStaleTransition)
	}
	if err := insertEvent(ctx, tx, record.Plan.ID, projectID, actionv1.StatusApproved, "", approvedBy, now); err != nil {
		return Reservation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Reservation{}, err
	}
	record.Status = actionv1.StatusApproved
	record.GrantHash = grantHash
	record.ApprovedBy = approvedBy
	record.DeviceID = deviceID
	return Reservation{Outcome: ReservationAcquired, Record: record}, nil
}

func (s *SQLiteStore) BeginExecution(ctx context.Context, reservation Record, started time.Time) (Record, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()
	record, err := scanRecord(tx.QueryRowContext(ctx, recordSelect+` WHERE project_id=? AND challenge_id=?`, reservation.Plan.ProjectID, reservation.Challenge.ID))
	if err != nil {
		return Record{}, err
	}
	if record.Status.Terminal() && record.GrantHash == reservation.GrantHash {
		return record, nil
	}
	if record.Status != actionv1.StatusApproved || record.NonceConsumed || record.GrantHash != reservation.GrantHash || record.ApprovedBy != reservation.ApprovedBy || record.DeviceID != reservation.DeviceID {
		return Record{}, ErrNonceConsumed
	}
	var lockOwner string
	if err := tx.QueryRowContext(ctx, `SELECT action_id FROM action_plane_target_locks WHERE target_key=?`, record.Plan.Target.Key()).Scan(&lockOwner); err != nil || lockOwner != record.Plan.ID {
		return Record{}, firstStoreError(err, ErrTargetLocked)
	}
	now := started.UTC().UnixNano()
	result, err := tx.ExecContext(ctx, `UPDATE action_plane_actions SET status=?,nonce_consumed=1,execution_started_at=?,updated_at=? WHERE action_id=? AND status=? AND nonce_consumed=0`, actionv1.StatusExecuting, now, now, record.Plan.ID, actionv1.StatusApproved)
	if err != nil {
		return Record{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Record{}, err
	}
	if rows != 1 {
		return Record{}, ErrNonceConsumed
	}
	if err := insertEvent(ctx, tx, record.Plan.ID, record.Plan.ProjectID, actionv1.StatusExecuting, "", record.ApprovedBy, now); err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	record.Status = actionv1.StatusExecuting
	record.NonceConsumed = true
	record.ExecutionStartedAt = started.UTC()
	return record, nil
}

func (s *SQLiteStore) Complete(ctx context.Context, record Record, result actionv1.ActionResult) (Record, error) {
	if err := result.Validate(); err != nil {
		return Record{}, err
	}
	if result.ActionID != record.Plan.ID || result.ChallengeID != record.Challenge.ID || result.ProjectID != record.Plan.ProjectID {
		return Record{}, errors.New("terminal result identity does not match action")
	}
	resultJSON, err := actionv1.CanonicalJSON(result)
	if err != nil {
		return Record{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()
	current, err := scanRecord(tx.QueryRowContext(ctx, recordSelect+` WHERE action_id=?`, record.Plan.ID))
	if err != nil {
		return Record{}, err
	}
	if current.Status.Terminal() {
		return current, nil
	}
	if current.Status != record.Status || (record.GrantHash != "" && current.GrantHash != "" && current.GrantHash != record.GrantHash) {
		return Record{}, ErrStaleTransition
	}
	updated, err := tx.ExecContext(ctx, `UPDATE action_plane_actions SET status=?,result_json=?,grant_hash=CASE WHEN ?<>'' THEN ? ELSE grant_hash END,updated_at=? WHERE action_id=? AND status=? AND result_json=''`, result.Status, string(resultJSON), record.GrantHash, record.GrantHash, result.FinishedAt.UnixNano(), record.Plan.ID, record.Status)
	if err != nil {
		return Record{}, err
	}
	rows, err := updated.RowsAffected()
	if err != nil || rows != 1 {
		return Record{}, firstStoreError(err, ErrStaleTransition)
	}
	if err := insertEvent(ctx, tx, record.Plan.ID, record.Plan.ProjectID, result.Status, result.FailureCode, result.ApprovedBy, result.FinishedAt.UnixNano()); err != nil {
		return Record{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM action_plane_target_locks WHERE target_key=? AND action_id=?`, record.Plan.Target.Key(), record.Plan.ID); err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	current.Status = result.Status
	current.Result = result
	if record.GrantHash != "" {
		current.GrantHash = record.GrantHash
	}
	return current, nil
}

func (s *SQLiteStore) Recoverable(ctx context.Context) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx, recordSelect+` WHERE status IN (?,?) ORDER BY updated_at,action_id`, actionv1.StatusApproved, actionv1.StatusExecuting)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

const recordSelect = `SELECT plan_json,state_json,challenge_json,status,nonce_consumed,grant_hash,approved_by,approved_device_id,execution_started_at,result_json FROM action_plane_actions`

type rowScanner interface{ Scan(...any) error }

func scanRecord(row rowScanner) (Record, error) {
	var record Record
	var planJSON, stateJSON, challengeJSON, statusValue, resultJSON string
	var consumed int
	var executionStarted sql.NullInt64
	err := row.Scan(&planJSON, &stateJSON, &challengeJSON, &statusValue, &consumed, &record.GrantHash, &record.ApprovedBy, &record.DeviceID, &executionStarted, &resultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	if err := actionv1.DecodeStrict([]byte(planJSON), &record.Plan); err != nil {
		return Record{}, err
	}
	if err := actionv1.DecodeStrict([]byte(stateJSON), &record.State); err != nil {
		return Record{}, err
	}
	if err := actionv1.DecodeStrict([]byte(challengeJSON), &record.Challenge); err != nil {
		return Record{}, err
	}
	record.Status = actionv1.ActionStatus(statusValue)
	record.NonceConsumed = consumed != 0
	if executionStarted.Valid {
		record.ExecutionStartedAt = time.Unix(0, executionStarted.Int64).UTC()
	}
	if resultJSON != "" {
		if err := actionv1.DecodeStrict([]byte(resultJSON), &record.Result); err != nil {
			return Record{}, err
		}
	}
	return record, nil
}

func insertEvent(ctx context.Context, tx *sql.Tx, actionID, projectID string, status actionv1.ActionStatus, failure actionv1.FailureCode, actor string, createdAt int64) error {
	id, err := randomID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO action_plane_events(id,action_id,project_id,status,failure_code,actor,created_at) VALUES(?,?,?,?,?,?,?)`, id, actionID, projectID, status, failure, actor, createdAt)
	return err
}

func randomID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate ActionPlane identity: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
func firstStoreError(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

var _ ActionStore = (*SQLiteStore)(nil)
