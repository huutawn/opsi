package deploy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
)

func TestRolloutHealthyAThenBrokenBRollsBackExactA(t *testing.T) {
	store := openTestStore(t)
	runtime := newFakeRolloutRuntime()
	engine := NewEngine(store, EngineConfig{Reconciler: runtime, RolloutTimeout: time.Second})
	a := testRuntimeSnapshot(t, "job-a", "a")
	recordA, err := engine.ReconcileRollout(context.Background(), testRolloutIntent(t, "rollout-a", a, nil), nil)
	if err != nil || recordA.State != deploymentv1.RolloutStateSucceeded {
		t.Fatalf("A record=%+v err=%v", recordA, err)
	}
	if recordA.Evidence == nil || len(recordA.Resources) == 0 || recordA.Intent.Desired.Image.Digest != a.Image.Digest {
		t.Fatalf("healthy A omitted factual readiness/resources: %+v", recordA)
	}
	knownA, err := store.CurrentKnownGood(context.Background(), a.Target)
	if err != nil || knownA == nil || knownA.ID == "" || knownA.SnapshotHash == "" || knownA.EvidenceHash == "" || knownA.Runtime.Image.Digest != a.Image.Digest {
		t.Fatalf("known-good A=%+v err=%v", knownA, err)
	}
	b := testRuntimeSnapshot(t, "job-b", "b")
	runtime.failReadiness[b.DeploymentJobID] = errors.New("application never became ready")
	var states []string
	recordB, err := engine.ReconcileRollout(context.Background(), testRolloutIntent(t, "rollout-b", b, knownA), func(event *ProgressEvent) error {
		if event != nil && event.Rollout != nil {
			states = append(states, event.Rollout.State)
		}
		return nil
	})
	if recordB.State != deploymentv1.RolloutStateRolledBack || err == nil {
		t.Fatalf("B record=%+v err=%v", recordB, err)
	}
	if !containsOrderedStates(states, deploymentv1.RolloutStateFailed, deploymentv1.RolloutStateRollingBack, deploymentv1.RolloutStateRolledBack) {
		t.Fatalf("B progress states=%v", states)
	}
	if recordB.Intent.Desired.Image.Digest != b.Image.Digest || recordB.Intent.PreviousDigest != a.Image.Digest || recordB.Evidence == nil || len(recordB.Resources) == 0 {
		t.Fatalf("B rollback omitted desired/previous/readiness/resources: %+v", recordB)
	}
	if runtime.current.DeploymentJobID != a.DeploymentJobID || runtime.current.Image.Digest != a.Image.Digest {
		t.Fatalf("rollback restored %+v instead of exact A", runtime.current)
	}
	current, err := store.CurrentKnownGood(context.Background(), a.Target)
	if err != nil || current == nil || current.ID != knownA.ID || current.SnapshotHash != knownA.SnapshotHash {
		t.Fatalf("broken B replaced known-good: current=%+v err=%v", current, err)
	}
	if broken, err := store.GetKnownGood(context.Background(), "rollout-b"); err != nil || broken != nil {
		t.Fatalf("broken B became known-good: %+v err=%v", broken, err)
	}
}

