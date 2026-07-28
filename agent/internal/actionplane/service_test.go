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
	mu      sync.Mutex
	state   actionv1.CurrentState
	calls   []actionv1.ActionKind
	postErr error
}

func (f *fakeRuntime) CurrentState(context.Context, actionv1.TargetIdentity, actionv1.ActionKind, actionv1.ActionParameters) (actionv1.CurrentState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, nil
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
	state := actionv1.CurrentState{SchemaVersion: actionv1.SchemaVersion, ProjectID: "p1", Target: actionv1.TargetIdentity{ProjectID: "p1", NodeID: "n1", ServiceID: "s1"}, Workload: &actionv1.WorkloadState{UID: "uid", ResourceVersion: "1", Generation: 1, ObservedGeneration: 1, DesiredReplicas: 1, ObservedReplicas: 1, ReadyReplicas: 1}}
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
