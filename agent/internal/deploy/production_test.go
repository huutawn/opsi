package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

func testAgentCommand(t *testing.T) deploymentv1.AgentCommand {
	t.Helper()
	spec := deploymentv1.WorkloadSpec{
		SchemaVersion:            deploymentv1.WorkloadSchemaVersion,
		ServiceKey:               "api",
		Replicas:                 1,
		ApplicationContainerName: deploymentv1.ApplicationContainer,
		ContainerPort:            8080,
		Resources: deploymentv1.Resources{
			Requests: deploymentv1.ResourceValues{CPU: "100m", Memory: "128Mi"},
			Limits:   deploymentv1.ResourceValues{CPU: "500m", Memory: "512Mi"},
		},
		TerminationGracePeriodSecond: 30,
		Exposure:                     deploymentv1.ExposureIntent{Mode: "internal"},
	}
	hash, err := spec.Hash()
	if err != nil {
		t.Fatal(err)
	}
	image, err := deploymentv1.NewImmutableImage("ghcr.io/example/api", "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	return deploymentv1.AgentCommand{SchemaVersion: deploymentv1.CommandSchemaVersion, JobID: "dep-1", ProjectID: "proj-1", EnvironmentID: "prod", RuntimeID: "runtime-1", NodeID: "node-1", AgentID: "agent-1", LeaseToken: "lease-1", Attempt: 1, Image: image, Workload: spec, SpecHash: hash}
}

func TestRenderProductionResourcesIsDeterministicAndOwned(t *testing.T) {
	command := testAgentCommand(t)
	first, resources, namespace, err := renderProductionResources(command)
	if err != nil {
		t.Fatal(err)
	}
	second, _, namespaceAgain, err := renderProductionResources(command)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || namespace != namespaceAgain {
		t.Fatal("renderer output is not deterministic")
	}
	sum := sha256.Sum256(first)
	if got := hex.EncodeToString(sum[:]); got != "3096d45f0e5717b0260cf09fb1fbcb1b3639ea7487ae32459e8f2faa2875f2c6" {
		t.Fatalf("renderer golden hash = %s", got)
	}
	var list map[string]any
	if err := json.Unmarshal(first, &list); err != nil {
		t.Fatal(err)
	}
	items := list["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("rendered item count = %d", len(items))
	}
	if resources.DeploymentName != resources.ServiceName || namespace == "" {
		t.Fatalf("resource identity = %+v namespace=%q", resources, namespace)
	}
	if !strings.Contains(string(first), `"type":"ClusterIP"`) || !strings.Contains(string(first), `"name":"app"`) {
		t.Fatalf("renderer omitted required ownership/service/application fields: %s", first)
	}
}

func TestProductionPullUsesDigestAndRejectsPrivateCredentialFallback(t *testing.T) {
	command := testAgentCommand(t)
	runner := &recordingRunner{outputs: map[string][]byte{
		"inspecti": []byte(`{"status":{"repoDigests":["ghcr.io/example/api@sha256:` + strings.Repeat("a", 64) + `"]}}`),
	}}
	adapter := ProductionAdapter{Runner: runner, Credentials: AnonymousRegistryCredentials{}, K3sPath: "k3s"}
	imageID, err := adapter.pull(context.Background(), command.Image)
	if err != nil || !strings.Contains(imageID, command.Image.Digest) {
		t.Fatalf("pull imageID=%q err=%v", imageID, err)
	}
	if len(runner.calls) != 2 || runner.calls[0][0] != "k3s" || runner.calls[0][1] != "crictl" || runner.calls[0][2] != "pull" || runner.calls[0][3] != command.Image.Reference {
		t.Fatalf("pull argv = %#v", runner.calls)
	}
	private := ProductionAdapter{Runner: runner, Credentials: staticCredentials{value: RegistryCredential{Username: "user", Secret: "secret"}}, K3sPath: "k3s"}
	if _, err := private.pull(context.Background(), command.Image); !errors.Is(err, errors.New("PRIVATE_REGISTRY_CREDENTIAL_UNSUPPORTED")) && !strings.Contains(err.Error(), "PRIVATE_REGISTRY_CREDENTIAL_UNSUPPORTED") {
		t.Fatalf("private credential error = %v", err)
	}
}

func TestRuntimeImageIDRequiresExactRequestedDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	if _, ok := runtimeImageID([]byte(`{"status":{"repoDigests":["ghcr.io/example/api@sha256:`+strings.Repeat("a", 64)+`"]}}`), digest); ok {
		t.Fatal("accepted a different runtime digest")
	}
	if id, ok := runtimeImageID([]byte(`{"status":{"repoDigests":["ghcr.io/example/api@`+digest+`"]}}`), digest); !ok || !strings.HasSuffix(id, "@"+digest) {
		t.Fatalf("runtime digest = %q ok=%v", id, ok)
	}
}

