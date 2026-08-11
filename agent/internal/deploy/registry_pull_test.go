package deploy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

type registryPullRunner struct {
	objects   map[string]map[string]any
	mutations []string
	args      [][]string
	revision  int
}

func newRegistryPullRunner() *registryPullRunner {
	return &registryPullRunner{objects: map[string]map[string]any{}}
}

func (r *registryPullRunner) Run(_ context.Context, stdin []byte, _ string, args ...string) ([]byte, error) {
	r.args = append(r.args, append([]string(nil), args...))
	if len(args) >= 3 && args[0] == "get" {
		namespace := ""
		for i := range args {
			if args[i] == "-n" && i+1 < len(args) {
				namespace = args[i+1]
			}
		}
		object := r.objects[args[1]+"/"+namespace+"/"+args[2]]
		if object == nil {
			return nil, nil
		}
		return json.Marshal(object)
	}
	if len(args) < 1 || (args[0] != "create" && args[0] != "replace") {
		return nil, errors.New("unexpected kubectl call")
	}
	var object map[string]any
	if err := json.Unmarshal(stdin, &object); err != nil {
		return nil, err
	}
	metadata := object["metadata"].(map[string]any)
	name, _ := metadata["name"].(string)
	namespace, _ := metadata["namespace"].(string)
	r.revision++
	metadata["uid"] = "uid-" + name
	metadata["resourceVersion"] = string(rune('0' + r.revision))
	kind := strings.ToLower(object["kind"].(string))
	r.objects[kind+"/"+namespace+"/"+name] = object
	r.mutations = append(r.mutations, args[0]+" "+kind+"/"+namespace+"/"+name)
	return nil, nil
}

func TestRegistryPullSecretIsScopedIdempotentRotationSafeAndIsolated(t *testing.T) {
	command := testAgentCommand(t)
	ref := deploymentv1.RegistryPullCredentialReference{Provider: "ghcr", CredentialID: "hosted-opsi", Registry: "ghcr.io"}
	command.Workload.RegistryPullCredential = &ref
	hash, err := command.Workload.Hash()
	if err != nil {
		t.Fatal(err)
	}
	command.SpecHash = hash
	command.RegistryPullCredential = &deploymentv1.RegistryPullCredential{Reference: ref, Username: "opsi-pull", Password: "token-one"}
	runner := newRegistryPullRunner()
	ensurer := KubernetesRegistryPullSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}
	if err := ensurer.Ensure(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if len(runner.mutations) != 2 {
		t.Fatalf("mutations=%v", runner.mutations)
	}
	if err := ensurer.Ensure(context.Background(), command); err != nil || len(runner.mutations) != 2 {
		t.Fatalf("idempotent ensure err=%v mutations=%v", err, runner.mutations)
	}
	command.RegistryPullCredential.Password = "token-two"
	if err := ensurer.Ensure(context.Background(), command); err != nil || len(runner.mutations) != 3 || !strings.HasPrefix(runner.mutations[2], "replace secret/") {
		t.Fatalf("rotation err=%v mutations=%v", err, runner.mutations)
	}
	for _, args := range runner.args {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "token-one") || strings.Contains(joined, "token-two") {
			t.Fatalf("credential leaked through kubectl arguments: %q", joined)
		}
	}
	rendered, resources, _, err := renderProductionResources(command)
	if err != nil {
		t.Fatal(err)
	}
	text := string(rendered)
	if !strings.Contains(text, command.Image.Reference) || !strings.Contains(text, registryPullSecretName(ref)) || strings.Contains(text, "opsi-pull") || strings.Contains(text, "token-two") || strings.Contains(text, "volumeMounts") {
		t.Fatalf("rendered workload violated image or credential isolation: %s", text)
	}
	podSpec := resources.Deployment["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	if _, ok := podSpec["imagePullSecrets"]; !ok {
		t.Fatal("managed pull secret was not attached through the pod image pull mechanism")
	}
	secret := runner.objects["secret/"+resources.Namespace+"/"+registryPullSecretName(ref)]
	encoded := secret["data"].(map[string]any)[".dockerconfigjson"].(string)
	config, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || !strings.Contains(string(config), "token-two") {
		t.Fatalf("rotated docker config was not stored: %s err=%v", config, err)
	}
}

func TestPublicWorkloadDoesNotUseRegistryPullSecret(t *testing.T) {
	command := testAgentCommand(t)
	runner := newRegistryPullRunner()
	if err := (KubernetesRegistryPullSecretEnsurer{Runner: runner}).Ensure(context.Background(), command); err != nil || len(runner.args) != 0 {
		t.Fatalf("public ensure err=%v calls=%v", err, runner.args)
	}
	rendered, _, _, err := renderProductionResources(command)
	if err != nil || strings.Contains(string(rendered), "imagePullSecrets") {
		t.Fatalf("public workload render err=%v manifest=%s", err, rendered)
	}
}

