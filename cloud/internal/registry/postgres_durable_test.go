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
	if err != nil || len(jobs) != 1 || jobs[0].Snapshot == nil || jobs[0].Mode != "rollout" || jobs[0].RolloutIntent == nil {
		t.Fatalf("immutable snapshot did not survive restart: jobs=%+v err=%v", jobs, err)
	}
	lease, ok, err := restarted.LeaseDeployment(project.ID, job.NodeID)
	if err != nil || !ok || lease.Command == nil || lease.Command.Rollout == nil || lease.Deployment.AttemptCount != 1 {
		t.Fatalf("immutable lease=%+v ok=%v err=%v", lease, ok, err)
	}
	events, err := restarted.DeploymentEvents(project.ID, job.ID)
	if err != nil || len(events) < 2 {
		t.Fatalf("immutable events=%+v err=%v", events, err)
	}
	for _, event := range events {
		if event.SchemaVersion != deploymentv1.RolloutEventVersion && event.SchemaVersion != deploymentv1.EventSchemaVersion {
			t.Fatalf("immutable Postgres event omitted version: %+v", event)
		}
	}
	if record.ID != job.ServiceID || lease.Command.Image.Reference != snapshot.Image.Reference {
		t.Fatalf("immutable identity drifted: record=%+v job=%+v command=%+v", record, job, lease.Command)
	}
}

