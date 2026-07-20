package deploymentv1

import (
	"strings"
	"testing"
	"time"

	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
)

func validRuntimeSnapshot(t *testing.T) RuntimeSnapshot {
	t.Helper()
	workload := WorkloadSpec{SchemaVersion: WorkloadSchemaVersion, ServiceKey: "api", Replicas: 1, ApplicationContainerName: ApplicationContainer, ContainerPort: 8080, Resources: Resources{Requests: ResourceValues{CPU: "100m", Memory: "128Mi"}, Limits: ResourceValues{CPU: "500m", Memory: "512Mi"}}, TerminationGracePeriodSecond: 30, Exposure: ExposureIntent{Mode: "internal"}}
	workloadHash, err := workload.Hash()
	if err != nil {
		t.Fatal(err)
	}
	image, err := NewImmutableImage("ghcr.io/example/api", "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	exposure, err := (exposurev1.ExposureSpec{SchemaVersion: exposurev1.SchemaVersion, ProjectID: "proj-1", EnvironmentID: "prod", RuntimeID: "runtime-1", ServiceKey: "api", DeploymentJobID: "job-1", Hostname: "api.example.com", Path: "/", ServicePort: 8080, TLS: exposurev1.TLSConfig{Mode: exposurev1.TLSDisabled}, Metadata: &exposurev1.Metadata{DisplayName: "API"}}).Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("b", 64)
	return RuntimeSnapshot{SchemaVersion: RuntimeSnapshotVersion, Target: RuntimeTarget{ProjectID: "proj-1", EnvironmentID: "prod", RuntimeID: "runtime-1", ServiceKey: "api", NodeID: "node-1", AgentID: "agent-1"}, DeploymentJobID: "job-1", Image: image, Workload: workload, WorkloadSpecHash: workloadHash, Exposure: exposure, ExposureSpecHash: exposure.SpecHash, Authority: RuntimeAuthority{TopologyPlanID: "topology-1", TopologyRevision: 1, TopologyHash: hash, DeploymentPolicyID: "policy-1", DeploymentPolicyRevision: 1, DeploymentPolicyHash: strings.Repeat("c", 64), RoutingDecisionHash: strings.Repeat("d", 64)}}
}

func TestRuntimeSnapshotHashSeparatesRuntimeAndDisplayFields(t *testing.T) {
	snapshot := validRuntimeSnapshot(t)
	first, err := snapshot.Hash()
	if err != nil {
		t.Fatal(err)
	}
	display := snapshot
	metadata := *snapshot.Exposure.Metadata
	metadata.DisplayName = "Renamed API"
	display.Exposure.Metadata = &metadata
	second, err := display.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("display metadata changed runtime snapshot hash")
	}
	authority := snapshot
	authority.DeploymentJobID = "job-2"
	authority.Exposure.DeploymentJobID = "job-2"
	authority.Exposure.SpecHash = ""
	authority.Exposure, err = authority.Exposure.Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	authority.ExposureSpecHash = authority.Exposure.SpecHash
	third, err := authority.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if first == third {
		t.Fatal("deployment job authority did not change runtime snapshot hash")
	}
}

func TestRolloutIntentAndTransitionAllowlist(t *testing.T) {
	snapshot := validRuntimeSnapshot(t)
	intent, err := (RolloutIntent{SchemaVersion: RolloutSchemaVersion, RolloutID: "rollout-1", Target: snapshot.Target, Desired: snapshot, Attempt: 1, CreatedAt: time.Unix(1, 0).UTC()}).Canonicalize()
	if err != nil || intent.IntentHash == "" {
		t.Fatalf("intent=%+v err=%v", intent, err)
	}
	if !CanTransitionRollout(RolloutStatePrepared, RolloutStateApplying) || CanTransitionRollout(RolloutStatePrepared, RolloutStateSucceeded) || CanTransitionRollout(RolloutStateSucceeded, RolloutStateApplying) {
		t.Fatal("rollout state transition allowlist is not fail-closed")
	}
	changed := intent
	changed.Desired.Image.Digest = "sha256:" + strings.Repeat("e", 64)
	changed.Desired.Image.Reference = changed.Desired.Image.Repository + "@" + changed.Desired.Image.Digest
	changed.IntentHash = ""
	changed, err = changed.Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	if changed.IntentHash == intent.IntentHash {
		t.Fatal("immutable image digest did not change rollout intent hash")
	}
}
