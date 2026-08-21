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
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
)

func TestExecCommandRunnerSeparatesSuccessfulStdoutAndStderr(t *testing.T) {
	runner := ExecCommandRunner{}
	clean, err := runner.Run(context.Background(), nil, "sh", "-c", "printf clean")
	if err != nil || string(clean) != "clean" {
		t.Fatalf("clean output=%q err=%v", clean, err)
	}
	jsonOutput := `{"items":[]}`
	out, err := runner.Run(context.Background(), nil, "sh", "-c", "printf '%s' '"+jsonOutput+"'; printf '%s' 'Warning: v1 Endpoints is deprecated' >&2")
	if err != nil || string(out) != jsonOutput {
		t.Fatalf("output=%q err=%v", out, err)
	}
	var value map[string]any
	if err := decodeSingleJSON(out, &value); err != nil {
		t.Fatalf("stdout JSON was contaminated by stderr: %v", err)
	}
}

func TestExecCommandRunnerFailureDiagnostics(t *testing.T) {
	runner := ExecCommandRunner{}
	for _, tc := range []struct {
		name       string
		command    string
		contains   string
		notContain string
	}{
		{name: "stderr preferred", command: "printf stdout; printf stderr >&2; exit 1", contains: "stderr", notContain: "stdout"},
		{name: "stdout fallback", command: "printf stdout; exit 1", contains: "stdout"},
		{name: "secret redacted", command: "printf 'token=supersecret' >&2; exit 1", contains: "[REDACTED]", notContain: "supersecret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runner.Run(context.Background(), nil, "sh", "-c", tc.command)
			if err == nil || out != nil || !strings.Contains(err.Error(), tc.contains) || tc.notContain != "" && strings.Contains(err.Error(), tc.notContain) {
				t.Fatalf("output=%q err=%v", out, err)
			}
		})
	}
}

func TestExecCommandRunnerFailsClosedOnEitherStreamOverflow(t *testing.T) {
	runner := ExecCommandRunner{}
	for index, command := range []string{
		"yes x | head -c 262145",
		"(yes x | head -c 262145) >&2",
	} {
		if out, err := runner.Run(context.Background(), nil, "sh", "-c", command); err == nil || out != nil || err.Error() != "command output exceeded the allowed bound" {
			t.Fatalf("case=%d output length=%d err=%v", index, len(out), err)
		}
	}
}

func TestExecCommandRunnerCancellationDoesNotExposeOutput(t *testing.T) {
	runner := ExecCommandRunner{}
	for _, tc := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
	}{
		{name: "timeout", ctx: func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 10*time.Millisecond)
		}},
		{name: "cancelled", ctx: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, func() {}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := tc.ctx()
			defer cancel()
			out, err := runner.Run(ctx, nil, "sh", "-c", "printf 'token=supersecret' >&2; sleep 1")
			if err == nil || out != nil || err.Error() != "command cancelled" || strings.Contains(err.Error(), "supersecret") {
				t.Fatalf("output=%q err=%v", out, err)
			}
			if !errors.Is(ctx.Err(), context.DeadlineExceeded) && !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("context error=%v", ctx.Err())
			}
		})
	}
}

func TestExecCommandRunnerDoesNotMakeMalformedStdoutValid(t *testing.T) {
	out, err := (ExecCommandRunner{}).Run(context.Background(), nil, "sh", "-c", "printf 'warning{\\\"items\\\":[]}'")
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if decodeSingleJSON(out, &value) == nil {
		t.Fatalf("malformed stdout was accepted: %q", out)
	}
}

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

func TestRenderProductionResourcesUsesTypedSecretRefs(t *testing.T) {
	command := testAgentCommand(t)
	command.Workload.SecretReferences = []deploymentv1.SecretReference{{EnvName: "CACHE_PASSWORD", SecretID: "mrcred-res-1"}}
	command.SecretMaterials = []deploymentv1.SecretMaterial{{SecretID: "mrcred-res-1", Values: map[string]string{"CACHE_PASSWORD": "must-not-be-inline"}}}
	command.Workload.Environment = nil
	data, _, _, err := renderProductionResources(command)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "must-not-be-inline") || !strings.Contains(string(data), "secretKeyRef") || !strings.Contains(string(data), "CACHE_PASSWORD") {
		t.Fatalf("secret was rendered unsafely: %s", data)
	}
}

