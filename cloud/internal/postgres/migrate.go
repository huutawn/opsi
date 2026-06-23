package postgres

import (
	"context"
	"database/sql"
)

func Migrate(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			email TEXT UNIQUE NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS project_memberships (
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			role TEXT NOT NULL CHECK (role IN ('Owner','Developer','Viewer')),
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (project_id, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS personal_access_tokens (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			token_hash TEXT NOT NULL,
			expires_at TIMESTAMPTZ NOT NULL,
			revoked BOOLEAN NOT NULL DEFAULT false,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS personal_access_tokens_user_id_idx ON personal_access_tokens(user_id)`,
		`CREATE TABLE IF NOT EXISTS otp_requests (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			purpose TEXT NOT NULL,
			salt TEXT NOT NULL,
			code_hash TEXT NOT NULL,
			expires_at TIMESTAMPTZ NOT NULL,
			used_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS otp_requests_user_created_idx ON otp_requests(user_id, created_at)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
