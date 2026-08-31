package auth

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	cloudpostgres "github.com/opsi-dev/opsi/cloud/internal/postgres"
)

func TestPostgresStoreProvisionOAuthUserCreatesPersonalWorkspaceIdempotently(t *testing.T) {
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("OPSI_REQUIRE_POSTGRES_TESTS") == "1" {
			t.Fatal("set OPSI_TEST_DATABASE_URL to run Postgres auth test")
		}
		t.Skip("set OPSI_TEST_DATABASE_URL to run Postgres auth test")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("auth_oauth_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(t.Context(), `CREATE SCHEMA `+schema); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		_ = admin.Close()
	})
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
	t.Cleanup(func() { _ = db.Close() })
	if err := cloudpostgres.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	store := PostgresStore{DB: db, Now: func() time.Time { return now }}
	userID, err := store.ProvisionOAuthUser(t.Context(), "github", "143307746")
	if err != nil || userID == "" {
		t.Fatalf("userID=%q err=%v", userID, err)
	}
	reusedUserID, err := store.ProvisionOAuthUser(t.Context(), "github", "143307746")
	if err != nil || reusedUserID != userID {
		t.Fatalf("reusedUserID=%q first=%q err=%v", reusedUserID, userID, err)
	}
	projects, err := store.UserProjectCandidates(t.Context(), userID)
	if err != nil || len(projects) != 1 || projects[0].Role != "owner" {
		t.Fatalf("projects=%+v err=%v", projects, err)
	}
	resolved, err := store.OAuthUser(t.Context(), "github", "143307746")
	if err != nil || resolved != userID {
		t.Fatalf("resolved=%q userID=%q err=%v", resolved, userID, err)
	}
}
