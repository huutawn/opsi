package actionplane

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
)

func TestSQLiteMigrationAddsExecutionEvidenceColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "historical.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE action_plane_actions (
action_id TEXT PRIMARY KEY, challenge_id TEXT NOT NULL UNIQUE, project_id TEXT NOT NULL, target_key TEXT NOT NULL,
plan_json TEXT NOT NULL, state_json TEXT NOT NULL, challenge_json TEXT NOT NULL, status TEXT NOT NULL,
nonce_consumed INTEGER NOT NULL DEFAULT 0, grant_hash TEXT NOT NULL DEFAULT '', result_json TEXT NOT NULL DEFAULT '',
created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.db.Query(`PRAGMA table_info(action_plane_actions)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	for _, name := range []string{"approved_by", "approved_device_id", "execution_started_at"} {
		if !columns[name] {
			t.Fatalf("additive migration missing %s", name)
		}
	}
}

func TestSQLiteActionDurabilityAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actions.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	plan, state, challenge := testActionFixtures(now)
	if err := store.Create(context.Background(), plan, state, challenge); err != nil {
		t.Fatal(err)
	}
	reservation, err := store.ReserveExecution(context.Background(), plan.ProjectID, challenge.ID, "grant", "u1", "device-1")
	if err != nil || reservation.Outcome != ReservationAcquired {
		t.Fatalf("reservation=%+v err=%v", reservation, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, err := store.Load(context.Background(), plan.ProjectID, challenge.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Plan.ID != plan.ID || record.Status != actionv1.StatusApproved || record.Challenge.Nonce != challenge.Nonce {
		t.Fatalf("durability lost: %#v", record)
	}
}

func TestSQLiteRecoveryFailsClosedForInterruptedExecution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actions.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	plan, state, challenge := testActionFixtures(now)
	if err := store.Create(context.Background(), plan, state, challenge); err != nil {
		t.Fatal(err)
	}
	reservation, err := store.ReserveExecution(context.Background(), plan.ProjectID, challenge.ID, "grant", "u1", "device-1")
	if err != nil || reservation.Outcome != ReservationAcquired {
		t.Fatalf("reservation=%+v err=%v", reservation, err)
	}
	if _, err := store.BeginExecution(context.Background(), reservation.Record, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, err := store.Load(context.Background(), plan.ProjectID, challenge.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != actionv1.StatusExecuting || !record.NonceConsumed || record.GrantHash != "grant" || record.ApprovedBy != "u1" || record.DeviceID != "device-1" || !record.ExecutionStartedAt.Equal(now.Add(time.Second)) {
		t.Fatalf("restart invented a terminal result: %#v", record)
	}
	if got := lockCount(t, store, plan.ID); got != 1 {
		t.Fatalf("unresolved execution lock count=%d", got)
	}
	runtime := &fakeRuntime{state: state}
	service := &Service{Store: store, Runtime: runtime, Now: func() time.Time { return now.Add(2 * time.Second) }}
	if err := service.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	record, err = store.Load(context.Background(), plan.ProjectID, challenge.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != actionv1.StatusSucceeded || record.Result.Message != "action result recovered after Agent restart" {
		t.Fatalf("recovered result=%#v", record)
	}
	if len(runtime.calls) != 0 || runtime.postCalls != 1 {
		t.Fatalf("recovery re-executed mutation: calls=%v post_checks=%d", runtime.calls, runtime.postCalls)
	}
	if got := lockCount(t, store, plan.ID); got != 0 {
		t.Fatalf("terminal lock count=%d", got)
	}
}

func TestSQLiteRecoveryKeepsUnreadableExecutionUnresolvedAndTargetLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actions.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	plan, state, challenge := testActionFixtures(now)
	if err := store.Create(context.Background(), plan, state, challenge); err != nil {
		t.Fatal(err)
	}
	reservation, err := store.ReserveExecution(context.Background(), plan.ProjectID, challenge.ID, "grant", "u1", "device-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginExecution(context.Background(), reservation.Record, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := &fakeRuntime{state: state, currentErr: ErrFactualStateUnavailable}
	if err := (&Service{Store: store, Runtime: runtime}).Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	record, err := store.Load(context.Background(), plan.ProjectID, challenge.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != actionv1.StatusExecuting || lockCount(t, store, plan.ID) != 1 || len(runtime.calls) != 0 {
		t.Fatalf("unreadable recovery changed execution: %#v calls=%v", record, runtime.calls)
	}
	secondPlan, secondState, secondChallenge := testActionFixtures(now.Add(time.Second))
	secondPlan.ID, secondChallenge.ID, secondChallenge.ActionID = "a2", "c2", "a2"
	secondPlan.PlanHash, _ = actionv1.PlanHash(secondPlan)
	secondChallenge.PlanHash = secondPlan.PlanHash
	if err := store.Create(context.Background(), secondPlan, secondState, secondChallenge); err != nil {
		t.Fatal(err)
	}
	locked, err := store.ReserveExecution(context.Background(), secondPlan.ProjectID, secondChallenge.ID, "grant-2", "u1", "device-1")
	if err != nil || locked.Outcome != ReservationTargetLocked {
		t.Fatalf("second reservation=%+v err=%v", locked, err)
	}
}

func TestSQLiteRecoveryRejectsReservedPreMutationAndReleasesLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actions.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	plan, state, challenge := testActionFixtures(now)
	if err := store.Create(context.Background(), plan, state, challenge); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveExecution(context.Background(), plan.ProjectID, challenge.ID, "grant", "u1", "device-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := (&Service{Store: store, Runtime: &fakeRuntime{state: state}, Now: func() time.Time { return now.Add(time.Second) }}).Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	record, err := store.Load(context.Background(), plan.ProjectID, challenge.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != actionv1.StatusRejected || record.Result.FailureCode != actionv1.FailureInterruptedPreMutation || lockCount(t, store, plan.ID) != 0 {
		t.Fatalf("reserved recovery=%#v", record)
	}
}

func TestCompleteGuardKeepsFirstTerminalResultAndEvent(t *testing.T) {
	store, err := OpenSQLiteStore("")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1_800_000_000, 0).UTC()
	plan, state, challenge := testActionFixtures(now)
	if err := store.Create(context.Background(), plan, state, challenge); err != nil {
		t.Fatal(err)
	}
	reservation, err := store.ReserveExecution(context.Background(), plan.ProjectID, challenge.ID, "grant", "u1", "device-1")
	if err != nil {
		t.Fatal(err)
	}
	executing, err := store.BeginExecution(context.Background(), reservation.Record, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	succeeded := actionv1.ActionResult{SchemaVersion: actionv1.SchemaVersion, ActionID: plan.ID, ChallengeID: challenge.ID, ProjectID: plan.ProjectID, Status: actionv1.StatusSucceeded, ApprovedBy: "u1", DeviceID: "device-1", StartedAt: executing.ExecutionStartedAt, FinishedAt: now.Add(2 * time.Second)}
	if _, err := store.Complete(context.Background(), executing, succeeded); err != nil {
		t.Fatal(err)
	}
	failed := succeeded
	failed.Status, failed.FailureCode, failed.FinishedAt = actionv1.StatusFailed, actionv1.FailurePostCheck, now.Add(3*time.Second)
	persisted, err := store.Complete(context.Background(), executing, failed)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Result.Status != actionv1.StatusSucceeded {
		t.Fatalf("stale writer overwrote result: %+v", persisted.Result)
	}
	assertEventCounts(t, store, plan.ID, map[actionv1.ActionStatus]int{actionv1.StatusSucceeded: 1, actionv1.StatusFailed: 0})
}

func TestMigrationReleasesOnlyPersistedTerminalLocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actions.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	plan, state, challenge := testActionFixtures(now)
	if err := store.Create(context.Background(), plan, state, challenge); err != nil {
		t.Fatal(err)
	}
	reservation, err := store.ReserveExecution(context.Background(), plan.ProjectID, challenge.ID, "grant", "u1", "device-1")
	if err != nil {
		t.Fatal(err)
	}
	executing, err := store.BeginExecution(context.Background(), reservation.Record, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	result := actionv1.ActionResult{SchemaVersion: actionv1.SchemaVersion, ActionID: plan.ID, ChallengeID: challenge.ID, ProjectID: plan.ProjectID, Status: actionv1.StatusSucceeded, FinishedAt: now.Add(2 * time.Second)}
	if _, err := store.Complete(context.Background(), executing, result); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO action_plane_target_locks(target_key,action_id,acquired_at) VALUES(?,?,?)`, plan.Target.Key(), plan.ID, now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if got := lockCount(t, store, plan.ID); got != 0 {
		t.Fatalf("terminal historical lock count=%d", got)
	}
}

