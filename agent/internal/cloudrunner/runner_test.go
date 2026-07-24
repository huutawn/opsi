package cloudrunner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
	"github.com/opsi-dev/opsi/agent/internal/nodelifecycle"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
)

type fakeClient struct {
	leases      []cloudrelay.DeploymentLease
	nodeLeases  []cloudrelay.NodeLifecycleLease
	results     []cloudrelay.DeploymentResult
	progress    []deploymentv1.Progress
	nodeResults []cloudrelay.NodeLifecycleResult
	heartbeats  int
	heartbeat   cloudrelay.Heartbeat
	cancel      context.CancelFunc
}

func (f *fakeClient) ProgressDeployment(_ context.Context, _ string, _ string, progress deploymentv1.Progress) error {
	f.progress = append(f.progress, progress)
	return nil
}

func (f *fakeClient) PollJob(context.Context, string, time.Duration) (*cloudrelay.JobLease, error) {
	if len(f.leases) > 0 {
		lease := f.leases[0]
		f.leases = f.leases[1:]
		return &cloudrelay.JobLease{Kind: "deployment", Deployment: &lease}, nil
	}
	if len(f.nodeLeases) > 0 {
		lease := f.nodeLeases[0]
		f.nodeLeases = f.nodeLeases[1:]
		return &cloudrelay.JobLease{Kind: "node_lifecycle", NodeLifecycle: &lease}, nil
	}
	return nil, context.Canceled
}

func (f *fakeClient) CompleteDeployment(_ context.Context, _ string, _ string, result cloudrelay.DeploymentResult) error {
	f.results = append(f.results, result)
	if f.cancel != nil {
		f.cancel()
	}
	return nil
}

func (f *fakeClient) CompleteNodeLifecycle(_ context.Context, _ string, _ string, result cloudrelay.NodeLifecycleResult) error {
	f.nodeResults = append(f.nodeResults, result)
	if f.cancel != nil {
		f.cancel()
	}
	return nil
}

func (f *fakeClient) Heartbeat(_ context.Context, _ string, heartbeat cloudrelay.Heartbeat) error {
	f.heartbeats++
	f.heartbeat = heartbeat
	return nil
}

type fakeLifecycle struct{}

func (fakeLifecycle) Execute(context.Context, nodelifecycle.Request) nodelifecycle.Result {
	return nodelifecycle.Result{Status: nodelifecycle.StatusCompleted, Verified: true}
}

type staticHealthProbe RuntimeHealth

func (p staticHealthProbe) Probe(context.Context) (RuntimeHealth, error) {
	return RuntimeHealth(p), nil
}

type fakeRolloutEngine struct {
	pendingCalls   int
	reconcileCalls int
	intent         deploymentv1.RolloutIntent
}

func (f *fakeRolloutEngine) ReconcilePending(context.Context, deploy.ProgressFunc) ([]deploymentv1.RolloutRecord, error) {
	f.pendingCalls++
	return nil, nil
}

