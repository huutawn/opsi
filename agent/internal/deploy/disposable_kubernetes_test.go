package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
)

// TestDisposableKubernetesAThenBThenA is intentionally opt-in. The verifier
// runs only against a caller-provided disposable kubeconfig and never touches
// a live Agent/VPS cluster.
func TestDisposableKubernetesAThenBThenA(t *testing.T) {
	kubeconfig := os.Getenv("OPSI_DISPOSABLE_KUBECONFIG")
	imageA := os.Getenv("OPSI_DISPOSABLE_IMAGE_A")
	imageB := os.Getenv("OPSI_DISPOSABLE_IMAGE_B")
	if kubeconfig == "" || imageA == "" || imageB == "" {
		t.Skip("set OPSI_DISPOSABLE_KUBECONFIG, OPSI_DISPOSABLE_IMAGE_A and OPSI_DISPOSABLE_IMAGE_B for the factual local K3s gate")
	}
	t.Setenv("KUBECONFIG", kubeconfig)
	databasePath := filepath.Join(t.TempDir(), "agent.sqlite")
	store, err := OpenSQLiteStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	adapter := ProductionAdapter{Runner: ExecCommandRunner{}, Timeout: 20 * time.Second, PollInterval: 500 * time.Millisecond, RoutingProbe: BoundedHTTPProbe{Scheme: "http", Address: "127.0.0.1", Port: disposableTraefikPort()}}
	engine := NewEngine(store, EngineConfig{Reconciler: adapter, RolloutTimeout: 30 * time.Second, PollInterval: 500 * time.Millisecond})
	a := disposableSnapshot(t, "disposable-a", imageA)
	recordA, err := engine.ReconcileRollout(context.Background(), testRolloutIntent(t, "disposable-rollout-a", a, nil), nil)
	if err != nil || recordA.State != deploymentv1.RolloutStateSucceeded {
		t.Fatalf("A record=%+v err=%v", recordA, err)
	}
	knownA, err := store.CurrentKnownGood(context.Background(), a.Target)
	if err != nil || knownA == nil {
		t.Fatalf("A known-good=%+v err=%v", knownA, err)
	}
	b := disposableSnapshot(t, "disposable-b", imageB)
	intentB := testRolloutIntent(t, "disposable-rollout-b", b, knownA)
	planB, err := adapter.PrepareRollout(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	wal, err := store.BeginRollout(context.Background(), intentB, planObservedIdentities(planB))
	if err != nil {
		t.Fatal(err)
	}
	wal, err = store.TransitionRollout(context.Background(), wal.Intent.RolloutID, deploymentv1.RolloutStateApplying, nil, wal.Resources, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	resourcesB, err := adapter.ApplyRollout(context.Background(), planB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionRollout(context.Background(), wal.Intent.RolloutID, deploymentv1.RolloutStateWaiting, nil, resourcesB, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLiteStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	engine = NewEngine(reopened, EngineConfig{Reconciler: adapter, RolloutTimeout: 30 * time.Second, PollInterval: 500 * time.Millisecond})
	results, reconcileErr := engine.ReconcilePending(context.Background(), nil)
	if reconcileErr == nil || len(results) != 1 || results[0].State != deploymentv1.RolloutStateRolledBack {
		t.Fatalf("B restart results=%+v err=%v", results, reconcileErr)
	}
	_ = reopened.Close()
	if err := assertDisposableResources(t, a, imageA); err != nil {
		t.Fatal(err)
	}
}

func disposableSnapshot(t *testing.T, jobID, imageReference string) deploymentv1.RuntimeSnapshot {
	t.Helper()
	parts := strings.Split(imageReference, "@sha256:")
	if len(parts) != 2 || len(parts[1]) != 64 {
		t.Fatalf("image must be repository@sha256:digest: %q", imageReference)
	}
	workload := deploymentv1.WorkloadSpec{SchemaVersion: deploymentv1.WorkloadSchemaVersion, ServiceKey: "nginx", Replicas: 1, ApplicationContainerName: deploymentv1.ApplicationContainer, ContainerPort: 80, Resources: deploymentv1.Resources{Requests: deploymentv1.ResourceValues{CPU: "10m", Memory: "32Mi"}, Limits: deploymentv1.ResourceValues{CPU: "100m", Memory: "128Mi"}}, TerminationGracePeriodSecond: 30, Exposure: deploymentv1.ExposureIntent{Mode: "internal"}}
	workloadHash, err := workload.Hash()
	if err != nil {
		t.Fatal(err)
	}
	image, err := deploymentv1.NewImmutableImage(parts[0], "sha256:"+parts[1])
	if err != nil {
		t.Fatal(err)
	}
	exposure, err := (exposurev1.ExposureSpec{SchemaVersion: exposurev1.SchemaVersion, ProjectID: "disposable-project", EnvironmentID: "local", RuntimeID: "runtime-1", ServiceKey: "nginx", DeploymentJobID: jobID, Hostname: "nginx.opsi.test", Path: "/", ServicePort: 80, TLS: exposurev1.TLSConfig{Mode: exposurev1.TLSDisabled}}).Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	return deploymentv1.RuntimeSnapshot{SchemaVersion: deploymentv1.RuntimeSnapshotVersion, Target: deploymentv1.RuntimeTarget{ProjectID: "disposable-project", EnvironmentID: "local", RuntimeID: "runtime-1", ServiceKey: "nginx", NodeID: "local-node", AgentID: "local-agent"}, DeploymentJobID: jobID, Image: image, Workload: workload, WorkloadSpecHash: workloadHash, Exposure: exposure, ExposureSpecHash: exposure.SpecHash, Authority: deploymentv1.RuntimeAuthority{TopologyPlanID: "topology-local", TopologyRevision: 1, TopologyHash: strings.Repeat("a", 64), DeploymentPolicyID: "policy-local", DeploymentPolicyRevision: 1, DeploymentPolicyHash: strings.Repeat("b", 64), RoutingDecisionHash: strings.Repeat("c", 64)}}
}

func disposableTraefikPort() int {
	if value := os.Getenv("OPSI_DISPOSABLE_TRAEFIK_PORT"); value != "" {
		var port int
		if _, err := fmt.Sscanf(value, "%d", &port); err == nil && port > 0 && port < 65536 {
			return port
		}
	}
	return 18080
}

func assertDisposableResources(t *testing.T, snapshot deploymentv1.RuntimeSnapshot, imageReference string) error {
	t.Helper()
	command := snapshot.AgentCommand()
	_, resources, _, err := renderProductionResources(command)
	if err != nil {
		return err
	}
	exposure, err := renderExposure(context.Background(), command, snapshot.Exposure, nil)
	if err != nil {
		return err
	}
	for _, resource := range []struct{ kind, name string }{{"deployment", resources.DeploymentName}, {"service", resources.ServiceName}, {"ingress", exposure.IngressName}} {
		out, err := (ExecCommandRunner{}).Run(context.Background(), nil, "kubectl", "get", resource.kind, resource.name, "-n", resources.Namespace, "-o", "json")
		if err != nil {
			return err
		}
		var object map[string]any
		if err := json.Unmarshal(out, &object); err != nil {
			return err
		}
		if resource.kind == "deployment" {
			if !deploymentHasExactAppImage(object, imageReference) {
				return errors.New("final Deployment app image does not match A")
			}
		}
		list, err := (ExecCommandRunner{}).Run(context.Background(), nil, "kubectl", "get", resource.kind, "-n", resources.Namespace, "-l", "opsi.dev/service="+safeLabel(snapshot.Target.ServiceKey), "-o", "json")
		if err != nil {
			return err
		}
		var inventory struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(list, &inventory); err != nil || len(inventory.Items) != 1 {
			return fmt.Errorf("expected one %s, got %d", resource.kind, len(inventory.Items))
		}
	}
	pods, err := (ProductionAdapter{Runner: ExecCommandRunner{}, KubectlPath: "kubectl"}).getJSON(context.Background(), "pods", "", resources.Namespace, resources.Selector)
	if err != nil {
		return err
	}
	if _, ready := applicationPodReadiness(pods, snapshot.Image.Digest); ready != int(snapshot.Workload.Replicas) {
		return errors.New("final application pod digest is not ready")
	}
	return nil
}
