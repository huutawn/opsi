package postgres

import (
	"context"
	"database/sql"
)

func MigrateP07B2Valkey(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS managed_resource_credentials (
		id TEXT PRIMARY KEY,
		ciphertext BYTEA NOT NULL,
		nonce BYTEA NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`)
	return err
}