func TestNoExternalRolloutRendersNoIngress(t *testing.T) {
	snapshot := testRuntimeSnapshot(t, "job-internal", "a")
	snapshot.Exposure = exposurev1.ExposureSpec{}
	snapshot.ExposureSpecHash = ""
	_, resources, _, err := renderProductionResources(snapshot.AgentCommand())
	if err != nil {
		t.Fatal(err)
	}
	objects, err := rolloutObjects(resources, RenderedExposure{}, snapshot.HasExternalExposure())
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 3 {
		t.Fatalf("no-external rollout rendered %d objects, want namespace/deployment/service", len(objects))
	}
	for _, object := range objects {
		if object.Kind == "Ingress" {
			t.Fatal("no-external rollout rendered a hidden Ingress")
		}
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

type recordingRunner struct {
	calls   [][]string
	inputs  [][]byte
	outputs map[string][]byte
}

func (r *recordingRunner) Run(_ context.Context, input []byte, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	r.inputs = append(r.inputs, append([]byte(nil), input...))
	if len(args) > 1 {
		if out, ok := r.outputs[args[1]]; ok {
			return out, nil
		}
	}
	return nil, nil
}

func TestPostgresBindingSecretMaterializesExactTypedValuesWithoutWorkloadPlaintext(t *testing.T) {
	command := testAgentCommand(t)
	command.ProjectID, command.EnvironmentID, command.RuntimeID = "project-db", "env-db", "runtime-db"
	command.Workload.ServiceKey = "api"
	command.Workload.Environment = []deploymentv1.EnvironmentVariable{{Name: "DATABASE_HOST", Value: "postgres.internal"}, {Name: "DATABASE_NAME", Value: "opsi"}, {Name: "DATABASE_PORT", Value: "5432"}}
	command.Workload.SecretReferences = []deploymentv1.SecretReference{{EnvName: "DATABASE_PASSWORD", SecretID: "rbcred-binding"}, {EnvName: "DATABASE_URL", SecretID: "rbcred-binding"}, {EnvName: "DATABASE_USER", SecretID: "rbcred-binding"}}
	command.SecretMaterials = []deploymentv1.SecretMaterial{{SecretID: "rbcred-binding", Values: map[string]string{"DATABASE_USER": "opsi_b_role", "DATABASE_PASSWORD": "binding-secret", "DATABASE_URL": "postgres://opsi_b_role:binding-secret@postgres.internal:5432/opsi?sslmode=disable"}}}
	runner := &recordingRunner{outputs: map[string][]byte{}}
	if err := (KubernetesWorkloadSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}).Ensure(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	var secret map[string]any
	if err := json.Unmarshal(runner.inputs[len(runner.inputs)-1], &secret); err != nil {
		t.Fatal(err)
	}
	metadata := secret["metadata"].(map[string]any)
	labels := metadata["labels"].(map[string]any)
	if metadata["name"] != workloadSecretName(command, "rbcred-binding") || metadata["name"] == "opsi-mr-postgres-server-acl" || labels["opsi.dev/workload-secret"] != "rbcred-binding" {
		t.Fatalf("secret identity=%+v", metadata)
	}
	data := secret["data"].(map[string]any)
	if len(data) != 3 || data["DATABASE_USER"] == nil || data["DATABASE_PASSWORD"] == nil || data["DATABASE_URL"] == nil {
		t.Fatalf("secret data keys=%+v", data)
	}
	manifest, _, _, err := renderProductionResources(command)
	if err != nil || strings.Contains(string(manifest), "binding-secret") || strings.Contains(string(manifest), "postgres://opsi_b_role") {
		t.Fatalf("workload manifest leaked binding credential: err=%v manifest=%s", err, manifest)
	}
}
