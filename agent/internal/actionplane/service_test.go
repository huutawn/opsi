package actionplane

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
)

type fakeRuntime struct {
	mu           sync.Mutex
	state        actionv1.CurrentState
	calls        []actionv1.ActionKind
	currentErr   error
	currentFn    func(context.Context) (actionv1.CurrentState, error)
	currentCalls int
	postErr      error
	postCalls    int
}

func (f *fakeRuntime) CurrentState(ctx context.Context, _ actionv1.TargetIdentity, _ actionv1.ActionKind, _ actionv1.ActionParameters) (actionv1.CurrentState, error) {
	f.mu.Lock()
	f.currentCalls++
	state, err, currentFn := f.state, f.currentErr, f.currentFn
	f.mu.Unlock()
	if currentFn != nil {
		return currentFn(ctx)
	}
	return state, err
}
func (f *fakeRuntime) RestartWorkload(context.Context, actionv1.TargetIdentity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, actionv1.ActionRestartWorkload)
	return nil
}
func (f *fakeRuntime) ScaleWorkload(_ context.Context, _ actionv1.TargetIdentity, replicas int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, actionv1.ActionScaleWorkload)
	f.state.Workload.DesiredReplicas = replicas
	return nil
}
func (f *fakeRuntime) GatewayReconcile(context.Context, actionv1.TargetIdentity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, actionv1.ActionGatewayReconcile)
	return nil
}
func (f *fakeRuntime) ResolveIncident(context.Context, actionv1.TargetIdentity, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, actionv1.ActionIncidentResolve)
	return nil
}
func (f *fakeRuntime) PostCheck(context.Context, actionv1.TargetIdentity, actionv1.ActionKind, actionv1.ActionParameters, actionv1.CurrentState) (actionv1.CurrentState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.postCalls++
	if f.postErr != nil {
		return f.state, f.postErr
	}
	return f.state, nil
}

type fakeDevices struct {
	device Device
	err    error
}

func (f fakeDevices) Resolve(context.Context, string, string, string) (Device, error) {
	if f.err != nil {
		return Device{}, f.err
	}
	return f.device, nil
}

func TestPreflightBindsTrustedOriginActorAndR4FailsClosed(t *testing.T) {
	runtime := &fakeRuntime{state: fixtureState()}
	service := newTestService(t, runtime, false)
	preflight, err := service.Preflight(context.Background(), fixtureRequest(actionv1.ActionRestartWorkload))
	if err != nil {
		t.Fatal(err)
	}
	if preflight.Plan.Origin != actionv1.OriginManualCLI || preflight.Plan.RequestedBy != "u1" || preflight.Challenge.ID == "" {
		t.Fatalf("untrusted plan: %#v", preflight)
	}
	if err := ValidateRisk(actionv1.RiskR4); err == nil {
		t.Fatal("R4 policy did not fail closed")
	}
}

func TestExecuteRejectsStaleStateAndDoesNotMutate(t *testing.T) {
	runtime := &fakeRuntime{state: fixtureState()}
	service := newTestService(t, runtime, false)
	preflight, err := service.Preflight(context.Background(), fixtureRequest(actionv1.ActionRestartWorkload))
	if err != nil {
		t.Fatal(err)
	}
	grant := signGrant(t, preflight.Challenge, service)
	runtime.state.Workload.ReadyReplicas = 0
	result, err := service.Execute(context.Background(), &actionv1.ExecuteRequest{ProjectID: "p1", ChallengeID: preflight.Challenge.ID, Grant: grant})
	if err != nil {
		t.Fatal(err)
	}
	if result.FailureCode != actionv1.FailureStateStale || len(runtime.calls) != 0 {
		t.Fatalf("stale execute result=%#v calls=%v", result, runtime.calls)
	}
}

