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
	ErrNotFound         = errors.New("action not found")
	ErrReplayConflict   = errors.New("action replay conflicts with persisted approval")
	ErrNonceConsumed    = errors.New("action nonce was already consumed")
	ErrTargetLocked     = errors.New("action target is locked")
	ErrActionInProgress = errors.New("action is already executing")
)

type Record struct {
	Plan          actionv1.ActionPlan
	State         actionv1.CurrentState
	Challenge     actionv1.ApprovalChallenge
	Status        actionv1.ActionStatus
	NonceConsumed bool
	GrantHash     string
	Result        actionv1.ActionResult
}

type ActionStore interface {
	Create(context.Context, actionv1.ActionPlan, actionv1.CurrentState, actionv1.ApprovalChallenge) error
	Load(context.Context, string, string) (Record, error)
	LoadAction(context.Context, string, string) (Record, error)
	MarkApproved(context.Context, string, string) error
	TryLock(context.Context, string, string) error
	BeginExecution(context.Context, string, string, string) (Record, error)
	Complete(context.Context, Record, actionv1.ActionResult) error
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
	if err := store.recoverInterrupted(context.Background()); err != nil {
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
	return err
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

func (s *SQLiteStore) MarkApproved(ctx context.Context, projectID, challengeID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	record, err := scanRecord(tx.QueryRowContext(ctx, recordSelect+` WHERE project_id=? AND challenge_id=?`, projectID, challengeID))
	if err != nil {
		return err
	}
	if record.Status.Terminal() || record.Status == actionv1.StatusExecuting {
		return nil
	}
	now := time.Now().UTC().UnixNano()
	if _, err := tx.ExecContext(ctx, `UPDATE action_plane_actions SET status=?,updated_at=? WHERE action_id=?`, actionv1.StatusApproved, now, record.Plan.ID); err != nil {
		return err
	}
	if err := insertEvent(ctx, tx, record.Plan.ID, projectID, actionv1.StatusApproved, "", record.Plan.RequestedBy, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) TryLock(ctx context.Context, targetKey, actionID string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO action_plane_target_locks(target_key,action_id,acquired_at) VALUES(?,?,?)`, targetKey, actionID, time.Now().UTC().UnixNano())
	if err != nil {
		var owner string
		if queryErr := s.db.QueryRowContext(ctx, `SELECT action_id FROM action_plane_target_locks WHERE target_key=?`, targetKey).Scan(&owner); queryErr == nil && owner == actionID {
			return ErrActionInProgress
		}
		return ErrTargetLocked
	}
	return nil
}

func (s *SQLiteStore) BeginExecution(ctx context.Context, projectID, challengeID, grantHash string) (Record, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()
	record, err := scanRecord(tx.QueryRowContext(ctx, recordSelect+` WHERE project_id=? AND challenge_id=?`, projectID, challengeID))
	if err != nil {
		return Record{}, err
	}
	if record.Status.Terminal() {
		if record.GrantHash != grantHash {
			return Record{}, ErrReplayConflict
		}
		return record, nil
	}
	if record.NonceConsumed {
		return Record{}, ErrNonceConsumed
	}
	now := time.Now().UTC().UnixNano()
	result, err := tx.ExecContext(ctx, `UPDATE action_plane_actions SET status=?,nonce_consumed=1,grant_hash=?,updated_at=? WHERE action_id=? AND nonce_consumed=0`, actionv1.StatusExecuting, grantHash, now, record.Plan.ID)
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
	if err := insertEvent(ctx, tx, record.Plan.ID, projectID, actionv1.StatusExecuting, "", record.Plan.RequestedBy, now); err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	record.Status = actionv1.StatusExecuting
	record.NonceConsumed = true
	record.GrantHash = grantHash
	return record, nil
}

func (s *SQLiteStore) Complete(ctx context.Context, record Record, result actionv1.ActionResult) error {
	resultJSON, err := actionv1.CanonicalJSON(result)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	updated, err := tx.ExecContext(ctx, `UPDATE action_plane_actions SET status=?,result_json=?,grant_hash=CASE WHEN ?<>'' THEN ? ELSE grant_hash END,updated_at=? WHERE action_id=?`, result.Status, string(resultJSON), record.GrantHash, record.GrantHash, result.FinishedAt.UnixNano(), record.Plan.ID)
	if err != nil {
		return err
	}
	rows, err := updated.RowsAffected()
	if err != nil || rows != 1 {
		return firstStoreError(err, ErrNotFound)
	}
	if err := insertEvent(ctx, tx, record.Plan.ID, record.Plan.ProjectID, result.Status, result.FailureCode, result.ApprovedBy, result.FinishedAt.UnixNano()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM action_plane_target_locks WHERE target_key=? AND action_id=?`, record.Plan.Target.Key(), record.Plan.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) recoverInterrupted(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, recordSelect+` WHERE status=?`, actionv1.StatusExecuting)
	if err != nil {
		return err
	}
	var records []Record
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			rows.Close()
			return err
		}
		records = append(records, record)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, record := range records {
		now := time.Now().UTC()
		result := actionv1.ActionResult{SchemaVersion: actionv1.SchemaVersion, ActionID: record.Plan.ID, ChallengeID: record.Challenge.ID, ProjectID: record.Plan.ProjectID, Status: actionv1.StatusFailed, FailureCode: actionv1.FailureInterrupted, Message: "execution interrupted before durable terminal result", StartedAt: now, FinishedAt: now}
		if err := s.Complete(ctx, record, result); err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM action_plane_target_locks WHERE action_id NOT IN (SELECT action_id FROM action_plane_actions WHERE status=?)`, actionv1.StatusExecuting)
	return err
}

const recordSelect = `SELECT plan_json,state_json,challenge_json,status,nonce_consumed,grant_hash,result_json FROM action_plane_actions`

type rowScanner interface{ Scan(...any) error }

func scanRecord(row rowScanner) (Record, error) {
	var record Record
	var planJSON, stateJSON, challengeJSON, statusValue, resultJSON string
	var consumed int
	err := row.Scan(&planJSON, &stateJSON, &challengeJSON, &statusValue, &consumed, &record.GrantHash, &resultJSON)
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
