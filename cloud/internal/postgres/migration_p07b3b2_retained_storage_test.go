//go:build postgresintegration

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresP07B3B2RetainedStorageMigrationIsAdditiveAndIdempotent(t *testing.T) {
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("OPSI_REQUIRE_POSTGRES_TESTS") == "1" {
			t.Fatal("set OPSI_TEST_DATABASE_URL to run P07B3B2 migration tests")
		}
		t.Skip("set OPSI_TEST_DATABASE_URL to run P07B3B2 migration tests")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("p07b3b2_retained_storage_%d", time.Now().UnixNano())
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
	var tables int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('retained_storages','retained_storage_destroy_intents')`).Scan(&tables); err != nil || tables != 2 {
		t.Fatalf("tables=%d err=%v", tables, err)
	}
	var lifecycleConstraint int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM information_schema.check_constraints WHERE constraint_schema=current_schema() AND check_clause LIKE '%destroy_failed%'`).Scan(&lifecycleConstraint); err != nil || lifecycleConstraint == 0 {
		t.Fatalf("lifecycle constraint=%d err=%v", lifecycleConstraint, err)
	}
}