func TestRegistryPullSecretNeverMutatesForeignSecret(t *testing.T) {
	command := testAgentCommand(t)
	ref := deploymentv1.RegistryPullCredentialReference{Provider: "ghcr", CredentialID: "hosted-opsi", Registry: "ghcr.io"}
	command.Workload.RegistryPullCredential = &ref
	command.RegistryPullCredential = &deploymentv1.RegistryPullCredential{Reference: ref, Username: "user", Password: "wrong"}
	runner := newRegistryPullRunner()
	namespace := deploymentv1.StableDNSName("opsi", command.ProjectID, command.EnvironmentID)
	runner.objects["namespace//"+namespace] = map[string]any{"metadata": map[string]any{"labels": map[string]any{"app.kubernetes.io/managed-by": "opsi", "opsi.dev/project": safeLabel(command.ProjectID), "opsi.dev/environment": safeLabel(command.EnvironmentID)}}}
	runner.objects["secret/"+namespace+"/"+registryPullSecretName(ref)] = map[string]any{"metadata": map[string]any{"labels": map[string]any{
		"app.kubernetes.io/managed-by": "opsi",
		"opsi.dev/project":             safeLabel(command.ProjectID),
		"opsi.dev/environment":         safeLabel(command.EnvironmentID),
		"opsi.dev/registry-pull":       "other",
	}}}
	err := (KubernetesRegistryPullSecretEnsurer{Runner: runner}).Ensure(context.Background(), command)
	var rolloutErr *deploymentv1.RolloutError
	if !errors.As(err, &rolloutErr) || rolloutErr.Code != deploymentv1.RolloutCodeOwnershipConflict || len(runner.mutations) != 0 {
		t.Fatalf("err=%v mutations=%v", err, runner.mutations)
	}
}

func TestImagePullFailureTaxonomy(t *testing.T) {
	for _, test := range []struct {
		message string
		code    string
	}{
		{"failed to authorize: 401 Unauthorized", deploymentv1.RolloutCodeRegistryAuthFailed},
		{"manifest unknown: manifest unknown", deploymentv1.RolloutCodeImageDigestNotFound},
		{"dial tcp: connection refused", deploymentv1.RolloutCodeImagePullFailed},
	} {
		pods := map[string]any{"items": []any{map[string]any{"status": map[string]any{"containerStatuses": []any{map[string]any{"name": deploymentv1.ApplicationContainer, "state": map[string]any{"waiting": map[string]any{"reason": "ErrImagePull", "message": test.message}}}}}}}}
		err := applicationImagePullFailure(pods)
		var rolloutErr *deploymentv1.RolloutError
		if !errors.As(err, &rolloutErr) || rolloutErr.Code != test.code {
			t.Fatalf("message=%q err=%v", test.message, err)
		}
	}
}