func (f *fakeRolloutEngine) ReconcileRollout(_ context.Context, intent deploymentv1.RolloutIntent, progress deploy.ProgressFunc) (deploymentv1.RolloutRecord, error) {
	f.reconcileCalls++
	f.intent = intent
	now := time.Now().UTC()
	resources := []deploymentv1.ResourceIdentity{{Kind: "Deployment", Namespace: "opsi", Name: "api", UID: "uid-api", ResourceVersion: "1", FunctionalHash: strings.Repeat("f", 64)}}
	evidence := deploymentv1.ReadinessEvidence{SchemaVersion: deploymentv1.ReadinessEvidenceVersion, RuntimeReady: true, LocalRoutingReady: true, WorkloadEvidenceHash: strings.Repeat("1", 64), ServiceEvidenceHash: strings.Repeat("2", 64), ExposureEvidenceHash: strings.Repeat("3", 64), ApplicationImageIDHash: strings.Repeat("4", 64), LocalProbeEvidenceHash: strings.Repeat("5", 64), ObservedAt: now}
	states := []string{deploymentv1.RolloutStatePrepared, deploymentv1.RolloutStateApplying, deploymentv1.RolloutStateWaiting, deploymentv1.RolloutStateSucceeded}
	var record deploymentv1.RolloutRecord
	for index, state := range states {
		record = deploymentv1.RolloutRecord{SchemaVersion: deploymentv1.RolloutRecordVersion, Intent: intent, State: state, Version: uint64(index + 1), StateHash: strings.Repeat(string(rune('a'+index)), 64), Resources: resources, CreatedAt: now, UpdatedAt: now}
		if state == deploymentv1.RolloutStateSucceeded {
			record.Evidence = &evidence
			record.TerminalAt = &now
		}
		if progress != nil {
			copy := record
			if err := progress(&deploy.ProgressEvent{Phase: state, Message: "sanitized " + state, Percent: int32((index + 1) * 25), Rollout: &copy}); err != nil {
				return record, err
			}
		}
	}
	return record, nil
}

func TestRunnerRejectsCommandWithoutRolloutBeforeMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	connection := &ConnectionState{}
	intent := testCloudRolloutIntent(t)
	command := intent.Desired.AgentCommand()
	command.Rollout = nil
	command.LeaseToken = "lease-immutable"
	client := &fakeClient{cancel: cancel, leases: []cloudrelay.DeploymentLease{{
		Kind: "deployment", Action: "deploy", LeaseToken: "lease-immutable",
		Deployment: cloudrelay.DeploymentJobEnvelope{ID: command.JobID},
		Command:    &command,
	}}}
	engine := &fakeRolloutEngine{}
	runner := Runner{
		Client:            client,
		Engine:            engine,
		NodeID:            "node-1",
		PollInterval:      time.Millisecond,
		LongPollWait:      time.Millisecond,
		HeartbeatInterval: time.Hour,
		HealthProbe:       staticHealthProbe{NodeReady: true, K3SStatus: K3SStatusReady},
		ConnectionState:   connection,
	}
	if err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("run err = %v", err)
	}
	if len(client.results) != 1 || client.results[0].Status != deploymentv1.StateFailed || client.results[0].FailureCode != "LEGACY_DEPLOYMENT_RETIRED" || engine.reconcileCalls != 0 {
		t.Fatalf("result = %#v", client.results)
	}
	if client.heartbeats == 0 {
		t.Fatal("heartbeat was not sent")
	}
	if !client.heartbeat.NodeReady || client.heartbeat.K3SStatus != K3SStatusReady || client.heartbeat.Capabilities["deploy"] != true {
		t.Fatalf("heartbeat = %+v", client.heartbeat)
	}
	if !connection.Connected() {
		t.Fatal("successful heartbeat/poll did not update Cloud connection state")
	}
}

