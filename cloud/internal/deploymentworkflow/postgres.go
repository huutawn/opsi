package deploymentworkflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type PostgresStore struct{ DB *sql.DB }

func (s PostgresStore) Create(ctx context.Context, run Run, event Event, key string) (Run, bool, error) {
	if s.DB == nil {
		return Run{}, false, errors.New("database unavailable")
	}
	data, err := json.Marshal(run)
	if err != nil {
		return Run{}, false, err
	}
	eventData, err := json.Marshal(event.Metadata)
	if err != nil {
		return Run{}, false, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, false, err
	}
	defer tx.Rollback()
	var existingData []byte
	scanErr := tx.QueryRowContext(ctx, `SELECT run_data FROM deployment_runs WHERE project_id=$1 AND idempotency_key=$2`, run.ProjectID, key).Scan(&existingData)
	if scanErr == nil {
		var existing Run
		if json.Unmarshal(existingData, &existing) != nil {
			return Run{}, false, errors.New("stored deployment run is invalid")
		}
		return normalizeStoredRun(existing), true, nil
	}
	if !errors.Is(scanErr, sql.ErrNoRows) {
		return Run{}, false, scanErr
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO deployment_runs(id,project_id,idempotency_key,state,revision,run_data,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(project_id,idempotency_key) DO NOTHING`, run.ID, run.ProjectID, key, string(run.State), run.Revision, data, run.CreatedAt, run.UpdatedAt)
	if err != nil {
		return Run{}, false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return Run{}, false, err
	}
	if inserted == 0 {
		if err := tx.QueryRowContext(ctx, `SELECT run_data FROM deployment_runs WHERE project_id=$1 AND idempotency_key=$2`, run.ProjectID, key).Scan(&existingData); err != nil {
			return Run{}, false, err
		}
		var existing Run
		if err := json.Unmarshal(existingData, &existing); err != nil {
			return Run{}, false, errors.New("stored deployment run is invalid")
		}
		return normalizeStoredRun(existing), true, nil
	}
	if err = insertEvent(ctx, tx, event, eventData); err != nil {
		return Run{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return Run{}, false, err
	}
	return run, false, nil
}
func (s PostgresStore) Get(ctx context.Context, projectID, runID string) (Run, error) {
	if s.DB == nil {
		return Run{}, errors.New("database unavailable")
	}
	var data []byte
	if err := s.DB.QueryRowContext(ctx, `SELECT run_data FROM deployment_runs WHERE project_id=$1 AND id=$2`, projectID, runID).Scan(&data); errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	} else if err != nil {
		return Run{}, err
	}
	var run Run
	if json.Unmarshal(data, &run) != nil {
		return Run{}, errors.New("stored deployment run is invalid")
	}
	return normalizeStoredRun(run), nil
}
func (s PostgresStore) List(ctx context.Context, projectID string, limit int) ([]Run, error) {
	if s.DB == nil {
		return nil, errors.New("database unavailable")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT run_data FROM deployment_runs WHERE project_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var data []byte
		var run Run
		if rows.Scan(&data) != nil || json.Unmarshal(data, &run) != nil {
			return nil, errors.New("stored deployment run is invalid")
		}
		out = append(out, normalizeStoredRun(run))
	}
	return out, rows.Err()
}
func (s PostgresStore) Save(ctx context.Context, run Run, expected uint64, event Event) (Run, error) {
	if s.DB == nil {
		return Run{}, errors.New("database unavailable")
	}
	run.Revision = expected + 1
	data, err := json.Marshal(run)
	if err != nil {
		return Run{}, err
	}
	eventData, err := json.Marshal(event.Metadata)
	if err != nil {
		return Run{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE deployment_runs SET state=$4,revision=$5,run_data=$6,updated_at=$7 WHERE project_id=$1 AND id=$2 AND revision=$3`, run.ProjectID, run.ID, expected, string(run.State), run.Revision, data, run.UpdatedAt)
	if err != nil {
		return Run{}, err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return Run{}, ErrConflict
	}
	if event.ID != "" {
		if err = insertEvent(ctx, tx, event, eventData); err != nil {
			return Run{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Run{}, err
	}
	return run, nil
}
func (s PostgresStore) Events(ctx context.Context, projectID, runID string) ([]Event, error) {
	if s.DB == nil {
		return nil, errors.New("database unavailable")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,project_id,run_id,state,level,message,metadata,created_at FROM deployment_run_events WHERE project_id=$1 AND run_id=$2 ORDER BY created_at,id`, projectID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var event Event
		var metadata []byte
		if err := rows.Scan(&event.ID, &event.ProjectID, &event.RunID, &event.State, &event.Level, &event.Message, &metadata, &event.CreatedAt); err != nil {
			return nil, err
		}
		if len(metadata) > 0 {
			if err := json.Unmarshal(metadata, &event.Metadata); err != nil {
				return nil, errors.New("stored deployment run event is invalid")
			}
		}
		out = append(out, event)
	}
	if len(out) == 0 {
		if _, err := s.Get(ctx, projectID, runID); err != nil {
			return nil, err
		}
	}
	return out, rows.Err()
}
func (s PostgresStore) AcquireLease(ctx context.Context, projectID, runID, owner string, now time.Time, ttl time.Duration) (Run, bool, error) {
	if s.DB == nil {
		return Run{}, false, errors.New("database unavailable")
	}
	var data []byte
	err := s.DB.QueryRowContext(ctx, `UPDATE deployment_runs SET lease_owner=$3,lease_expires_at=$4 WHERE project_id=$1 AND id=$2 AND (lease_owner IS NULL OR lease_owner=$3 OR lease_expires_at<=$5) RETURNING run_data`, projectID, runID, owner, now.Add(ttl), now).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		run, getErr := s.Get(ctx, projectID, runID)
		if getErr != nil {
			return Run{}, false, getErr
		}
		return run, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}
	var run Run
	if json.Unmarshal(data, &run) != nil {
		return Run{}, false, errors.New("stored deployment run is invalid")
	}
	return normalizeStoredRun(run), true, nil
}
func (s PostgresStore) ReleaseLease(ctx context.Context, projectID, runID, owner string) error {
	if s.DB == nil {
		return errors.New("database unavailable")
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE deployment_runs SET lease_owner=NULL,lease_expires_at=NULL WHERE project_id=$1 AND id=$2 AND lease_owner=$3`, projectID, runID, owner)
	return err
}
func (s PostgresStore) RenewLease(ctx context.Context, projectID, runID, owner string, now time.Time, ttl time.Duration) (bool, error) {
	if s.DB == nil {
		return false, errors.New("database unavailable")
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE deployment_runs SET lease_expires_at=$4 WHERE project_id=$1 AND id=$2 AND lease_owner=$3 AND lease_expires_at>$5`, projectID, runID, owner, now.Add(ttl), now)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}
func (s PostgresStore) Runnable(ctx context.Context, limit int) ([]Run, error) {
	if s.DB == nil {
		return nil, errors.New("database unavailable")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT run_data FROM deployment_runs WHERE state IN ('provisioning','building','preflighting','deploying','verifying','rolling_back','cleaning_up') ORDER BY updated_at,id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var data []byte
		var run Run
		if rows.Scan(&data) != nil || json.Unmarshal(data, &run) != nil {
			return nil, errors.New("stored deployment run is invalid")
		}
		run = normalizeStoredRun(run)
		if Runnable(run.State) {
			out = append(out, run)
		}
	}
	return out, rows.Err()
}

type dbtx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertEvent(ctx context.Context, tx dbtx, event Event, metadata []byte) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO deployment_run_events(id,project_id,run_id,state,level,message,metadata,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, event.ID, event.ProjectID, event.RunID, string(event.State), event.Level, event.Message, metadata, event.CreatedAt)
	return err
}
