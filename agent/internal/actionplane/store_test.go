package actionplane

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
)

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
	if err := store.MarkApproved(context.Background(), plan.ProjectID, challenge.ID); err != nil {
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
	if err := store.MarkApproved(context.Background(), plan.ProjectID, challenge.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginExecution(context.Background(), plan.ProjectID, challenge.ID, "grant"); err != nil {
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
	if record.Status != actionv1.StatusFailed || record.Result.FailureCode != actionv1.FailureInterrupted {
		t.Fatalf("interrupted action not failed closed: %#v", record)
	}
}

func testActionFixtures(now time.Time) (actionv1.ActionPlan, actionv1.CurrentState, actionv1.ApprovalChallenge) {
	state := actionv1.CurrentState{SchemaVersion: actionv1.SchemaVersion, ProjectID: "p1", Target: actionv1.TargetIdentity{ProjectID: "p1", NodeID: "n1", ServiceID: "s1"}, Workload: &actionv1.WorkloadState{UID: "uid", ResourceVersion: "1", Generation: 1, DesiredReplicas: 1, ObservedReplicas: 1, ReadyReplicas: 1}}
	state.StateHash, _ = actionv1.StateHash(state)
	plan := actionv1.ActionPlan{SchemaVersion: actionv1.SchemaVersion, ID: "a1", ProjectID: "p1", NodeID: "n1", ServiceID: "s1", Target: state.Target, Kind: actionv1.ActionRestartWorkload, Parameters: actionv1.ActionParameters{RestartWorkload: &actionv1.RestartWorkloadParameters{}}, Origin: actionv1.OriginManualCLI, RequestedBy: "u1", Risk: actionv1.RiskR2, CurrentStateHash: state.StateHash, IssuedAt: now, ExpiresAt: now.Add(actionv1.MaxPlanTTL)}
	plan.PlanHash, _ = actionv1.PlanHash(plan)
	challenge := actionv1.ApprovalChallenge{SchemaVersion: actionv1.SchemaVersion, ID: "c1", ActionID: plan.ID, ProjectID: plan.ProjectID, PlanHash: plan.PlanHash, StateHash: state.StateHash, Nonce: "nonce", IssuedAt: now, ExpiresAt: now.Add(actionv1.MaxChallengeTTL)}
	return plan, state, challenge
}