func TestReserveExecutionReportsPersistedApprovedStateWithoutSyntheticExecuting(t *testing.T) {
	store, err := OpenSQLiteStore("")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1_800_000_000, 0).UTC()
	plan, state, challenge := testActionFixtures(now)
	if err := store.Create(context.Background(), plan, state, challenge); err != nil {
		t.Fatal(err)
	}
	first, err := store.ReserveExecution(context.Background(), plan.ProjectID, challenge.ID, "grant", "u1", "device-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ReserveExecution(context.Background(), plan.ProjectID, challenge.ID, "grant", "u1", "device-1")
	if err != nil || second.Outcome != ReservationSameInProgress || second.Record.Status != actionv1.StatusApproved {
		t.Fatalf("reservation=%+v err=%v", second, err)
	}
	if first.Record.Status != actionv1.StatusApproved {
		t.Fatalf("first status=%s", first.Record.Status)
	}
}

func lockCount(t *testing.T, store *SQLiteStore, actionID string) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM action_plane_target_locks WHERE action_id=?`, actionID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func testActionFixtures(now time.Time) (actionv1.ActionPlan, actionv1.CurrentState, actionv1.ApprovalChallenge) {
	state := actionv1.CurrentState{SchemaVersion: actionv1.SchemaVersion, ProjectID: "p1", Target: actionv1.TargetIdentity{ProjectID: "p1", NodeID: "n1", ServiceID: "s1", EnvironmentID: "prod", RuntimeID: "runtime-1"}, Workload: &actionv1.WorkloadState{UID: "uid", ResourceVersion: "1", Generation: 1, DesiredReplicas: 1, ObservedReplicas: 1, ReadyReplicas: 1}}
	state.StateHash, _ = actionv1.StateHash(state)
	plan := actionv1.ActionPlan{SchemaVersion: actionv1.SchemaVersion, ID: "a1", ProjectID: "p1", NodeID: "n1", ServiceID: "s1", Target: state.Target, Kind: actionv1.ActionRestartWorkload, Parameters: actionv1.ActionParameters{RestartWorkload: &actionv1.RestartWorkloadParameters{}}, Origin: actionv1.OriginManualCLI, RequestedBy: "u1", Risk: actionv1.RiskR2, CurrentStateHash: state.StateHash, IssuedAt: now, ExpiresAt: now.Add(actionv1.MaxPlanTTL)}
	plan.PlanHash, _ = actionv1.PlanHash(plan)
	challenge := actionv1.ApprovalChallenge{SchemaVersion: actionv1.SchemaVersion, ID: "c1", ActionID: plan.ID, ProjectID: plan.ProjectID, PlanHash: plan.PlanHash, StateHash: state.StateHash, Nonce: "nonce", IssuedAt: now, ExpiresAt: now.Add(actionv1.MaxChallengeTTL)}
	return plan, state, challenge
}