func TestRolloutFailureWithoutKnownGoodIsTerminalFailed(t *testing.T) {
	store := openTestStore(t)
	runtime := newFakeRolloutRuntime()
	snapshot := testRuntimeSnapshot(t, "job-broken", "c")
	runtime.failReadiness[snapshot.DeploymentJobID] = errors.New("unready")
	engine := NewEngine(store, EngineConfig{Reconciler: runtime, RolloutTimeout: time.Second})
	var states []string
	record, err := engine.ReconcileRollout(context.Background(), testRolloutIntent(t, "rollout-broken", snapshot, nil), func(event *ProgressEvent) error {
		if event != nil && event.Rollout != nil {
			states = append(states, event.Rollout.State)
		}
		return nil
	})
	if record.State != deploymentv1.RolloutStateFailed || record.TerminalAt == nil || rolloutErrorCode(err) != deploymentv1.RolloutCodeNoKnownGood {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	if containsOrderedStates(states, deploymentv1.RolloutStateRollingBack) || record.Intent.PreviousKnownGoodID != "" {
		t.Fatalf("failure without known-good claimed rollback: states=%v record=%+v", states, record)
	}
	if known, err := store.CurrentKnownGood(context.Background(), snapshot.Target); err != nil || known != nil {
		t.Fatalf("failed rollout created known-good: %+v err=%v", known, err)
	}
}

func TestRolloutPreWALFailuresReturnNoRecordAndDoNotMutate(t *testing.T) {
	t.Run("stale previous known-good", func(t *testing.T) {
		store := openTestStore(t)
		runtime := newFakeRolloutRuntime()
		engine := NewEngine(store, EngineConfig{Reconciler: runtime, RolloutTimeout: time.Second})
		a := testRuntimeSnapshot(t, "job-a", "a")
		if record, err := engine.ReconcileRollout(context.Background(), testRolloutIntent(t, "rollout-a", a, nil), nil); err != nil || record.State != deploymentv1.RolloutStateSucceeded {
			t.Fatalf("seed A record=%+v err=%v", record, err)
		}
		knownA, _ := store.CurrentKnownGood(context.Background(), a.Target)
		b := testRuntimeSnapshot(t, "job-b", "b")
		intent := testRolloutIntent(t, "rollout-b", b, knownA)
		intent.PreviousKnownGoodHash = strings.Repeat("f", 64)
		intent.IntentHash = ""
		intent, err := intent.Canonicalize()
		if err != nil {
			t.Fatal(err)
		}
		runtime.prepareCalls, runtime.applyCalls, runtime.rollbackCalls = 0, 0, 0
		runtime.rollbackTargetID = a.DeploymentJobID
		record, err := engine.ReconcileRollout(context.Background(), intent, nil)
		if rolloutErrorCode(err) != deploymentv1.RolloutCodeConflict || record.Intent.RolloutID != "" || runtime.prepareCalls != 0 || runtime.applyCalls != 0 || runtime.rollbackCalls != 0 {
			t.Fatalf("record=%+v err=%v prepare=%d apply=%d rollback=%d", record, err, runtime.prepareCalls, runtime.applyCalls, runtime.rollbackCalls)
		}
		current, _ := store.CurrentKnownGood(context.Background(), a.Target)
		if current == nil || current.ID != knownA.ID || current.SnapshotHash != knownA.SnapshotHash || runtime.current.Image.Digest != a.Image.Digest {
			t.Fatalf("preflight changed known-good/runtime: current=%+v runtime=%+v", current, runtime.current)
		}
	})

	t.Run("ownership conflict during prepare", func(t *testing.T) {
		store := openTestStore(t)
		runtime := newFakeRolloutRuntime()
		engine := NewEngine(store, EngineConfig{Reconciler: runtime, RolloutTimeout: time.Second})
		a := testRuntimeSnapshot(t, "job-a", "a")
		if _, err := engine.ReconcileRollout(context.Background(), testRolloutIntent(t, "rollout-a", a, nil), nil); err != nil {
			t.Fatal(err)
		}
		knownA, _ := store.CurrentKnownGood(context.Background(), a.Target)
		b := testRuntimeSnapshot(t, "job-b", "b")
		runtime.failPrepare[b.DeploymentJobID] = deploymentv1.NewRolloutError(deploymentv1.RolloutCodeOwnershipConflict, "deployment is owned by another controller", false)
		runtime.prepareCalls, runtime.applyCalls, runtime.rollbackCalls = 0, 0, 0
		runtime.rollbackTargetID = a.DeploymentJobID
		record, err := engine.ReconcileRollout(context.Background(), testRolloutIntent(t, "rollout-b", b, knownA), nil)
		if rolloutErrorCode(err) != deploymentv1.RolloutCodeOwnershipConflict || record.Intent.RolloutID != "" || runtime.prepareCalls != 1 || runtime.applyCalls != 0 || runtime.rollbackCalls != 0 {
			t.Fatalf("record=%+v err=%v prepare=%d apply=%d rollback=%d", record, err, runtime.prepareCalls, runtime.applyCalls, runtime.rollbackCalls)
		}
		current, _ := store.CurrentKnownGood(context.Background(), a.Target)
		if current == nil || current.ID != knownA.ID || runtime.current.Image.Digest != a.Image.Digest {
			t.Fatalf("preflight changed known-good/runtime: current=%+v runtime=%+v", current, runtime.current)
		}
	})

	t.Run("generic prepare failure without known-good", func(t *testing.T) {
		store := openTestStore(t)
		runtime := newFakeRolloutRuntime()
		snapshot := testRuntimeSnapshot(t, "job-b", "b")
		runtime.failPrepare[snapshot.DeploymentJobID] = errors.New("generic preflight failure")
		engine := NewEngine(store, EngineConfig{Reconciler: runtime, RolloutTimeout: time.Second})
		record, err := engine.ReconcileRollout(context.Background(), testRolloutIntent(t, "rollout-b", snapshot, nil), nil)
		if err == nil || record.Intent.RolloutID != "" || runtime.prepareCalls != 1 || runtime.applyCalls != 0 || runtime.rollbackCalls != 0 {
			t.Fatalf("record=%+v err=%v prepare=%d apply=%d rollback=%d", record, err, runtime.prepareCalls, runtime.applyCalls, runtime.rollbackCalls)
		}
		if current, _ := store.CurrentKnownGood(context.Background(), snapshot.Target); current != nil || runtime.current.DeploymentJobID != "" {
			t.Fatalf("preflight fabricated known-good/runtime: current=%+v runtime=%+v", current, runtime.current)
		}
	})

	t.Run("begin rollout conflict", func(t *testing.T) {
		store := openTestStore(t)
		runtime := newFakeRolloutRuntime()
		a := testRuntimeSnapshot(t, "job-a", "a")
		if _, err := store.BeginRollout(context.Background(), testRolloutIntent(t, "rollout-a", a, nil), nil); err != nil {
			t.Fatal(err)
		}
		b := testRuntimeSnapshot(t, "job-b", "b")
		engine := NewEngine(store, EngineConfig{Reconciler: runtime, RolloutTimeout: time.Second})
		record, err := engine.ReconcileRollout(context.Background(), testRolloutIntent(t, "rollout-b", b, nil), nil)
		if err == nil || record.Intent.RolloutID != "" || runtime.prepareCalls != 1 || runtime.applyCalls != 0 || runtime.rollbackCalls != 0 {
			t.Fatalf("record=%+v err=%v prepare=%d apply=%d rollback=%d", record, err, runtime.prepareCalls, runtime.applyCalls, runtime.rollbackCalls)
		}
	})
}

func TestRolloutApplyAndReadinessRollbackFailuresAreTerminal(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*fakeRolloutRuntime, deploymentv1.RuntimeSnapshot)
	}{
		{name: "apply", configure: func(runtime *fakeRolloutRuntime, a deploymentv1.RuntimeSnapshot) {
			runtime.failApply[a.DeploymentJobID] = errors.New("rollback apply failed")
		}},
		{name: "readiness", configure: func(runtime *fakeRolloutRuntime, a deploymentv1.RuntimeSnapshot) {
			runtime.failReadiness[a.DeploymentJobID] = errors.New("rollback readiness failed")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openTestStore(t)
			runtime := newFakeRolloutRuntime()
			engine := NewEngine(store, EngineConfig{Reconciler: runtime, RolloutTimeout: time.Second})
			a := testRuntimeSnapshot(t, "job-a", "a")
			if record, err := engine.ReconcileRollout(context.Background(), testRolloutIntent(t, "rollout-a", a, nil), nil); err != nil || record.State != deploymentv1.RolloutStateSucceeded {
				t.Fatalf("seed A record=%+v err=%v", record, err)
			}
			knownA, _ := store.CurrentKnownGood(context.Background(), a.Target)
			tc.configure(runtime, a)
			b := testRuntimeSnapshot(t, "job-b", "b")
			runtime.failReadiness[b.DeploymentJobID] = errors.New("B failed")
			record, err := engine.ReconcileRollout(context.Background(), testRolloutIntent(t, "rollout-b", b, knownA), nil)
			if err == nil || record.State != deploymentv1.RolloutStateRollbackFailed || record.TerminalAt == nil {
				t.Fatalf("record=%+v err=%v", record, err)
			}
		})
	}
}

