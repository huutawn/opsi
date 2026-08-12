package postgres

import (
	"context"
	"database/sql"
)

func MigrateP07AResources(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	statements := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS environments_id_project_uidx ON environments(id,project_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS control_services_id_project_environment_uidx ON control_services(id,project_id,environment_id)`,
		`CREATE TABLE IF NOT EXISTS resources (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			environment_id TEXT NOT NULL,
			name TEXT NOT NULL,
			kind TEXT NOT NULL CHECK (kind IN ('managed_service','external_resource')),
			provider TEXT NOT NULL,
			type TEXT NOT NULL,
			lifecycle TEXT NOT NULL CHECK (lifecycle IN ('unplaced','planned','provisioning','ready','degraded','failed','deleting','unknown','configured')),
			managed_spec JSONB NOT NULL DEFAULT 'null'::jsonb,
			external_spec JSONB NOT NULL DEFAULT 'null'::jsonb,
			internal_name TEXT,
			created_by TEXT,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL,
			UNIQUE(project_id,environment_id,name),
			FOREIGN KEY(environment_id,project_id) REFERENCES environments(id,project_id) ON DELETE CASCADE,
			CHECK ((kind='managed_service' AND managed_spec<>'null'::jsonb AND external_spec='null'::jsonb) OR (kind='external_resource' AND external_spec<>'null'::jsonb AND managed_spec='null'::jsonb))
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS resources_id_project_environment_uidx ON resources(id,project_id,environment_id)`,
		`CREATE INDEX IF NOT EXISTS resources_project_environment_idx ON resources(project_id,environment_id,created_at)`,
		`CREATE TABLE IF NOT EXISTS resource_bindings (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			environment_id TEXT NOT NULL,
			source_kind TEXT NOT NULL CHECK (source_kind='application'),
			source_id TEXT NOT NULL,
			target_kind TEXT NOT NULL CHECK (target_kind IN ('managed_service','external_resource')),
			target_id TEXT NOT NULL,
			protocol TEXT NOT NULL CHECK (protocol IN ('postgres','redis','nats','amqp','mysql','http','tcp','custom')),
			logical_name TEXT NOT NULL,
			runtime_references JSONB NOT NULL DEFAULT '[]'::jsonb,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL,
			UNIQUE(project_id,environment_id,source_id,logical_name),
			FOREIGN KEY(environment_id,project_id) REFERENCES environments(id,project_id) ON DELETE CASCADE,
			FOREIGN KEY(source_id,project_id,environment_id) REFERENCES control_services(id,project_id,environment_id) ON DELETE CASCADE,
			FOREIGN KEY(target_id,project_id,environment_id) REFERENCES resources(id,project_id,environment_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS resource_bindings_target_idx ON resource_bindings(project_id,target_id)`,
		`CREATE TABLE IF NOT EXISTS resource_idempotency (
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			operation TEXT NOT NULL CHECK (operation IN ('create_resource','create_binding')),
			idempotency_key TEXT NOT NULL,
			payload_hash TEXT NOT NULL,
			resource_id TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL,
			PRIMARY KEY(project_id,operation,idempotency_key)
		)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
