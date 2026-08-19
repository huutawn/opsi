package buildjob

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	cloudpostgres "github.com/opsi-dev/opsi/cloud/internal/postgres"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
)

func TestPostgresBuildpackEvidenceFinalizesRecords(t *testing.T) {
	evidenceDir := os.Getenv("OPSI_BUILDPACK_EVIDENCE_DIR")
	registryHost := os.Getenv("OPSI_TEST_REGISTRY_HOST")
	if evidenceDir == "" || registryHost == "" {
		t.Skip("Buildpacks evidence environment is not configured")
	}
	db := newBuildJobPostgres(t)
	registry := RegistryConfig{Host: registryHost, Namespace: "opsi", RepositoryPrefix: "buildpacks", Visibility: "private"}
	for index, runtime := range []string{"node", "go", "java", "python"} {
		data, err := os.ReadFile(filepath.Join(evidenceDir, runtime+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var evidence struct {
			Runtime           string       `json:"runtime"`
			ResolvedCommitSHA string       `json:"resolved_commit_sha"`
			ApplicationRoot   string       `json:"application_root"`
			Result            RunnerResult `json:"result"`
		}
		if json.Unmarshal(data, &evidence) != nil || evidence.Runtime != runtime || !validSHA40(evidence.ResolvedCommitSHA) {
			t.Fatalf("invalid %s evidence", runtime)
		}
		job := postgresBuildJob(evidence.Result.BuildJobID, evidence.Result.BuildJobID+"-key")
		job.Source.ResolvedCommitSHA = evidence.ResolvedCommitSHA
		job.Source.ApplicationRoot = evidence.ApplicationRoot
		job.Source.BuildContext = evidence.ApplicationRoot
		job.RequestedBuildStrategy = StrategyAuto
		job.ResolvedBuildStrategy = StrategyBuildpack
		job.DockerfilePath = ""
		job, attempt, token, now := postgresRunningJob(t, db, job, uint64(2000+index))
		if evidence.Result.AttemptID != attempt.AttemptID {
			t.Fatalf("attempt=%s evidence=%s", attempt.AttemptID, evidence.Result.AttemptID)
		}
		service := Service{Store: PostgresStore{DB: db}, Executor: executorTestConfig(), Registry: registry, Now: func() time.Time { return now.Add(time.Minute) }}
		completion, err := service.Complete(context.Background(), evidence.Result, token)
		if err != nil || completion.Digest != evidence.Result.Digest || completion.BuildJobState != StatusSucceeded || completion.BuildRecordID == "" {
			t.Fatalf("runtime=%s completion=%+v err=%v", runtime, completion, err)
		}
		var digest, strategy, sha string
		var builderJSON []byte
		if err := db.QueryRow(`SELECT oci_digest,build_strategy,sha,builder_metadata FROM build_records WHERE id=$1`, completion.BuildRecordID).Scan(&digest, &strategy, &sha, &builderJSON); err != nil {
			t.Fatal(err)
		}
		var builder buildrecordv1.BuilderMetadata
		if json.Unmarshal(builderJSON, &builder) != nil || digest != evidence.Result.Digest || strategy != StrategyBuildpack || sha != evidence.ResolvedCommitSHA || builder.PackVersion == "" || builder.BuilderImageDigest == "" || builder.RunImageDigest == "" || builder.LifecycleVersion == "" || len(builder.Buildpacks) == 0 || len(builder.Processes) == 0 {
			t.Fatalf("runtime=%s digest=%s strategy=%s sha=%s builder=%+v", runtime, digest, strategy, sha, builder)
		}
		t.Logf("runtime=%s build_record_id=%s commit=%s image_digest=%s pack=%s builder=%s lifecycle=%s", runtime, completion.BuildRecordID, sha, digest, builder.PackVersion, builder.BuilderImageDigest, builder.LifecycleVersion)
	}
}

func TestPostgresBuildpackDependencyStateAndWorkerClaim(t *testing.T) {
	db := newBuildJobPostgres(t)
	job := postgresBuildJob("job-bp-dep-1", "job-bp-dep-key-1")
	job.RequestedBuildStrategy = StrategyAuto
	job.ResolvedBuildStrategy = StrategyBuildpack
	job.DockerfilePath = ""
	job.Source.BuildDependencyState = "hash-dep-state-url-a"
	job.Source.BuildEnvironment = map[string]string{
		"PUBLIC_API_ORIGIN": "https://api-buildpack-a.example.test",
	}

	job, attempt, token, now := postgresRunningJob(t, db, job, 3001)

	// In a fresh Service instance without shared in-memory state:
	freshService := Service{
		Store:    PostgresStore{DB: db},
		Executor: executorTestConfig(),
		Registry: executorTestRegistry(),
		Now:      func() time.Time { return now.Add(time.Minute) },
	}

	// Remote worker reconstructs BuildSpec from durable lease claim
	spec, err := freshService.BuildSpec(context.Background(), job.ID, token)
	if err != nil {
		t.Fatalf("BuildSpec error: %v", err)
	}
	if spec.ResolvedBuildStrategy != StrategyBuildpack || spec.BuildEnvironment["PUBLIC_API_ORIGIN"] != "https://api-buildpack-a.example.test" {
		t.Fatalf("reconstructed BuildSpec invalid: %+v", spec)
	}

	// Verify database columns
	var readDepState string
	var readEnvRaw []byte
	if err := db.QueryRow(`SELECT COALESCE(build_dependency_state,''), COALESCE(build_environment,'{}'::jsonb) FROM build_jobs WHERE id=$1`, job.ID).Scan(&readDepState, &readEnvRaw); err != nil {
		t.Fatal(err)
	}
	if readDepState != "hash-dep-state-url-a" {
		t.Fatalf("persisted build_dependency_state = %s, want hash-dep-state-url-a", readDepState)
	}
	var readEnv map[string]string
	if err := json.Unmarshal(readEnvRaw, &readEnv); err != nil || readEnv["PUBLIC_API_ORIGIN"] != "https://api-buildpack-a.example.test" {
		t.Fatalf("persisted build_environment = %+v, err = %v", readEnv, err)
	}

	// Complete build record
	result := postgresRunnerResult(t, job, attempt)
	result.Executor.Strategy = StrategyBuildpack
	result.Executor.Builder = buildrecordv1.BuilderMetadata{
		PackVersion:        "0.40.9",
		BuilderImage:       "paketobuildpacks/ubuntu-noble-builder:0.0.167@sha256:cebbe41ca97c166e10f4fc6076724df39c4e247f8ee9c81b852a9219b7a993c0",
		BuilderImageDigest: "sha256:cebbe41ca97c166e10f4fc6076724df39c4e247f8ee9c81b852a9219b7a993c0",
		RunImage:           "paketobuildpacks/ubuntu-noble-run:0.0.112@sha256:a9433b9e0b786dc2f90a433464cf7c11ede0877e30e4155a66abe35001a56d20",
		RunImageDigest:     "sha256:a9433b9e0b786dc2f90a433464cf7c11ede0877e30e4155a66abe35001a56d20",
		LifecycleVersion:   "0.21.15",
		Buildpacks:         []buildrecordv1.Buildpack{{ID: "paketo-buildpacks/node-engine", Version: "8.5.0"}},
		Processes:          []buildrecordv1.Process{{Type: "web", Command: []string{"node", "server.js"}, Direct: true, Default: true}},
	}
	completion, err := freshService.Complete(context.Background(), result, token)
	if err != nil {
		t.Fatalf("Complete error: %v", err)
	}

	var recStrategy, recDigest string
	if err := db.QueryRow(`SELECT build_strategy, oci_digest FROM build_records WHERE id=$1`, completion.BuildRecordID).Scan(&recStrategy, &recDigest); err != nil {
		t.Fatal(err)
	}
	if recStrategy != StrategyBuildpack || recDigest != result.Digest {
		t.Fatalf("unexpected build_record: strategy=%s digest=%s", recStrategy, recDigest)
	}
}

func TestPostgresBuildCompletionFinalizesOneAcceptedRecord(t *testing.T) {
	db := newBuildJobPostgres(t)
	job, attempt, token, now := postgresRunningBuild(t, db, "job-finalize")
	service := Service{Store: PostgresStore{DB: db}, Executor: executorTestConfig(), Registry: executorTestRegistry(), Now: func() time.Time { return now.Add(time.Minute) }}
	result := postgresRunnerResult(t, job, attempt)

	const callers = 8
	var wait sync.WaitGroup
	responses := make(chan CompletionResult, callers)
	errs := make(chan error, callers)
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response, err := service.Complete(context.Background(), result, token)
			responses <- response
			errs <- err
		}()
	}
	wait.Wait()
	close(responses)
	close(errs)
	reused := 0
	var recordID string
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for response := range responses {
		if response.Reused {
			reused++
		}
		if recordID == "" {
			recordID = response.BuildRecordID
		}
		if response.BuildRecordID != recordID || response.Digest != result.Digest || response.BuildJobState != StatusSucceeded {
			t.Fatalf("response=%+v", response)
		}
	}
	if reused != callers-1 {
		t.Fatalf("reused=%d", reused)
	}
	stored, err := PostgresStore{DB: db}.Get(context.Background(), job.ProjectID, job.ApplicationID, job.ID)
	if err != nil || stored.Status != StatusSucceeded || stored.BuildRecordID != recordID || stored.CompletedAt == nil {
		t.Fatalf("job=%+v err=%v", stored, err)
	}
	var buildRecordDigest, buildJobID, repository string
	if err := db.QueryRow(`SELECT oci_digest,build_job_id,oci_repository FROM build_records WHERE id=$1`, recordID).Scan(&buildRecordDigest, &buildJobID, &repository); err != nil {
		t.Fatal(err)
	}
	if result.Executor.BuildDescriptor.Digest != result.Executor.Remote.Descriptor.Digest || result.Digest != buildRecordDigest || buildJobID != job.ID || repository != executorTestRegistry().Target(job.ApplicationID, job.ID).Repository {
		t.Fatalf("buildkit=%s remote=%s record=%s build_job=%s repository=%s", result.Executor.BuildDescriptor.Digest, result.Executor.Remote.Descriptor.Digest, buildRecordDigest, buildJobID, repository)
	}
	t.Logf("buildkit_digest=%s remote_digest=%s build_record_digest=%s build_record_id=%s build_job_state=%s", result.Executor.BuildDescriptor.Digest, result.Executor.Remote.Descriptor.Digest, buildRecordDigest, recordID, stored.Status)
	var persisted string
	if err := db.QueryRow(`SELECT row_to_json(j)::text || row_to_json(r)::text || COALESCE((SELECT string_agg(metadata_redacted::text,'') FROM cloud_audit_events WHERE resource_id=r.id),'') FROM build_jobs j JOIN build_records r ON r.id=j.build_record_id WHERE j.id=$1`, job.ID).Scan(&persisted); err != nil || strings.Contains(persisted, token) || queryCount(t, db, `SELECT count(*) FROM cloud_audit_events WHERE action='BUILD_RECORD_FINALIZED' AND resource_id=$1`, recordID) != 1 {
		t.Fatalf("credential persisted=%v audit_count_invalid=%v err=%v", strings.Contains(persisted, token), queryCount(t, db, `SELECT count(*) FROM cloud_audit_events WHERE action='BUILD_RECORD_FINALIZED' AND resource_id=$1`, recordID) != 1, err)
	}
	conflict := result
	conflict.Executor.Remote.Manifest = []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","size":2},"layers":[]}`)
	conflictSum := sha256.Sum256(conflict.Executor.Remote.Manifest)
	conflict.Digest = "sha256:" + fmt.Sprintf("%x", conflictSum[:])
	conflict.RegistryReference = executorTestRegistry().Target(job.ApplicationID, job.ID).DigestReference(conflict.Digest)
	conflict.Executor.BuildDescriptor.Digest = conflict.Digest
	conflict.Executor.Remote.Descriptor.Digest = conflict.Digest
	if _, err := service.Complete(context.Background(), conflict, token); Code(err) != "BUILD_RESULT_CONFLICT" {
		t.Fatalf("conflict err=%v", err)
	}
	if count := queryCount(t, db, `SELECT count(*) FROM build_records WHERE build_job_id=$1`, job.ID); count != 1 {
		t.Fatalf("records=%d", count)
	}
}

