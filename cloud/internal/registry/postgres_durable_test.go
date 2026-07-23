package registry

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

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

func TestPostgresLegacyDeploymentIsRetiredWithoutBlockingCanonicalLease(t *testing.T) {
	dsn := requirePostgresTestDSN(t, "legacy deployment retirement")
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	suffix := strings.ToLower(newID("retiredpg"))
	orgID, userID := "org-"+suffix, "user-"+suffix
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email) VALUES($1,$2)`, userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO organizations(id,name,slug) VALUES($1,$2,$3)`, orgID, "Retired", "retired-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = db.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	now := time.Date(2026, 7, 23, 11, 0, 0, 0, time.UTC)
	service := PostgresService{DB: db, Now: func() time.Time { return now }}
	project, err := service.CreateProject(orgID, "Retired", "retired-"+suffix, userID, "project-key")
	if err != nil {
		t.Fatal(err)
	}
	record, snapshot := postgresImmutableSnapshot(t, service, project.ID, suffix)
	canonical, _, err := service.StartImmutableDeployment(snapshot, userID, "canonical-key", "canonical-request")
	if err != nil {
		t.Fatal(err)
	}
	legacyID := "dep-legacy-" + suffix
	if _, err := db.ExecContext(ctx, `INSERT INTO deployment_jobs(id,org_id,project_id,environment_id,runtime_id,service_id,status,idempotency_key,requested_by,agent_id,node_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)`, legacyID, orgID, project.ID, record.EnvironmentID, record.RuntimeID, record.ID, DeploymentQueued, "legacy-key", userID, snapshot.Authority.AgentID, snapshot.Authority.NodeID, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	lease, ok, err := service.LeaseDeployment(project.ID, snapshot.Authority.NodeID)
	if err != nil || !ok {
		t.Fatalf("canonical lease ok=%v err=%v", ok, err)
	}
	if lease.Deployment.ID != canonical.ID || lease.Command == nil {
		t.Fatalf("legacy job reached Agent or canonical command missing: %+v", lease)
	}
	retired, err := service.GetDeployment(project.ID, legacyID)
	if err != nil || retired.Status != DeploymentFailed || retired.FailureCode != LegacyDeploymentRetired || retired.FinishedAt == nil {
		t.Fatalf("retired legacy job=%+v err=%v", retired, err)
	}
	events, err := service.DeploymentEvents(project.ID, legacyID)
	if err != nil || len(events) != 1 || events[0].MessageRedacted != "legacy deployment jobs are retired" {
		t.Fatalf("legacy retirement evidence=%+v err=%v", events, err)
	}
	if _, _, err := service.RetryDeployment(project.ID, legacyID, "retry-legacy", "retry-request"); apiCode(err) != LegacyDeploymentRetired {
		t.Fatalf("legacy retry err=%v", err)
	}
	if _, err := service.RollbackDeployment(project.ID, legacyID, userID, "rollback-legacy", "rollback-request"); apiCode(err) != LegacyDeploymentRetired {
		t.Fatalf("legacy rollback err=%v", err)
	}
}

