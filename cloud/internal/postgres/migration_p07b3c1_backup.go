package postgres

import (
	"context"
	"database/sql"
)

func MigrateP07B3C1Backup(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS backups (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			environment_id TEXT NOT NULL,
			source_resource_id TEXT NOT NULL,
			source_node_id TEXT NOT NULL,
			resource_type TEXT NOT NULL CHECK (resource_type='postgres'),
			backup_type TEXT NOT NULL CHECK (backup_type='postgres_logical'),
			source_database TEXT NOT NULL CHECK (source_database='opsi'),
			source_postgres_version TEXT,
			source_spec_revision BIGINT NOT NULL CHECK (source_spec_revision>0),
			source_spec_hash TEXT NOT NULL CHECK (source_spec_hash ~ '^[0-9a-f]{64}$'),
			source_pvc_name TEXT NOT NULL,
			source_pvc_uid TEXT NOT NULL,
			source_pv_name TEXT,
			source_pv_uid TEXT,
			source_storage_hash TEXT NOT NULL CHECK (source_storage_hash ~ '^[0-9a-f]{64}$'),
			dump_format TEXT NOT NULL CHECK (dump_format='custom'),
			dump_options JSONB NOT NULL,
			lifecycle TEXT NOT NULL CHECK (lifecycle IN ('queued','leased','running','succeeded','failed')),
			store_id TEXT NOT NULL,
			object_key TEXT NOT NULL,
			object_etag TEXT,
			object_version_id TEXT,
			artifact_size BIGINT CHECK (artifact_size IS NULL OR artifact_size>0),
			sha256 TEXT CHECK (sha256 IS NULL OR sha256 ~ '^[0-9a-f]{64}$'),
			pg_dump_version TEXT,
			archive_verified BOOLEAN NOT NULL DEFAULT false,
			requested_by TEXT NOT NULL,
			requested_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL,
			leased_at TIMESTAMPTZ,
			started_at TIMESTAMPTZ,
			completed_at TIMESTAMPTZ,
			failure_code TEXT,
			failure_message_redacted TEXT,
			attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count>=0),
			lease_token TEXT,
			lease_expires_at TIMESTAMPTZ,
			UNIQUE(store_id,object_key),
			FOREIGN KEY(environment_id,project_id) REFERENCES environments(id,project_id) ON DELETE CASCADE,
			CHECK ((lifecycle='succeeded') = (artifact_size IS NOT NULL AND sha256 IS NOT NULL AND pg_dump_version IS NOT NULL AND archive_verified AND completed_at IS NOT NULL)),
			CHECK ((lifecycle='failed') = (failure_code IS NOT NULL AND failure_message_redacted IS NOT NULL AND completed_at IS NOT NULL))
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS backups_one_active_per_resource_uidx ON backups(project_id,source_resource_id) WHERE lifecycle IN ('queued','leased','running')`,
		`CREATE INDEX IF NOT EXISTS backups_project_resource_created_idx ON backups(project_id,source_resource_id,created_at,id)`,
		`CREATE INDEX IF NOT EXISTS backups_lease_idx ON backups(project_id,source_node_id,lifecycle,lease_expires_at,created_at)`,
		`CREATE TABLE IF NOT EXISTS backup_idempotency (
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			idempotency_key TEXT NOT NULL,
			payload_hash TEXT NOT NULL CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
			backup_id TEXT NOT NULL REFERENCES backups(id) ON DELETE CASCADE,
			created_at TIMESTAMPTZ NOT NULL,
			PRIMARY KEY(project_id,idempotency_key)
		)`,
		`CREATE OR REPLACE FUNCTION prevent_backup_authority_mutation() RETURNS trigger AS $$
		BEGIN
			IF ROW(OLD.project_id,OLD.environment_id,OLD.source_resource_id,OLD.source_node_id,OLD.resource_type,OLD.backup_type,OLD.source_database,OLD.source_spec_revision,OLD.source_spec_hash,OLD.source_pvc_name,OLD.source_pvc_uid,OLD.source_pv_name,OLD.source_pv_uid,OLD.source_storage_hash,OLD.dump_format,OLD.dump_options,OLD.store_id,OLD.object_key,OLD.requested_by,OLD.requested_at,OLD.created_at)
			 IS DISTINCT FROM ROW(NEW.project_id,NEW.environment_id,NEW.source_resource_id,NEW.source_node_id,NEW.resource_type,NEW.backup_type,NEW.source_database,NEW.source_spec_revision,NEW.source_spec_hash,NEW.source_pvc_name,NEW.source_pvc_uid,NEW.source_pv_name,NEW.source_pv_uid,NEW.source_storage_hash,NEW.dump_format,NEW.dump_options,NEW.store_id,NEW.object_key,NEW.requested_by,NEW.requested_at,NEW.created_at) THEN
				RAISE EXCEPTION 'Backup source authority is immutable';
			END IF;
			IF OLD.lifecycle='succeeded' AND OLD IS DISTINCT FROM NEW THEN
				RAISE EXCEPTION 'Succeeded Backup authority is immutable';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql`,
		`DROP TRIGGER IF EXISTS backups_authority_immutable ON backups`,
		`CREATE TRIGGER backups_authority_immutable BEFORE UPDATE ON backups FOR EACH ROW EXECUTE FUNCTION prevent_backup_authority_mutation()`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
