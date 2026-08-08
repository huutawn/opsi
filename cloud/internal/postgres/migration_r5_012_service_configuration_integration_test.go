//go:build postgresintegration

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
)

func TestR5012ServiceConfigurationMigrationPreservesBindingsUntilCountsMatch(t *testing.T) {
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("OPSI_TEST_DATABASE_URL is required")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("r5_012_%d", time.Now().UnixNano())
	ctx := context.Background()
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE control_services (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, name TEXT NOT NULL, bindings JSONB NOT NULL DEFAULT '[]'::jsonb)`,
		`INSERT INTO control_services(id,project_id,name,bindings) VALUES ('source','project-1','web','[{"service_id":"missing","env_prefix":"API"}]'::jsonb)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := MigrateR5012ServiceConfiguration(ctx, db); err == nil {
		t.Fatal("migration dropped an unresolved legacy binding")
	}
	var bindingsColumn bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='control_services' AND column_name='bindings')`).Scan(&bindingsColumn); err != nil || !bindingsColumn {
		t.Fatalf("bindings column exists=%v err=%v", bindingsColumn, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO control_services(id,project_id,name,bindings) VALUES ('missing','project-1','api','[]'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	if err := MigrateR5012ServiceConfiguration(ctx, db); err != nil {
		t.Fatal(err)
	}
	var configuration []byte
	var stateHash string
	if err := db.QueryRowContext(ctx, `SELECT configuration, configuration_state_hash FROM control_services WHERE id='source'`).Scan(&configuration, &stateHash); err != nil {
		t.Fatal(err)
	}
	expected := serviceconfigurationv1.StateHash(serviceconfigurationv1.Draft{Bindings: []serviceconfigurationv1.Binding{{Kind: serviceconfigurationv1.BindingInternalHTTP, TargetServiceID: "missing", TargetServiceKey: "api", EnvPrefix: "API"}}})
	if stateHash != expected {
		t.Fatalf("state hash=%q want canonical SHA-256 %q configuration=%s", stateHash, expected, configuration)
	}
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='control_services' AND column_name='bindings')`).Scan(&bindingsColumn); err != nil || bindingsColumn {
		t.Fatalf("bindings column exists=%v err=%v", bindingsColumn, err)
	}
}
