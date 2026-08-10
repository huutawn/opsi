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
)

func TestP05AApplicationSourceMigrationPreservesLegacyBinding(t *testing.T) {
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("OPSI_TEST_DATABASE_URL is required")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	ctx := context.Background()
	schema := fmt.Sprintf("p05a_%d", time.Now().UnixNano())
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
		`CREATE TABLE control_services (id TEXT PRIMARY KEY, branch TEXT, build_method TEXT, build_context TEXT, dockerfile TEXT)`,
		`CREATE TABLE github_repositories (repository_id BIGINT, installation_id BIGINT, default_branch TEXT, PRIMARY KEY(repository_id,installation_id))`,
		`CREATE TABLE github_service_bindings (id TEXT PRIMARY KEY, service_id TEXT, repository_id BIGINT, installation_id BIGINT)`,
		`INSERT INTO control_services VALUES ('api','release','dockerfile','apps/api','apps/api/Dockerfile')`,
		`INSERT INTO github_repositories VALUES (7,9,'main')`,
		`INSERT INTO github_service_bindings VALUES ('binding','api',7,9)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := MigrateP05AApplicationSource(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := MigrateP05AApplicationSource(ctx, db); err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}
	var selectedRef, applicationRoot, buildContext, buildStrategy, dockerfilePath string
	if err := db.QueryRowContext(ctx, `SELECT selected_ref,application_root,build_context,build_strategy,dockerfile_path FROM github_service_bindings WHERE id='binding'`).Scan(&selectedRef, &applicationRoot, &buildContext, &buildStrategy, &dockerfilePath); err != nil {
		t.Fatal(err)
	}
	if selectedRef != "release" || applicationRoot != "apps/api" || buildContext != "apps/api" || buildStrategy != "dockerfile" || dockerfilePath != "apps/api/Dockerfile" {
		t.Fatalf("legacy mapping ref=%q root=%q context=%q strategy=%q dockerfile=%q", selectedRef, applicationRoot, buildContext, buildStrategy, dockerfilePath)
	}
	if _, err := db.ExecContext(ctx, `UPDATE github_service_bindings SET application_root='other',build_context='apps' WHERE id='binding'`); err == nil {
		t.Fatal("application root outside build context was accepted")
	}
}
