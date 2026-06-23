package otp

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type PostgresStore struct {
	DB *sql.DB
}

func (s PostgresStore) Put(ctx context.Context, id string, item entry) error {
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO otp_requests (id, project_id, user_id, purpose, salt, code_hash, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)`, id, item.ProjectID, item.UserID, item.Purpose, item.Salt, item.Hash, item.ExpiresAt)
	return err
}

func (s PostgresStore) Get(ctx context.Context, id string) (entry, error) {
	var item entry
	var usedAt sql.NullTime
	err := s.DB.QueryRowContext(ctx, `
SELECT project_id, user_id, purpose, salt, code_hash, expires_at, used_at
FROM otp_requests
WHERE id = $1`, id).Scan(&item.ProjectID, &item.UserID, &item.Purpose, &item.Salt, &item.Hash, &item.ExpiresAt, &usedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return entry{}, ErrNotFound
	}
	if err != nil {
		return entry{}, err
	}
	if usedAt.Valid {
		item.UsedAt = usedAt.Time.UTC()
	}
	return item, nil
}

func (s PostgresStore) MarkUsed(ctx context.Context, id string, usedAt time.Time) error {
	result, err := s.DB.ExecContext(ctx, `UPDATE otp_requests SET used_at = $2 WHERE id = $1 AND used_at IS NULL`, id, usedAt.UTC())
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrUsed
	}
	return nil
}
