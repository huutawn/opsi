package deploy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

const workloadSecretFieldManager = "opsi-workload-secret"

type KubernetesWorkloadSecretEnsurer struct {
	Runner      CommandRunner
	KubectlPath string
}

func (e KubernetesWorkloadSecretEnsurer) Ensure(ctx context.Context, command deploymentv1.AgentCommand) error {
	if len(command.Workload.SecretReferences) == 0 {
		return nil
	}
	if e.Runner == nil {
		return errors.New("workload secret delivery is unavailable")
	}
	materials := map[string]deploymentv1.SecretMaterial{}
	for _, material := range command.SecretMaterials {
		if err := material.Validate(); err != nil {
			return errors.New("workload secret material is invalid")
		}
		materials[material.SecretID] = material
	}
	namespace := deploymentv1.StableDNSName("opsi", command.ProjectID, command.EnvironmentID)
	if command.Preview != nil {
		namespace = command.Preview.Namespace
	}
	refsBySecret := map[string][]string{}
	for _, ref := range command.Workload.SecretReferences {
		refsBySecret[ref.SecretID] = append(refsBySecret[ref.SecretID], ref.EnvName)
	}
	for secretID, names := range refsBySecret {
		material, ok := materials[secretID]
		if !ok {
			return errors.New("workload secret material is unavailable")
		}
		data := map[string]any{}
		for _, name := range names {
			value, ok := material.Values[name]
			if !ok {
				return errors.New("workload secret value is unavailable")
			}
			data[name] = base64.StdEncoding.EncodeToString([]byte(value))
		}
		manifest := map[string]any{
			"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
			"metadata": map[string]any{"name": workloadSecretName(command, secretID), "namespace": namespace, "labels": map[string]any{"app.kubernetes.io/managed-by": "opsi", "opsi.dev/project": safeLabel(command.ProjectID), "opsi.dev/environment": safeLabel(command.EnvironmentID), "opsi.dev/service": safeLabel(command.Workload.ServiceKey), "opsi.dev/workload-secret": safeLabel(secretID)}},
			"data":     data,
		}
		if err := e.ensureSecret(ctx, manifest); err != nil {
			return err
		}
	}
	return nil
}

func workloadSecretName(command deploymentv1.AgentCommand, secretID string) string {
	return deploymentv1.StableDNSName("opsi", command.Workload.ServiceKey, command.RuntimeID, "binding", secretID)
}

func (e KubernetesWorkloadSecretEnsurer) ensureSecret(ctx context.Context, desired map[string]any) error {
	metadata := desired["metadata"].(map[string]any)
	name, namespace := metadata["name"].(string), metadata["namespace"].(string)
	current, exists, err := (KubernetesRegistryPullSecretEnsurer{Runner: e.Runner, KubectlPath: e.KubectlPath}).get(ctx, "secret", name, namespace)
	if err != nil {
		return err
	}
	if exists {
		currentMetadata := current["metadata"].(map[string]any)
		for key, value := range stringMap(metadata["labels"]) {
			if stringMap(currentMetadata["labels"])[key] != value {
				return errors.New("workload secret name is owned by another controller")
			}
		}
		if equalLogical(current["data"], desired["data"]) {
			return nil
		}
		metadata["uid"] = currentMetadata["uid"]
		metadata["resourceVersion"] = currentMetadata["resourceVersion"]
	}
	data, _ := json.Marshal(desired)
	verb := "create"
	if exists {
		verb = "replace"
	}
	kubectlPath := e.KubectlPath
	if kubectlPath == "" {
		kubectlPath = "kubectl"
	}
	_, err = e.Runner.Run(ctx, data, kubectlPath, verb, "--field-manager="+workloadSecretFieldManager, "-f", "-")
	if err != nil {
		return errors.New("Kubernetes workload secret apply failed")
	}
	return nil
}
