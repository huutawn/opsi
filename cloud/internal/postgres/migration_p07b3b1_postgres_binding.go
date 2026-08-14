package postgres

import (
	"context"
	"database/sql"
)

func MigrateP07B3B1PostgresBinding(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	statements := []string{
		`ALTER TABLE resource_bindings ADD COLUMN IF NOT EXISTS lifecycle TEXT NOT NULL DEFAULT 'ready' CHECK (lifecycle IN ('provisioning','ready','failed','deleting'))`,
		`ALTER TABLE resource_bindings ADD COLUMN IF NOT EXISTS credential_id TEXT`,
		`ALTER TABLE resource_bindings ADD COLUMN IF NOT EXISTS role_name TEXT`,
		`ALTER TABLE resource_bindings ADD COLUMN IF NOT EXISTS database_name TEXT`,
		`ALTER TABLE resource_bindings ADD COLUMN IF NOT EXISTS failure_code TEXT`,
		`CREATE UNIQUE INDEX IF NOT EXISTS resource_bindings_credential_uidx ON resource_bindings(credential_id) WHERE credential_id IS NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS resource_bindings_role_uidx ON resource_bindings(project_id,target_id,role_name) WHERE role_name IS NOT NULL`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
