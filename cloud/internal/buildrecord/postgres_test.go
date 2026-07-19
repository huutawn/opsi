package buildrecord

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
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
)

func TestPostgresBuildRecordRestartConcurrencyAndAppendOnly(t *testing.T) {
	db := newBuildRecordPostgres(t)
	store := PostgresStore{DB: db}
	record := postgresBuildRecord()
	var wait sync.WaitGroup
	results := make(chan bool, 8)
	errs := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, reused, err := store.Create(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", record)
			results <- reused
			errs <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errs)
	created, reused := 0, 0
	for value := range results {
		if value {
			reused++
		} else {
			created++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if created != 1 || reused != 7 {
		t.Fatalf("created=%d reused=%d", created, reused)
	}
	wrongOwner := record
	wrongOwner.ID = "br-wrong-owner"
	wrongOwner.RepositoryOwnerID = 9
	wrongOwner.Workload.RepositoryOwnerID = 9
	wrongOwner.Workload.RunID = 100
	if _, _, err := store.Create(context.Background(), strings.Repeat("e", 64), wrongOwner); err == nil {
		t.Fatal("BuildRecord bypassed stored repository owner identity")
	}
	restarted := PostgresStore{DB: db}
	got, err := restarted.Get(context.Background(), "project-1", record.ID)
	if err != nil || got.Build.OCIDigest != record.Build.OCIDigest {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	list, err := restarted.List(context.Background(), "project-1", ListFilter{Limit: 50})
	if err != nil || len(list.Records) != 1 {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	conflict := record
	conflict.Build.OCIDigest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	if _, _, err := restarted.Create(context.Background(), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", conflict); err == nil || err.(Error).Code != "BUILD_RECORD_CONFLICT" {
		t.Fatalf("conflict=%v", err)
	}
	if _, err := db.Exec(`UPDATE build_records SET build_status='succeeded' WHERE id=$1`, record.ID); err == nil {
		t.Fatal("BuildRecord update passed append-only trigger")
	}
	if _, err := db.Exec(`DELETE FROM build_records WHERE id=$1`, record.ID); err == nil {
		t.Fatal("BuildRecord delete passed append-only trigger")
	}
	var sensitive int
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='build_records' AND column_name ~ '(token|jwt|claims_json|body|logs)'`).Scan(&sensitive); err != nil || sensitive != 0 {
		t.Fatalf("sensitive columns=%d err=%v", sensitive, err)
	}
	if _, err := db.Exec(`UPDATE github_service_bindings SET status='revoked', removed_at=now() WHERE id='binding-1'`); err != nil {
		t.Fatal(err)
	}
	got, err = restarted.Get(context.Background(), "project-1", record.ID)
	if err != nil || got.ActiveBindingID != "binding-1" {
		t.Fatalf("historical record changed after binding removal: got=%+v err=%v", got, err)
	}
}

func newBuildRecordPostgres(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("OPSI_TEST_DATABASE_URL")
	if dsn == "" {
		message := "set OPSI_TEST_DATABASE_URL to run Postgres BuildRecord tests"
		if os.Getenv("OPSI_REQUIRE_POSTGRES_TESTS") == "1" {
			t.Fatal(message)
		}
		t.Skip(message)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("r5_007_build_record_%d", time.Now().UnixNano())
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
	if err := postgres.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO users(id,email) VALUES('user-1','user@example.test')`,
		`INSERT INTO organizations(id,name,slug) VALUES('org-1','Org','org')`,
		`INSERT INTO projects(id,org_id,name,slug,status,created_by) VALUES('project-1','org-1','Project','project','ready','user-1')`,
		`INSERT INTO environments(id,org_id,project_id,name,type) VALUES('env-1','org-1','project-1','dev','dev')`,
		`INSERT INTO runtimes(id,org_id,project_id,environment_id,name) VALUES('runtime-1','org-1','project-1','env-1','runtime')`,
		`INSERT INTO control_services(id,org_id,project_id,environment_id,runtime_id,name,type,status,source_type,namespace) VALUES('service-1','org-1','project-1','env-1','runtime-1','api','backend','ready','git','opsi')`,
		`INSERT INTO github_installations(installation_id,account_id,account_login,account_type,status,suspended,created_at,updated_at) VALUES(100,200,'huutawn','User','active',false,now(),now())`,
		`INSERT INTO github_repositories(repository_id,installation_id,owner_id,owner_login,name,full_name,private,archived,disabled,default_branch,status,created_at,updated_at) VALUES(7,100,8,'huutawn','opsi','huutawn/opsi',false,false,false,'developer','active',now(),now())`,
		`INSERT INTO github_service_bindings(id,project_id,service_id,repository_id,installation_id,service_key,config_path,status,created_by,created_at,updated_at) VALUES('binding-1','project-1','service-1',7,100,'api','.opsi/opsi-cd.yaml','active','user-1',now(),now())`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func postgresBuildRecord() buildrecordv1.Record {
	return buildrecordv1.Record{SchemaVersion: buildrecordv1.SchemaVersion, ID: "br-postgres", ProjectID: "project-1", RepositoryID: 7, RepositoryOwnerID: 8, ActiveBindingID: "binding-1", ServiceID: "service-1", ServiceKey: "api", CreatedAt: time.Unix(100, 0).UTC(), Workload: buildrecordv1.WorkloadIdentity{Issuer: "https://token.actions.githubusercontent.com", Subject: "repo:huutawn/opsi:ref:refs/heads/developer", RepositoryID: 7, RepositoryOwnerID: 8, Ref: "refs/heads/developer", SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", EventName: "push", Workflow: "opsi-cd", WorkflowRef: "huutawn/opsi/.github/workflows/opsi-cd.yaml@refs/heads/developer", RunID: 99, RunAttempt: 1}, Build: buildrecordv1.BuildMetadata{ConfigHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Platform: "linux/amd64", OCIRepository: "ghcr.io/huutawn/opsi/api", OCIDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Status: "succeeded"}}
}
