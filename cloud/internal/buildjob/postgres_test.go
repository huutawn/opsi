package buildjob

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	cloudpostgres "github.com/opsi-dev/opsi/cloud/internal/postgres"
)

func TestPostgresBuildJobIdempotencyImmutabilityAndQueryableSchema(t *testing.T) {
	db := newBuildJobPostgres(t)
	store := PostgresStore{DB: db}
	job := postgresBuildJob("job-1", "same-key")
	var wait sync.WaitGroup
	results := make(chan Job, 8)
	errs := make(chan error, 8)
	for index := range 8 {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			candidate := job
			candidate.ID = fmt.Sprintf("job-%d", index+1)
			stored, _, err := store.Create(context.Background(), candidate)
			results <- stored
			errs <- err
		}(index)
	}
	wait.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var storedID string
	for stored := range results {
		if storedID == "" {
			storedID = stored.ID
		}
		if stored.ID != storedID || stored.Source.ResolvedCommitSHA != job.Source.ResolvedCommitSHA {
			t.Fatalf("stored=%+v", stored)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM build_jobs WHERE project_id='project-1' AND application_id='application-1'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if _, err := db.Exec(`UPDATE build_jobs SET resolved_commit_sha=$1 WHERE id=$2`, strings.Repeat("b", 40), storedID); err == nil {
		t.Fatal("immutable commit SHA was updated")
	}
	read, err := store.Get(context.Background(), "project-1", "application-1", storedID)
	if err != nil || read.Source.ResolvedCommitSHA != strings.Repeat("a", 40) || read.Source.BuildContext != "." {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	distinct := postgresBuildJob("job-distinct", "different-key")
	if created, reused, err := store.Create(context.Background(), distinct); err != nil || reused || created.ID != distinct.ID {
		t.Fatalf("created=%+v reused=%v err=%v", created, reused, err)
	}
	jobs, err := store.List(context.Background(), "project-1", "application-1", StatusReady, 50)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
	rows, err := db.Query(`SELECT column_name FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='build_jobs'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(column, "token") || strings.Contains(column, "password") || strings.Contains(column, "private_key") {
			t.Fatalf("credential column persisted in BuildJob schema: %s", column)
		}
	}
}

func newBuildJobPostgres(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		message := "set OPSI_TEST_DATABASE_URL to run Postgres BuildJob tests"
		if os.Getenv("OPSI_REQUIRE_POSTGRES_TESTS") == "1" {
			t.Fatal(message)
		}
		t.Skip(message)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("p05b1_build_job_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`); _ = admin.Close() })
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
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	if err := cloudpostgres.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := cloudpostgres.Migrate(context.Background(), db); err != nil {
		t.Fatalf("migration is not idempotent: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO users(id,email) VALUES('user-1','user@example.test')`,
		`INSERT INTO organizations(id,name,slug) VALUES('org-1','Org','org')`,
		`INSERT INTO projects(id,org_id,name,slug,status,created_by) VALUES('project-1','org-1','Project','project','ready','user-1')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func postgresBuildJob(id, key string) Job {
	now := time.Unix(100, 0).UTC()
	return Job{
		ID: id, ProjectID: "project-1", EnvironmentID: "environment-1", ApplicationID: "application-1",
		Source:                 SourceSnapshot{BindingID: "binding-1", BindingUpdatedAt: time.Unix(50, 0).UTC(), InstallationID: 10, RepositoryID: 20, RepositoryOwnerID: 30, RepositoryFullName: "owner/repository", SelectedRef: "main", ResolvedCommitSHA: strings.Repeat("a", 40), ApplicationRoot: "apps/api", BuildContext: "."},
		RequestedBuildStrategy: StrategyAuto, ResolvedBuildStrategy: StrategyDockerfile, DockerfilePath: "apps/api/Dockerfile",
		Status: StatusReady, CreatedBy: "user-1", IdempotencyKey: key, CreatedAt: now, UpdatedAt: now,
	}
}
