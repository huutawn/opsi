package registry

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

func TestPostgresDeploymentJobRestartRetryDeadLetterAndIdempotency(t *testing.T) {
	dsn := requirePostgresTestDSN(t, "registry durability")
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := postgres.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	suffix := strings.ToLower(newID("dur"))
	orgID, userID := "org-"+suffix, "user-"+suffix
	_, _ = db.ExecContext(context.Background(), `DELETE FROM organizations WHERE id=$1`, orgID)
	if _, err := db.ExecContext(context.Background(), `INSERT INTO users(id,email) VALUES($1,$2)`, userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO organizations(id,name,slug) VALUES($1,'Durable Relay',$2)`, orgID, "durable-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
	})

	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	fresh := func() PostgresService { return PostgresService{DB: db, Now: func() time.Time { return now }} }
	service := fresh()
	project, err := service.CreateProject(orgID, "Durable Relay", "durable-"+suffix, userID, "project-key")
	if err != nil {
		t.Fatal(err)
	}
	node, err := service.UpsertNode(project.ID, "server-1", "server", NodeHealthy, "203.0.113.44", "", "node-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegisterAgent(project.ID, node.ID, "sha256:test", "hash", "v1", "agent-key", map[string]any{"deploy": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordAgentHeartbeat(project.ID, node.ID, AgentHeartbeat{Version: "v1", NodeReady: true, K3SStatus: "ready", Capabilities: map[string]any{"deploy": true}}); err != nil {
		t.Fatal(err)
	}
	record, err := service.CreateService(project.ID, ServiceDraft{Name: "api", Type: "application", SourceType: "git", RepoURL: "https://example.test/repo.git", Branch: "main", GitSHA: "0123456789abcdef", BuildContext: "services/api", Dockerfile: "Dockerfile", ManifestPath: "deploy/api.yaml"}, "service-key")
	if err != nil {
		t.Fatal(err)
	}

	job, err := service.StartDeployment(project.ID, record.ID, userID, "deploy-key", "req-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE deployment_jobs SET max_attempts=2 WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}

	restarted := fresh()
	again, err := restarted.StartDeployment(project.ID, record.ID, userID, "deploy-key", "req-idempotent")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != job.ID {
		t.Fatalf("idempotency did not survive restart: %s != %s", again.ID, job.ID)
	}
	deployments, err := restarted.ListDeployments(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deployments) != 1 || deployments[0].Status != DeploymentQueued {
		t.Fatalf("queued job not recovered after restart: %+v", deployments)
	}

	first, ok, err := restarted.LeaseDeployment(project.ID, node.ID)
	if err != nil || !ok {
		t.Fatalf("first lease ok=%v err=%v", ok, err)
	}
	if first.Deployment.AttemptCount != 1 || first.Deployment.LeaseExpiresAt == nil {
		t.Fatalf("first lease state not persisted: %+v", first.Deployment)
	}
	afterLease := fresh()
	persisted, err := afterLease.ListDeployments(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted[0].AttemptCount != 1 || persisted[0].LeaseExpiresAt == nil || !persisted[0].LeaseExpiresAt.Equal(*first.Deployment.LeaseExpiresAt) {
		t.Fatalf("attempt/next retry time lost after restart: got %+v want lease_exp=%v", persisted[0], first.Deployment.LeaseExpiresAt)
	}

	now = now.Add(defaultDeploymentLeaseDuration + time.Second)
	second, ok, err := fresh().LeaseDeployment(project.ID, node.ID)
	if err != nil || !ok {
		t.Fatalf("retry lease ok=%v err=%v", ok, err)
	}
	if second.LeaseToken == first.LeaseToken || second.Deployment.AttemptCount != 2 {
		t.Fatalf("retry state not durable: first=%+v second=%+v", first.Deployment, second.Deployment)
	}

	now = now.Add(defaultDeploymentLeaseDuration + time.Second)
	if _, ok, err := fresh().LeaseDeployment(project.ID, node.ID); err != nil || ok {
		t.Fatalf("dead-letter lease ok=%v err=%v", ok, err)
	}
	finalJobs, err := fresh().ListDeployments(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	final := finalJobs[0]
	if final.Status != DeploymentDeadLetter || final.AttemptCount != 2 || final.FailureCode != "DEPLOYMENT_LEASE_ATTEMPTS_EXHAUSTED" || final.FinishedAt == nil {
		t.Fatalf("terminal failure not durable: %+v", final)
	}
	events, err := fresh().DeploymentEvents(project.ID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasDeploymentStep(events, EventAgentLeaseExpired) || !hasDeploymentStep(events, EventDeploymentDeadLetter) {
		t.Fatalf("restart-visible transition evidence missing: %+v", events)
	}
	audit, err := fresh().ListAudit(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasAuditAction(audit, "DEPLOYMENT_RETRY_SCHEDULED") || !hasAuditAction(audit, "DEPLOYMENT_DEAD_LETTERED") {
		t.Fatalf("restart-visible audit evidence missing: %+v", audit)
	}
}

func TestPostgresImmutableDeploymentSnapshotAndEventsSurviveRestart(t *testing.T) {
	dsn := requirePostgresTestDSN(t, "immutable deployment durability")
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	suffix := strings.ToLower(newID("immutable"))
	orgID, userID := "org-"+suffix, "user-"+suffix
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email) VALUES($1,$2)`, userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO organizations(id,name,slug) VALUES($1,$2,$3)`, orgID, "Immutable", "immutable-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = db.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	now := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	fresh := func() PostgresService { return PostgresService{DB: db, Now: func() time.Time { return now }} }
	service := fresh()
	project, err := service.CreateProject(orgID, "Immutable", "immutable-"+suffix, userID, "project-key")
	if err != nil {
		t.Fatal(err)
	}
	record, snapshot := postgresImmutableSnapshot(t, service, project.ID, suffix)
	job, reused, err := service.StartImmutableDeployment(snapshot, userID, "immutable-key", "request-1")
	if err != nil || reused || job.Status != deploymentv1.StateQueued {
		t.Fatalf("create immutable job=%+v reused=%v err=%v", job, reused, err)
	}
	restarted := fresh()
	replay, reused, err := restarted.StartImmutableDeployment(snapshot, userID, "immutable-key", "request-replay")
	if err != nil || !reused || replay.ID != job.ID {
		t.Fatalf("immutable replay=%+v reused=%v err=%v", replay, reused, err)
	}
	jobs, err := restarted.ListDeployments(project.ID)
	if err != nil || len(jobs) != 1 || jobs[0].Snapshot == nil || jobs[0].Mode != "immutable_image" {
		t.Fatalf("immutable snapshot did not survive restart: jobs=%+v err=%v", jobs, err)
	}
	lease, ok, err := restarted.LeaseDeployment(project.ID, job.NodeID)
	if err != nil || !ok || lease.Command == nil || lease.Deployment.AttemptCount != 1 {
		t.Fatalf("immutable lease=%+v ok=%v err=%v", lease, ok, err)
	}
	events, err := restarted.DeploymentEvents(project.ID, job.ID)
	if err != nil || len(events) < 2 {
		t.Fatalf("immutable events=%+v err=%v", events, err)
	}
	for _, event := range events {
		if event.SchemaVersion != deploymentv1.EventSchemaVersion {
			t.Fatalf("immutable Postgres event omitted version: %+v", event)
		}
	}
	if record.ID != job.ServiceID || lease.Command.Image.Reference != snapshot.Image.Reference {
		t.Fatalf("immutable identity drifted: record=%+v job=%+v command=%+v", record, job, lease.Command)
	}
}

func postgresImmutableSnapshot(t *testing.T, service PostgresService, projectID, suffix string) (ServiceRecord, deploymentv1.JobSnapshot) {
	t.Helper()
	node, err := service.UpsertNode(projectID, "server-"+suffix, "server", NodeHealthy, "203.0.113.77", "", "node-key")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := service.RegisterAgent(projectID, node.ID, "sha256:immutable", "immutable", "v1", "agent-key", map[string]any{"deploy": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordAgentHeartbeat(projectID, node.ID, AgentHeartbeat{Version: "v1", NodeReady: true, K3SStatus: "ready", Capabilities: map[string]any{"deploy": true}}); err != nil {
		t.Fatal(err)
	}
	record, err := service.CreateService(projectID, ServiceDraft{Name: "api-" + suffix, Type: "application", SourceType: "git", RepoURL: "https://example.test/repo.git", Branch: "main", GitSHA: strings.Repeat("a", 40), BuildContext: "services/api", Dockerfile: "Dockerfile", ManifestPath: "deploy/api.yaml"}, "service-key")
	if err != nil {
		t.Fatal(err)
	}
	image, err := deploymentv1.NewImmutableImage("ghcr.io/example/api", "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	spec := deploymentv1.WorkloadSpec{SchemaVersion: deploymentv1.WorkloadSchemaVersion, ServiceKey: record.Name, Replicas: 1, ApplicationContainerName: deploymentv1.ApplicationContainer, ContainerPort: 8080, Resources: deploymentv1.Resources{Requests: deploymentv1.ResourceValues{CPU: "100m", Memory: "128Mi"}, Limits: deploymentv1.ResourceValues{CPU: "500m", Memory: "512Mi"}}, TerminationGracePeriodSecond: 30, Exposure: deploymentv1.ExposureIntent{Mode: "internal"}}
	specHash, err := spec.Hash()
	if err != nil {
		t.Fatal(err)
	}
	return record, deploymentv1.JobSnapshot{SchemaVersion: deploymentv1.JobSchemaVersion, ProjectID: projectID, Image: image, Workload: spec, SpecHash: specHash, PayloadHash: "payload-" + suffix, Authority: deploymentv1.AuthoritySnapshot{BuildRecord: buildrecordv1.Record{SchemaVersion: buildrecordv1.SchemaVersion, ID: "br-" + suffix, ProjectID: projectID, ServiceID: record.ID, ServiceKey: record.Name, ActiveBindingID: "binding-" + suffix, Build: buildrecordv1.BuildMetadata{OCIRepository: image.Repository, OCIDigest: image.Digest, Status: "succeeded"}}, EnvironmentID: record.EnvironmentID, RuntimeID: record.RuntimeID, NodeID: node.ID, AgentID: agent.ID}}
}

func hasDeploymentStep(events []DeploymentEvent, step string) bool {
	for _, event := range events {
		if event.Step == step {
			return true
		}
	}
	return false
}

func hasAuditAction(events []AuditEvent, action string) bool {
	for _, event := range events {
		if event.Action == action {
			return true
		}
	}
	return false
}
