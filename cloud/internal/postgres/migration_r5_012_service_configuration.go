package postgres

import (
	"context"
	"database/sql"
)

// MigrateR5012ServiceConfiguration replaces the legacy binding-only JSON with
// the factual reviewed runtime configuration owned by each service.
func MigrateR5012ServiceConfiguration(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	statements := []string{
		`ALTER TABLE control_services ADD COLUMN IF NOT EXISTS configuration JSONB NOT NULL DEFAULT '{"schema_version":"opsi.service_configuration/v1"}'::jsonb`,
		`ALTER TABLE control_services ADD COLUMN IF NOT EXISTS configuration_revision BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE control_services ADD COLUMN IF NOT EXISTS configuration_state_hash TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE control_services ADD COLUMN IF NOT EXISTS configuration_applied_by TEXT`,
		`ALTER TABLE control_services ADD COLUMN IF NOT EXISTS configuration_applied_at TIMESTAMPTZ`,
		`DO $migration$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'control_services' AND column_name = 'bindings'
			) THEN
				EXECUTE $sql$
					UPDATE control_services source SET configuration = jsonb_build_object(
						'schema_version', 'opsi.service_configuration/v1',
						'bindings', COALESCE((
							SELECT jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
								'kind', 'internal_http',
								'target_service_id', legacy.value->>'service_id',
								'target_service_key', target.name,
								'env_prefix', NULLIF(legacy.value->>'env_prefix','')
							)))
							FROM jsonb_array_elements(source.bindings) legacy(value)
							JOIN control_services target ON target.id = legacy.value->>'service_id' AND target.project_id = source.project_id
						), '[]'::jsonb)
					) WHERE source.bindings <> '[]'::jsonb AND source.configuration = '{"schema_version":"opsi.service_configuration/v1"}'::jsonb
				$sql$;
				EXECUTE 'ALTER TABLE control_services DROP COLUMN IF EXISTS bindings';
			END IF;
		END
		$migration$`,
		`UPDATE control_services SET configuration_state_hash = md5(configuration::text) WHERE configuration_state_hash = ''`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
