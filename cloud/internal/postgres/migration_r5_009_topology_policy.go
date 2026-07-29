package postgres

import (
	"context"
	"database/sql"
)

// MigrateR5009TopologyPolicy adds immutable revision records and small mutable
// heads. Heads identify the active revision; audited revisions are never
// updated or physically deleted.
func MigrateR5009TopologyPolicy(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS topology_plan_revisions (
			id TEXT NOT NULL,
			revision BIGINT NOT NULL CHECK (revision > 0),
			project_id TEXT NOT NULL REFERENCES projects(id),
			schema_version TEXT NOT NULL CHECK (schema_version = 'opsi.topology_plan/v1'),
			plan_hash TEXT NOT NULL CHECK (plan_hash ~ '^[0-9a-f]{64}$'),
			state_hash TEXT NOT NULL CHECK (state_hash ~ '^[0-9a-f]{64}$'),
			assignments_json JSONB NOT NULL,
			created_by TEXT NOT NULL REFERENCES users(id),
			applied_by TEXT NOT NULL REFERENCES users(id),
			created_at TIMESTAMPTZ NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL,
			PRIMARY KEY (id, revision),
			UNIQUE (project_id, revision)
		)`,
		`CREATE TABLE IF NOT EXISTS topology_plan_heads (
			project_id TEXT PRIMARY KEY REFERENCES projects(id),
			plan_id TEXT NOT NULL,
			current_revision BIGINT NOT NULL CHECK (current_revision > 0),
			state_hash TEXT NOT NULL CHECK (state_hash ~ '^[0-9a-f]{64}$'),
			updated_at TIMESTAMPTZ NOT NULL,
			FOREIGN KEY (plan_id, current_revision) REFERENCES topology_plan_revisions(id, revision)
		)`,
		`CREATE INDEX IF NOT EXISTS topology_plan_revisions_project_idx ON topology_plan_revisions(project_id, revision DESC)`,
		`CREATE TABLE IF NOT EXISTS deployment_policy_revisions (
			id TEXT NOT NULL,
			revision BIGINT NOT NULL CHECK (revision > 0),
			project_id TEXT NOT NULL REFERENCES projects(id),
			schema_version TEXT NOT NULL CHECK (schema_version = 'opsi.deployment_policy/v1'),
			policy_hash TEXT NOT NULL CHECK (policy_hash ~ '^[0-9a-f]{64}$'),
			state_hash TEXT NOT NULL CHECK (state_hash ~ '^[0-9a-f]{64}$'),
			policy_json JSONB NOT NULL,
			enabled BOOLEAN NOT NULL,
			created_by TEXT NOT NULL REFERENCES users(id),
			applied_by TEXT NOT NULL REFERENCES users(id),
			created_at TIMESTAMPTZ NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL,
			PRIMARY KEY (id, revision),
			UNIQUE (project_id, id, revision)
		)`,
		`CREATE TABLE IF NOT EXISTS deployment_policy_heads (
			policy_id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id),
			current_revision BIGINT NOT NULL CHECK (current_revision > 0),
			state_hash TEXT NOT NULL CHECK (state_hash ~ '^[0-9a-f]{64}$'),
			enabled BOOLEAN NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL,
			FOREIGN KEY (policy_id, current_revision) REFERENCES deployment_policy_revisions(id, revision)
		)`,
		`CREATE INDEX IF NOT EXISTS deployment_policy_heads_project_idx ON deployment_policy_heads(project_id, enabled, policy_id)`,
		`CREATE TABLE IF NOT EXISTS operator_capacity_revisions (
			id TEXT NOT NULL,
			revision BIGINT NOT NULL CHECK (revision > 0),
			project_id TEXT NOT NULL REFERENCES projects(id),
			runtime_id TEXT NOT NULL REFERENCES runtimes(id),
			source TEXT NOT NULL CHECK (source = 'operator_declared'),
			cpu_millicores BIGINT NOT NULL CHECK (cpu_millicores > 0),
			memory_bytes BIGINT NOT NULL CHECK (memory_bytes > 0),
			reserved_cpu_millicores BIGINT NOT NULL CHECK (reserved_cpu_millicores >= 0 AND reserved_cpu_millicores < cpu_millicores),
			reserved_memory_bytes BIGINT NOT NULL CHECK (reserved_memory_bytes >= 0 AND reserved_memory_bytes < memory_bytes),
			state_hash TEXT NOT NULL CHECK (state_hash ~ '^[0-9a-f]{64}$'),
			declared_by TEXT NOT NULL REFERENCES users(id),
			declared_at TIMESTAMPTZ NOT NULL,
			PRIMARY KEY (id, revision),
			UNIQUE (project_id, runtime_id, revision)
		)`,
		`CREATE TABLE IF NOT EXISTS operator_capacity_heads (
			project_id TEXT NOT NULL REFERENCES projects(id),
			runtime_id TEXT NOT NULL REFERENCES runtimes(id),
			capacity_id TEXT NOT NULL,
			current_revision BIGINT NOT NULL CHECK (current_revision > 0),
			state_hash TEXT NOT NULL CHECK (state_hash ~ '^[0-9a-f]{64}$'),
			updated_at TIMESTAMPTZ NOT NULL,
			PRIMARY KEY (project_id, runtime_id),
			FOREIGN KEY (capacity_id, current_revision) REFERENCES operator_capacity_revisions(id, revision)
		)`,
		`CREATE TABLE IF NOT EXISTS control_mutation_idempotency (
			project_id TEXT NOT NULL REFERENCES projects(id),
			operation TEXT NOT NULL,
			idempotency_key TEXT NOT NULL,
			payload_hash TEXT NOT NULL CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
			response_json JSONB NOT NULL,
			created_at TIMESTAMPTZ NOT NULL,
			PRIMARY KEY (project_id, operation, idempotency_key)
		)`,
		`CREATE OR REPLACE FUNCTION prevent_r5_009_revision_mutation() RETURNS trigger AS $$
		BEGIN RAISE EXCEPTION 'R5-009 revision records are append-only'; END; $$ LANGUAGE plpgsql`,
		`DROP TRIGGER IF EXISTS topology_plan_revisions_no_update ON topology_plan_revisions`,
		`DROP TRIGGER IF EXISTS topology_plan_revisions_no_delete ON topology_plan_revisions`,
		`DROP TRIGGER IF EXISTS deployment_policy_revisions_no_update ON deployment_policy_revisions`,
		`DROP TRIGGER IF EXISTS deployment_policy_revisions_no_delete ON deployment_policy_revisions`,
		`DROP TRIGGER IF EXISTS operator_capacity_revisions_no_update ON operator_capacity_revisions`,
		`DROP TRIGGER IF EXISTS operator_capacity_revisions_no_delete ON operator_capacity_revisions`,
		`CREATE TRIGGER topology_plan_revisions_no_update BEFORE UPDATE ON topology_plan_revisions FOR EACH ROW EXECUTE FUNCTION prevent_r5_009_revision_mutation()`,
		`CREATE TRIGGER topology_plan_revisions_no_delete BEFORE DELETE ON topology_plan_revisions FOR EACH ROW EXECUTE FUNCTION prevent_r5_009_revision_mutation()`,
		`CREATE TRIGGER deployment_policy_revisions_no_update BEFORE UPDATE ON deployment_policy_revisions FOR EACH ROW EXECUTE FUNCTION prevent_r5_009_revision_mutation()`,
		`CREATE TRIGGER deployment_policy_revisions_no_delete BEFORE DELETE ON deployment_policy_revisions FOR EACH ROW EXECUTE FUNCTION prevent_r5_009_revision_mutation()`,
		`CREATE TRIGGER operator_capacity_revisions_no_update BEFORE UPDATE ON operator_capacity_revisions FOR EACH ROW EXECUTE FUNCTION prevent_r5_009_revision_mutation()`,
		`CREATE TRIGGER operator_capacity_revisions_no_delete BEFORE DELETE ON operator_capacity_revisions FOR EACH ROW EXECUTE FUNCTION prevent_r5_009_revision_mutation()`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
