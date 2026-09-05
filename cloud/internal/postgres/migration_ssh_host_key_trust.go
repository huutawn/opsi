package postgres

import (
	"context"
	"database/sql"
)

// MigrateSSHHostKeyTrust creates tables for project-scoped SSH host-key trust authority
// and temporary probe observations.
func MigrateSSHHostKeyTrust(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS ssh_host_key_trusts (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			host TEXT NOT NULL,
			port INTEGER NOT NULL,
			algorithm TEXT NOT NULL,
			public_key TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('active', 'superseded')),
			created_by TEXT REFERENCES users(id),
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			superseded_at TIMESTAMPTZ,
			superseded_by TEXT REFERENCES users(id)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ssh_host_key_trusts_active_uidx
			ON ssh_host_key_trusts(project_id, host, port) WHERE status = 'active'`,
		`CREATE INDEX IF NOT EXISTS ssh_host_key_trusts_project_idx
			ON ssh_host_key_trusts(project_id, created_at DESC)`,

		`CREATE TABLE IF NOT EXISTS ssh_host_key_observations (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			host TEXT NOT NULL,
			port INTEGER NOT NULL,
			resolved_ip TEXT NOT NULL,
			algorithm TEXT NOT NULL,
			public_key TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			trust_state TEXT NOT NULL CHECK (trust_state IN ('first_seen', 'matched', 'changed')),
			previous_fingerprint TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL CHECK (status IN ('pending', 'confirmed', 'consumed', 'expired')),
			created_by TEXT REFERENCES users(id),
			expires_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS ssh_host_key_observations_project_idx
			ON ssh_host_key_observations(project_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS ssh_host_key_observations_lookup_idx
			ON ssh_host_key_observations(project_id, host, port, expires_at)`,

		`ALTER TABLE bootstrap_sessions ADD COLUMN IF NOT EXISTS ssh_host_key_trust_id TEXT REFERENCES ssh_host_key_trusts(id)`,
		`ALTER TABLE bootstrap_sessions ADD COLUMN IF NOT EXISTS resolved_ip TEXT NOT NULL DEFAULT ''`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