func TestRunnerExecutesRolloutLeaseAndReportsSanitizedLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	intent := testCloudRolloutIntent(t)
	command := intent.Desired.AgentCommand()
	command.Rollout = &intent
	command.LeaseToken = "lease-rollout"
	engine := &fakeRolloutEngine{}
	client := &fakeClient{cancel: cancel, leases: []cloudrelay.DeploymentLease{{
		Kind: "deployment", Action: deploymentv1.RolloutOperationApply, LeaseToken: "lease-rollout",
		Deployment: cloudrelay.DeploymentJobEnvelope{ID: intent.Desired.DeploymentJobID},
		Command:    &command,
	}}}
	runner := Runner{Client: client, Engine: engine, NodeID: intent.Target.NodeID, PollInterval: time.Millisecond, LongPollWait: time.Millisecond, HeartbeatInterval: time.Hour}
	if err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("run err=%v", err)
	}
	if engine.pendingCalls != 1 || engine.intent.IntentHash != intent.IntentHash || engine.intent.RolloutID != intent.RolloutID {
		t.Fatalf("pending=%d intent=%+v results=%+v progress=%+v", engine.pendingCalls, engine.intent, client.results, client.progress)
	}
	wantStates := []string{deploymentv1.RolloutStatePrepared, deploymentv1.RolloutStateApplying, deploymentv1.RolloutStateWaiting, deploymentv1.RolloutStateSucceeded}
	if len(client.progress) != len(wantStates) {
		t.Fatalf("progress=%+v", client.progress)
	}
	for index, state := range wantStates {
		if client.progress[index].State != state || client.progress[index].LeaseToken != "lease-rollout" || client.progress[index].IntentHash != intent.IntentHash {
			t.Fatalf("progress[%d]=%+v", index, client.progress[index])
		}
	}
	if len(client.results) != 1 || client.results[0].Status != deploymentv1.StateSucceeded || client.results[0].RolloutResult == nil || client.results[0].RolloutResult.CurrentDigest != intent.Desired.Image.Digest || client.results[0].RolloutResult.KnownGoodID != intent.RolloutID || client.results[0].RolloutResult.LeaseToken != "" {
		t.Fatalf("result=%+v", client.results)
	}
}

func TestConnectionStateFailsClosedAfterCloudError(t *testing.T) {
	state := &ConnectionState{}
	state.SetConnected(true)
	Runner{Client: &failingCloudClient{}, ConnectionState: state}.sendHeartbeat(context.Background())
	if state.Connected() {
		t.Fatal("Cloud connection state remained true after a heartbeat error")
	}
}

func TestHeartbeatHealthAndCapabilitiesFailClosed(t *testing.T) {
	tests := []struct {
		name          string
		probe         HealthProbe
		engine        DeployEngine
		lifecycle     NodeLifecycleExecutor
		wantStatus    string
		wantReady     bool
		wantDeploy    bool
		wantLifecycle bool
	}{
		{name: "ready", probe: staticHealthProbe{NodeReady: true, K3SStatus: K3SStatusReady}, engine: &fakeRolloutEngine{}, wantStatus: K3SStatusReady, wantReady: true, wantDeploy: true},
		{name: "unavailable", probe: staticHealthProbe{K3SStatus: K3SStatusUnavailable}, engine: &fakeRolloutEngine{}, wantStatus: K3SStatusUnavailable},
		{name: "not ready", probe: staticHealthProbe{K3SStatus: K3SStatusNotReady}, engine: &fakeRolloutEngine{}, wantStatus: K3SStatusNotReady},
		{name: "missing probe", engine: &fakeRolloutEngine{}, wantStatus: K3SStatusUnavailable},
		{name: "lifecycle does not imply health", lifecycle: fakeLifecycle{}, wantStatus: K3SStatusUnavailable, wantLifecycle: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeClient{}
			Runner{Client: client, Engine: tt.engine, NodeLifecycle: tt.lifecycle, HealthProbe: tt.probe}.sendHeartbeat(context.Background())
			if client.heartbeat.NodeReady != tt.wantReady || client.heartbeat.K3SStatus != tt.wantStatus || client.heartbeat.Capabilities["deploy"] != tt.wantDeploy || client.heartbeat.Capabilities["node_lifecycle"] != tt.wantLifecycle {
				t.Fatalf("heartbeat = %+v", client.heartbeat)
			}
		})
	}
}

type failingCloudClient struct{ fakeClient }

func (failingCloudClient) Heartbeat(context.Context, string, cloudrelay.Heartbeat) error {
	return errors.New("unavailable")
}

