package actionplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/deploy"
	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

type fakeKubectl struct {
	calls  [][]string
	output []byte
	err    error
	wait   bool
}

type projectionStore struct {
	snapshot *deploymentv1.KnownGoodSnapshot
}

func (s projectionStore) CurrentKnownGood(context.Context, deploymentv1.RuntimeTarget) (*deploymentv1.KnownGoodSnapshot, error) {
	return s.snapshot, nil
}

func (f *fakeKubectl) Run(ctx context.Context, _ []byte, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.output, f.err
}

func TestKubernetesRuntimeUsesFixedTypedOperationsAndBoundedTimeout(t *testing.T) {
	runner := &fakeKubectl{}
	runtime := KubernetesRuntime{Runner: runner, KubectlPath: "kubectl", Timeout: time.Second, Projection: testActionProjection(t)}
	target := fixtureState().Target
	if err := runtime.RestartWorkload(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ScaleWorkload(context.Background(), target, 2); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(runner.calls[0], " ") + "\n" + strings.Join(runner.calls[1], " ")
	if strings.Contains(joined, "sh -c") || strings.Contains(joined, "bash -c") || !strings.Contains(joined, "patch deployment") || !strings.Contains(joined, "--replicas=2") {
		t.Fatalf("unsafe kubectl args: %s", joined)
	}
	runner.wait = true
	runtime.Timeout = 10 * time.Millisecond
	if err := runtime.ScaleWorkload(context.Background(), target, 1); err == nil {
		t.Fatal("unbounded kubectl call succeeded")
	}
}

func TestKubernetesRuntimeRejectsInvalidScaleAndMalformedOutput(t *testing.T) {
	runner := &fakeKubectl{output: []byte(`{"metadata":`)}
	runtime := KubernetesRuntime{Runner: runner, Timeout: time.Second, Projection: testActionProjection(t)}
	if err := runtime.ScaleWorkload(context.Background(), fixtureState().Target, actionv1.MaxReplicas+1); err == nil {
		t.Fatal("invalid replica count accepted")
	}
	if _, err := runtime.CurrentState(context.Background(), fixtureState().Target, actionv1.ActionRestartWorkload, actionv1.ActionParameters{RestartWorkload: &actionv1.RestartWorkloadParameters{}}); err == nil {
		t.Fatal("malformed Kubernetes JSON accepted")
	}
}

func TestKubernetesRuntimeFailsClosedWithoutAuthoritativeProjection(t *testing.T) {
	runtime := KubernetesRuntime{Runner: &fakeKubectl{}}
	if _, err := runtime.CurrentState(context.Background(), fixtureState().Target, actionv1.ActionRestartWorkload, actionv1.ActionParameters{RestartWorkload: &actionv1.RestartWorkloadParameters{}}); err == nil {
		t.Fatal("nil ActionProjection used guessed Kubernetes identity")
	}
}

func TestKubernetesJSONIsSingleStrictBoundedValue(t *testing.T) {
	valid := []byte("{\"metadata\":{}} \n\t")
	if _, err := decodeKubernetesJSON(valid); err != nil {
		t.Fatalf("valid whitespace rejected: %v", err)
	}
	for _, body := range [][]byte{
		[]byte(`{"metadata":{}} garbage`),
		[]byte(`{"metadata":{}} {"metadata":{}}`),
		[]byte("warning\n{\"metadata\":{}}"),
	} {
		if _, err := decodeKubernetesJSON(body); err == nil {
			t.Fatalf("invalid JSON accepted: %q", body)
		}
	}
	overflow := make([]byte, deploy.MaxCommandOutputBytes+1)
	overflow[0] = '{'
	if _, err := decodeKubernetesJSON(overflow); err == nil {
		t.Fatal("oversized JSON accepted")
	}
}

func TestKubernetesWorkloadIdentityAndNumbersFailClosed(t *testing.T) {
	identity := deploy.ActionWorkloadIdentity{ServiceName: "opsi-api-runtime-1", Selector: expectedOwnership()}
	target := fixtureState().Target
	deployment := validDeployment(identity.Selector)
	pods := validPods(identity.Selector)
	if _, err := workloadState(deployment, pods, identity, target); err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{"uid", "resourceVersion"} {
		broken := cloneJSONMap(t, deployment)
		delete(broken["metadata"].(map[string]any), field)
		if _, err := workloadState(broken, pods, identity, target); err == nil {
			t.Fatalf("missing metadata.%s accepted", field)
		}
	}
	brokenNumber := cloneJSONMap(t, deployment)
	brokenNumber["metadata"].(map[string]any)["generation"] = json.Number("not-a-number")
	if _, err := workloadState(brokenNumber, pods, identity, target); err == nil {
		t.Fatal("malformed generation became zero")
	}
	brokenLabels := cloneJSONMap(t, deployment)
	delete(brokenLabels["metadata"].(map[string]any)["labels"].(map[string]any), "opsi.dev/runtime")
	if _, err := workloadState(brokenLabels, pods, identity, target); err == nil {
		t.Fatal("Deployment missing runtime label accepted")
	}
}

func TestPodReadinessUsesFullServiceAndRuntimeOwnership(t *testing.T) {
	expected := expectedOwnership()
	pods := validPods(expected)
	items := pods["items"].([]any)
	wrongService := cloneJSONMap(t, items[0].(map[string]any))
	wrongService["metadata"].(map[string]any)["labels"].(map[string]any)["opsi.dev/service"] = "other"
	wrongRuntime := cloneJSONMap(t, items[0].(map[string]any))
	wrongRuntime["metadata"].(map[string]any)["labels"].(map[string]any)["opsi.dev/runtime"] = "other"
	pods["items"] = append(items, wrongService, wrongRuntime)
	ready, err := readyPods(pods, expected)
	if err != nil || ready != 1 {
		t.Fatalf("ready=%d err=%v", ready, err)
	}
	selectorValue := selector(expected)
	if !strings.Contains(selectorValue, "opsi.dev/service=s1") || !strings.Contains(selectorValue, "opsi.dev/runtime=runtime-1") {
		t.Fatalf("project-only selector=%q", selectorValue)
	}
}

func TestIngressRequiresFullOwnershipAndExactBackend(t *testing.T) {
	identity := deploy.ActionWorkloadIdentity{ServiceName: "opsi-api-runtime-1", Selector: expectedOwnership()}
	ingress := validIngress(identity.Selector, identity.ServiceName)
	if _, err := gatewayState(ingress, identity); err != nil {
		t.Fatal(err)
	}
	wrong := cloneJSONMap(t, ingress)
	backend := wrong["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["http"].(map[string]any)["paths"].([]any)[0].(map[string]any)["backend"].(map[string]any)["service"].(map[string]any)
	backend["name"] = "other-service"
	if _, err := gatewayState(wrong, identity); err == nil {
		t.Fatal("Ingress with wrong backend accepted")
	}
}

func expectedOwnership() map[string]string {
	return map[string]string{"app.kubernetes.io/managed-by": "opsi", "opsi.dev/project": "p1", "opsi.dev/environment": "prod", "opsi.dev/runtime": "runtime-1", "opsi.dev/service": "s1", "opsi.dev/workload": "opsi-api-runtime-1"}
}

func validDeployment(labels map[string]string) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"uid": "uid-1", "resourceVersion": "7", "generation": json.Number("2"), "labels": stringAnyMap(labels)},
		"spec":     map[string]any{"replicas": json.Number("1"), "template": map[string]any{"metadata": map[string]any{"annotations": map[string]any{"opsi.dev/restarted-at": "token"}}}},
		"status":   map[string]any{"observedGeneration": json.Number("2"), "availableReplicas": json.Number("1")},
	}
}

