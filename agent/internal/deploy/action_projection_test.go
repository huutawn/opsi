package deploy

import (
	"context"
	"strings"
	"testing"

	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

type actionProjectionStore struct {
	snapshot *deploymentv1.KnownGoodSnapshot
}

func (s actionProjectionStore) CurrentKnownGood(context.Context, deploymentv1.RuntimeTarget) (*deploymentv1.KnownGoodSnapshot, error) {
	return s.snapshot, nil
}

func TestActionProjectionRequiresExactKnownGoodIdentity(t *testing.T) {
	target := actionv1.TargetIdentity{ProjectID: "p1", NodeID: "n1", ServiceID: "s1", EnvironmentID: "prod", RuntimeID: "runtime-1"}
	if _, err := (ActionProjection{Store: actionProjectionStore{}}).WorkloadIdentity(context.Background(), target); err == nil {
		t.Fatal("missing known-good snapshot accepted")
	}
	snapshot := projectionSnapshot(t)
	snapshot.Target.RuntimeID = "other-runtime"
	if _, err := (ActionProjection{Store: actionProjectionStore{snapshot: snapshot}}).WorkloadIdentity(context.Background(), target); err == nil {
		t.Fatal("mismatched runtime snapshot accepted")
	}
	snapshot = projectionSnapshot(t)
	identity, err := (ActionProjection{Store: actionProjectionStore{snapshot: snapshot}}).WorkloadIdentity(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"app.kubernetes.io/managed-by", "opsi.dev/project", "opsi.dev/environment", "opsi.dev/service", "opsi.dev/runtime"} {
		if identity.Selector[key] == "" {
			t.Fatalf("authoritative selector missing %s: %v", key, identity.Selector)
		}
	}
}

func projectionSnapshot(t *testing.T) *deploymentv1.KnownGoodSnapshot {
	t.Helper()
	workload := deploymentv1.WorkloadSpec{SchemaVersion: deploymentv1.WorkloadSchemaVersion, ServiceKey: "s1", Replicas: 1, ApplicationContainerName: deploymentv1.ApplicationContainer, ContainerPort: 8080, Resources: deploymentv1.Resources{Requests: deploymentv1.ResourceValues{CPU: "100m", Memory: "128Mi"}, Limits: deploymentv1.ResourceValues{CPU: "500m", Memory: "512Mi"}}, TerminationGracePeriodSecond: 30, Exposure: deploymentv1.ExposureIntent{Mode: "internal"}}
	hash, err := workload.Hash()
	if err != nil {
		t.Fatal(err)
	}
	image, err := deploymentv1.NewImmutableImage("ghcr.io/example/api", "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	target := deploymentv1.RuntimeTarget{ProjectID: "p1", EnvironmentID: "prod", RuntimeID: "runtime-1", ServiceKey: "s1", NodeID: "n1", AgentID: "agent-1"}
	return &deploymentv1.KnownGoodSnapshot{Target: target, Runtime: deploymentv1.RuntimeSnapshot{SchemaVersion: deploymentv1.RuntimeSnapshotVersion, Target: target, DeploymentJobID: "job-1", Image: image, Workload: workload, WorkloadSpecHash: hash}}
}
