package deploy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

const registryPullFieldManager = "opsi-registry-pull"

type KubernetesRegistryPullSecretEnsurer struct {
	Runner      CommandRunner
	KubectlPath string
}

func (e KubernetesRegistryPullSecretEnsurer) Ensure(ctx context.Context, command deploymentv1.AgentCommand) error {
	ref := command.Workload.RegistryPullCredential
	credential := command.RegistryPullCredential
	if ref == nil {
		if credential != nil {
			return deploymentv1.NewRolloutError(deploymentv1.RolloutCodeInvalid, "unexpected registry pull credential", false)
		}
		return nil
	}
	if credential == nil {
		return deploymentv1.NewRolloutError(deploymentv1.RolloutCodeRegistryCredentialUnavailable, "registry pull credential is unavailable", false)
	}
	if err := credential.Validate(); err != nil || credential.Reference != *ref || !strings.HasPrefix(command.Image.Repository, ref.Registry+"/") {
		return deploymentv1.NewRolloutError(deploymentv1.RolloutCodeRegistryCredentialUnavailable, "registry pull credential does not match the immutable image", false)
	}
	if e.Runner == nil {
		return deploymentv1.NewRolloutError(deploymentv1.RolloutCodeRegistryCredentialUnavailable, "registry pull secret delivery is unavailable", false)
	}
	namespace := deploymentv1.StableDNSName("opsi", command.ProjectID, command.EnvironmentID)
	if command.Preview != nil {
		namespace = command.Preview.Namespace
	}
	labels := map[string]any{
		"app.kubernetes.io/managed-by": "opsi",
		"opsi.dev/project":             safeLabel(command.ProjectID),
		"opsi.dev/environment":         safeLabel(command.EnvironmentID),
	}
	if err := e.ensureNamespace(ctx, namespace, labels); err != nil {
		return err
	}
	config, err := dockerConfigJSON(*credential)
	if err != nil {
		return deploymentv1.NewRolloutError(deploymentv1.RolloutCodeRegistryCredentialUnavailable, "registry pull credential could not be encoded", false)
	}
	name := registryPullSecretName(*ref)
	secretLabels := cloneMap(labels)
	secretLabels["opsi.dev/registry-pull"] = safeLabel(ref.Provider)
	manifest := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"type":       "kubernetes.io/dockerconfigjson",
		"metadata":   map[string]any{"name": name, "namespace": namespace, "labels": secretLabels},
		"data":       map[string]any{".dockerconfigjson": base64.StdEncoding.EncodeToString(config)},
	}
	return e.ensureSecret(ctx, manifest)
}

func registryPullSecretName(ref deploymentv1.RegistryPullCredentialReference) string {
	return deploymentv1.StableDNSName("opsi", "registry", ref.Provider, ref.CredentialID)
}

func dockerConfigJSON(credential deploymentv1.RegistryPullCredential) ([]byte, error) {
	auth := base64.StdEncoding.EncodeToString([]byte(credential.Username + ":" + credential.Password))
	return json.Marshal(map[string]any{"auths": map[string]any{credential.Reference.Registry: map[string]string{"username": credential.Username, "password": credential.Password, "auth": auth}}})
}

func (e KubernetesRegistryPullSecretEnsurer) ensureNamespace(ctx context.Context, name string, labels map[string]any) error {
	current, exists, err := e.get(ctx, "namespace", name, "")
	if err != nil {
		return err
	}
	if exists {
		metadata, _ := current["metadata"].(map[string]any)
		currentLabels := stringMap(metadata["labels"])
		if currentLabels["app.kubernetes.io/managed-by"] != "opsi" || currentLabels["opsi.dev/project"] != labels["opsi.dev/project"] || currentLabels["opsi.dev/environment"] != labels["opsi.dev/environment"] {
			return deploymentv1.NewRolloutError(deploymentv1.RolloutCodeOwnershipConflict, "runtime namespace is not owned by Opsi", false)
		}
		return nil
	}
	return e.mutate(ctx, "create", map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": name, "labels": labels}})
}

func (e KubernetesRegistryPullSecretEnsurer) ensureSecret(ctx context.Context, desired map[string]any) error {
	metadata := desired["metadata"].(map[string]any)
	name, namespace := metadata["name"].(string), metadata["namespace"].(string)
	current, exists, err := e.get(ctx, "secret", name, namespace)
	if err != nil {
		return err
	}
	if !exists {
		return e.mutate(ctx, "create", desired)
	}
	currentMetadata, _ := current["metadata"].(map[string]any)
	labels := stringMap(currentMetadata["labels"])
	for key, value := range stringMap(metadata["labels"]) {
		if labels[key] != value {
			return deploymentv1.NewRolloutError(deploymentv1.RolloutCodeOwnershipConflict, "registry pull secret name is owned by another controller", false)
		}
	}
	if current["type"] == desired["type"] && equalLogical(current["data"], desired["data"]) {
		return nil
	}
	metadata["uid"] = currentMetadata["uid"]
	metadata["resourceVersion"] = currentMetadata["resourceVersion"]
	return e.mutate(ctx, "replace", desired)
}

func (e KubernetesRegistryPullSecretEnsurer) get(ctx context.Context, kind, name, namespace string) (map[string]any, bool, error) {
	args := []string{"get", kind, name}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	args = append(args, "-o", "json", "--ignore-not-found")
	out, err := e.Runner.Run(ctx, nil, e.kubectlPath(), args...)
	if err != nil {
		return nil, false, deploymentv1.NewRolloutError(deploymentv1.RolloutCodeRuntimeFailed, "Kubernetes registry pull prerequisite read failed", true)
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return nil, false, nil
	}
	var object map[string]any
	if err := json.Unmarshal(out, &object); err != nil {
		return nil, false, deploymentv1.NewRolloutError(deploymentv1.RolloutCodeRuntimeFailed, "Kubernetes registry pull prerequisite response was invalid", true)
	}
	return object, true, nil
}

func (e KubernetesRegistryPullSecretEnsurer) mutate(ctx context.Context, verb string, manifest map[string]any) error {
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	_, err = e.Runner.Run(ctx, data, e.kubectlPath(), verb, "--field-manager="+registryPullFieldManager, "-f", "-")
	if err != nil {
		return deploymentv1.NewRolloutError(deploymentv1.RolloutCodeRuntimeFailed, "Kubernetes registry pull prerequisite apply failed", true)
	}
	return nil
}

func (e KubernetesRegistryPullSecretEnsurer) kubectlPath() string {
	if e.KubectlPath != "" {
		return e.KubectlPath
	}
	return "kubectl"
}
