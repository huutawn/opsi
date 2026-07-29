package actiondevice

import (
	"context"
	"database/sql"
	"errors"
	"sync"
)

type PostgresStore struct {
	DB         *sql.DB
	once       sync.Once
	migrateErr error
}

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{DB: db} }

func (s *PostgresStore) ensure(ctx context.Context) error {
	s.once.Do(func() {
		if s.DB == nil {
			s.migrateErr = ErrStorageUnavailable
			return
		}
		_, s.migrateErr = s.DB.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS action_devices (
  id text PRIMARY KEY,
  schema_version text NOT NULL,
  project_id text NOT NULL,
  owner_principal text NOT NULL,
  display_name text NOT NULL,
  public_key bytea NOT NULL,
  fingerprint_sha256 text NOT NULL,
  status text NOT NULL CHECK (status IN ('active','revoked')),
  trusted_actor text NOT NULL,
  idempotency_key text NOT NULL,
  created_at timestamptz NOT NULL,
  revoked_at timestamptz,
  UNIQUE(project_id, owner_principal, idempotency_key)
);
CREATE INDEX IF NOT EXISTS action_devices_project_idx ON action_devices(project_id, created_at, id);
CREATE TABLE IF NOT EXISTS action_device_audit (
  id text PRIMARY KEY,
  project_id text NOT NULL,
  actor text NOT NULL,
  action text NOT NULL,
  device_id text NOT NULL,
  created_at timestamptz NOT NULL
);`)
	})
	if s.migrateErr != nil {
		return ErrStorageUnavailable
	}
	return nil
}

func (s *PostgresStore) Register(ctx context.Context, device Device) (Device, bool, error) {
	if err := s.ensure(ctx); err != nil {
		return Device{}, false, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Device{}, false, ErrStorageUnavailable
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO action_devices(id,schema_version,project_id,owner_principal,display_name,public_key,fingerprint_sha256,status,trusted_actor,idempotency_key,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(project_id,owner_principal,idempotency_key) DO NOTHING`, device.ID, device.SchemaVersion, device.ProjectID, device.OwnerPrincipal, device.DisplayName, device.PublicKey, device.FingerprintSHA256, device.Status, device.TrustedActor, device.IdempotencyKey, device.CreatedAt)
	if err != nil {
		return Device{}, false, ErrStorageUnavailable
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Device{}, false, ErrStorageUnavailable
	}
	if rows == 0 {
		current, err := scanDevice(tx.QueryRowContext(ctx, deviceSelect+` WHERE project_id=$1 AND owner_principal=$2 AND idempotency_key=$3`, device.ProjectID, device.OwnerPrincipal, device.IdempotencyKey))
		if err != nil {
			return Device{}, false, ErrStorageUnavailable
		}
		if current.DisplayName != device.DisplayName || current.FingerprintSHA256 != device.FingerprintSHA256 || string(current.PublicKey) != string(device.PublicKey) {
			return Device{}, false, ErrReplayConflict
		}
		if err := tx.Commit(); err != nil {
			return Device{}, false, ErrStorageUnavailable
		}
		return current, true, nil
	}
	auditID, err := newID("audit")
	if err != nil {
		return Device{}, false, ErrStorageUnavailable
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO action_device_audit(id,project_id,actor,action,device_id,created_at) VALUES($1,$2,$3,'action_device.register',$4,$5)`, auditID, device.ProjectID, device.TrustedActor, device.ID, device.CreatedAt); err != nil {
		return Device{}, false, ErrStorageUnavailable
	}
	if err := tx.Commit(); err != nil {
		return Device{}, false, ErrStorageUnavailable
	}
	return device, false, nil
}

func (s *PostgresStore) List(ctx context.Context, projectID string) ([]Device, error) {
	if err := s.ensure(ctx); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, deviceSelect+` WHERE project_id=$1 ORDER BY created_at,id`, projectID)
	if err != nil {
		return nil, ErrStorageUnavailable
	}
	defer rows.Close()
	result := []Device{}
	for rows.Next() {
		device, err := scanDevice(rows)
		if err != nil {
			return nil, ErrStorageUnavailable
		}
		result = append(result, device)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrStorageUnavailable
	}
	return result, nil
}

func (s *PostgresStore) Get(ctx context.Context, projectID, deviceID string) (Device, error) {
	if err := s.ensure(ctx); err != nil {
		return Device{}, err
	}
	device, err := scanDevice(s.DB.QueryRowContext(ctx, deviceSelect+` WHERE project_id=$1 AND id=$2`, projectID, deviceID))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrDeviceNotFound
	}
	if err != nil {
		return Device{}, ErrStorageUnavailable
	}
	return device, nil
}

func (s *PostgresStore) Revoke(ctx context.Context, projectID, deviceID, actor string, revokedAt int64) (Device, bool, error) {
	if err := s.ensure(ctx); err != nil {
		return Device{}, false, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Device{}, false, ErrStorageUnavailable
	}
	defer tx.Rollback()
	at := unixNano(revokedAt)
	result, err := tx.ExecContext(ctx, `UPDATE action_devices SET status='revoked',revoked_at=$3 WHERE project_id=$1 AND id=$2 AND status='active'`, projectID, deviceID, at)
	if err != nil {
		return Device{}, false, ErrStorageUnavailable
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Device{}, false, ErrStorageUnavailable
	}
	device, err := scanDevice(tx.QueryRowContext(ctx, deviceSelect+` WHERE project_id=$1 AND id=$2`, projectID, deviceID))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, false, ErrDeviceNotFound
	}
	if err != nil {
		return Device{}, false, ErrStorageUnavailable
	}
	if rows == 1 {
		auditID, idErr := newID("audit")
		if idErr != nil {
			return Device{}, false, ErrStorageUnavailable
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO action_device_audit(id,project_id,actor,action,device_id,created_at) VALUES($1,$2,$3,'action_device.revoke',$4,$5)`, auditID, projectID, actor, deviceID, at); err != nil {
			return Device{}, false, ErrStorageUnavailable
		}
	}
	if err := tx.Commit(); err != nil {
		return Device{}, false, ErrStorageUnavailable
	}
	return device, rows == 1, nil
}

const deviceSelect = `SELECT id,schema_version,project_id,owner_principal,display_name,public_key,fingerprint_sha256,status,trusted_actor,idempotency_key,created_at,revoked_at FROM action_devices`

type scanner interface{ Scan(...any) error }

func scanDevice(row scanner) (Device, error) {
	var device Device
	var revoked sql.NullTime
	err := row.Scan(&device.ID, &device.SchemaVersion, &device.ProjectID, &device.OwnerPrincipal, &device.DisplayName, &device.PublicKey, &device.FingerprintSHA256, &device.Status, &device.TrustedActor, &device.IdempotencyKey, &device.CreatedAt, &revoked)
	if revoked.Valid {
		value := revoked.Time.UTC()
		device.RevokedAt = &value
	}
	device.CreatedAt = device.CreatedAt.UTC()
	return device, err
}