func TestProductionResultIdentitySurvivesSQLiteRestart(t *testing.T) {
	store := openTestStore(t)
	record := Record{DeployID: "dep-production", ProjectID: "proj-1", ServiceID: "api", ServiceName: "api", StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC(), GitSHA: "revision", ImageTag: "ghcr.io/example/api@sha256:" + strings.Repeat("a", 64), Status: StatusSuccess, TriggeredBy: "cloud", SpecHash: "spec-hash", ImageID: "docker-pullable://ghcr.io/example/api@sha256:" + strings.Repeat("a", 64), Namespace: "opsi-proj", DeploymentName: "opsi-api", KubernetesServiceName: "opsi-api", AvailableReplicas: 2}
	if err := store.Insert(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.FindSuccessful(context.Background(), record.ProjectID, record.ServiceID, record.GitSHA)
	if err != nil || loaded == nil {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	if loaded.SpecHash != record.SpecHash || loaded.ImageID != record.ImageID || loaded.Namespace != record.Namespace || loaded.AvailableReplicas != record.AvailableReplicas {
		t.Fatalf("production result identity was not durable: %+v", loaded)
	}
}

func TestApplicationReadinessIgnoresInjectedSidecar(t *testing.T) {
	digest := "sha256:" + strings.Repeat("c", 64)
	pods := map[string]any{"items": []any{map[string]any{"status": map[string]any{"containerStatuses": []any{
		map[string]any{"name": "mesh-sidecar", "ready": true, "imageID": "docker-pullable://mesh@sha256:" + strings.Repeat("d", 64)},
		map[string]any{"name": deploymentv1.ApplicationContainer, "ready": true, "imageID": "docker-pullable://ghcr.io/example/api@" + digest},
	}}}}}
	imageID, ready := applicationPodReadiness(pods, digest)
	if ready != 1 || !strings.Contains(imageID, digest) {
		t.Fatalf("imageID=%q ready=%d", imageID, ready)
	}
}

func TestApplicationReadinessReportsRequestedDigestDuringMixedRollout(t *testing.T) {
	digest := "sha256:" + strings.Repeat("e", 64)
	oldDigest := "sha256:" + strings.Repeat("f", 64)
	pods := map[string]any{"items": []any{
		map[string]any{"status": map[string]any{"containerStatuses": []any{map[string]any{"name": deploymentv1.ApplicationContainer, "ready": true, "imageID": "docker-pullable://ghcr.io/example/api@" + digest}}}},
		map[string]any{"status": map[string]any{"containerStatuses": []any{map[string]any{"name": deploymentv1.ApplicationContainer, "ready": true, "imageID": "docker-pullable://ghcr.io/example/api@" + oldDigest}}}},
	}}
	imageID, ready := applicationPodReadiness(pods, digest)
	if ready != 1 || !strings.Contains(imageID, digest) {
		t.Fatalf("imageID=%q ready=%d", imageID, ready)
	}
}

func TestOwnershipCheckRejectsForeignCollision(t *testing.T) {
	command := testAgentCommand(t)
	_, resources, namespace, err := renderProductionResources(command)
	if err != nil {
		t.Fatal(err)
	}
	foreign, _ := json.Marshal(map[string]any{"metadata": map[string]any{"labels": map[string]any{"app.kubernetes.io/managed-by": "someone-else"}}})
	runner := &recordingRunner{outputs: map[string][]byte{"namespace": foreign}}
	adapter := ProductionAdapter{Runner: runner, KubectlPath: "kubectl"}
	if err := adapter.verifyOwnership(context.Background(), resources, namespace, command); err == nil || !strings.Contains(err.Error(), "not Opsi-owned") {
		t.Fatalf("foreign ownership err=%v", err)
	}
}

func TestEngineProductionBranchDoesNotRequireGitOrBuilder(t *testing.T) {
	command := testAgentCommand(t)
	store := openTestStore(t)
	runtime := &fakeProductionRuntime{}
	engine := NewEngine(store, EngineConfig{Production: runtime})
	record, err := engine.Deploy(context.Background(), Request{Production: &command}, nil)
	if err != nil || record.Status != StatusSuccess {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	if _, err := engine.Deploy(context.Background(), Request{Production: &command}, nil); err != nil || runtime.calls != 1 {
		t.Fatalf("same-job retry was not idempotent: calls=%d err=%v", runtime.calls, err)
	}
	second := command
	second.JobID = "dep-2"
	second.LeaseToken = "lease-2"
	if _, err := engine.Deploy(context.Background(), Request{Production: &second}, nil); err != nil || runtime.calls != 2 {
		t.Fatalf("new job with same image/spec did not reconcile: calls=%d err=%v", runtime.calls, err)
	}
}

type fakeProductionRuntime struct{ calls int }

func (f *fakeProductionRuntime) Deploy(_ context.Context, command deploymentv1.AgentCommand, _ ProgressFunc) (Record, error) {
	f.calls++
	return Record{DeployID: command.JobID, ProjectID: command.ProjectID, ServiceID: command.Workload.ServiceKey, ServiceName: command.Workload.ServiceKey, ImageTag: command.Image.Reference, ImageID: "docker-pullable://" + command.Image.Reference, SpecHash: command.SpecHash, Status: StatusSuccess}, nil
}

type recordingRunner struct {
	calls   [][]string
	outputs map[string][]byte
}

func (r *recordingRunner) Run(_ context.Context, _ []byte, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	if len(args) > 1 {
		if out, ok := r.outputs[args[1]]; ok {
			return out, nil
		}
	}
	return nil, nil
}

type staticCredentials struct{ value RegistryCredential }

func (s staticCredentials) Credentials(context.Context, deploymentv1.ImmutableImage) (RegistryCredential, error) {
	return s.value, nil
}
