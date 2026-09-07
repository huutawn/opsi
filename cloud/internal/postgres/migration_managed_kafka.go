package postgres

import (
	"context"
	"database/sql"
)

func MigrateManagedKafka(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	statements := []string{
		`ALTER TABLE resource_bindings DROP CONSTRAINT IF EXISTS resource_bindings_protocol_check, ADD CONSTRAINT resource_bindings_protocol_check CHECK (protocol IN ('postgres','redis','nats','amqp','mysql','http','tcp','custom','kafka'))`,
		`ALTER TABLE retained_storages DROP CONSTRAINT IF EXISTS retained_storages_resource_type_check, ADD CONSTRAINT retained_storages_resource_type_check CHECK (resource_type IN ('postgres','kafka'))`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