func TestPostgresExposureRolloutSurvivesRestartAndSerializesConcurrentApply(t *testing.T) {
	dsn := requirePostgresTestDSN(t, "exposure rollout durability")
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	suffix := strings.ToLower(newID("rolloutpg"))
	orgID, userID := "org-"+suffix, "user-"+suffix
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email) VALUES($1,$2)`, userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO organizations(id,name,slug) VALUES($1,$2,$3)`, orgID, "Rollout", "rollout-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = db.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	now := time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)
	fresh := func() PostgresService { return PostgresService{DB: db, Now: func() time.Time { return now }} }
	service := fresh()
	project, err := service.CreateProject(orgID, "Rollout", "rollout-"+suffix, userID, "project-key")
	if err != nil {
		t.Fatal(err)
	}
	_, snapshot := postgresImmutableSnapshot(t, service, project.ID, suffix)
	snapshot.Authority.TopologyPlanID = "topology-" + suffix
	snapshot.Authority.TopologyRevision = 1
	snapshot.Authority.TopologyHash = strings.Repeat("1", 64)
	snapshot.Authority.DeploymentPolicyID = "policy-" + suffix
	snapshot.Authority.DeploymentPolicyRevision = 1
	snapshot.Authority.DeploymentPolicyHash = strings.Repeat("2", 64)
	snapshot.Authority.RoutingDecisionHash = strings.Repeat("3", 64)
	baseJob, _, err := service.StartImmutableDeployment(snapshot, userID, "base-key", "base-request")
	if err != nil {
		t.Fatal(err)
	}
	baseLease, ok, err := service.LeaseDeployment(project.ID, baseJob.NodeID)
	if err != nil || !ok {
		t.Fatalf("base lease ok=%v err=%v", ok, err)
	}
	base, err := service.CompleteDeployment(project.ID, baseJob.NodeID, baseJob.ID, "base-result", DeploymentResult{SchemaVersion: deploymentv1.ResultSchemaVersion, Status: deploymentv1.StateSucceeded, LeaseToken: baseLease.LeaseToken, SpecHash: snapshot.SpecHash, ApplicationImage: snapshot.Image.Reference, ApplicationImageID: "containerd://" + snapshot.Image.Digest, AvailableReplicas: 1})
	if err != nil {
		t.Fatal(err)
	}

	request := rolloutExposureRequest(t, base, "dep-pg-exposure-"+suffix, "pg.example.com", "/")
	job, reused, err := service.StartExposureRollout(project.ID, userID, "exposure-key", "create", request)
	if err != nil || reused {
		t.Fatalf("job=%+v reused=%v err=%v", job, reused, err)
	}
	replay, reused, err := fresh().StartExposureRollout(project.ID, userID, "exposure-key", "replay", request)
	if err != nil || !reused || replay.ID != job.ID || replay.RolloutIntent == nil || replay.RolloutIntent.IntentHash != job.RolloutIntent.IntentHash {
		t.Fatalf("restart replay=%+v reused=%v err=%v", replay, reused, err)
	}
	if _, err := fresh().GetDeployment("foreign-project", job.ID); err != ErrNotFound {
		t.Fatalf("cross-project lookup disclosed rollout: %v", err)
	}
	lease, ok, err := fresh().LeaseDeployment(project.ID, job.NodeID)
	if err != nil || !ok || lease.Command == nil || lease.Command.Rollout == nil {
		t.Fatalf("rollout lease=%+v ok=%v err=%v", lease, ok, err)
	}
	progress := rolloutProgress(lease, deploymentv1.RolloutStateApplying, "4", "")
	firstProgress, err := fresh().ProgressImmutableDeployment(project.ID, job.NodeID, job.ID, "applying", progress)
	if err != nil {
		t.Fatal(err)
	}
	eventsBeforeReplay, err := fresh().DeploymentEvents(project.ID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondProgress, err := fresh().ProgressImmutableDeployment(project.ID, job.NodeID, job.ID, "applying-replay", progress)
	if err != nil {
		t.Fatal(err)
	}
	eventsAfterReplay, _ := fresh().DeploymentEvents(project.ID, job.ID)
	if firstProgress.RolloutVersion != secondProgress.RolloutVersion || len(eventsBeforeReplay) != len(eventsAfterReplay) {
		t.Fatalf("progress replay mutated durable history: versions=%d/%d events=%d/%d", firstProgress.RolloutVersion, secondProgress.RolloutVersion, len(eventsBeforeReplay), len(eventsAfterReplay))
	}
	if _, err := fresh().ProgressImmutableDeployment(project.ID, job.NodeID, job.ID, "waiting", rolloutProgress(lease, deploymentv1.RolloutStateWaiting, "5", "")); err != nil {
		t.Fatal(err)
	}
	terminalProgress, err := fresh().ProgressImmutableDeployment(project.ID, job.NodeID, job.ID, "succeeded", rolloutProgress(lease, deploymentv1.RolloutStateSucceeded, "6", ""))
	if err != nil || terminalProgress.Status != deploymentv1.RolloutStateWaiting || terminalProgress.RolloutState != deploymentv1.RolloutStateSucceeded || terminalProgress.TerminalResult != nil {
		t.Fatalf("terminal progress=%+v err=%v", terminalProgress, err)
	}
	result := rolloutResult(lease, deploymentv1.RolloutStateSucceeded, "6", job.DesiredDigest, "known-pg-a", strings.Repeat("a", 64), "")
	finished, err := fresh().CompleteDeployment(project.ID, job.NodeID, job.ID, "result", result)
	if err != nil || finished.TerminalResult == nil || finished.CurrentDigest != job.DesiredDigest {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
	restarted := fresh()
	persisted, err := restarted.GetDeployment(project.ID, job.ID)
	if err != nil || persisted.TerminalResult == nil || persisted.RolloutIntent == nil || persisted.ExposureSpec == nil || persisted.RolloutStateHash != strings.Repeat("6", 64) {
		t.Fatalf("restart persisted=%+v err=%v", persisted, err)
	}
	terminalEvents, _ := restarted.DeploymentEvents(project.ID, job.ID)
	if _, err := restarted.CompleteDeployment(project.ID, job.NodeID, job.ID, "result-replay", result); err != nil {
		t.Fatal(err)
	}
	terminalEventsReplay, _ := restarted.DeploymentEvents(project.ID, job.ID)
	if len(terminalEventsReplay) != len(terminalEvents) {
		t.Fatalf("terminal replay duplicated event: %d/%d", len(terminalEvents), len(terminalEventsReplay))
	}

	requests := []deploymentv1.ExposureMutationRequest{
		rolloutExposureRequest(t, base, "dep-pg-concurrent-a-"+suffix, "pg-a.example.com", "/"),
		rolloutExposureRequest(t, base, "dep-pg-concurrent-b-"+suffix, "pg-b.example.com", "/"),
	}
	var wait sync.WaitGroup
	errorsFound := make(chan error, len(requests))
	for index, candidate := range requests {
		wait.Add(1)
		go func(index int, candidate deploymentv1.ExposureMutationRequest) {
			defer wait.Done()
			_, _, err := fresh().StartExposureRollout(project.ID, userID, "concurrent-"+string(rune('a'+index)), "concurrent", candidate)
			errorsFound <- err
		}(index, candidate)
	}
	wait.Wait()
	close(errorsFound)
	winners, locked := 0, 0
	for err := range errorsFound {
		if err == nil {
			winners++
		} else if apiCode(err) == "DEPLOYMENT_LOCKED" {
			locked++
		}
	}
	if winners != 1 || locked != 1 {
		t.Fatalf("Postgres concurrent apply winners=%d locked=%d", winners, locked)
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
