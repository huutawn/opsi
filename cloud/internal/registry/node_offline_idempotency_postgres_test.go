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
)

type nodeOfflinePostgresFixture struct {
	db        *sql.DB
	ctx       context.Context
	project   Project
	actorID   string
	node      Node
	otherNode Node
	now       time.Time
}

func newNodeOfflinePostgresFixture(t *testing.T) *nodeOfflinePostgresFixture {
	t.Helper()
	db, err := sql.Open("pgx", requirePostgresTestDSN(t, "node offline idempotency"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	suffix := strings.ToLower(newID("offlinepg"))
	orgID, actorID := "org-"+suffix, "user-"+suffix
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email) VALUES($1,$2)`, actorID, actorID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO organizations(id,name,slug) VALUES($1,$2,$3)`, orgID, "Node Offline", "node-offline-"+suffix); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = db.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, actorID)
	})
	fixture := &nodeOfflinePostgresFixture{db: db, ctx: ctx, actorID: actorID, now: time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)}
	service := fixture.service()
	fixture.project, err = service.CreateProject(orgID, "Node Offline", "node-offline-"+suffix, actorID, "project-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE scope=$1`, nodeOfflineScope+fixture.project.ID)
	})
	fixture.node, err = service.UpsertNode(fixture.project.ID, "target", "server", NodeHealthy, "203.0.113.21", "", "target-node")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := service.RegisterAgent(fixture.project.ID, fixture.node.ID, "sha256:target", "credential", "v1", "target-agent", map[string]any{"deploy": true})
	if err != nil {
		t.Fatal(err)
	}
	fixture.node, err = service.RecordAgentHeartbeat(fixture.project.ID, fixture.node.ID, AgentHeartbeat{Version: "v1", NodeReady: true, K3SStatus: "ready", Capabilities: map[string]any{"deploy": true}})
	if err != nil || fixture.node.AgentID != agent.ID {
		t.Fatalf("heartbeat node=%+v agent=%+v err=%v", fixture.node, agent, err)
	}
	fixture.otherNode, err = service.UpsertNode(fixture.project.ID, "other", "worker", NodeHealthy, "203.0.113.22", "", "other-node")
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (f *nodeOfflinePostgresFixture) service() PostgresService {
	return PostgresService{DB: f.db, Now: func() time.Time { return f.now }}
}

type nodeOfflinePostgresSnapshot struct {
	nodeStatus, failureCode, agentStatus, runtimeStatus, serverNodeID, projectStatus string
	nodeUpdated, agentUpdated, runtimeUpdated, projectUpdated                        time.Time
}

func sameNodeOfflinePostgresSnapshot(left, right nodeOfflinePostgresSnapshot) bool {
	return left.nodeStatus == right.nodeStatus && left.failureCode == right.failureCode && left.agentStatus == right.agentStatus && left.runtimeStatus == right.runtimeStatus && left.serverNodeID == right.serverNodeID && left.projectStatus == right.projectStatus && left.nodeUpdated.Equal(right.nodeUpdated) && left.agentUpdated.Equal(right.agentUpdated) && left.runtimeUpdated.Equal(right.runtimeUpdated) && left.projectUpdated.Equal(right.projectUpdated)
}

