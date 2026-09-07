package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type kafkaMigrationRecorder struct{ statements []string }

func (r *kafkaMigrationRecorder) ExecContext(_ context.Context, statement string, _ ...any) (sql.Result, error) {
	r.statements = append(r.statements, statement)
	return nil, nil
}

func TestMigrateManagedKafkaExpandsProtocolAndStorageConstraints(t *testing.T) {
	recorder := &kafkaMigrationRecorder{}
	if err := MigrateManagedKafka(context.Background(), recorder); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(recorder.statements, "\n")
	if !strings.Contains(joined, "'postgres','redis','nats','amqp','mysql','http','tcp','custom','kafka'") {
		t.Fatal("resource_bindings protocol constraint does not allow kafka")
	}
	if !strings.Contains(joined, "'postgres','kafka'") {
		t.Fatal("retained_storages resource_type constraint does not allow kafka")
	}
	if len(recorder.statements) != 2 || !strings.Contains(joined, "DROP CONSTRAINT IF EXISTS") || !strings.Contains(joined, "ADD CONSTRAINT") {
		t.Fatal("existing constraints are not migrated idempotently")
	}
}

func TestPostgresManagedKafkaMigrationIsIdempotent(t *testing.T) {
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("OPSI_REQUIRE_POSTGRES_TESTS") == "1" {
			t.Fatal("set OPSI_TEST_DATABASE_URL to run managed Kafka migration tests")
		}
		t.Skip("set OPSI_TEST_DATABASE_URL to run managed Kafka migration tests")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("managed_kafka_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(context.Background(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	db, err := sql.Open("pgx", dsnWithSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	constraints := map[string]string{
		"resource_bindings": "resource_bindings_protocol_check",
		"retained_storages": "retained_storages_resource_type_check",
	}
	for table, constraint := range constraints {
		var definition string
		if err := db.QueryRowContext(context.Background(), `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid=$1::regclass AND conname=$2`, table, constraint).Scan(&definition); err != nil {
			t.Fatalf("read %s constraint: %v", table, err)
		}
		if !strings.Contains(definition, "kafka") {
			t.Fatalf("%s constraint does not allow Kafka: %s", table, definition)
		}
	}
}
