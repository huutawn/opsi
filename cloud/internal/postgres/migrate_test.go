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
)

func TestGitHubInventoryMigrationCreatesSchemaAndPreservesP08Data(t *testing.T) {
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("OPSI_REQUIRE_POSTGRES_TESTS") == "1" {
			t.Fatal("set OPSI_TEST_DATABASE_URL to run GitHub migration tests")
		}
		t.Skip("set OPSI_TEST_DATABASE_URL to run GitHub migration tests")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("p09_migrate_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(context.Background(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })
	db, err := sql.Open("pgx", dsnWithSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	legacy := []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT UNIQUE NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`CREATE TABLE organizations (id TEXT PRIMARY KEY, name TEXT NOT NULL, slug TEXT UNIQUE NOT NULL, plan TEXT NOT NULL DEFAULT 'free', default_region TEXT, status TEXT NOT NULL DEFAULT 'active', created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`CREATE TABLE projects (id TEXT PRIMARY KEY, org_id TEXT REFERENCES organizations(id), name TEXT NOT NULL, slug TEXT, status TEXT NOT NULL DEFAULT 'no_nodes', created_by TEXT REFERENCES users(id), created_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`INSERT INTO users(id,email) VALUES('legacy-user','legacy@example.test')`,
		`INSERT INTO organizations(id,name,slug) VALUES('legacy-org','Legacy','legacy')`,
		`INSERT INTO projects(id,org_id,name,slug,status,created_by) VALUES('legacy-project','legacy-org','Legacy Project','legacy-project','no_nodes','legacy-user')`,
	}
	for _, statement := range legacy {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var legacyProjects int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM projects WHERE id='legacy-project' AND created_by='legacy-user'`).Scan(&legacyProjects); err != nil || legacyProjects != 1 {
		t.Fatalf("legacy project count=%d err=%v", legacyProjects, err)
	}
	var tables int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('github_installations','github_repositories','github_installation_project_links','github_repository_claims','github_service_bindings','github_webhook_deliveries')`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 6 {
		t.Fatalf("GitHub table count=%d", tables)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO github_installations(installation_id,account_id,account_login,account_type,status,suspended,created_at,updated_at) VALUES(0,1,'x','User','active',false,now(),now())`); err == nil {
		t.Fatal("non-positive installation ID passed database constraint")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO github_installations(installation_id,account_id,account_login,account_type,status,suspended,created_at,updated_at) VALUES(1,1,'x','User','invalid',false,now(),now())`); err == nil {
		t.Fatal("invalid installation status passed database constraint")
	}
	var partialIndexes int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM (VALUES (to_regclass('github_repository_claims_active_repository_uidx')), (to_regclass('github_service_bindings_active_service_uidx')), (to_regclass('github_service_bindings_active_repository_key_uidx'))) AS indexes(index_oid) WHERE index_oid IS NOT NULL`).Scan(&partialIndexes); err != nil {
		t.Fatal(err)
	}
	if partialIndexes != 3 {
		t.Fatalf("partial unique index count=%d", partialIndexes)
	}
}

func dsnWithSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
