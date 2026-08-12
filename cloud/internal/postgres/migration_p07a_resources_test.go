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

func TestPostgresP07AResourceMigrationIsAdditiveAndIdempotent(t *testing.T) {
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("OPSI_REQUIRE_POSTGRES_TESTS") == "1" {
			t.Fatal("set OPSI_TEST_DATABASE_URL to run P07A migration tests")
		}
		t.Skip("set OPSI_TEST_DATABASE_URL to run P07A migration tests")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("p07a_resources_%d", time.Now().UnixNano())
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
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('resources','resource_bindings','resource_idempotency')`).Scan(&tables); err != nil || tables != 3 {
		t.Fatalf("tables=%d err=%v", tables, err)
	}
	var applications int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM resources`).Scan(&applications); err != nil || applications != 0 {
		t.Fatalf("resources=%d err=%v", applications, err)
	}
	var oldTables int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('control_services','build_records','build_jobs','deployment_jobs','topology_plan_revisions')`).Scan(&oldTables); err != nil || oldTables != 5 {
		t.Fatalf("existing tables=%d err=%v", oldTables, err)
	}
}
