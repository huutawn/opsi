package postgres

import (
	"context"
	"database/sql"
	"encoding/json"

	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
)

// MigrateR5012ServiceConfiguration replaces the legacy binding-only JSON with
// the factual reviewed runtime configuration owned by each service.
func MigrateR5012ServiceConfiguration(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) error {
	statements := []string{
		`ALTER TABLE control_services ADD COLUMN IF NOT EXISTS configuration JSONB NOT NULL DEFAULT '{"schema_version":"opsi.service_configuration/v1"}'::jsonb`,
		`ALTER TABLE control_services ADD COLUMN IF NOT EXISTS configuration_revision BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE control_services ADD COLUMN IF NOT EXISTS configuration_state_hash TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE control_services ADD COLUMN IF NOT EXISTS configuration_applied_by TEXT`,
		`ALTER TABLE control_services ADD COLUMN IF NOT EXISTS configuration_applied_at TIMESTAMPTZ`,
		`DO $migration$
		DECLARE
			legacy_binding_count BIGINT;
			migrated_binding_count BIGINT;
		BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'control_services' AND column_name = 'bindings'
			) THEN
				EXECUTE $sql$
					SELECT COALESCE(sum(jsonb_array_length(COALESCE(bindings, '[]'::jsonb))), 0)
					FROM control_services
				$sql$ INTO legacy_binding_count;
				EXECUTE $sql$
					SELECT count(*)
					FROM control_services source
					CROSS JOIN LATERAL jsonb_array_elements(COALESCE(source.bindings, '[]'::jsonb)) legacy(value)
					JOIN control_services target ON target.id = legacy.value->>'service_id' AND target.project_id = source.project_id
				$sql$ INTO migrated_binding_count;
				IF migrated_binding_count <> legacy_binding_count THEN
					RAISE EXCEPTION 'R5-012 migrated % of % legacy service bindings', migrated_binding_count, legacy_binding_count;
				END IF;
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
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT id, configuration FROM control_services WHERE configuration_state_hash = ''`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type pendingHash struct {
		id   string
		hash string
	}
	updates := make([]pendingHash, 0)
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return err
		}
		var draft serviceconfigurationv1.Draft
		if err := json.Unmarshal(raw, &draft); err != nil {
			return err
		}
		updates = append(updates, pendingHash{id: id, hash: serviceconfigurationv1.StateHash(draft)})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, update := range updates {
		if _, err := db.ExecContext(ctx, `UPDATE control_services SET configuration_state_hash=$1 WHERE id=$2 AND configuration_state_hash=''`, update.hash, update.id); err != nil {
			return err
		}
	}
	return nil
}