func TestPostgresRolloutProgressResourcePhasesSurviveRestartAndReplay(t *testing.T) {
	dsn := requirePostgresTestDSN(t, "rollout resource phase durability")
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	suffix := strings.ToLower(newID("resourcepg"))
	orgID, userID := "org-"+suffix, "user-"+suffix
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email) VALUES($1,$2)`, userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO organizations(id,name,slug) VALUES($1,$2,$3)`, orgID, "Resource Phase", "resource-phase-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = db.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	now := time.Date(2026, 7, 25, 8, 0, 0, 0, time.UTC)
	fresh := func() PostgresService { return PostgresService{DB: db, Now: func() time.Time { return now }} }
	service := fresh()
	project, err := service.CreateProject(orgID, "Resource Phase", "resource-phase-"+suffix, userID, "project-key")
	if err != nil {
		t.Fatal(err)
	}
	_, snapshot := postgresImmutableSnapshot(t, service, project.ID, suffix)
	job, _, err := service.StartImmutableDeployment(snapshot, userID, "resource-phase-key", "create")
	if err != nil {
		t.Fatal(err)
	}
	lease, ok, err := service.LeaseDeployment(project.ID, job.NodeID)
	if err != nil || !ok {
		t.Fatalf("lease=%+v ok=%v err=%v", lease, ok, err)
	}
	planned := plannedRolloutResource()
	prepared := rolloutProgress(lease, deploymentv1.RolloutStatePrepared, "1", "")
	prepared.Resources = []deploymentv1.ResourceIdentity{planned}
	if _, err := service.ProgressImmutableDeployment(project.ID, job.NodeID, job.ID, "prepared-planned", prepared); err != nil {
		t.Fatalf("prepared planned resource rejected: %v", err)
	}

	restarted := fresh()
	applying := rolloutProgress(lease, deploymentv1.RolloutStateApplying, "2", "")
	applying.Resources = []deploymentv1.ResourceIdentity{planned}
	firstApplying, err := restarted.ProgressImmutableDeployment(project.ID, job.NodeID, job.ID, "applying-planned", applying)
	if err != nil {
		t.Fatalf("applying planned resource rejected after restart: %v", err)
	}
	eventsBeforeReplay, err := restarted.DeploymentEvents(project.ID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	restarted = fresh()
	replayedApplying, err := restarted.ProgressImmutableDeployment(project.ID, job.NodeID, job.ID, "applying-planned-replay", applying)
	if err != nil {
		t.Fatal(err)
	}
	eventsAfterReplay, err := restarted.DeploymentEvents(project.ID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replayedApplying.RolloutVersion != firstApplying.RolloutVersion || len(eventsAfterReplay) != len(eventsBeforeReplay) {
		t.Fatalf("restart replay mutated durable history: versions=%d/%d events=%d/%d", firstApplying.RolloutVersion, replayedApplying.RolloutVersion, len(eventsBeforeReplay), len(eventsAfterReplay))
	}

	waiting := rolloutProgress(lease, deploymentv1.RolloutStateWaiting, "3", "")
	waiting.Resources = []deploymentv1.ResourceIdentity{planned}
	beforeReject, err := restarted.GetDeployment(project.ID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ProgressImmutableDeployment(project.ID, job.NodeID, job.ID, "waiting-planned", waiting); apiCode(err) != "DEPLOYMENT_STATE_INVALID" {
		t.Fatalf("waiting accepted unmaterialized resource: %v", err)
	}
	restarted = fresh()
	afterReject, err := restarted.GetDeployment(project.ID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	eventsAfterReject, err := restarted.DeploymentEvents(project.ID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterReject.RolloutState != beforeReject.RolloutState || afterReject.RolloutStateHash != beforeReject.RolloutStateHash || afterReject.RolloutVersion != beforeReject.RolloutVersion || len(eventsAfterReject) != len(eventsAfterReplay) {
		t.Fatalf("rejected waiting progress mutated durable state/history: before=%+v after=%+v events=%d/%d", beforeReject, afterReject, len(eventsAfterReplay), len(eventsAfterReject))
	}
	waiting.Resources = []deploymentv1.ResourceIdentity{materializedRolloutResource()}
	accepted, err := restarted.ProgressImmutableDeployment(project.ID, job.NodeID, job.ID, "waiting-materialized", waiting)
	if err != nil || accepted.RolloutState != deploymentv1.RolloutStateWaiting || accepted.RolloutVersion != beforeReject.RolloutVersion+1 {
		t.Fatalf("waiting materialized progress=%+v err=%v", accepted, err)
	}
}

func TestPostgresUnresolvedRolloutRetainsServiceOwnership(t *testing.T) {
	dsn := requirePostgresTestDSN(t, "unresolved rollout ownership")
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	suffix := strings.ToLower(newID("ownershippg"))
	orgID, userID := "org-"+suffix, "user-"+suffix
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email) VALUES($1,$2)`, userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO organizations(id,name,slug) VALUES($1,$2,$3)`, orgID, "Ownership", "ownership-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = db.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	fresh := func() PostgresService { return PostgresService{DB: db, Now: func() time.Time { return now }} }
	service := fresh()
	project, err := service.CreateProject(orgID, "Ownership", "ownership-"+suffix, userID, "project-key")
	if err != nil {
		t.Fatal(err)
	}
	record, snapshot := postgresImmutableSnapshot(t, service, project.ID, suffix)
	variant := func(key string) deploymentv1.JobSnapshot {
		copy := snapshot
		copy.PayloadHash = "payload-" + key
		copy.Authority.BuildRecord.ID = "br-" + key
		return copy
	}
	queued, _, err := service.StartImmutableDeployment(variant("queued"), userID, "queued-key", "queued")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(31 * time.Minute)
	if _, _, err := fresh().StartImmutableDeployment(variant("queued-blocked"), userID, "queued-blocked-key", "queued-blocked"); apiCode(err) != "DEPLOYMENT_LOCKED" {
		t.Fatalf("queued rollout lost ownership after TTL: %v", err)
	}
	cancelled, _, err := fresh().CancelDeployment(project.ID, queued.ID, "cancel-queued", "cancel-queued")
	if err != nil || cancelled.Status != deploymentv1.StateCancelled {
		t.Fatalf("safe cancel=%+v err=%v", cancelled, err)
	}

	job, _, err := fresh().StartImmutableDeployment(variant("leased"), userID, "leased-key", "leased")
	if err != nil {
		t.Fatal(err)
	}
	firstLease, ok, err := fresh().LeaseDeployment(project.ID, job.NodeID)
	if err != nil || !ok {
		t.Fatalf("first lease=%+v ok=%v err=%v", firstLease, ok, err)
	}
	now = now.Add(31 * time.Minute)
	if _, ok, err := fresh().LeaseDeployment(project.ID, job.NodeID); err != nil || ok {
		t.Fatalf("expired lease bypassed backoff ok=%v err=%v", ok, err)
	}
	requeued, err := fresh().GetDeployment(project.ID, job.ID)
	if err != nil || requeued.AttemptCount != 1 || requeued.TerminalResult != nil || requeued.RetryAfter == nil {
		t.Fatalf("requeued=%+v err=%v", requeued, err)
	}
	if _, _, err := fresh().CancelDeployment(project.ID, job.ID, "cancel-requeued", "cancel-requeued"); apiCode(err) != "CANCEL_UNSAFE" {
		t.Fatalf("requeued leased rollout cancellation err=%v", err)
	}
	now = *requeued.RetryAfter
	if _, err := db.ExecContext(ctx, `UPDATE deployment_jobs SET max_attempts=2 WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	secondLease, ok, err := fresh().LeaseDeployment(project.ID, job.NodeID)
	if err != nil || !ok {
		t.Fatalf("second lease=%+v ok=%v err=%v", secondLease, ok, err)
	}
	if _, err := fresh().ProgressImmutableDeployment(project.ID, job.NodeID, job.ID, "applying", rolloutProgress(secondLease, deploymentv1.RolloutStateApplying, "4", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh().ProgressImmutableDeployment(project.ID, job.NodeID, job.ID, "waiting", rolloutProgress(secondLease, deploymentv1.RolloutStateWaiting, "5", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh().ProgressImmutableDeployment(project.ID, job.NodeID, job.ID, "failed", rolloutProgress(secondLease, deploymentv1.RolloutStateFailed, "6", deploymentv1.RolloutCodeNoKnownGood)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(defaultDeploymentLeaseDuration + time.Second)
	if _, ok, err := fresh().LeaseDeployment(project.ID, job.NodeID); err != nil || ok {
		t.Fatalf("exhausted lease ok=%v err=%v", ok, err)
	}
	exhausted, err := fresh().GetDeployment(project.ID, job.ID)
	if err != nil || exhausted.Status != deploymentv1.StateFailed || exhausted.FailureCode != "DEPLOYMENT_LEASE_ATTEMPTS_EXHAUSTED" || exhausted.TerminalResult != nil {
		t.Fatalf("exhausted=%+v err=%v", exhausted, err)
	}
	var lockOwner string
	if err := db.QueryRowContext(ctx, `SELECT deployment_id FROM service_deployment_locks WHERE service_id=$1`, record.ID).Scan(&lockOwner); err != nil || lockOwner != job.ID {
		t.Fatalf("exhausted lock owner=%q err=%v", lockOwner, err)
	}
	if _, _, err := fresh().StartImmutableDeployment(variant("restart-blocked"), userID, "restart-blocked-key", "restart-blocked"); apiCode(err) != "DEPLOYMENT_LOCKED" || !strings.Contains(err.Error(), job.ID) {
		t.Fatalf("restart allowed replacement rollout: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE service_deployment_locks SET expires_at=$1 WHERE service_id=$2`, now.Add(-time.Hour), record.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fresh().StartImmutableDeployment(variant("expired-lock-blocked"), userID, "expired-lock-blocked-key", "expired-lock-blocked"); apiCode(err) != "DEPLOYMENT_LOCKED" {
		t.Fatalf("expired lock allowed replacement rollout: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM service_deployment_locks WHERE service_id=$1`, record.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fresh().StartImmutableDeployment(variant("missing-lock-blocked"), userID, "missing-lock-blocked-key", "missing-lock-blocked"); apiCode(err) != "DEPLOYMENT_LOCKED" {
		t.Fatalf("missing lock allowed replacement rollout: %v", err)
	}
	retried, reused, err := fresh().RetryDeployment(project.ID, job.ID, "retry-one", "retry-one")
	if err != nil || reused || retried.ID != job.ID || retried.Status != deploymentv1.StateQueued {
		t.Fatalf("retry=%+v reused=%v err=%v", retried, reused, err)
	}
	if replay, reused, err := fresh().RetryDeployment(project.ID, job.ID, "retry-one", "retry-one-replay"); err != nil || !reused || replay.ID != job.ID {
		t.Fatalf("retry replay=%+v reused=%v err=%v", replay, reused, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT deployment_id FROM service_deployment_locks WHERE service_id=$1`, record.ID).Scan(&lockOwner); err != nil || lockOwner != job.ID {
		t.Fatalf("retry did not restore ownership: owner=%q err=%v", lockOwner, err)
	}

	thirdLease, ok, err := fresh().LeaseDeployment(project.ID, job.NodeID)
	if err != nil || !ok {
		t.Fatalf("third lease=%+v ok=%v err=%v", thirdLease, ok, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE deployment_jobs SET max_attempts=attempt_count WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(defaultDeploymentLeaseDuration + time.Second)
	if _, ok, err := fresh().LeaseDeployment(project.ID, job.NodeID); err != nil || ok {
		t.Fatalf("second exhaustion ok=%v err=%v", ok, err)
	}
	var retryJob DeploymentJob
	var retryReused bool
	var retryErr, createErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		retryJob, retryReused, retryErr = fresh().RetryDeployment(project.ID, job.ID, "retry-concurrent", "retry-concurrent")
	}()
	go func() {
		defer wait.Done()
		_, _, createErr = fresh().StartImmutableDeployment(variant("concurrent-new"), userID, "concurrent-new-key", "concurrent-new")
	}()
	wait.Wait()
	if retryErr != nil || retryReused || retryJob.ID != job.ID || apiCode(createErr) != "DEPLOYMENT_LOCKED" {
		t.Fatalf("concurrent retry=%+v reused=%v retryErr=%v createErr=%v", retryJob, retryReused, retryErr, createErr)
	}

	finalLease, ok, err := fresh().LeaseDeployment(project.ID, job.NodeID)
	if err != nil || !ok {
		t.Fatalf("final lease=%+v ok=%v err=%v", finalLease, ok, err)
	}
	finished, err := fresh().CompleteDeployment(project.ID, job.NodeID, job.ID, "terminal", rolloutResult(finalLease, deploymentv1.RolloutStateFailed, "6", "", "", "", deploymentv1.RolloutCodeNoKnownGood))
	if err != nil || finished.TerminalResult == nil {
		t.Fatalf("terminal=%+v err=%v", finished, err)
	}
	if _, _, err := fresh().StartImmutableDeployment(variant("after-terminal"), userID, "after-terminal-key", "after-terminal"); err != nil {
		t.Fatalf("factual terminal did not release ownership: %v", err)
	}
}

func TestPostgresConfirmedResetReleasesOwnershipAcrossRestart(t *testing.T) {
	dsn := requirePostgresTestDSN(t, "confirmed reset ownership recovery")
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	suffix := strings.ToLower(newID("resetownershippg"))
	orgID, userID := "org-"+suffix, "user-"+suffix
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email) VALUES($1,$2)`, userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO organizations(id,name,slug) VALUES($1,$2,$3)`, orgID, "Reset Ownership", "reset-ownership-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = db.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	now := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	fresh := func() PostgresService { return PostgresService{DB: db, Now: func() time.Time { return now }} }
	project, err := fresh().CreateProject(orgID, "Reset Ownership", "reset-ownership-"+suffix, userID, "project-key")
	if err != nil {
		t.Fatal(err)
	}
	record, snapshot := postgresImmutableSnapshot(t, fresh(), project.ID, suffix)
	oldJob, _, err := fresh().StartImmutableDeployment(snapshot, userID, "old-key", "old-create")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE deployment_jobs SET max_attempts=1 WHERE id=$1`, oldJob.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := fresh().LeaseDeployment(project.ID, oldJob.NodeID); err != nil || !ok {
		t.Fatalf("old lease ok=%v err=%v", ok, err)
	}
	now = now.Add(defaultDeploymentLeaseDuration + time.Second)
	if _, ok, err := fresh().LeaseDeployment(project.ID, oldJob.NodeID); err != nil || ok {
		t.Fatalf("lease exhaustion ok=%v err=%v", ok, err)
	}
	exhausted, err := fresh().GetDeployment(project.ID, oldJob.ID)
	if err != nil || exhausted.Status != deploymentv1.StateFailed || exhausted.FailureCode != "DEPLOYMENT_LEASE_ATTEMPTS_EXHAUSTED" || exhausted.TerminalResult != nil {
		t.Fatalf("exhausted=%+v err=%v", exhausted, err)
	}
	eventsBefore, err := fresh().DeploymentEvents(project.ID, oldJob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, reused, err := fresh().MarkNodeOffline(project.ID, oldJob.NodeID, userID, "confirmed-reset", "confirmed-reset"); err != nil || reused {
		t.Fatalf("confirmed reset reused=%v err=%v", reused, err)
	}
	oldNode, err := fresh().getNode(ctx, oldJob.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	oldAgent, err := fresh().getAgent(ctx, oldJob.AgentID)
	if err != nil || oldNode.Status != NodeOffline || oldNode.FailureCode != "OPERATOR_CONFIRMED_TARGET_RESET" || oldAgent.Status != "revoked" {
		t.Fatalf("reset node=%+v agent=%+v err=%v", oldNode, oldAgent, err)
	}

	now = now.Add(time.Second)
	replacementNode, err := fresh().UpsertNode(project.ID, "replacement-"+suffix, "server", NodeHealthy, "203.0.113.78", "", "replacement-node")
	if err != nil {
		t.Fatal(err)
	}
	replacementAgent, err := fresh().RegisterAgent(project.ID, replacementNode.ID, "sha256:replacement", "replacement", "v1", "replacement-agent", map[string]any{"deploy": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh().RecordAgentHeartbeat(project.ID, replacementNode.ID, AgentHeartbeat{Version: "v1", NodeReady: true, K3SStatus: "ready", Capabilities: map[string]any{"deploy": true}}); err != nil {
		t.Fatal(err)
	}
	replacement := snapshot
	replacement.PayloadHash = "payload-replacement-" + suffix
	replacement.Authority.BuildRecord.ID = "br-replacement-" + suffix
	replacement.Authority.NodeID = replacementNode.ID
	replacement.Authority.AgentID = replacementAgent.ID
	if _, err := db.ExecContext(ctx, `DELETE FROM service_deployment_locks WHERE service_id=$1`, record.ID); err != nil {
		t.Fatal(err)
	}

	var newJob DeploymentJob
	var createErr, retryErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		newJob, _, createErr = fresh().StartImmutableDeployment(replacement, userID, "replacement-key", "replacement-create")
	}()
	go func() {
		defer wait.Done()
		_, _, retryErr = fresh().RetryDeployment(project.ID, oldJob.ID, "retired-retry", "retired-retry")
	}()
	wait.Wait()
	if createErr != nil || newJob.NodeID != replacementNode.ID || newJob.AgentID != replacementAgent.ID || apiCode(retryErr) != "RETRY_TARGET_RETIRED" {
		t.Fatalf("new=%+v createErr=%v retryErr=%v", newJob, createErr, retryErr)
	}

	restarted := fresh()
	persisted, err := restarted.GetDeployment(project.ID, oldJob.ID)
	if err != nil || persisted.TerminalResult != nil || persisted.IntentHash != exhausted.IntentHash || persisted.RolloutStateHash != exhausted.RolloutStateHash || persisted.FailureCode != exhausted.FailureCode {
		t.Fatalf("old evidence before=%+v after=%+v err=%v", exhausted, persisted, err)
	}
	eventsAfter, err := restarted.DeploymentEvents(project.ID, oldJob.ID)
	if err != nil || len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("old events changed before=%d after=%d err=%v", len(eventsBefore), len(eventsAfter), err)
	}
	if lease, ok, err := restarted.LeaseDeployment(project.ID, replacementNode.ID); err != nil || !ok || lease.Deployment.ID != newJob.ID {
		t.Fatalf("replacement lease=%+v ok=%v err=%v", lease, ok, err)
	}
	if _, ok, err := restarted.LeaseDeployment(project.ID, oldJob.NodeID); err != nil || ok {
		t.Fatalf("retired old target lease ok=%v err=%v", ok, err)
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
	for _, progress := range []deploymentv1.Progress{
		rolloutProgress(baseLease, deploymentv1.RolloutStateApplying, "1", ""),
		rolloutProgress(baseLease, deploymentv1.RolloutStateWaiting, "2", ""),
		rolloutProgress(baseLease, deploymentv1.RolloutStateSucceeded, "3", ""),
	} {
		if _, err := fresh().ProgressImmutableDeployment(project.ID, baseJob.NodeID, baseJob.ID, "base-progress-"+progress.State, progress); err != nil {
			t.Fatal(err)
		}
	}
	base, err := service.CompleteDeployment(project.ID, baseJob.NodeID, baseJob.ID, "base-result", rolloutResult(baseLease, deploymentv1.RolloutStateSucceeded, "3", baseJob.DesiredDigest, baseJob.RolloutIntent.RolloutID, strings.Repeat("a", 64), ""))
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

func TestPostgresPreMutationFailureSurvivesRestartAndReplaysExactly(t *testing.T) {
	dsn := requirePostgresTestDSN(t, "pre-mutation rollout durability")
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	suffix := strings.ToLower(newID("preflightpg"))
	orgID, userID := "org-"+suffix, "user-"+suffix
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email) VALUES($1,$2)`, userID, userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO organizations(id,name,slug) VALUES($1,$2,$3)`, orgID, "Preflight", "preflight-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = db.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	now := time.Date(2026, 7, 24, 9, 0, 0, 0, time.UTC)
	fresh := func() PostgresService { return PostgresService{DB: db, Now: func() time.Time { return now }} }
	service := fresh()
	project, err := service.CreateProject(orgID, "Preflight", "preflight-"+suffix, userID, "project-key")
	if err != nil {
		t.Fatal(err)
	}
	record, snapshot := postgresImmutableSnapshot(t, service, project.ID, suffix)
	job, _, err := service.StartImmutableDeployment(snapshot, userID, "preflight-key", "create")
	if err != nil {
		t.Fatal(err)
	}
	lease, ok, err := service.LeaseDeployment(project.ID, job.NodeID)
	if err != nil || !ok {
		t.Fatalf("lease=%+v ok=%v err=%v", lease, ok, err)
	}
	result := preMutationRolloutResult(lease, deploymentv1.RolloutCodePreflightFailed)
	finished, err := service.CompleteDeployment(project.ID, job.NodeID, job.ID, "failed", result)
	if err != nil || finished.TerminalResult == nil || finished.FailureCode != deploymentv1.RolloutCodePreflightFailed || finished.CurrentDigest != "" || finished.KnownGoodID != "" {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
	var auditCount int
	var actorType, actorUserID, outcome, auditMetadata string
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(actor_type),''), COALESCE(MAX(actor_user_id),''), COALESCE(MAX(result),''), COALESCE(MAX(metadata_redacted::text),'') FROM cloud_audit_events WHERE action='DEPLOYMENT_AGENT_RESULT_RECORDED' AND resource_id=$1`, job.ID).Scan(&auditCount, &actorType, &actorUserID, &outcome, &auditMetadata); err != nil || auditCount != 1 || actorType != "agent" || actorUserID != "" || outcome != "failure" || !strings.Contains(auditMetadata, deploymentv1.RolloutCodePreflightFailed) || strings.Contains(auditMetadata, lease.LeaseToken) {
		t.Fatalf("terminal audit count=%d actor=%q/%q outcome=%q metadata=%q err=%v", auditCount, actorType, actorUserID, outcome, auditMetadata, err)
	}
	restarted := fresh()
	persisted, err := restarted.GetDeployment(project.ID, job.ID)
	if err != nil || persisted.TerminalResult == nil || persisted.RolloutStateHash != result.RolloutResult.StateHash || persisted.FailureCode != result.FailureCode {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}
	events, err := restarted.DeploymentEvents(project.ID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := restarted.CompleteDeployment(project.ID, job.NodeID, job.ID, "failed-replay", result)
	if err != nil || replay.RolloutStateHash != persisted.RolloutStateHash || replay.FailureCode != persisted.FailureCode {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	replayedEvents, _ := restarted.DeploymentEvents(project.ID, job.ID)
	if len(replayedEvents) != len(events) {
		t.Fatalf("terminal replay duplicated history: %d/%d", len(events), len(replayedEvents))
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cloud_audit_events WHERE action='DEPLOYMENT_AGENT_RESULT_RECORDED' AND resource_id=$1`, job.ID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("terminal replay audit count=%d err=%v", auditCount, err)
	}
	if leased, ok, err := restarted.LeaseDeployment(project.ID, job.NodeID); err != nil || ok || leased.Deployment.ID == job.ID {
		t.Fatalf("terminal failed job was leased again: lease=%+v ok=%v err=%v", leased, ok, err)
	}
	var locks int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM service_deployment_locks WHERE service_id=$1`, record.ID).Scan(&locks); err != nil || locks != 0 {
		t.Fatalf("deployment lock count=%d err=%v", locks, err)
	}

	postJob, reused, err := fresh().StartImmutableDeployment(snapshot, userID, "post-mutation-key", "post-create")
	if err != nil || reused {
		t.Fatalf("post-mutation job=%+v reused=%v err=%v", postJob, reused, err)
	}
	postLease, ok, err := fresh().LeaseDeployment(project.ID, postJob.NodeID)
	if err != nil || !ok {
		t.Fatalf("post-mutation lease=%+v ok=%v err=%v", postLease, ok, err)
	}
	if _, err := fresh().ProgressImmutableDeployment(project.ID, postJob.NodeID, postJob.ID, "applying", rolloutProgress(postLease, deploymentv1.RolloutStateApplying, "7", "")); err != nil {
		t.Fatal(err)
	}
	forged := preMutationRolloutResult(postLease, deploymentv1.RolloutCodePreflightFailed)
	if _, err := fresh().CompleteDeployment(project.ID, postJob.NodeID, postJob.ID, "forged-pre-mutation", forged); apiCode(err) != "DEPLOYMENT_RESULT_MISMATCH" {
		t.Fatalf("forged pre-mutation err=%v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM service_deployment_locks WHERE service_id=$1`, record.ID).Scan(&locks); err != nil || locks != 1 {
		t.Fatalf("forged result lock count=%d err=%v", locks, err)
	}
	if _, err := fresh().ProgressImmutableDeployment(project.ID, postJob.NodeID, postJob.ID, "failed", rolloutProgress(postLease, deploymentv1.RolloutStateFailed, "8", deploymentv1.RolloutCodeNoKnownGood)); err != nil {
		t.Fatal(err)
	}
	postResult := rolloutResult(postLease, deploymentv1.RolloutStateFailed, "8", "", "", "", deploymentv1.RolloutCodeNoKnownGood)
	postFinished, err := fresh().CompleteDeployment(project.ID, postJob.NodeID, postJob.ID, "post-mutation-result", postResult)
	if err != nil || postFinished.TerminalResult == nil || postFinished.TerminalResult.FailurePhase != deploymentv1.FailurePhasePostMutation {
		t.Fatalf("post-mutation result=%+v err=%v", postFinished, err)
	}
	postPersisted, err := fresh().GetDeployment(project.ID, postJob.ID)
	if err != nil || postPersisted.TerminalResult == nil || postPersisted.TerminalResult.FailurePhase != deploymentv1.FailurePhasePostMutation {
		t.Fatalf("post-mutation persisted=%+v err=%v", postPersisted, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM service_deployment_locks WHERE service_id=$1`, record.ID).Scan(&locks); err != nil || locks != 0 {
		t.Fatalf("factual terminal lock count=%d err=%v", locks, err)
	}

	rollbackJob, reused, err := fresh().StartImmutableDeployment(snapshot, userID, "audit-rollback-key", "audit-rollback-create")
	if err != nil || reused {
		t.Fatalf("audit rollback job=%+v reused=%v err=%v", rollbackJob, reused, err)
	}
	rollbackLease, ok, err := fresh().LeaseDeployment(project.ID, rollbackJob.NodeID)
	if err != nil || !ok {
		t.Fatalf("audit rollback lease=%+v ok=%v err=%v", rollbackLease, ok, err)
	}
	var eventsBefore int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployment_events WHERE deployment_id=$1`, rollbackJob.ID).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE OR REPLACE FUNCTION opsi_test_reject_terminal_audit() RETURNS trigger AS $$ BEGIN IF NEW.action='DEPLOYMENT_AGENT_RESULT_RECORDED' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END; $$ LANGUAGE plpgsql`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER opsi_test_reject_terminal_audit_trigger BEFORE INSERT ON cloud_audit_events FOR EACH ROW EXECUTE FUNCTION opsi_test_reject_terminal_audit()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DROP TRIGGER IF EXISTS opsi_test_reject_terminal_audit_trigger ON cloud_audit_events`)
		_, _ = db.ExecContext(ctx, `DROP FUNCTION IF EXISTS opsi_test_reject_terminal_audit()`)
	})
	if _, err := fresh().CompleteDeployment(project.ID, rollbackJob.NodeID, rollbackJob.ID, "audit-rollback", preMutationRolloutResult(rollbackLease, deploymentv1.RolloutCodePreflightFailed)); err == nil {
		t.Fatal("completion succeeded despite required audit failure")
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER opsi_test_reject_terminal_audit_trigger ON cloud_audit_events`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP FUNCTION opsi_test_reject_terminal_audit()`); err != nil {
		t.Fatal(err)
	}
	var terminalJSON sql.NullString
	var leaseToken sql.NullString
	var eventsAfter int
	if err := db.QueryRowContext(ctx, `SELECT terminal_result_json::text, lease_token FROM deployment_jobs WHERE id=$1`, rollbackJob.ID).Scan(&terminalJSON, &leaseToken); err != nil || terminalJSON.Valid || !leaseToken.Valid {
		t.Fatalf("completion was not rolled back: terminal=%+v lease=%+v err=%v", terminalJSON, leaseToken, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployment_events WHERE deployment_id=$1`, rollbackJob.ID).Scan(&eventsAfter); err != nil || eventsAfter != eventsBefore {
		t.Fatalf("audit failure event count=%d/%d err=%v", eventsBefore, eventsAfter, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM service_deployment_locks WHERE service_id=$1 AND deployment_id=$2`, record.ID, rollbackJob.ID).Scan(&locks); err != nil || locks != 1 {
		t.Fatalf("audit failure released service lock count=%d err=%v", locks, err)
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
	return record, deploymentv1.JobSnapshot{SchemaVersion: deploymentv1.JobSchemaVersion, ProjectID: projectID, Image: image, Workload: spec, SpecHash: specHash, PayloadHash: "payload-" + suffix, Authority: deploymentv1.AuthoritySnapshot{BuildRecord: buildrecordv1.Record{SchemaVersion: buildrecordv1.SchemaVersion, ID: "br-" + suffix, ProjectID: projectID, ServiceID: record.ID, ServiceKey: record.Name, ActiveBindingID: "binding-" + suffix, Build: buildrecordv1.BuildMetadata{OCIRepository: image.Repository, OCIDigest: image.Digest, Status: "succeeded"}}, TopologyPlanID: "topology-" + suffix, TopologyRevision: 1, TopologyHash: strings.Repeat("1", 64), ServiceConfigurationRevision: record.Configuration.Revision, ServiceConfigurationStateHash: record.Configuration.StateHash, DeploymentPolicyID: "policy-" + suffix, DeploymentPolicyRevision: 1, DeploymentPolicyHash: strings.Repeat("2", 64), RoutingDecisionHash: strings.Repeat("3", 64), EnvironmentID: record.EnvironmentID, RuntimeID: record.RuntimeID, NodeID: node.ID, AgentID: agent.ID}}
}
