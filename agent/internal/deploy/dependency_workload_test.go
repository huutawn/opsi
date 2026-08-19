package deploy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

type mockCapturingRunner struct {
	captured [][]byte
}

func (m *mockCapturingRunner) Run(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "get" {
		return []byte(""), nil
	}
	if len(input) > 0 {
		m.captured = append(m.captured, input)
	}
	return []byte(`{"apiVersion":"v1","kind":"Secret"}`), nil
}

func TestDependencyWorkload_RenderAndMaterializeSecrets(t *testing.T) {
	command := testAgentCommand(t)
	command.Workload.Environment = []deploymentv1.EnvironmentVariable{
		{Name: "DB_HOST", Value: "postgres.svc.cluster.local"},
		{Name: "DB_PORT", Value: "5432"},
		{Name: "DB_NAME", Value: "opsi"},
		{Name: "CACHE_HOST", Value: "valkey.svc.cluster.local"},
		{Name: "CACHE_PORT", Value: "6379"},
	}
	command.Workload.SecretReferences = []deploymentv1.SecretReference{
		{EnvName: "APP_DATABASE_URL", SecretID: "mrcred-pg-1"},
		{EnvName: "DB_USER", SecretID: "mrcred-pg-1"},
		{EnvName: "DB_PASSWORD", SecretID: "mrcred-pg-1"},
		{EnvName: "APP_REDIS_URL", SecretID: "mrcred-valkey-1"},
		{EnvName: "CACHE_PASSWORD", SecretID: "mrcred-valkey-1"},
	}
	command.SecretMaterials = []deploymentv1.SecretMaterial{
		{
			SecretID: "mrcred-pg-1",
			Values: map[string]string{
				"APP_DATABASE_URL": "postgres://appuser:topsecretpg@postgres.svc.cluster.local:5432/opsi?sslmode=disable",
				"DB_USER":          "appuser",
				"DB_PASSWORD":      "topsecretpg",
			},
		},
		{
			SecretID: "mrcred-valkey-1",
			Values: map[string]string{
				"APP_REDIS_URL":  "redis://:topsecretredis@valkey.svc.cluster.local:6379",
				"CACHE_PASSWORD": "topsecretredis",
			},
		},
	}

	data, resources, namespace, err := renderProductionResources(command)
	if err != nil {
		t.Fatalf("renderProductionResources failed: %v", err)
	}

	manifestStr := string(data)

	// 1. Plaintext secrets must NEVER appear in the Deployment manifest JSON/YAML
	if strings.Contains(manifestStr, "topsecretpg") || strings.Contains(manifestStr, "topsecretredis") {
		t.Fatalf("plaintext secret leaked in Deployment manifest: %s", manifestStr)
	}

	// 2. Secret environment variables must reference secretKeyRef
	for _, envName := range []string{"APP_DATABASE_URL", "DB_USER", "DB_PASSWORD", "APP_REDIS_URL", "CACHE_PASSWORD"} {
		if !strings.Contains(manifestStr, `"name":"`+envName+`"`) {
			t.Fatalf("manifest missing environment name %s: %s", envName, manifestStr)
		}
		if !strings.Contains(manifestStr, `"secretKeyRef"`) {
			t.Fatalf("manifest missing secretKeyRef for %s: %s", envName, manifestStr)
		}
	}

	// 3. Non-secret environment variables must have values directly
	if !strings.Contains(manifestStr, `"name":"DB_HOST","value":"postgres.svc.cluster.local"`) {
		t.Fatalf("DB_HOST missing in manifest: %s", manifestStr)
	}
	if !strings.Contains(manifestStr, `"name":"CACHE_PORT","value":"6379"`) {
		t.Fatalf("CACHE_PORT missing in manifest: %s", manifestStr)
	}

	// 4. Test Secret Ensurer delivers secret materials cleanly into K8s Secrets
	runner := &mockCapturingRunner{}
	ensurer := KubernetesWorkloadSecretEnsurer{
		Runner:      runner,
		KubectlPath: "kubectl",
	}

	if err := ensurer.Ensure(context.Background(), command); err != nil {
		t.Fatalf("ensurer.Ensure failed: %v", err)
	}

	if len(runner.captured) != 2 {
		t.Fatalf("expected 2 secrets ensured, got %d", len(runner.captured))
	}

	for _, raw := range runner.captured {
		var secretMap map[string]any
		if err := json.Unmarshal(raw, &secretMap); err != nil {
			t.Fatalf("unmarshal ensured secret failed: %v", err)
		}
		secretData := secretMap["data"].(map[string]any)
		if strings.Contains(secretMap["metadata"].(map[string]any)["name"].(string), "mrcred-pg-1") {
			decodedPass, _ := base64.StdEncoding.DecodeString(secretData["DB_PASSWORD"].(string))
			if string(decodedPass) != "topsecretpg" {
				t.Fatalf("expected DB_PASSWORD=topsecretpg, got %s", string(decodedPass))
			}
			decodedUser, _ := base64.StdEncoding.DecodeString(secretData["DB_USER"].(string))
			if string(decodedUser) != "appuser" {
				t.Fatalf("expected DB_USER=appuser, got %s", string(decodedUser))
			}
		} else if strings.Contains(secretMap["metadata"].(map[string]any)["name"].(string), "mrcred-valkey-1") {
			decodedPass, _ := base64.StdEncoding.DecodeString(secretData["CACHE_PASSWORD"].(string))
			if string(decodedPass) != "topsecretredis" {
				t.Fatalf("expected CACHE_PASSWORD=topsecretredis, got %s", string(decodedPass))
			}
		}
	}

	_ = resources
	_ = namespace
}
