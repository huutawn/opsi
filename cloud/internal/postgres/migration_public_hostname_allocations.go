package postgres

import (
	"context"
	"database/sql"
)

func MigratePublicHostnameAllocations(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS public_hostname_allocations (
			id TEXT PRIMARY KEY,
			hostname TEXT NOT NULL UNIQUE CHECK (hostname = lower(hostname)),
			owner_user_id TEXT NOT NULL REFERENCES users(id),
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
			runtime_id TEXT REFERENCES runtimes(id) ON DELETE SET NULL,
			target_ip INET,
			cloudflare_record_id TEXT,
			status TEXT NOT NULL CHECK (status IN ('reserved','provisioning','active','release_pending','failed','released')),
			publication_error_code TEXT,
			publication_error_message TEXT,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL,
			released_at TIMESTAMPTZ,
			CHECK ((status = 'released') = (released_at IS NOT NULL))
		)`,
		`CREATE INDEX IF NOT EXISTS public_hostname_allocations_owner_quota_idx ON public_hostname_allocations(owner_user_id,status)`,
		`CREATE INDEX IF NOT EXISTS public_hostname_allocations_project_idx ON public_hostname_allocations(project_id,updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS public_hostname_allocations_reconcile_idx ON public_hostname_allocations(status,updated_at) WHERE status IN ('provisioning','failed','release_pending')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