func TestExecuteRunsAllTypedActionsAndExactReplay(t *testing.T) {
	for _, kind := range []actionv1.ActionKind{actionv1.ActionRestartWorkload, actionv1.ActionScaleWorkload, actionv1.ActionGatewayReconcile, actionv1.ActionIncidentResolve} {
		runtime := &fakeRuntime{state: fixtureState()}
		service := newTestService(t, runtime, false)
		request := fixtureRequest(kind)
		preflight, err := service.Preflight(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		grant := signGrant(t, preflight.Challenge, service)
		first, err := service.Execute(context.Background(), &actionv1.ExecuteRequest{ProjectID: "p1", ChallengeID: preflight.Challenge.ID, Grant: grant})
		if err != nil || first.Status != actionv1.StatusSucceeded {
			t.Fatalf("%s result=%#v err=%v", kind, first, err)
		}
		second, err := service.Execute(context.Background(), &actionv1.ExecuteRequest{ProjectID: "p1", ChallengeID: preflight.Challenge.ID, Grant: grant})
		if err != nil || second.Message != first.Message || len(runtime.calls) != 1 {
			t.Fatalf("%s replay result=%#v err=%v calls=%v", kind, second, err, runtime.calls)
		}
	}
}

func TestExecuteRejectsWrongUserDeviceAndMalformedSignature(t *testing.T) {
	runtime := &fakeRuntime{state: fixtureState()}
	service := newTestService(t, runtime, false)
	preflight, err := service.Preflight(context.Background(), fixtureRequest(actionv1.ActionRestartWorkload))
	if err != nil {
		t.Fatal(err)
	}
	grant := signGrant(t, preflight.Challenge, service)
	grant.Signature[0] ^= 1
	result, err := service.Execute(context.Background(), &actionv1.ExecuteRequest{ProjectID: "p1", ChallengeID: preflight.Challenge.ID, Grant: grant})
	if err != nil || result.FailureCode != actionv1.FailureSignatureInvalid {
		t.Fatalf("malformed signature result=%#v err=%v", result, err)
	}
	service.Authenticate = func(context.Context, string) (Principal, error) {
		return Principal{ProjectID: "p1", UserID: "other", Role: "developer"}, nil
	}
	if _, err := service.Execute(context.Background(), &actionv1.ExecuteRequest{ProjectID: "p1", ChallengeID: preflight.Challenge.ID, Grant: signGrant(t, preflight.Challenge, service)}); !errors.Is(err, ErrWrongUser) {
		t.Fatalf("wrong user error=%v", err)
	}
}

func TestExecuteRejectsRevokedAndExpiredApprovalDurably(t *testing.T) {
	runtime := &fakeRuntime{state: fixtureState()}
	service := newTestService(t, runtime, true)
	preflight, err := service.Preflight(context.Background(), fixtureRequest(actionv1.ActionRestartWorkload))
	if err != nil {
		t.Fatal(err)
	}
	grant := signGrant(t, preflight.Challenge, service)
	revoked, err := service.Execute(context.Background(), &actionv1.ExecuteRequest{ProjectID: "p1", ChallengeID: preflight.Challenge.ID, Grant: grant})
	if err != nil || revoked.FailureCode != actionv1.FailureDeviceRevoked {
		t.Fatalf("revoked result=%#v err=%v", revoked, err)
	}
	replayed, err := service.Execute(context.Background(), &actionv1.ExecuteRequest{ProjectID: "p1", ChallengeID: preflight.Challenge.ID, Grant: grant})
	if err != nil || replayed.Message != revoked.Message || len(runtime.calls) != 0 {
		t.Fatalf("revoked replay=%#v err=%v calls=%v", replayed, err, runtime.calls)
	}

	runtime = &fakeRuntime{state: fixtureState()}
	service = newTestService(t, runtime, false)
	preflight, err = service.Preflight(context.Background(), fixtureRequest(actionv1.ActionRestartWorkload))
	if err != nil {
		t.Fatal(err)
	}
	service.Now = func() time.Time { return preflight.Challenge.ExpiresAt.Add(time.Second) }
	expired, err := service.Execute(context.Background(), &actionv1.ExecuteRequest{ProjectID: "p1", ChallengeID: preflight.Challenge.ID, Grant: signGrant(t, preflight.Challenge, service)})
	if err != nil || expired.FailureCode != actionv1.FailureChallengeExpired || len(runtime.calls) != 0 {
		t.Fatalf("expired result=%#v err=%v calls=%v", expired, err, runtime.calls)
	}
}

func TestExecutePostCheckFailureIsTerminal(t *testing.T) {
	runtime := &fakeRuntime{state: fixtureState(), postErr: errors.New("not ready")}
	service := newTestService(t, runtime, false)
	preflight, err := service.Preflight(context.Background(), fixtureRequest(actionv1.ActionRestartWorkload))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Execute(context.Background(), &actionv1.ExecuteRequest{ProjectID: "p1", ChallengeID: preflight.Challenge.ID, Grant: signGrant(t, preflight.Challenge, service)})
	if err != nil || result.FailureCode != actionv1.FailurePostCheck || result.Status != actionv1.StatusFailed {
		t.Fatalf("post-check result=%#v err=%v", result, err)
	}
}

func TestExecuteKeepsPreMutationFactualUnavailableReserved(t *testing.T) {
	runtime := &fakeRuntime{state: fixtureState()}
	service := newTestService(t, runtime, false)
	preflight, err := service.Preflight(context.Background(), fixtureRequest(actionv1.ActionRestartWorkload))
	if err != nil {
		t.Fatal(err)
	}
	grant := signGrant(t, preflight.Challenge, service)
	runtime.mu.Lock()
	runtime.currentErr = ErrFactualStateUnavailable
	runtime.mu.Unlock()
	result, err := service.Execute(context.Background(), &actionv1.ExecuteRequest{ProjectID: preflight.Plan.ProjectID, ChallengeID: preflight.Challenge.ID, Grant: grant})
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.Store.Load(context.Background(), preflight.Plan.ProjectID, preflight.Challenge.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != actionv1.StatusApproved || record.Status != actionv1.StatusApproved || record.Result.ActionID != "" || lockCount(t, service.Store.(*SQLiteStore), preflight.Plan.ID) != 1 || len(runtime.calls) != 0 {
		t.Fatalf("pre-mutation unavailable result=%+v record=%+v locks=%d executor_calls=%d", result, record, lockCount(t, service.Store.(*SQLiteStore), preflight.Plan.ID), len(runtime.calls))
	}
}

func TestRecoveryFailsOnlyAfterReadableBoundedPostCheck(t *testing.T) {
	path := t.TempDir() + "/actions.db"
	store, err := OpenSQLiteStore(path)
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
	if _, err := store.BeginExecution(context.Background(), reservation.Record, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRuntime{state: state, postErr: errors.New("postcondition not reached")}
	service := &Service{Store: store, Runtime: runtime, Now: func() time.Time { return now.Add(2 * time.Second) }}
	if err := service.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	record, err := store.Load(context.Background(), plan.ProjectID, challenge.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != actionv1.StatusFailed || record.Result.FailureCode != actionv1.FailurePostCheck || runtime.postCalls != 1 || len(runtime.calls) != 0 {
		t.Fatalf("recovery result=%+v post=%d mutations=%v", record.Result, runtime.postCalls, runtime.calls)
	}
}

func TestRecoveryKeepsFactualUnavailableActionUnresolved(t *testing.T) {
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
	if _, err := store.BeginExecution(context.Background(), reservation.Record, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRuntime{state: state, currentErr: ErrFactualStateUnavailable}
	service := &Service{Store: store, Runtime: runtime, Now: func() time.Time { return now.Add(2 * time.Second) }}
	if err := service.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	record, err := store.Load(context.Background(), plan.ProjectID, challenge.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != actionv1.StatusExecuting || record.Result.ActionID != "" || len(runtime.calls) != 0 {
		t.Fatalf("unavailable action was terminalized: record=%+v calls=%v", record, runtime.calls)
	}
	if got := lockCount(t, store, plan.ID); got != 1 {
		t.Fatalf("unavailable action lock count=%d", got)
	}
	t.Logf("status=%s terminal_result=%t locks=1 executor_calls=%d", record.Status, record.Result.ActionID != "", len(runtime.calls))
}

func TestIncidentRecoveryUnavailableRetainsLock(t *testing.T) {
	store, err := OpenSQLiteStore("")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1_800_000_000, 0).UTC()
	plan, state, challenge := testActionFixtures(now)
	state.Workload = nil
	state.Incident = &actionv1.IncidentState{IncidentID: "incident-1", Status: "open"}
	state.StateHash, _ = actionv1.StateHash(state)
	plan.Kind = actionv1.ActionIncidentResolve
	plan.Parameters = actionv1.ActionParameters{IncidentResolve: &actionv1.IncidentResolveParameters{IncidentID: state.Incident.IncidentID}}
	plan.CurrentStateHash = state.StateHash
	plan.PlanHash, _ = actionv1.PlanHash(plan)
	challenge.PlanHash, challenge.StateHash = plan.PlanHash, state.StateHash
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
	executorCalls := 0
	runtime := KubernetesRuntime{
		IncidentState: func(context.Context, actionv1.TargetIdentity, string) (actionv1.IncidentState, error) {
			return actionv1.IncidentState{}, errors.New("incident store unavailable")
		},
		Incident: func(context.Context, actionv1.TargetIdentity, string) error {
			executorCalls++
			return nil
		},
	}
	if err := (&Service{Store: store, Runtime: runtime}).Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	record, err := store.Load(context.Background(), plan.ProjectID, challenge.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != actionv1.StatusExecuting || record.Result.ActionID != "" || lockCount(t, store, plan.ID) != 1 || executorCalls != 0 {
		t.Fatalf("record=%+v locks=%d executor_calls=%d", record, lockCount(t, store, plan.ID), executorCalls)
	}
	t.Logf("incident_status=%s terminal_result=false locks=1 executor_calls=0", record.Status)
}

func TestRecoveryLoopRetriesUnavailableWithoutExecutor(t *testing.T) {
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
	if _, err := store.BeginExecution(context.Background(), reservation.Record, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRuntime{state: state, currentErr: ErrFactualStateUnavailable}
	service := &Service{Store: store, Runtime: runtime, Now: func() time.Time { return now.Add(2 * time.Second) }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	started := time.Now()
	go func() { done <- service.RecoverLoop(ctx, 5*time.Millisecond) }()
	unavailableDeadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(unavailableDeadline) {
		runtime.mu.Lock()
		currentCalls := runtime.currentCalls
		runtime.mu.Unlock()
		if currentCalls >= 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	runtime.mu.Lock()
	unavailableCalls := runtime.currentCalls
	runtime.mu.Unlock()
	if unavailableCalls < 2 {
		t.Fatalf("recovery did not retry unavailable state: calls=%d", unavailableCalls)
	}
	record, err := store.Load(context.Background(), plan.ProjectID, challenge.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != actionv1.StatusExecuting || record.Result.ActionID != "" || lockCount(t, store, plan.ID) != 1 {
		t.Fatalf("unavailable recovery changed state: %+v locks=%d", record, lockCount(t, store, plan.ID))
	}
	runtime.mu.Lock()
	runtime.currentErr = nil
	runtime.mu.Unlock()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		record, err = store.Load(context.Background(), plan.ProjectID, challenge.ID)
		if err != nil {
			t.Fatal(err)
		}
		if record.Status == actionv1.StatusSucceeded {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("recovery loop error=%v", err)
	}
	if record.Status != actionv1.StatusSucceeded || lockCount(t, store, plan.ID) != 0 || len(runtime.calls) != 0 {
		t.Fatalf("recovery did not finish factual action: %+v locks=%d executor_calls=%d", record, lockCount(t, store, plan.ID), len(runtime.calls))
	}
	runtime.mu.Lock()
	currentCalls, postCalls := runtime.currentCalls, runtime.postCalls
	runtime.mu.Unlock()
	t.Logf("elapsed=%s current_state_calls=%d post_checks=%d executor_calls=0 locks=0", time.Since(started), currentCalls, postCalls)
}

func TestRecoveryLoopStopsOnCancellation(t *testing.T) {
	store, err := OpenSQLiteStore("")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := &fakeRuntime{state: fixtureState(), currentErr: ErrFactualStateUnavailable}
	service := &Service{Store: store, Runtime: runtime}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.RecoverLoop(ctx, time.Millisecond) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("recovery loop error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("recovery loop did not stop")
	}
}

func TestRecoveryPassTimeoutRetriesWithoutBusyLoop(t *testing.T) {
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
	if _, err := store.BeginExecution(context.Background(), reservation.Record, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRuntime{currentFn: func(ctx context.Context) (actionv1.CurrentState, error) {
		<-ctx.Done()
		return actionv1.CurrentState{}, ctx.Err()
	}}
	service := &Service{Store: store, Runtime: runtime, recoveryTimeout: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.RecoverLoop(ctx, 15*time.Millisecond) }()
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		runtime.mu.Lock()
		calls := runtime.currentCalls
		runtime.mu.Unlock()
		if calls >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("recovery loop error=%v", err)
	}
	runtime.mu.Lock()
	currentCalls := runtime.currentCalls
	runtime.mu.Unlock()
	record, err := store.Load(context.Background(), plan.ProjectID, challenge.ID)
	if err != nil {
		t.Fatal(err)
	}
	if currentCalls < 2 || currentCalls > 4 || record.Status != actionv1.StatusExecuting || lockCount(t, store, plan.ID) != 1 {
		t.Fatalf("passes=%d record=%+v locks=%d", currentCalls, record, lockCount(t, store, plan.ID))
	}
	t.Logf("timed_out_passes=%d status=%s locks=1", currentCalls, record.Status)
}

func TestRecoveryTerminalizesNonUnavailableCurrentStateError(t *testing.T) {
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
	if _, err := store.BeginExecution(context.Background(), reservation.Record, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRuntime{state: state, currentErr: errors.New("ownership mismatch")}
	service := &Service{Store: store, Runtime: runtime, Now: func() time.Time { return now.Add(2 * time.Second) }}
	if err := service.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	record, err := store.Load(context.Background(), plan.ProjectID, challenge.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != actionv1.StatusFailed || record.Result.FailureCode != actionv1.FailureExecution || lockCount(t, store, plan.ID) != 0 || len(runtime.calls) != 0 {
		t.Fatalf("record=%+v locks=%d executor_calls=%d", record, lockCount(t, store, plan.ID), len(runtime.calls))
	}
}

func TestRecoveryPassGivesLaterRecordOpportunityAfterEarlierDeadline(t *testing.T) {
	store, err := OpenSQLiteStore("")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1_800_000_000, 0).UTC()
	firstPlan, firstState, firstChallenge := testActionFixtures(now)
	secondPlan, secondState, secondChallenge := testActionFixtures(now)
	secondPlan.ID, secondPlan.ServiceID, secondPlan.Target.ServiceID = "action-second", "s2", "s2"
	secondState.Target.ServiceID = "s2"
	secondState.StateHash, _ = actionv1.StateHash(secondState)
	secondPlan.CurrentStateHash = secondState.StateHash
	secondPlan.PlanHash, _ = actionv1.PlanHash(secondPlan)
	secondChallenge.ID, secondChallenge.ActionID, secondChallenge.PlanHash, secondChallenge.StateHash = "challenge-second", secondPlan.ID, secondPlan.PlanHash, secondState.StateHash
	if err := store.Create(context.Background(), firstPlan, firstState, firstChallenge); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), secondPlan, secondState, secondChallenge); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		plan      actionv1.ActionPlan
		challenge actionv1.ApprovalChallenge
	}{
		{firstPlan, firstChallenge}, {secondPlan, secondChallenge},
	} {
		reservation, err := store.ReserveExecution(context.Background(), item.plan.ProjectID, item.challenge.ID, "grant-"+item.plan.ID, "u1", "device-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.BeginExecution(context.Background(), reservation.Record, now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	runtime := &starvingRecoveryRuntime{state: secondState, firstService: firstPlan.ServiceID}
	service := &Service{Store: store, Runtime: runtime, Now: func() time.Time { return now.Add(2 * time.Second) }}
	passCtx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	if err := service.Recover(passCtx); err != nil {
		t.Fatal(err)
	}
	first, _ := store.Load(context.Background(), firstPlan.ProjectID, firstChallenge.ID)
	second, _ := store.Load(context.Background(), secondPlan.ProjectID, secondChallenge.ID)
	if first.Status != actionv1.StatusExecuting || second.Status != actionv1.StatusSucceeded || lockCount(t, store, firstPlan.ID) != 1 || lockCount(t, store, secondPlan.ID) != 0 || runtime.executorCalls != 0 {
		t.Fatalf("first=%+v second=%+v locks=%d/%d executor_calls=%d", first, second, lockCount(t, store, firstPlan.ID), lockCount(t, store, secondPlan.ID), runtime.executorCalls)
	}
}

func TestRecoveryLoopReportsTransientCompleteFailureAndContinues(t *testing.T) {
	base, err := OpenSQLiteStore("")
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	now := time.Unix(1_800_000_000, 0).UTC()
	plan, state, challenge := testActionFixtures(now)
	if err := base.Create(context.Background(), plan, state, challenge); err != nil {
		t.Fatal(err)
	}
	reservation, err := base.ReserveExecution(context.Background(), plan.ProjectID, challenge.ID, "grant", "u1", "device-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.BeginExecution(context.Background(), reservation.Record, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	store := &completeFailActionStore{ActionStore: base, failures: 1}
	runtime := &fakeRuntime{state: state}
	var categories []string
	service := &Service{Store: store, Runtime: runtime, ReportRecovery: func(category string) { categories = append(categories, category) }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.RecoverLoop(ctx, time.Millisecond) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		record, _ := base.Load(context.Background(), plan.ProjectID, challenge.ID)
		if record.Status == actionv1.StatusSucceeded {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("loop error=%v", err)
	}
	record, _ := base.Load(context.Background(), plan.ProjectID, challenge.ID)
	if record.Status != actionv1.StatusSucceeded || len(categories) == 0 || categories[0] != RecoveryCompleteFailed || len(runtime.calls) != 0 {
		t.Fatalf("record=%+v categories=%v executor_calls=%d", record, categories, len(runtime.calls))
	}
}

type starvingRecoveryRuntime struct {
	state         actionv1.CurrentState
	firstService  string
	executorCalls int
}

func (r *starvingRecoveryRuntime) CurrentState(ctx context.Context, target actionv1.TargetIdentity, _ actionv1.ActionKind, _ actionv1.ActionParameters) (actionv1.CurrentState, error) {
	if target.ServiceID == r.firstService {
		<-ctx.Done()
		return actionv1.CurrentState{}, ctx.Err()
	}
	return r.state, nil
}
func (r *starvingRecoveryRuntime) RestartWorkload(context.Context, actionv1.TargetIdentity) error {
	r.executorCalls++
	return nil
}
func (r *starvingRecoveryRuntime) ScaleWorkload(context.Context, actionv1.TargetIdentity, int32) error {
	r.executorCalls++
	return nil
}
func (r *starvingRecoveryRuntime) GatewayReconcile(context.Context, actionv1.TargetIdentity) error {
	r.executorCalls++
	return nil
}
func (r *starvingRecoveryRuntime) ResolveIncident(context.Context, actionv1.TargetIdentity, string) error {
	r.executorCalls++
	return nil
}
func (r *starvingRecoveryRuntime) PostCheck(context.Context, actionv1.TargetIdentity, actionv1.ActionKind, actionv1.ActionParameters, actionv1.CurrentState) (actionv1.CurrentState, error) {
	return r.state, nil
}

type completeFailActionStore struct {
	ActionStore
	failures int
}

func (s *completeFailActionStore) Complete(ctx context.Context, record Record, result actionv1.ActionResult) (Record, error) {
	if s.failures > 0 {
		s.failures--
		return record, errors.New("transient complete failure")
	}
	return s.ActionStore.Complete(ctx, record, result)
}

func TestConcurrentRecoveryAndReplayPersistOneTerminalEvent(t *testing.T) {
	runtime := &fakeRuntime{state: fixtureState()}
	service := newTestService(t, runtime, false)
	preflight, err := service.Preflight(context.Background(), fixtureRequest(actionv1.ActionRestartWorkload))
	if err != nil {
		t.Fatal(err)
	}
	grant := signGrant(t, preflight.Challenge, service)
	grantHash, err := approvalHash(grant)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := service.Store.ReserveExecution(context.Background(), preflight.Plan.ProjectID, preflight.Challenge.ID, grantHash, "u1", service.device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Store.BeginExecution(context.Background(), reservation.Record, preflight.Plan.IssuedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var recoveryErr, replayErr error
	var replay *actionv1.ActionResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		recoveryErr = service.Recover(context.Background())
	}()
	go func() {
		defer wg.Done()
		<-start
		replay, replayErr = service.Execute(context.Background(), &actionv1.ExecuteRequest{ProjectID: preflight.Plan.ProjectID, ChallengeID: preflight.Challenge.ID, Grant: grant})
	}()
	close(start)
	wg.Wait()
	if recoveryErr != nil || replayErr != nil || replay == nil {
		t.Fatalf("recovery_err=%v replay=%+v replay_err=%v", recoveryErr, replay, replayErr)
	}
	terminal, err := service.Execute(context.Background(), &actionv1.ExecuteRequest{ProjectID: preflight.Plan.ProjectID, ChallengeID: preflight.Challenge.ID, Grant: grant})
	if err != nil || terminal.Status != actionv1.StatusSucceeded {
		t.Fatalf("terminal replay=%+v err=%v", terminal, err)
	}
	store := service.Store.(*SQLiteStore)
	assertEventCounts(t, store, preflight.Plan.ID, map[actionv1.ActionStatus]int{actionv1.StatusSucceeded: 1, actionv1.StatusFailed: 0})
	if len(runtime.calls) != 0 || runtime.postCalls != 1 || lockCount(t, store, preflight.Plan.ID) != 0 {
		t.Fatalf("executor=%d post=%d locks=%d", len(runtime.calls), runtime.postCalls, lockCount(t, store, preflight.Plan.ID))
	}
	t.Logf("terminal_status=%s terminal_events=1 post_checks=1 executor_calls=0 locks=0", terminal.Status)
}

type testService struct {
	*Service
	device     Device
	privateKey ed25519.PrivateKey
}

func newTestService(t *testing.T, runtime *fakeRuntime, revoked bool) *testService {
	store, err := OpenSQLiteStore("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	device := Device{ID: "device-1", ProjectID: "p1", OwnerPrincipal: "u1", PublicKey: publicKey, Status: DeviceActive}
	if revoked {
		device.Status = DeviceRevoked
	}
	return &testService{Service: &Service{Store: store, Runtime: runtime, Devices: fakeDevices{device: device}, Now: func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }, Authenticate: func(context.Context, string) (Principal, error) {
		return Principal{ProjectID: "p1", UserID: "u1", Role: "developer"}, nil
	}}, device: device, privateKey: privateKey}
}

func fixtureState() actionv1.CurrentState {
	state := actionv1.CurrentState{SchemaVersion: actionv1.SchemaVersion, ProjectID: "p1", Target: actionv1.TargetIdentity{ProjectID: "p1", NodeID: "n1", ServiceID: "s1", EnvironmentID: "prod", RuntimeID: "runtime-1"}, Workload: &actionv1.WorkloadState{UID: "uid", ResourceVersion: "1", Generation: 1, ObservedGeneration: 1, DesiredReplicas: 1, ObservedReplicas: 1, ReadyReplicas: 1}}
	state.StateHash, _ = actionv1.StateHash(state)
	return state
}
func fixtureRequest(kind actionv1.ActionKind) *actionv1.PreflightRequest {
	request := &actionv1.PreflightRequest{SchemaVersion: actionv1.SchemaVersion, ProjectID: "p1", NodeID: "n1", ServiceID: "s1", Target: fixtureState().Target, Kind: kind}
	switch kind {
	case actionv1.ActionRestartWorkload:
		request.Parameters.RestartWorkload = &actionv1.RestartWorkloadParameters{}
	case actionv1.ActionScaleWorkload:
		request.Parameters.ScaleWorkload = &actionv1.ScaleWorkloadParameters{Replicas: 2}
	case actionv1.ActionGatewayReconcile:
		request.Parameters.GatewayReconcile = &actionv1.GatewayReconcileParameters{}
	case actionv1.ActionIncidentResolve:
		request.Parameters.IncidentResolve = &actionv1.IncidentResolveParameters{IncidentID: "i1"}
	}
	return request
}
func signGrant(t *testing.T, challenge actionv1.ApprovalChallenge, service *testService) actionv1.ApprovalGrant {
	bytes, err := actionv1.ApprovalSigningBytes(challenge, service.device.ID)
	if err != nil {
		t.Fatal(err)
	}
	return actionv1.ApprovalGrant{SchemaVersion: actionv1.SchemaVersion, ChallengeID: challenge.ID, ActionID: challenge.ActionID, ProjectID: challenge.ProjectID, DeviceID: service.device.ID, PlanHash: challenge.PlanHash, StateHash: challenge.StateHash, Nonce: challenge.Nonce, IssuedAt: challenge.IssuedAt, ExpiresAt: challenge.ExpiresAt, Signature: ed25519.Sign(service.privateKey, bytes)}
}
