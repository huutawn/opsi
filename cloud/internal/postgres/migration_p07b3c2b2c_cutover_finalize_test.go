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

func TestPostgresP07B3C2B2CCutoverFinalizeMigrationIsAdditiveAndIdempotent(t *testing.T) {
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("OPSI_REQUIRE_POSTGRES_TESTS") == "1" {
			t.Fatal("set OPSI_TEST_DATABASE_URL to run P07B3C2B2C migration tests")
		}
		t.Skip("set OPSI_TEST_DATABASE_URL to run P07B3C2B2C migration tests")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("p07b3c2b2c_finalize_%d", time.Now().UnixNano())
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
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('application_cutover_finalizations','application_cutover_finalization_idempotency')`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("tables=%d err=%v", count, err)
	}
	var indexes int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM pg_indexes WHERE schemaname=current_schema() AND indexname='application_cutover_finalizations_lease_idx'`).Scan(&indexes); err != nil || indexes != 1 {
		t.Fatalf("indexes=%d err=%v", indexes, err)
	}
}
