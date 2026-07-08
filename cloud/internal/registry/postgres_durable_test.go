package registry

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
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