func TestRolloutRestartFromWaitingKeepsIDAndRollsBack(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.sqlite")
	store, err := OpenSQLiteStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	runtime := newFakeRolloutRuntime()
	engine := NewEngine(store, EngineConfig{Reconciler: runtime, RolloutTimeout: time.Second})
	a := testRuntimeSnapshot(t, "job-a", "d")
	if record, err := engine.ReconcileRollout(context.Background(), testRolloutIntent(t, "rollout-a", a, nil), nil); err != nil || record.State != deploymentv1.RolloutStateSucceeded {
		t.Fatalf("seed A record=%+v err=%v", record, err)
	}
	knownA, _ := store.CurrentKnownGood(context.Background(), a.Target)
	b := testRuntimeSnapshot(t, "job-b", "e")
	intentB := testRolloutIntent(t, "rollout-b", b, knownA)
	planB, _ := runtime.PrepareRollout(context.Background(), b)
	wal, err := store.BeginRollout(context.Background(), intentB, nil)
	if err != nil {
		t.Fatal(err)
	}
	wal, err = store.TransitionRollout(context.Background(), wal.Intent.RolloutID, deploymentv1.RolloutStateApplying, nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	resources, _ := runtime.ApplyRollout(context.Background(), planB)
	if _, err := store.TransitionRollout(context.Background(), wal.Intent.RolloutID, deploymentv1.RolloutStateWaiting, nil, resources, nil, false); err != nil {
		t.Fatal(err)
	}
	runtime.failReadiness[b.DeploymentJobID] = errors.New("B unready after restart")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLiteStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := NewEngine(reopened, EngineConfig{Reconciler: runtime, RolloutTimeout: time.Second})
	results, err := restarted.ReconcilePending(context.Background(), nil)
	if err == nil || len(results) != 1 || results[0].Intent.RolloutID != "rollout-b" || results[0].State != deploymentv1.RolloutStateRolledBack {
		t.Fatalf("results=%+v err=%v", results, err)
	}
	if runtime.current.DeploymentJobID != a.DeploymentJobID {
		t.Fatalf("restart rollback restored %s", runtime.current.DeploymentJobID)
	}
}

func TestExplicitRollbackRestartFromPreparedRestoresKnownGoodWithoutApplyingDesired(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "agent.sqlite")
	store, err := OpenSQLiteStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	runtime := newFakeRolloutRuntime()
	engine := NewEngine(store, EngineConfig{Reconciler: runtime, RolloutTimeout: time.Second})
	a := testRuntimeSnapshot(t, "job-a", "8")
	if record, err := engine.ReconcileRollout(context.Background(), testRolloutIntent(t, "rollout-a", a, nil), nil); err != nil || record.State != deploymentv1.RolloutStateSucceeded {
		t.Fatalf("seed A record=%+v err=%v", record, err)
	}
	knownA, _ := store.CurrentKnownGood(context.Background(), a.Target)
	b := testRuntimeSnapshot(t, "job-b", "9")
	if record, err := engine.ReconcileRollout(context.Background(), testRolloutIntent(t, "rollout-b", b, knownA), nil); err != nil || record.State != deploymentv1.RolloutStateSucceeded {
		t.Fatalf("seed B record=%+v err=%v", record, err)
	}
	knownB, _ := store.CurrentKnownGood(context.Background(), b.Target)
	rollback := testRolloutIntent(t, "rollout-back-to-a", b, knownA)
	rollback.Operation = deploymentv1.RolloutOperationRollback
	rollback.PreviousDigest = a.Image.Digest
	rollback.ExpectedKnownGoodID = knownB.ID
	rollback.ExpectedKnownGoodHash = knownB.SnapshotHash
	rollback.IntentHash = ""
	rollback, err = rollback.Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	stale := rollback
	stale.RolloutID = "rollout-stale-back-to-a"
	stale.ExpectedKnownGoodID = knownA.ID
	stale.ExpectedKnownGoodHash = knownA.SnapshotHash
	stale.IntentHash = ""
	stale, err = stale.Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ReconcileRollout(context.Background(), stale, nil); rolloutErrorCode(err) != deploymentv1.RolloutCodeConflict {
		t.Fatalf("stale explicit rollback err=%v", err)
	}
	if _, err := store.BeginRollout(context.Background(), rollback, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLiteStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := NewEngine(reopened, EngineConfig{Reconciler: runtime, RolloutTimeout: time.Second})
	results, err := restarted.ReconcilePending(context.Background(), nil)
	if err != nil || len(results) != 1 || results[0].State != deploymentv1.RolloutStateRolledBack {
		t.Fatalf("results=%+v err=%v", results, err)
	}
	if runtime.current.DeploymentJobID != a.DeploymentJobID || runtime.current.Image.Digest != a.Image.Digest {
		t.Fatalf("prepared rollback restart applied desired B instead of known-good A: %+v", runtime.current)
	}
}

func TestRolloutWALReplayConflictLockAndTerminalImmutability(t *testing.T) {
	store := openTestStore(t)
	snapshot := testRuntimeSnapshot(t, "job-a", "f")
	intent := testRolloutIntent(t, "rollout-a", snapshot, nil)
	first, err := store.BeginRollout(context.Background(), intent, nil)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.BeginRollout(context.Background(), intent, nil)
	if err != nil || replay.StateHash != first.StateHash || replay.Version != first.Version {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	conflictSnapshot := testRuntimeSnapshot(t, "job-conflict", "1")
	conflict := testRolloutIntent(t, intent.RolloutID, conflictSnapshot, nil)
	if _, err := store.BeginRollout(context.Background(), conflict, nil); !strings.Contains(errorText(err), deploymentv1.RolloutCodeConflict) {
		t.Fatalf("conflicting replay err=%v", err)
	}
	busy := testRolloutIntent(t, "rollout-busy", snapshot, nil)
	if _, err := store.BeginRollout(context.Background(), busy, nil); !strings.Contains(errorText(err), deploymentv1.RolloutCodeTargetBusy) {
		t.Fatalf("target lock err=%v", err)
	}
	applying, err := store.TransitionRollout(context.Background(), intent.RolloutID, deploymentv1.RolloutStateApplying, nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := store.TransitionRollout(context.Background(), intent.RolloutID, deploymentv1.RolloutStateFailed, deploymentv1.NewRolloutError(deploymentv1.RolloutCodeNoKnownGood, "none", false), applying.Resources, nil, true)
	if err != nil || failed.TerminalAt == nil {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	if _, err := store.TransitionRollout(context.Background(), intent.RolloutID, deploymentv1.RolloutStateRollingBack, nil, nil, nil, false); !strings.Contains(errorText(err), deploymentv1.RolloutCodeTerminalImmutable) {
		t.Fatalf("terminal mutation err=%v", err)
	}
}

func TestRolloutWALConcurrentTargetHasOneWinner(t *testing.T) {
	store := openTestStore(t)
	snapshot := testRuntimeSnapshot(t, "job-a", "2")
	intents := []deploymentv1.RolloutIntent{testRolloutIntent(t, "rollout-a", snapshot, nil), testRolloutIntent(t, "rollout-b", snapshot, nil)}
	var wait sync.WaitGroup
	errorsFound := make(chan error, len(intents))
	for _, intent := range intents {
		intent := intent
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := store.BeginRollout(context.Background(), intent, nil)
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(errorsFound)
	successes, busy := 0, 0
	for err := range errorsFound {
		if err == nil {
			successes++
		} else if strings.Contains(err.Error(), deploymentv1.RolloutCodeTargetBusy) {
			busy++
		}
	}
	if successes != 1 || busy != 1 {
		t.Fatalf("successes=%d busy=%d", successes, busy)
	}
}

func TestSQLiteRolloutMigrationFromCurrentSchema(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "legacy.sqlite")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE services (id TEXT NOT NULL, project_id TEXT NOT NULL, name TEXT NOT NULL, type TEXT NOT NULL, namespace TEXT NOT NULL, repo_url TEXT NOT NULL, branch TEXT NOT NULL, build_context TEXT NOT NULL, dockerfile TEXT NOT NULL, manifest_path TEXT NOT NULL, desired_state_json TEXT NOT NULL DEFAULT '{}', current_image_tag TEXT NOT NULL DEFAULT '', health TEXT NOT NULL DEFAULT 'unknown', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, PRIMARY KEY(project_id, id)); CREATE TABLE deployments (deploy_id TEXT PRIMARY KEY, project_id TEXT NOT NULL, service_id TEXT NOT NULL, service_name TEXT NOT NULL, started_at_unix INTEGER NOT NULL, finished_at_unix INTEGER NOT NULL DEFAULT 0, git_sha TEXT NOT NULL, image_tag TEXT NOT NULL, status TEXT NOT NULL, duration_ms INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '', triggered_by TEXT NOT NULL);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenSQLiteStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var rolloutTable string
	if err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='rollouts'`).Scan(&rolloutTable); err != nil || rolloutTable != "rollouts" {
		t.Fatalf("rollout migration table=%q err=%v", rolloutTable, err)
	}
	columns, err := store.tableColumns(context.Background(), "deployments")
	if err != nil || !columns["spec_hash"] || !columns["available_replicas"] {
		t.Fatalf("deployment migration columns=%v err=%v", columns, err)
	}
}

func TestRolloutCASRejectsCreateAndUpdateRaces(t *testing.T) {
	snapshot := testRuntimeSnapshot(t, "job-a", "3")
	command := snapshot.AgentCommand()
	_, resources, _, err := renderProductionResources(command)
	if err != nil {
		t.Fatal(err)
	}
	desired := rolloutObject{Kind: "Deployment", Namespace: resources.Namespace, Name: resources.DeploymentName, Manager: ProductionFieldManager, Object: resources.Deployment, Functional: objectFunctionalHash(resources.Deployment)}
	foreign := cloneMap(resources.Deployment)
	foreignMetadata := foreign["metadata"].(map[string]any)
	foreignMetadata["uid"] = "foreign-uid"
	foreignMetadata["resourceVersion"] = "1"
	foreignMetadata["labels"].(map[string]any)["app.kubernetes.io/managed-by"] = "foreign"
	runner := &sequenceRunner{outputs: [][]byte{mustJSON(t, foreign)}}
	plan := RolloutPlan{Snapshot: snapshot, DesiredObjects: []rolloutObject{desired}, Observed: []rolloutObservation{{rolloutObject: desired}}}
	if _, err := (ProductionAdapter{Runner: runner}).ApplyRollout(context.Background(), plan); rolloutErrorCode(err) != deploymentv1.RolloutCodeResourceChanged || runner.mutations != 0 {
		t.Fatalf("create race err=%v mutations=%d", err, runner.mutations)
	}
	owned := cloneMap(resources.Deployment)
	metadata := owned["metadata"].(map[string]any)
	metadata["uid"] = "owned-uid"
	metadata["resourceVersion"] = "2"
	runner = &sequenceRunner{outputs: [][]byte{mustJSON(t, owned)}}
	observed := rolloutObservation{rolloutObject: desired, Exists: true, UID: "owned-uid", ResourceVersion: "1"}
	observed.Functional = objectFunctionalHash(owned)
	plan.Observed = []rolloutObservation{observed}
	if _, err := (ProductionAdapter{Runner: runner}).ApplyRollout(context.Background(), plan); rolloutErrorCode(err) != deploymentv1.RolloutCodeResourceChanged || runner.mutations != 0 {
		t.Fatalf("resourceVersion race err=%v mutations=%d", err, runner.mutations)
	}
}

func TestRolloutCASRejectsForeignSpecManagerAndNeverForces(t *testing.T) {
	snapshot := testRuntimeSnapshot(t, "job-a", "4")
	_, resources, _, err := renderProductionResources(snapshot.AgentCommand())
	if err != nil {
		t.Fatal(err)
	}
	desired := rolloutObject{Kind: "Deployment", Namespace: resources.Namespace, Name: resources.DeploymentName, Manager: ProductionFieldManager, Object: resources.Deployment, Functional: objectFunctionalHash(resources.Deployment)}
	owned := cloneMap(resources.Deployment)
	metadata := owned["metadata"].(map[string]any)
	metadata["uid"] = "owned-uid"
	metadata["resourceVersion"] = "1"
	metadata["managedFields"] = []any{map[string]any{"manager": "foreign-manager", "fieldsV1": map[string]any{"f:spec": map[string]any{}}}}
	runner := &sequenceRunner{outputs: [][]byte{mustJSON(t, owned)}}
	observed := rolloutObservation{rolloutObject: desired, Exists: true, UID: "owned-uid", ResourceVersion: "1"}
	observed.Functional = objectFunctionalHash(owned)
	plan := RolloutPlan{Snapshot: snapshot, DesiredObjects: []rolloutObject{desired}, Observed: []rolloutObservation{observed}}
	if _, err := (ProductionAdapter{Runner: runner}).ApplyRollout(context.Background(), plan); rolloutErrorCode(err) != deploymentv1.RolloutCodeOwnershipConflict || runner.mutations != 0 {
		t.Fatalf("foreign manager err=%v mutations=%d", err, runner.mutations)
	}
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call, " "), "force-conflicts") {
			t.Fatalf("force adoption argv=%v", call)
		}
	}
}

func TestRolloutReadinessUsesAppDigestServiceEndpointsAndIngress(t *testing.T) {
	snapshot := testRuntimeSnapshot(t, "job-ready", "5")
	plan, runner := readinessFixture(t, snapshot)
	probe := staticRoutingProbe{result: RoutingProbeResult{StatusCode: 200, EvidenceHash: strings.Repeat("9", 64)}}
	adapter := ProductionAdapter{Runner: runner, RoutingProbe: probe, RequireLocalRouting: true}
	evidence, _, ready, err := adapter.readinessOnce(context.Background(), plan)
	if err != nil || !ready || !evidence.RuntimeReady || !evidence.LocalRoutingReady {
		t.Fatalf("evidence=%+v ready=%v err=%v", evidence, ready, err)
	}
	if evidence.ExternalReady || evidence.ExternalEvidenceHash != "" {
		t.Fatalf("local readiness fabricated public evidence: %+v", evidence)
	}
}

func TestRolloutReadinessFailsClosedForStaleAndMismatchedState(t *testing.T) {
	snapshot := testRuntimeSnapshot(t, "job-ready", "6")
	for _, tc := range []struct {
		name   string
		mutate func(map[string][]byte)
	}{
		{name: "stale generation", mutate: func(outputs map[string][]byte) {
			key := readinessKey("deployment", snapshot)
			object := decodeMap(t, outputs[key])
			object["status"].(map[string]any)["observedGeneration"] = 1
			outputs[key] = mustJSON(t, object)
		}},
		{name: "service selector", mutate: func(outputs map[string][]byte) {
			key := readinessKey("service", snapshot)
			object := decodeMap(t, outputs[key])
			object["spec"].(map[string]any)["selector"].(map[string]any)["opsi.dev/service"] = "other"
			outputs[key] = mustJSON(t, object)
		}},
		{name: "ingress class", mutate: func(outputs map[string][]byte) {
			_, resources, _, _ := renderProductionResources(snapshot.AgentCommand())
			exposure, _ := renderExposure(context.Background(), snapshot.AgentCommand(), snapshot.Exposure, nil)
			key := kubectlKey("get", "ingress", exposure.IngressName, "-n", resources.Namespace, "-o", "json")
			object := decodeMap(t, outputs[key])
			object["spec"].(map[string]any)["ingressClassName"] = "foreign"
			outputs[key] = mustJSON(t, object)
		}},
		{name: "app image id", mutate: func(outputs map[string][]byte) {
			_, resources, _, _ := renderProductionResources(snapshot.AgentCommand())
			key := kubectlKey("get", "pods", "-n", resources.Namespace, "-l", selectorString(resources.Selector), "-o", "json")
			object := decodeMap(t, outputs[key])
			statuses := object["items"].([]any)[0].(map[string]any)["status"].(map[string]any)["containerStatuses"].([]any)
			statuses[1].(map[string]any)["imageID"] = "containerd://sha256:" + strings.Repeat("0", 64)
			outputs[key] = mustJSON(t, object)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, runner := readinessFixture(t, snapshot)
			tc.mutate(runner.outputs)
			adapter := ProductionAdapter{Runner: runner, RoutingProbe: staticRoutingProbe{result: RoutingProbeResult{StatusCode: 200, EvidenceHash: strings.Repeat("9", 64)}}}
			_, _, ready, err := adapter.readinessOnce(context.Background(), plan)
			if err != nil || ready {
				t.Fatalf("ready=%v err=%v", ready, err)
			}
		})
	}
	plan, runner := readinessFixture(t, snapshot)
	if _, _, _, err := (ProductionAdapter{Runner: runner, RequireLocalRouting: true}).readinessOnce(context.Background(), plan); rolloutErrorCode(err) != deploymentv1.RolloutCodeExternalUnavailable {
		t.Fatalf("missing local probe err=%v", err)
	}
}

func TestBoundedHTTPProbeRejectsUnsafeTargets(t *testing.T) {
	snapshot := testRuntimeSnapshot(t, "job-probe", "7")
	for _, probe := range []BoundedHTTPProbe{
		{Scheme: "file", Address: "127.0.0.1", Port: 80},
		{Scheme: "http", Address: "169.254.169.254", Port: 80},
		{Scheme: "http", Address: "127.0.0.1", Port: 0},
	} {
		if _, err := probe.Probe(context.Background(), snapshot); err == nil {
			t.Fatalf("unsafe probe accepted: %+v", probe)
		}
	}
}

func testRuntimeSnapshot(t *testing.T, jobID, digestCharacter string) deploymentv1.RuntimeSnapshot {
	t.Helper()
	command := testAgentCommand(t)
	command.JobID = jobID
	command.Image.Digest = "sha256:" + strings.Repeat(digestCharacter, 64)
	command.Image.Reference = command.Image.Repository + "@" + command.Image.Digest
	exposure, err := (exposurev1.ExposureSpec{SchemaVersion: exposurev1.SchemaVersion, ProjectID: command.ProjectID, EnvironmentID: command.EnvironmentID, RuntimeID: command.RuntimeID, ServiceKey: command.Workload.ServiceKey, DeploymentJobID: jobID, Hostname: "api.example.com", Path: "/", ServicePort: command.Workload.ContainerPort, TLS: exposurev1.TLSConfig{Mode: exposurev1.TLSDisabled}}).Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	return deploymentv1.RuntimeSnapshot{SchemaVersion: deploymentv1.RuntimeSnapshotVersion, Target: deploymentv1.RuntimeTarget{ProjectID: command.ProjectID, EnvironmentID: command.EnvironmentID, RuntimeID: command.RuntimeID, ServiceKey: command.Workload.ServiceKey, NodeID: command.NodeID, AgentID: command.AgentID}, DeploymentJobID: jobID, Image: command.Image, Workload: command.Workload, WorkloadSpecHash: command.SpecHash, Exposure: exposure, ExposureSpecHash: exposure.SpecHash, Authority: deploymentv1.RuntimeAuthority{TopologyPlanID: "topology-1", TopologyRevision: 1, TopologyHash: strings.Repeat("a", 64), DeploymentPolicyID: "policy-1", DeploymentPolicyRevision: 1, DeploymentPolicyHash: strings.Repeat("b", 64), RoutingDecisionHash: strings.Repeat("c", 64)}}
}

func testRolloutIntent(t *testing.T, rolloutID string, desired deploymentv1.RuntimeSnapshot, previous *deploymentv1.KnownGoodSnapshot) deploymentv1.RolloutIntent {
	t.Helper()
	intent := deploymentv1.RolloutIntent{SchemaVersion: deploymentv1.RolloutSchemaVersion, RolloutID: rolloutID, Target: desired.Target, Desired: desired, Attempt: 1, CreatedAt: time.Now().UTC()}
	if previous != nil {
		intent.PreviousKnownGoodID = previous.ID
		intent.PreviousKnownGoodHash = previous.SnapshotHash
		intent.PreviousDigest = previous.Runtime.Image.Digest
	}
	canonical, err := intent.Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func containsOrderedStates(states []string, expected ...string) bool {
	index := 0
	for _, state := range states {
		if index < len(expected) && state == expected[index] {
			index++
		}
	}
	return index == len(expected)
}

type fakeRolloutRuntime struct {
	current          deploymentv1.RuntimeSnapshot
	failPrepare      map[string]error
	failApply        map[string]error
	failReadiness    map[string]error
	resourceVersion  int
	prepareCalls     int
	applyCalls       int
	rollbackCalls    int
	rollbackTargetID string
}

type sequenceRunner struct {
	outputs   [][]byte
	calls     [][]string
	mutations int
}

type staticRoutingProbe struct {
	result RoutingProbeResult
	err    error
}

func (p staticRoutingProbe) Probe(context.Context, deploymentv1.RuntimeSnapshot) (RoutingProbeResult, error) {
	return p.result, p.err
}

func readinessFixture(t *testing.T, snapshot deploymentv1.RuntimeSnapshot) (RolloutPlan, *exposureRunner) {
	t.Helper()
	command := snapshot.AgentCommand()
	_, resources, namespace, err := renderProductionResources(command)
	if err != nil {
		t.Fatal(err)
	}
	exposure, err := renderExposure(context.Background(), command, snapshot.Exposure, nil)
	if err != nil {
		t.Fatal(err)
	}
	deployment := cloneMap(resources.Deployment)
	deploymentMetadata := deployment["metadata"].(map[string]any)
	deploymentMetadata["uid"] = "uid-deployment"
	deploymentMetadata["resourceVersion"] = "1"
	deploymentMetadata["generation"] = 2
	deployment["status"] = map[string]any{"observedGeneration": 2, "availableReplicas": 1}
	service := cloneMap(resources.Service)
	serviceMetadata := service["metadata"].(map[string]any)
	serviceMetadata["uid"] = "uid-service"
	serviceMetadata["resourceVersion"] = "1"
	ingress := cloneMap(exposure.Ingress)
	ingressMetadata := ingress["metadata"].(map[string]any)
	ingressMetadata["uid"] = "uid-ingress"
	ingressMetadata["resourceVersion"] = "1"
	ingressMetadata["generation"] = 1
	endpoints := map[string]any{
		"metadata": map[string]any{"name": resources.ServiceName, "namespace": namespace},
		"subsets": []any{map[string]any{
			"addresses": []any{map[string]any{"ip": "10.0.0.1"}},
			"ports":     []any{map[string]any{"name": "http", "port": command.Workload.ContainerPort}},
		}},
	}
	pods := map[string]any{"items": []any{map[string]any{"status": map[string]any{"containerStatuses": []any{
		map[string]any{"name": "sidecar", "ready": true, "imageID": "containerd://sha256:" + strings.Repeat("0", 64)},
		map[string]any{"name": deploymentv1.ApplicationContainer, "ready": true, "imageID": "containerd://" + command.Image.Digest},
	}}}}}
	outputs := map[string][]byte{
		kubectlKey("get", "deployment", resources.DeploymentName, "-n", namespace, "-o", "json"):           mustJSON(t, deployment),
		kubectlKey("get", "service", resources.ServiceName, "-n", namespace, "-o", "json"):                 mustJSON(t, service),
		kubectlKey("get", "endpoints", resources.ServiceName, "-n", namespace, "-o", "json"):               mustJSON(t, endpoints),
		kubectlKey("get", "ingress", exposure.IngressName, "-n", namespace, "-o", "json"):                  mustJSON(t, ingress),
		kubectlKey("get", "pods", "-n", namespace, "-l", selectorString(resources.Selector), "-o", "json"): mustJSON(t, pods),
	}
	return RolloutPlan{Snapshot: snapshot, Command: command, Resources: resources, Exposure: exposure}, &exposureRunner{outputs: outputs, errors: map[string]error{}}
}

func readinessKey(kind string, snapshot deploymentv1.RuntimeSnapshot) string {
	_, resources, namespace, _ := renderProductionResources(snapshot.AgentCommand())
	name := resources.DeploymentName
	if kind == "service" {
		name = resources.ServiceName
	}
	return kubectlKey("get", kind, name, "-n", namespace, "-o", "json")
}

func decodeMap(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func (r *sequenceRunner) Run(_ context.Context, input []byte, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	if len(args) > 0 && (args[0] == "create" || args[0] == "replace") {
		r.mutations++
		var value map[string]any
		if err := json.Unmarshal(input, &value); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if len(r.outputs) == 0 {
		return nil, errors.New("unexpected read")
	}
	output := r.outputs[0]
	r.outputs = r.outputs[1:]
	return output, nil
}

func newFakeRolloutRuntime() *fakeRolloutRuntime {
	return &fakeRolloutRuntime{failPrepare: map[string]error{}, failApply: map[string]error{}, failReadiness: map[string]error{}}
}

func (f *fakeRolloutRuntime) PrepareRollout(_ context.Context, snapshot deploymentv1.RuntimeSnapshot) (RolloutPlan, error) {
	f.prepareCalls++
	if err := f.failPrepare[snapshot.DeploymentJobID]; err != nil {
		return RolloutPlan{}, err
	}
	return RolloutPlan{Snapshot: snapshot}, nil
}

func (f *fakeRolloutRuntime) ApplyRollout(_ context.Context, plan RolloutPlan) ([]deploymentv1.ResourceIdentity, error) {
	f.applyCalls++
	if plan.Snapshot.DeploymentJobID == f.rollbackTargetID {
		f.rollbackCalls++
	}
	if err := f.failApply[plan.Snapshot.DeploymentJobID]; err != nil {
		return nil, err
	}
	f.current = plan.Snapshot
	f.resourceVersion++
	return fakeResourceIdentities(f.resourceVersion), nil
}

func (f *fakeRolloutRuntime) ObserveReadiness(_ context.Context, plan RolloutPlan) (deploymentv1.ReadinessEvidence, []deploymentv1.ResourceIdentity, error) {
	if err := f.failReadiness[plan.Snapshot.DeploymentJobID]; err != nil {
		return deploymentv1.ReadinessEvidence{}, nil, err
	}
	desiredHash, _ := plan.Snapshot.Hash()
	currentHash, _ := f.current.Hash()
	if desiredHash != currentHash {
		return deploymentv1.ReadinessEvidence{}, nil, errors.New("runtime snapshot mismatch")
	}
	evidence := deploymentv1.ReadinessEvidence{SchemaVersion: deploymentv1.ReadinessEvidenceVersion, RuntimeReady: true, LocalRoutingReady: true, WorkloadEvidenceHash: strings.Repeat("1", 64), ServiceEvidenceHash: strings.Repeat("2", 64), ExposureEvidenceHash: strings.Repeat("3", 64), ApplicationImageIDHash: strings.Repeat("4", 64), LocalProbeEvidenceHash: strings.Repeat("5", 64), ObservedAt: time.Now().UTC()}
	return evidence, fakeResourceIdentities(f.resourceVersion), nil
}

func fakeResourceIdentities(version int) []deploymentv1.ResourceIdentity {
	rv := fmtInt(version)
	return []deploymentv1.ResourceIdentity{
		{Kind: "Deployment", Namespace: "opsi", Name: "api", UID: "uid-deployment", ResourceVersion: rv, FunctionalHash: strings.Repeat("6", 64)},
		{Kind: "Service", Namespace: "opsi", Name: "api", UID: "uid-service", ResourceVersion: rv, FunctionalHash: strings.Repeat("7", 64)},
		{Kind: "Ingress", Namespace: "opsi", Name: "api", UID: "uid-ingress", ResourceVersion: rv, FunctionalHash: strings.Repeat("8", 64)},
	}
}

func fmtInt(value int) string {
	if value == 0 {
		return "1"
	}
	return strconv.Itoa(value)
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