func TestRunnerExecutesNodeLifecycleLeaseAndReportsResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &fakeClient{cancel: cancel, nodeLeases: []cloudrelay.NodeLifecycleLease{{
		Kind: "node_lifecycle", ID: "nlj-1", Action: "drain", TargetNodeID: "node-target", TargetName: "node-a", LeaseToken: "lease-1",
	}}}
	runner := Runner{
		Client:            client,
		Engine:            &fakeRolloutEngine{},
		NodeLifecycle:     fakeLifecycle{},
		NodeID:            "node-1",
		PollInterval:      time.Millisecond,
		LongPollWait:      time.Millisecond,
		HeartbeatInterval: time.Hour,
	}
	if err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("run err = %v", err)
	}
	if len(client.nodeResults) != 1 || client.nodeResults[0].Status != "completed" || !client.nodeResults[0].Verified || client.nodeResults[0].LeaseToken != "lease-1" {
		t.Fatalf("node result = %#v", client.nodeResults)
	}
}

func TestRolloutIntentFromLeaseRejectsMismatchedCommand(t *testing.T) {
	intent := testCloudRolloutIntent(t)
	command := intent.Desired.AgentCommand()
	command.Rollout = &intent
	command.LeaseToken = "lease-1"
	_, err := RolloutIntentFromLease(cloudrelay.DeploymentLease{Action: deploymentv1.RolloutOperationApply, LeaseToken: "lease-2", Deployment: cloudrelay.DeploymentJobEnvelope{ID: command.JobID}, Command: &command}, intent.Target.NodeID)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v", err)
	}
}

func testCloudRolloutIntent(t *testing.T) deploymentv1.RolloutIntent {
	t.Helper()
	workload := deploymentv1.WorkloadSpec{SchemaVersion: deploymentv1.WorkloadSchemaVersion, ServiceKey: "api", Replicas: 1, ApplicationContainerName: deploymentv1.ApplicationContainer, ContainerPort: 8080, Resources: deploymentv1.Resources{Requests: deploymentv1.ResourceValues{CPU: "100m", Memory: "128Mi"}, Limits: deploymentv1.ResourceValues{CPU: "500m", Memory: "512Mi"}}, TerminationGracePeriodSecond: 30, Exposure: deploymentv1.ExposureIntent{Mode: "internal"}}
	workloadHash, err := workload.Hash()
	if err != nil {
		t.Fatal(err)
	}
	image, err := deploymentv1.NewImmutableImage("ghcr.io/example/api", "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	exposure, err := (exposurev1.ExposureSpec{SchemaVersion: exposurev1.SchemaVersion, ProjectID: "proj-1", EnvironmentID: "prod", RuntimeID: "runtime-1", ServiceKey: "api", DeploymentJobID: "dep-rollout", Hostname: "api.example.com", Path: "/", ServicePort: 8080, TLS: exposurev1.TLSConfig{Mode: exposurev1.TLSDisabled}}).Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	target := deploymentv1.RuntimeTarget{ProjectID: "proj-1", EnvironmentID: "prod", RuntimeID: "runtime-1", ServiceKey: "api", NodeID: "node-1", AgentID: "agent-1"}
	snapshot := deploymentv1.RuntimeSnapshot{SchemaVersion: deploymentv1.RuntimeSnapshotVersion, Target: target, DeploymentJobID: "dep-rollout", Image: image, Workload: workload, WorkloadSpecHash: workloadHash, Exposure: exposure, ExposureSpecHash: exposure.SpecHash, Authority: deploymentv1.RuntimeAuthority{TopologyPlanID: "topology-1", TopologyRevision: 1, TopologyHash: strings.Repeat("b", 64), DeploymentPolicyID: "policy-1", DeploymentPolicyRevision: 1, DeploymentPolicyHash: strings.Repeat("c", 64), RoutingDecisionHash: strings.Repeat("d", 64)}}
	intent, err := (deploymentv1.RolloutIntent{SchemaVersion: deploymentv1.RolloutSchemaVersion, RolloutID: "rollout-1", Operation: deploymentv1.RolloutOperationApply, Target: target, Desired: snapshot, Attempt: 1, CreatedAt: time.Now().UTC()}).Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	return intent
}