func TestPrivateRegistryK3sIntegration(t *testing.T) {
	reference := os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_IMAGE")
	username := os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_USERNAME")
	password := os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_PASSWORD")
	if reference == "" || username == "" || password == "" {
		t.Skip("set OPSI_PRIVATE_REGISTRY_E2E_IMAGE/USERNAME/PASSWORD for real K3s private pull")
	}
	parts := strings.Split(reference, "@")
	if len(parts) != 2 {
		t.Fatal("private registry image must be an immutable digest reference")
	}
	image, err := deploymentv1.NewImmutableImage(parts[0], parts[1])
	if err != nil {
		t.Fatal(err)
	}
	registry := strings.SplitN(image.Repository, "/", 2)[0]
	ref := deploymentv1.RegistryPullCredentialReference{Provider: "local", CredentialID: "e2e", Registry: registry}
	command := testAgentCommand(t)
	command.ProjectID = "private-e2e"
	command.EnvironmentID = "registry"
	command.RuntimeID = "k3s"
	command.Image = image
	command.Workload.RegistryPullCredential = &ref
	command.Workload.ContainerPort = 80
	command.Workload.Resources = deploymentv1.Resources{Requests: deploymentv1.ResourceValues{CPU: "10m", Memory: "16Mi"}, Limits: deploymentv1.ResourceValues{CPU: "500m", Memory: "256Mi"}}
	command.SpecHash, err = command.Workload.Hash()
	if err != nil {
		t.Fatal(err)
	}
	command.RegistryPullCredential = &deploymentv1.RegistryPullCredential{Reference: ref, Username: username, Password: password}
	runner := ExecCommandRunner{}
	ensurer := KubernetesRegistryPullSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}
	namespace := deploymentv1.StableDNSName("opsi", command.ProjectID, command.EnvironmentID)
	t.Cleanup(func() {
		_, _ = runner.Run(context.Background(), nil, "kubectl", "delete", "namespace", namespace, "--wait=false")
	})
	if err := ensurer.Ensure(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	secretBefore, err := runner.Run(context.Background(), nil, "kubectl", "get", "secret", registryPullSecretName(ref), "-n", namespace, "-o", "jsonpath={.metadata.uid}")
	if err != nil {
		t.Fatal(err)
	}
	if err := ensurer.Ensure(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	secretAfter, err := runner.Run(context.Background(), nil, "kubectl", "get", "secret", registryPullSecretName(ref), "-n", namespace, "-o", "jsonpath={.metadata.uid}")
	if err != nil || string(secretBefore) == "" || string(secretBefore) != string(secretAfter) {
		t.Fatalf("managed secret was duplicated: before=%q after=%q err=%v", secretBefore, secretAfter, err)
	}
	manifest, resources, _, err := renderProductionResources(command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), manifest, "kubectl", "apply", "--server-side", "--field-manager=opsi-private-registry-e2e", "-f", "-"); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, err := runner.Run(waitCtx, nil, "kubectl", "rollout", "status", "deployment/"+resources.DeploymentName, "-n", namespace, "--timeout=150s"); err != nil {
		t.Fatal(err)
	}
	pods, err := (ProductionAdapter{Runner: runner, KubectlPath: "kubectl"}).getJSON(context.Background(), "pods", "", namespace, resources.Selector)
	if err != nil {
		t.Fatal(err)
	}
	imageID, ready := applicationPodReadiness(pods, image.Digest)
	if ready != 1 || !containsExactDigest(imageID, image.Digest) {
		t.Fatalf("ready=%d imageID=%q digest=%q", ready, imageID, image.Digest)
	}
	podName, err := runner.Run(context.Background(), nil, "kubectl", "get", "pods", "-n", namespace, "-l", selectorString(resources.Selector), "-o", "jsonpath={.items[0].metadata.name}")
	if err != nil {
		t.Fatal(err)
	}
	inside, err := runner.Run(context.Background(), nil, "kubectl", "exec", "-n", namespace, string(podName), "--", "sh", "-c", "env; mount")
	if err != nil || strings.Contains(string(inside), username) || strings.Contains(string(inside), password) || strings.Contains(string(inside), registryPullSecretName(ref)) {
		t.Fatalf("application container credential isolation failed: err=%v output=%s", err, inside)
	}
	t.Logf("private image ready imageID=%s secret=%s", imageID, registryPullSecretName(ref))
}

func TestPrivateRegistryK3sWrongCredentialIntegration(t *testing.T) {
	reference := os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_WRONG_IMAGE")
	username := os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_USERNAME")
	password := os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_PASSWORD")
	if reference == "" || username == "" || password == "" {
		t.Skip("set private registry E2E environment for a real wrong-credential pull")
	}
	parts := strings.Split(reference, "@")
	if len(parts) != 2 {
		t.Fatal("wrong-credential image must be an immutable digest reference")
	}
	image, err := deploymentv1.NewImmutableImage(parts[0], parts[1])
	if err != nil {
		t.Fatal(err)
	}
	registry := strings.SplitN(image.Repository, "/", 2)[0]
	ref := deploymentv1.RegistryPullCredentialReference{Provider: "local", CredentialID: "wrong-e2e", Registry: registry}
	command := testAgentCommand(t)
	command.ProjectID = "private-e2e-wrong"
	command.EnvironmentID = "registry"
	command.RuntimeID = "k3s"
	command.Image = image
	command.Workload.RegistryPullCredential = &ref
	command.Workload.ContainerPort = 80
	command.SpecHash, err = command.Workload.Hash()
	if err != nil {
		t.Fatal(err)
	}
	command.RegistryPullCredential = &deploymentv1.RegistryPullCredential{Reference: ref, Username: username, Password: password + "-wrong"}
	runner := ExecCommandRunner{}
	namespace := deploymentv1.StableDNSName("opsi", command.ProjectID, command.EnvironmentID)
	t.Cleanup(func() {
		_, _ = runner.Run(context.Background(), nil, "kubectl", "delete", "namespace", namespace, "--wait=false")
	})
	if err := (KubernetesRegistryPullSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}).Ensure(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	manifest, resources, _, err := renderProductionResources(command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), manifest, "kubectl", "apply", "--server-side", "--field-manager=opsi-private-registry-e2e", "-f", "-"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		pods, err := (ProductionAdapter{Runner: runner, KubectlPath: "kubectl"}).getJSON(context.Background(), "pods", "", namespace, resources.Selector)
		if err != nil {
			t.Fatal(err)
		}
		pullErr := applicationImagePullFailure(pods)
		var rolloutErr *deploymentv1.RolloutError
		if errors.As(pullErr, &rolloutErr) && rolloutErr.Code == deploymentv1.RolloutCodeRegistryAuthFailed {
			t.Log("wrong credential rejected with REGISTRY_AUTH_FAILED")
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("wrong registry credential did not produce REGISTRY_AUTH_FAILED evidence")
}
