package registry

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/opsi-dev/opsi/cloud/internal/postgres"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

func TestPostgresDeploymentKnownGoodDoesNotCrossRuntimeTarget(t *testing.T) {
	fixture := newKnownGoodPostgresFixture(t, "targetscope")
	base := fixture.completeSuccess(t, fixture.snapshot, "base", "a")
	fixture.advance()
	node, agent := fixture.replaceRuntimeTarget(t, base)

	snapshot := snapshotForRuntimeTarget(base, node.ID, agent.ID, "new-target")
	job, _, err := fixture.service().StartImmutableDeployment(snapshot, fixture.userID, "new-target", "new-target")
	if err != nil {
		t.Fatal(err)
	}
	assertNoPreviousKnownGood(t, job)
}

func TestPostgresFailedPreMutationDoesNotPoisonKnownGood(t *testing.T) {
	fixture := newKnownGoodPostgresFixture(t, "failedpoison")
	base := fixture.completeSuccess(t, fixture.snapshot, "base", "a")
	fixture.advance()

	agent, err := fixture.service().RegisterAgent(fixture.projectID, base.NodeID, "sha256:new-agent", "new-agent-hash", "v2", "new-agent", map[string]any{"deploy": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service().RecordAgentHeartbeat(fixture.projectID, base.NodeID, AgentHeartbeat{Version: "v2", NodeReady: true, K3SStatus: "ready", Capabilities: map[string]any{"deploy": true}}); err != nil {
		t.Fatal(err)
	}

	poisonedSnapshot := snapshotForRuntimeTarget(base, base.NodeID, agent.ID, "failed-target")
	poisoned, _, err := fixture.service().StartImmutableDeployment(poisonedSnapshot, fixture.userID, "failed-target", "failed-target")
	if err != nil {
		t.Fatal(err)
	}
	assertNoPreviousKnownGood(t, poisoned)
	lease, ok, err := fixture.service().LeaseDeployment(fixture.projectID, poisoned.NodeID)
	if err != nil || !ok {
		t.Fatalf("lease=%+v ok=%v err=%v", lease, ok, err)
	}
	if _, err := fixture.service().CompleteDeployment(fixture.projectID, poisoned.NodeID, poisoned.ID, "failed-target", preMutationRolloutResult(lease, deploymentv1.RolloutCodeOwnershipConflict)); err != nil {
		t.Fatal(err)
	}

	fixture.advance()
	nextSnapshot := snapshotForRuntimeTarget(base, base.NodeID, agent.ID, "after-failure")
	next, _, err := fixture.service().StartImmutableDeployment(nextSnapshot, fixture.userID, "after-failure", "after-failure")
	if err != nil {
		t.Fatal(err)
	}
	assertNoPreviousKnownGood(t, next)
}

func TestPostgresKnownGoodAcceptsExactSuccessAndRollbackAcrossRestart(t *testing.T) {
	fixture := newKnownGoodPostgresFixture(t, "restartselection")
	base := fixture.completeSuccess(t, fixture.snapshot, "base", "a")
	assertPostgresKnownGood(t, fixture, base, base.KnownGoodID, base.KnownGoodHash, base.CurrentDigest)
	differentAgent := base
	differentAgent.AgentID = "agent-other"
	assertPostgresKnownGood(t, fixture, differentAgent, "", "", "")
	differentNode := base
	differentNode.NodeID = "node-other"
	assertPostgresKnownGood(t, fixture, differentNode, "", "", "")
	fixture.advance()

	rolledBack := canonicalKnownGoodCandidate(t, base, "dep-postgres-rolled-back", deploymentv1.RolloutStateRolledBack, fixture.now)
	if !validKnownGoodCandidate(rolledBack) {
		t.Fatal("canonical rolled-back fixture was invalid before persistence")
	}
	insertKnownGoodPostgresCandidate(t, fixture, rolledBack)
	persistedRollback, err := fixture.service().GetDeployment(fixture.projectID, rolledBack.ID)
	if err != nil || !validKnownGoodCandidate(persistedRollback) {
		t.Fatalf("canonical rolled-back fixture was invalid after reconstruction: %+v err=%v", persistedRollback, err)
	}

	fixture.advance()
	failed := canonicalKnownGoodCandidate(t, base, "dep-postgres-failed", deploymentv1.RolloutStateSucceeded, fixture.now)
	failed.Status = deploymentv1.RolloutStateFailed
	failed.RolloutState = deploymentv1.RolloutStateFailed
	failed.TerminalResult.Status = deploymentv1.RolloutStateFailed
	failed.TerminalResult.RolloutState = deploymentv1.RolloutStateFailed
	setKnownGoodCandidateIdentity(&failed, "c")
	insertKnownGoodPostgresCandidate(t, fixture, failed)

	for _, state := range []string{deploymentv1.RolloutStateRollbackFailed, "cancelled"} {
		fixture.advance()
		excluded := canonicalKnownGoodCandidate(t, base, "dep-postgres-"+state, deploymentv1.RolloutStateSucceeded, fixture.now)
		excluded.Status = state
		excluded.RolloutState = state
		excluded.TerminalResult.Status = state
		excluded.TerminalResult.RolloutState = state
		setKnownGoodCandidateIdentity(&excluded, "c")
		insertKnownGoodPostgresCandidate(t, fixture, excluded)
	}
	fixture.advance()
	resultless := canonicalKnownGoodCandidate(t, base, "dep-postgres-resultless", deploymentv1.RolloutStateSucceeded, fixture.now)
	setKnownGoodCandidateIdentity(&resultless, "c")
	resultless.TerminalResult = nil
	insertKnownGoodPostgresCandidate(t, fixture, resultless)

	fixture.advance()
	preview := canonicalKnownGoodCandidate(t, base, "dep-postgres-preview", deploymentv1.RolloutStateSucceeded, fixture.now)
	previewSnapshot := *base.Snapshot
	previewSnapshot.Preview = &deploymentv1.PreviewSpec{}
	preview.Snapshot = &previewSnapshot
	setKnownGoodCandidateIdentity(&preview, "c")
	insertKnownGoodPostgresCandidate(t, fixture, preview)

	malformed := []struct {
		name   string
		mutate func(*DeploymentJob)
	}{
		{"legacy-mode", func(job *DeploymentJob) { job.Mode = "" }},
		{"nil-intent", func(job *DeploymentJob) { job.RolloutIntent = nil }},
		{"invalid-intent-schema", func(job *DeploymentJob) { job.RolloutIntent.SchemaVersion = "" }},
		{"invalid-snapshot", func(job *DeploymentJob) { job.Snapshot.SchemaVersion = "" }},
		{"invalid-job-schema", func(job *DeploymentJob) { job.SchemaVersion = "" }},
		{"invalid-result-schema", func(job *DeploymentJob) { job.TerminalResult.SchemaVersion = "" }},
		{"invalid-hash", func(job *DeploymentJob) { job.TerminalResult.KnownGoodHash = "" }},
		{"invalid-digest", func(job *DeploymentJob) { job.TerminalResult.CurrentDigest = "sha256:invalid" }},
	}
	for _, test := range malformed {
		fixture.advance()
		candidate := canonicalKnownGoodCandidate(t, base, "dep-postgres-"+test.name, deploymentv1.RolloutStateSucceeded, fixture.now)
		setKnownGoodCandidateIdentity(&candidate, "c")
		test.mutate(&candidate)
		insertKnownGoodPostgresCandidate(t, fixture, candidate)
	}

	assertPostgresKnownGood(t, fixture, base, rolledBack.KnownGoodID, rolledBack.KnownGoodHash, rolledBack.CurrentDigest)
}

func TestPostgresKnownGoodRejectsLegacyExactTargetHistory(t *testing.T) {
	fixture := newKnownGoodPostgresFixture(t, "legacyhistory")
	base := fixture.completeSuccess(t, fixture.snapshot, "base", "a")
	fixture.advance()
	legacy := legacyKnownGoodCandidate(base, "postgres-legacy-history", deploymentv1.RolloutStateSucceeded, fixture.now)
	legacy.SchemaVersion = deploymentv1.JobSchemaVersion
	legacy.OrgID = base.OrgID
	legacy.Action = deploymentv1.RolloutOperationApply
	legacy.IdempotencyKey = legacy.ID
	legacy.PayloadHash = "payload-" + legacy.ID
	legacy.KnownGoodID, legacy.KnownGoodHash = knownGoodIdentity("c")
	legacy.CurrentDigest = "sha256:" + strings.Repeat("c", 64)
	legacy.TerminalResult.KnownGoodID = legacy.KnownGoodID
	legacy.TerminalResult.KnownGoodHash = legacy.KnownGoodHash
	legacy.TerminalResult.CurrentDigest = legacy.CurrentDigest
	insertKnownGoodPostgresCandidate(t, fixture, legacy)

	assertPostgresKnownGood(t, fixture, base, base.KnownGoodID, base.KnownGoodHash, base.CurrentDigest)
}

func TestPostgresKnownGoodRejectsTerminalColumnMismatch(t *testing.T) {
	fixture := newKnownGoodPostgresFixture(t, "terminalmismatch")
	base := fixture.completeSuccess(t, fixture.snapshot, "base", "a")
	fixture.advance()
	mismatch := canonicalKnownGoodCandidate(t, base, "dep-postgres-terminal-mismatch", deploymentv1.RolloutStateSucceeded, fixture.now)
	mismatch.KnownGoodID, mismatch.KnownGoodHash = knownGoodIdentity("c")
	mismatch.CurrentDigest = "sha256:" + strings.Repeat("c", 64)
	insertKnownGoodPostgresCandidate(t, fixture, mismatch)

	assertPostgresKnownGood(t, fixture, base, base.KnownGoodID, base.KnownGoodHash, base.CurrentDigest)
}

func TestPostgresKnownGoodRejectsIntentTargetMismatch(t *testing.T) {
	fixture := newKnownGoodPostgresFixture(t, "intenttargetmismatch")
	base := fixture.completeSuccess(t, fixture.snapshot, "base", "a")
	fixture.advance()
	mismatch := canonicalKnownGoodCandidate(t, base, "dep-postgres-intent-target-mismatch", deploymentv1.RolloutStateSucceeded, fixture.now)
	mismatch.RolloutIntent.Target.AgentID = "agent-other"
	mismatch.RolloutIntent.Desired.Target.AgentID = "agent-other"
	mismatch.RolloutIntent.IntentHash = ""
	intent, err := mismatch.RolloutIntent.Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	mismatch.RolloutIntent = &intent
	mismatch.IntentHash = intent.IntentHash
	mismatch.TerminalResult.RolloutID = intent.RolloutID
	mismatch.TerminalResult.IntentHash = intent.IntentHash
	mismatch.KnownGoodID, mismatch.KnownGoodHash = knownGoodIdentity("c")
	mismatch.TerminalResult.KnownGoodID = mismatch.KnownGoodID
	mismatch.TerminalResult.KnownGoodHash = mismatch.KnownGoodHash
	insertKnownGoodPostgresCandidate(t, fixture, mismatch)

	assertPostgresKnownGood(t, fixture, base, base.KnownGoodID, base.KnownGoodHash, base.CurrentDigest)
}

func TestPostgresExposureKnownGoodDoesNotCrossAgentTarget(t *testing.T) {
	fixture := newKnownGoodPostgresFixture(t, "exposuretarget")
	base := fixture.completeSuccess(t, fixture.snapshot, "base", "a")
	fixture.advance()
	agent, err := fixture.service().RegisterAgent(fixture.projectID, base.NodeID, "sha256:exposure-agent", "exposure-agent-hash", "v2", "exposure-agent", map[string]any{"deploy": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service().RecordAgentHeartbeat(fixture.projectID, base.NodeID, AgentHeartbeat{Version: "v2", NodeReady: true, K3SStatus: "ready", Capabilities: map[string]any{"deploy": true}}); err != nil {
		t.Fatal(err)
	}

	legacyBase := legacyBaseForRuntimeTarget(base, base.NodeID, agent.ID, "dep-postgres-exposure-new-target", fixture.now)
	legacyBase.KnownGoodID, legacyBase.KnownGoodHash = knownGoodIdentity("c")
	legacyBase.CurrentDigest = "sha256:" + strings.Repeat("c", 64)
	legacyBase.TerminalResult = &deploymentv1.AgentResult{Status: deploymentv1.RolloutStateSucceeded, RolloutState: deploymentv1.RolloutStateSucceeded, KnownGoodID: legacyBase.KnownGoodID, KnownGoodHash: legacyBase.KnownGoodHash, CurrentDigest: legacyBase.CurrentDigest}
	legacyBase.SchemaVersion = deploymentv1.JobSchemaVersion
	legacyBase.Mode = "rollout"
	legacyBase.OrgID = base.OrgID
	legacyBase.Action = deploymentv1.RolloutOperationApply
	insertKnownGoodPostgresCandidate(t, fixture, legacyBase)
	request := rolloutExposureRequest(t, legacyBase, "dep-postgres-exposure-target-scope", "pg-target.example.com", "/")
	job, _, err := fixture.service().StartExposureRollout(fixture.projectID, fixture.userID, "exposure-target-scope", "exposure-target-scope", request)
	if err != nil {
		t.Fatal(err)
	}
	assertNoPreviousKnownGood(t, job)
}

type knownGoodPostgresFixture struct {
	db        *sql.DB
	projectID string
	userID    string
	snapshot  deploymentv1.JobSnapshot
	now       time.Time
}

func newKnownGoodPostgresFixture(t *testing.T, name string) *knownGoodPostgresFixture {
	t.Helper()
	dsn := requirePostgresTestDSN(t, "runtime-target known-good")
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	suffix := strings.ToLower(newID(name))
	fixture := &knownGoodPostgresFixture{db: db, userID: "user-" + suffix, now: time.Date(2026, 8, 3, 1, 0, 0, 0, time.UTC)}
	orgID := "org-" + suffix
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email) VALUES($1,$2)`, fixture.userID, fixture.userID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO organizations(id,name,slug) VALUES($1,$2,$3)`, orgID, "Known Good", "known-good-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = db.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, fixture.userID)
	})
	project, err := fixture.service().CreateProject(orgID, "Known Good", "known-good-"+suffix, fixture.userID, "project-key")
	if err != nil {
		t.Fatal(err)
	}
	fixture.projectID = project.ID
	_, fixture.snapshot = postgresImmutableSnapshot(t, fixture.service(), project.ID, suffix)
	return fixture
}

func (f *knownGoodPostgresFixture) service() PostgresService {
	return PostgresService{DB: f.db, Now: func() time.Time { return f.now }}
}

func (f *knownGoodPostgresFixture) advance() {
	f.now = f.now.Add(time.Second)
}

func (f *knownGoodPostgresFixture) completeSuccess(t *testing.T, snapshot deploymentv1.JobSnapshot, key, hashCharacter string) DeploymentJob {
	t.Helper()
	job, _, err := f.service().StartImmutableDeployment(snapshot, f.userID, key, key)
	if err != nil {
		t.Fatal(err)
	}
	lease, ok, err := f.service().LeaseDeployment(f.projectID, job.NodeID)
	if err != nil || !ok {
		t.Fatalf("lease=%+v ok=%v err=%v", lease, ok, err)
	}
	for _, state := range []string{deploymentv1.RolloutStateApplying, deploymentv1.RolloutStateWaiting, deploymentv1.RolloutStateSucceeded} {
		if _, err := f.service().ProgressImmutableDeployment(f.projectID, job.NodeID, job.ID, key+"-"+state, rolloutProgress(lease, state, hashCharacter, "")); err != nil {
			t.Fatal(err)
		}
	}
	id, hash := knownGoodIdentity(hashCharacter)
	finished, err := f.service().CompleteDeployment(f.projectID, job.NodeID, job.ID, key+"-result", rolloutResult(lease, deploymentv1.RolloutStateSucceeded, hashCharacter, job.DesiredDigest, id, hash, ""))
	if err != nil {
		t.Fatal(err)
	}
	return finished
}

func (f *knownGoodPostgresFixture) replaceRuntimeTarget(t *testing.T, base DeploymentJob) (Node, Agent) {
	t.Helper()
	if _, _, err := f.service().MarkNodeOffline(f.projectID, base.NodeID, f.userID, "retire-old-target", "retire-old-target"); err != nil {
		t.Fatal(err)
	}
	node, err := f.service().UpsertNode(f.projectID, "replacement", "server", NodeHealthy, "203.0.113.88", "", "replacement-node")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := f.service().RegisterAgent(f.projectID, node.ID, "sha256:replacement", "replacement-hash", "v2", "replacement-agent", map[string]any{"deploy": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service().RecordAgentHeartbeat(f.projectID, node.ID, AgentHeartbeat{Version: "v2", NodeReady: true, K3SStatus: "ready", Capabilities: map[string]any{"deploy": true}}); err != nil {
		t.Fatal(err)
	}
	return node, agent
}

func insertKnownGoodPostgresCandidate(t *testing.T, fixture *knownGoodPostgresFixture, job DeploymentJob) {
	t.Helper()
	tx, err := fixture.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := insertDeployment(context.Background(), tx, job); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func assertPostgresKnownGood(t *testing.T, fixture *knownGoodPostgresFixture, target DeploymentJob, expectedID, expectedHash, expectedDigest string) {
	t.Helper()
	restarted := fixture.service()
	tx, err := restarted.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	id, hash, digest, err := latestPostgresKnownGood(context.Background(), tx, target.ProjectID, target.EnvironmentID, target.RuntimeID, target.ServiceID, target.NodeID, target.AgentID)
	if err != nil || id != expectedID || hash != expectedHash || digest != expectedDigest {
		t.Fatalf("restart selection id=%q hash=%q digest=%q err=%v", id, hash, digest, err)
	}
}