func TestPostgresBuildCompletionCancellationRace(t *testing.T) {
	for iteration := range 12 {
		db := newBuildJobPostgres(t)
		job, attempt, token, now := postgresRunningBuild(t, db, fmt.Sprintf("job-race-%d", iteration))
		service := Service{Store: PostgresStore{DB: db}, Executor: executorTestConfig(), Registry: executorTestRegistry(), Now: func() time.Time { return now.Add(time.Minute) }}
		start := make(chan struct{})
		completionErr := make(chan error, 1)
		cancelErr := make(chan error, 1)
		go func() {
			<-start
			_, err := service.Complete(context.Background(), postgresRunnerResult(t, job, attempt), token)
			completionErr <- err
		}()
		go func() {
			<-start
			_, err := db.Exec(`UPDATE build_jobs SET status='cancelled',updated_at=$2 WHERE id=$1 AND status='running'`, job.ID, now.Add(time.Minute))
			cancelErr <- err
		}()
		close(start)
		completeResult := <-completionErr
		cancelResult := <-cancelErr
		stored, err := PostgresStore{DB: db}.Get(context.Background(), job.ProjectID, job.ApplicationID, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		records := queryCount(t, db, `SELECT count(*) FROM build_records WHERE build_job_id=$1`, job.ID)
		switch stored.Status {
		case StatusSucceeded:
			if completeResult != nil || cancelResult == nil || records != 1 || stored.BuildRecordID == "" {
				t.Fatalf("winner=completion job=%+v complete=%v cancel=%v records=%d", stored, completeResult, cancelResult, records)
			}
		case StatusCancelled:
			if Code(completeResult) != "RUNNER_LEASE_REVOKED" || cancelResult != nil || records != 0 || stored.BuildRecordID != "" {
				t.Fatalf("winner=cancellation job=%+v complete=%v cancel=%v records=%d", stored, completeResult, cancelResult, records)
			}
		default:
			t.Fatalf("nonterminal race result=%+v", stored)
		}
	}
}

func TestPostgresBuildRunnerFailureDoesNotCreateRecord(t *testing.T) {
	db := newBuildJobPostgres(t)
	job, attempt, token, now := postgresRunningBuild(t, db, "job-failed")
	service := Service{Store: PostgresStore{DB: db}, Now: func() time.Time { return now.Add(time.Minute) }}
	if err := service.Fail(context.Background(), RunnerFailure{BuildJobID: job.ID, AttemptID: attempt.AttemptID, Code: "REGISTRY_PUSH_FAILED"}, token); err != nil {
		t.Fatal(err)
	}
	stored, err := PostgresStore{DB: db}.Get(context.Background(), job.ProjectID, job.ApplicationID, job.ID)
	if err != nil || stored.Status != StatusFailed || stored.FailureCode != "REGISTRY_PUSH_FAILED" || queryCount(t, db, `SELECT count(*) FROM build_records WHERE build_job_id=$1`, job.ID) != 0 {
		t.Fatalf("job=%+v err=%v", stored, err)
	}
}

func TestPostgresBuildExecutorAtomicClaimAndScopedLease(t *testing.T) {
	db := newBuildJobPostgres(t)
	store := PostgresStore{DB: db}
	job := postgresBuildJob("job-executor", "executor-key")
	if _, _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(200, 0).UTC()
	attempt := DispatchAttempt{Provider: ExecutorProviderGitHubActions, AttemptID: "attempt-1", BuildJobID: job.ID, Workflow: executorTestConfig().Workflow, WorkflowRef: executorTestConfig().WorkflowRef(), ExecutorRef: executorTestConfig().Ref, DispatchedAt: now, LastState: DispatchStateDispatching}
	if err := store.ReserveDispatch(context.Background(), job.ProjectID, job.ApplicationID, attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteDispatch(context.Background(), attempt.AttemptID, DispatchFacts{}, now); err != nil {
		t.Fatal(err)
	}
	token := "postgres-runner-lease"
	hash := sha256.Sum256([]byte(token))
	identity := executorTestIdentity(99)
	var wait sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs <- store.ClaimDispatch(context.Background(), job.ID, attempt.AttemptID, identity, hash[:], now.Add(10*time.Minute), now)
		}()
	}
	wait.Wait()
	close(errs)
	winners := 0
	for err := range errs {
		if err == nil {
			winners++
			continue
		}
		if Code(err) != "RUNNER_CLAIM_CONSUMED" {
			t.Fatalf("claim err=%v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners=%d", winners)
	}
	stored, err := store.Get(context.Background(), job.ProjectID, job.ApplicationID, job.ID)
	if err != nil || stored.Status != StatusRunning {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	runnerJob, err := store.GetRunnerJob(context.Background(), RunnerAccess{JobID: job.ID, AttemptID: attempt.AttemptID, RunID: identity.RunID, RunAttempt: identity.RunAttempt, LeaseHash: hash[:]}, now.Add(time.Minute))
	if err != nil || runnerJob.ID != job.ID || runnerJob.Source.ResolvedCommitSHA != job.Source.ResolvedCommitSHA {
		t.Fatalf("job=%+v err=%v", runnerJob, err)
	}
	if _, err := store.GetRunnerJob(context.Background(), RunnerAccess{JobID: "another-job", LeaseHash: hash[:]}, now.Add(time.Minute)); Code(err) != "RUNNER_LEASE_SCOPE_MISMATCH" {
		t.Fatalf("scope err=%v", err)
	}
	if _, err := store.GetRunnerJob(context.Background(), RunnerAccess{JobID: job.ID, LeaseHash: hash[:]}, now.Add(10*time.Minute)); Code(err) != "RUNNER_LEASE_EXPIRED" {
		t.Fatalf("expiry err=%v", err)
	}
	var storedHash []byte
	if err := db.QueryRow(`SELECT lease_token_hash FROM build_executor_attempts WHERE attempt_id=$1`, attempt.AttemptID).Scan(&storedHash); err != nil || string(storedHash) == token || len(storedHash) != sha256.Size {
		t.Fatalf("stored hash len=%d err=%v", len(storedHash), err)
	}
}

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
		`INSERT INTO environments(id,org_id,project_id,name,type) VALUES('environment-1','org-1','project-1','dev','dev')`,
		`INSERT INTO runtimes(id,org_id,project_id,environment_id,name) VALUES('runtime-1','org-1','project-1','environment-1','runtime')`,
		`INSERT INTO control_services(id,org_id,project_id,environment_id,runtime_id,name,type,status,source_type,namespace) VALUES('application-1','org-1','project-1','environment-1','runtime-1','api','backend','ready','git','opsi')`,
		`INSERT INTO github_installations(installation_id,account_id,account_login,account_type,status,suspended,created_at,updated_at) VALUES(10,30,'owner','Organization','active',false,now(),now())`,
		`INSERT INTO github_repositories(repository_id,installation_id,owner_id,owner_login,name,full_name,private,archived,disabled,default_branch,status,created_at,updated_at) VALUES(20,10,30,'owner','repository','owner/repository',true,false,false,'main','active',now(),now())`,
		`INSERT INTO github_repository_claims(repository_id,installation_id,project_id,claimed_by,status,claimed_at) VALUES(20,10,'project-1','user-1','active',now())`,
		`INSERT INTO github_service_bindings(id,project_id,service_id,repository_id,installation_id,service_key,config_path,selected_ref,application_root,build_context,build_strategy,dockerfile_path,status,created_by,created_at,updated_at) VALUES('binding-1','project-1','application-1',20,10,'api','.opsi/opsi-cd.yaml','main','apps/api','.','auto','apps/api/Dockerfile','active','user-1',to_timestamp(50),to_timestamp(50))`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func postgresRunningBuild(t *testing.T, db *sql.DB, id string) (Job, DispatchAttempt, string, time.Time) {
	t.Helper()
	job := postgresBuildJob(id, id+"-key")
	return postgresRunningJob(t, db, job, uint64(1000+len(id)))
}

func postgresRunningJob(t *testing.T, db *sql.DB, job Job, runID uint64) (Job, DispatchAttempt, string, time.Time) {
	t.Helper()
	store := PostgresStore{DB: db}
	if _, _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(200, 0).UTC()
	attempt := DispatchAttempt{Provider: ExecutorProviderGitHubActions, AttemptID: job.ID + "-attempt", BuildJobID: job.ID, Workflow: executorTestConfig().Workflow, WorkflowRef: executorTestConfig().WorkflowRef(), ExecutorRef: executorTestConfig().Ref, DispatchedAt: now, LastState: DispatchStateDispatching}
	if err := store.ReserveDispatch(context.Background(), job.ProjectID, job.ApplicationID, attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteDispatch(context.Background(), attempt.AttemptID, DispatchFacts{}, now); err != nil {
		t.Fatal(err)
	}
	token := "lease-" + job.ID
	hash := sha256.Sum256([]byte(token))
	identity := executorTestIdentity(runID)
	if err := store.ClaimDispatch(context.Background(), job.ID, attempt.AttemptID, identity, hash[:], now.Add(10*time.Minute), now); err != nil {
		t.Fatal(err)
	}
	attempt.RunID = identity.RunID
	attempt.RunAttempt = identity.RunAttempt
	return job, attempt, token, now
}

func postgresRunnerResult(t *testing.T, job Job, attempt DispatchAttempt) RunnerResult {
	t.Helper()
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":2},"layers":[]}`)
	if path := os.Getenv("OPSI_TEST_REGISTRY_MANIFEST_FILE"); path != "" {
		var err error
		manifest, err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	sum := sha256.Sum256(manifest)
	digest := "sha256:" + fmt.Sprintf("%x", sum[:])
	var document struct {
		MediaType string `json:"mediaType"`
	}
	if json.Unmarshal(manifest, &document) != nil || document.MediaType == "" {
		t.Fatal("registry manifest is invalid")
	}
	descriptor := ImageDescriptor{Digest: digest, MediaType: document.MediaType, Size: int64(len(manifest))}
	target := executorTestRegistry().Target(job.ApplicationID, job.ID)
	return RunnerResult{BuildJobID: job.ID, AttemptID: attempt.AttemptID, RegistryReference: target.DigestReference(digest), Digest: digest, Executor: ExecutorResult{Strategy: StrategyDockerfile, Platform: "linux/amd64", BuildKitVersion: "v0.32.2", BuildxVersion: "v0.36.1", BuilderIdentity: "moby/buildkit:v0.32.2@sha256:28a898719c18a33f4e8000685287fa36fd0dd9560c6440227d3a732d79bb41d8", StartedAt: time.Unix(210, 0).UTC(), CompletedAt: time.Unix(220, 0).UTC(), BuildDescriptor: descriptor, Remote: RemoteRegistryEvidence{Descriptor: descriptor, Platform: "linux/amd64", Manifest: manifest, Private: true}}}
}

func queryCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
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