func (f *nodeOfflinePostgresFixture) snapshot(t *testing.T) nodeOfflinePostgresSnapshot {
	t.Helper()
	var got nodeOfflinePostgresSnapshot
	err := f.db.QueryRowContext(f.ctx, `SELECT n.status, COALESCE(n.failure_code,''), n.updated_at, a.status, a.updated_at, r.status, COALESCE(r.server_node_id,''), r.updated_at, p.status, p.updated_at FROM nodes n JOIN agents a ON a.id=n.agent_id JOIN runtimes r ON r.id=n.runtime_id JOIN projects p ON p.id=n.project_id WHERE n.id=$1`, f.node.ID).Scan(&got.nodeStatus, &got.failureCode, &got.nodeUpdated, &got.agentStatus, &got.agentUpdated, &got.runtimeStatus, &got.serverNodeID, &got.runtimeUpdated, &got.projectStatus, &got.projectUpdated)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (f *nodeOfflinePostgresFixture) counts(t *testing.T) (bindings, audits int) {
	t.Helper()
	if err := f.db.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM idempotency_keys WHERE scope=$1`, "node-offline:v1:"+f.project.ID).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM cloud_audit_events WHERE project_id=$1 AND action='NODE_MARKED_OFFLINE'`, f.project.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	return bindings, audits
}

func TestPostgresMarkNodeOfflineReplayIsDurableAndConflictSafe(t *testing.T) {
	fixture := newNodeOfflinePostgresFixture(t)
	key := "node-offline:" + fixture.node.ID
	first, reused, err := fixture.service().MarkNodeOffline(fixture.project.ID, fixture.node.ID, fixture.actorID, key, "req-first")
	if err != nil || reused || first.Status != NodeOffline {
		t.Fatalf("first=%+v reused=%v err=%v", first, reused, err)
	}
	stable := fixture.snapshot(t)
	if stable.nodeStatus != NodeOffline || stable.failureCode != "OPERATOR_CONFIRMED_TARGET_RESET" || stable.agentStatus != "revoked" || stable.runtimeStatus != RuntimeNoNodes || stable.serverNodeID != "" || stable.projectStatus != ProjectNoNodes {
		t.Fatalf("retired snapshot=%+v", stable)
	}
	if bindings, audits := fixture.counts(t); bindings != 1 || audits != 1 {
		t.Fatalf("first bindings=%d audits=%d", bindings, audits)
	}
	var auditActor, auditResource, auditResult, auditMetadata string
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT COALESCE(actor_user_id,''), resource_id, result, metadata_redacted::text FROM cloud_audit_events WHERE project_id=$1 AND action='NODE_MARKED_OFFLINE'`, fixture.project.ID).Scan(&auditActor, &auditResource, &auditResult, &auditMetadata); err != nil || auditActor != fixture.actorID || auditResource != fixture.node.ID || auditResult != "success" || !strings.Contains(auditMetadata, "req-first") {
		t.Fatalf("audit actor=%q resource=%q result=%q metadata=%q err=%v", auditActor, auditResource, auditResult, auditMetadata, err)
	}

	fixture.now = fixture.now.Add(time.Minute)
	replay, reused, err := fixture.service().MarkNodeOffline(fixture.project.ID, fixture.node.ID, fixture.actorID, key, "req-replay")
	if err != nil || !reused || hashJSON(replay) != hashJSON(first) {
		t.Fatalf("restart replay=%+v reused=%v err=%v", replay, reused, err)
	}
	if got := fixture.snapshot(t); !sameNodeOfflinePostgresSnapshot(got, stable) {
		t.Fatalf("restart replay churned state: stable=%+v got=%+v", stable, got)
	}
	if bindings, audits := fixture.counts(t); bindings != 1 || audits != 1 {
		t.Fatalf("replay bindings=%d audits=%d", bindings, audits)
	}

	var otherBefore time.Time
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT updated_at FROM nodes WHERE id=$1`, fixture.otherNode.ID).Scan(&otherBefore); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.service().MarkNodeOffline(fixture.project.ID, fixture.otherNode.ID, fixture.actorID, key, "req-conflict"); apiCode(err) != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("conflict err=%v", err)
	}
	var otherAfter time.Time
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT updated_at FROM nodes WHERE id=$1`, fixture.otherNode.ID).Scan(&otherAfter); err != nil {
		t.Fatal(err)
	}
	if !otherAfter.Equal(otherBefore) || !sameNodeOfflinePostgresSnapshot(fixture.snapshot(t), stable) {
		t.Fatalf("conflict changed state: other=%s/%s retired=%+v", otherBefore, otherAfter, fixture.snapshot(t))
	}

	fixture.now = fixture.now.Add(time.Minute)
	noOp, reused, err := fixture.service().MarkNodeOffline(fixture.project.ID, fixture.node.ID, fixture.actorID, "node-offline-again:"+fixture.node.ID, "req-new-key")
	if err != nil || reused || hashJSON(noOp) != hashJSON(first) || !sameNodeOfflinePostgresSnapshot(fixture.snapshot(t), stable) {
		t.Fatalf("new-key no-op=%+v reused=%v snapshot=%+v err=%v", noOp, reused, fixture.snapshot(t), err)
	}
	if bindings, audits := fixture.counts(t); bindings != 2 || audits != 1 {
		t.Fatalf("new-key no-op bindings=%d audits=%d", bindings, audits)
	}

	if _, _, err := fixture.service().MarkNodeOffline(fixture.project.ID, fixture.otherNode.ID, fixture.actorID, "invalid key", "req-invalid"); apiCode(err) != "IDEMPOTENCY_KEY_INVALID" {
		t.Fatalf("invalid key err=%v", err)
	}
	var otherInvalid time.Time
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT updated_at FROM nodes WHERE id=$1`, fixture.otherNode.ID).Scan(&otherInvalid); err != nil {
		t.Fatal(err)
	}
	if !otherInvalid.Equal(otherAfter) {
		t.Fatalf("invalid key changed node timestamp: %s/%s", otherAfter, otherInvalid)
	}
	if bindings, audits := fixture.counts(t); bindings != 2 || audits != 1 {
		t.Fatalf("invalid key bindings=%d audits=%d", bindings, audits)
	}
	if _, _, err := fixture.service().MarkNodeOffline(fixture.project.ID, fixture.otherNode.ID, "missing-user", "audit-rollback", "req-audit-rollback"); err == nil {
		t.Fatal("retirement succeeded despite required audit failure")
	}
	var otherRolledBackStatus string
	var otherRolledBackAt time.Time
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT status, updated_at FROM nodes WHERE id=$1`, fixture.otherNode.ID).Scan(&otherRolledBackStatus, &otherRolledBackAt); err != nil {
		t.Fatal(err)
	}
	if otherRolledBackStatus != NodeHealthy || !otherRolledBackAt.Equal(otherAfter) {
		t.Fatalf("audit failure did not roll back node: status=%q timestamp=%s/%s", otherRolledBackStatus, otherAfter, otherRolledBackAt)
	}
	if bindings, audits := fixture.counts(t); bindings != 2 || audits != 1 {
		t.Fatalf("audit failure bindings=%d audits=%d", bindings, audits)
	}
}

func TestPostgresMarkNodeOfflineConcurrentReplayMutatesOnce(t *testing.T) {
	fixture := newNodeOfflinePostgresFixture(t)
	const requests = 8
	key := "concurrent-offline"
	results := make(chan Node, requests)
	reused := make(chan bool, requests)
	errs := make(chan error, requests)
	var wait sync.WaitGroup
	for range requests {
		wait.Add(1)
		go func() {
			defer wait.Done()
			node, replay, err := fixture.service().MarkNodeOffline(fixture.project.ID, fixture.node.ID, fixture.actorID, key, "req-concurrent")
			results <- node
			reused <- replay
			errs <- err
		}()
	}
	wait.Wait()
	close(results)
	close(reused)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first Node
	for node := range results {
		if first.ID == "" {
			first = node
		} else if hashJSON(node) != hashJSON(first) {
			t.Fatalf("concurrent result mismatch: first=%+v got=%+v", first, node)
		}
	}
	replays := 0
	for replay := range reused {
		if replay {
			replays++
		}
	}
	if bindings, audits := fixture.counts(t); replays != requests-1 || bindings != 1 || audits != 1 {
		t.Fatalf("concurrent replays=%d bindings=%d audits=%d", replays, bindings, audits)
	}
}