func validPods(labels map[string]string) map[string]any {
	return map[string]any{"items": []any{map[string]any{"metadata": map[string]any{"labels": stringAnyMap(labels)}, "status": map[string]any{"containerStatuses": []any{map[string]any{"name": "opsi-app", "ready": true}}}}}}
}

func validIngress(labels map[string]string, backend string) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"uid": "uid-ingress", "resourceVersion": "8", "generation": json.Number("1"), "labels": stringAnyMap(labels), "annotations": map[string]any{"opsi.dev/spec-hash": "hash"}},
		"spec":     map[string]any{"rules": []any{map[string]any{"http": map[string]any{"paths": []any{map[string]any{"backend": map[string]any{"service": map[string]any{"name": backend}}}}}}}},
	}
}

func stringAnyMap(values map[string]string) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func cloneJSONMap(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var cloned map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func testActionProjection(t *testing.T) *deploy.ActionProjection {
	t.Helper()
	workload := deploymentv1.WorkloadSpec{SchemaVersion: deploymentv1.WorkloadSchemaVersion, ServiceKey: "s1", Replicas: 1, ApplicationContainerName: deploymentv1.ApplicationContainer, ContainerPort: 8080, Resources: deploymentv1.Resources{Requests: deploymentv1.ResourceValues{CPU: "100m", Memory: "128Mi"}, Limits: deploymentv1.ResourceValues{CPU: "500m", Memory: "512Mi"}}, TerminationGracePeriodSecond: 30, Exposure: deploymentv1.ExposureIntent{Mode: "internal"}}
	specHash, err := workload.Hash()
	if err != nil {
		t.Fatal(err)
	}
	image, err := deploymentv1.NewImmutableImage("ghcr.io/example/api", "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	target := deploymentv1.RuntimeTarget{ProjectID: "p1", EnvironmentID: "prod", RuntimeID: "runtime-1", ServiceKey: "s1", NodeID: "n1", AgentID: "agent-1"}
	snapshot := &deploymentv1.KnownGoodSnapshot{Runtime: deploymentv1.RuntimeSnapshot{SchemaVersion: deploymentv1.RuntimeSnapshotVersion, Target: target, DeploymentJobID: "job-1", Image: image, Workload: workload, WorkloadSpecHash: specHash}}
	snapshot.Target = target
	return &deploy.ActionProjection{Store: projectionStore{snapshot: snapshot}}
}
