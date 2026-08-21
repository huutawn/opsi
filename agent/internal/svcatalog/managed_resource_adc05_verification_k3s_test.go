package svcatalog

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	"github.com/opsi-dev/opsi/agent/internal/depverifier"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	verificationv1 "github.com/opsi-dev/opsi/contracts/go/verificationv1"
)

func TestManagedResourceRealK3sPostgresDependencyVerification(t *testing.T) {
	reference := os.Getenv("OPSI_ADC02_ACCEPTANCE_E2E_IMAGE")
	if reference == "" {
		reference = os.Getenv("OPSI_P07B3B1_ACCEPTANCE_E2E_IMAGE")
	}
	registryUsername := os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_USERNAME")
	registryPassword := os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_PASSWORD")
	if os.Getenv("OPSI_E2E_K3S_POSTGRES_BINDING") != "1" || reference == "" || registryUsername == "" || registryPassword == "" {
		t.Skip("set ADC02 / P07B3B1 K3s, immutable fixture, and private registry inputs")
	}
	referenceParts := strings.Split(reference, "@")
	if len(referenceParts) != 2 {
		t.Fatal("fixture image must be an immutable digest reference")
	}
	_, err := deploymentv1.NewImmutableImage(referenceParts[0], referenceParts[1])
	if err != nil {
		t.Fatal(err)
	}
	requireK3sInfrastructure(t)

	// 1. Provision Managed PostgreSQL
	spec := postgresBindingK3sSpec()
	spec.ResourceID = "res-pg-adc05-e2e"
	spec.CredentialID = "mrcred-res-pg-adc05-e2e"
	spec.Connection.Host = spec.Connection.ServiceName + "." + managedResourceNamespace(spec) + ".svc.cluster.local"
	spec.SpecHash, _ = spec.Hash()
	management := randomManagedCredential(t, spec.CredentialID, resourcev1.CredentialPurposeResourceManagement, spec.ResourceID, spec.ResourceID, "opsi")
	binding := postgresBindingOperation(t, spec, "binding-postgres-adc05", true)
	namespace := managedResourceNamespace(spec)
	_, _ = kubectlOutput(context.Background(), "delete", "namespace", namespace, "--ignore-not-found", "--wait=true", "--timeout=2m")
	t.Cleanup(func() {
		_, _ = kubectlOutput(context.Background(), "delete", "namespace", namespace, "--ignore-not-found", "--wait=true", "--timeout=2m")
	})

	reconciler := ManagedResourceReconciler{Timeout: 8 * time.Minute, PollInterval: time.Second}
	first := reconcilePostgresBindingK3s(t, reconciler, "binding-adc05-create", spec, management, binding)
	if first.Status != "ready" {
		t.Fatalf("first reconcile failed: %+v", first)
	}

	// 2. Execute platform-owned dependency verifier
	verifier := depverifier.Verifier{
		KubectlPath: "kubectl",
		Timeout:     30 * time.Second,
	}

	lease := cloudrelay.DepVerificationLease{
		ID:                    "lease-adc05-k3s-pg",
		LeaseToken:            "token-adc05",
		ProjectID:             spec.ProjectID,
		EnvironmentID:         spec.EnvironmentID,
		ConsumerApplicationID: "app-adc05-consumer",
		DependencyLogicalName: "database",
		ProviderKind:          "postgres",
		ProviderServiceName:   spec.Connection.ServiceName,
		ProviderNamespace:     namespace,
		ConsumerServiceKey:    "app-consumer",
		ConsumerNamespace:     namespace,
		BindingUsername:       binding.RoleName,
		BindingPassword:       binding.Credential.Password,
		BindingDatabase:       "opsi",
		AssertionPath:         "/health",
		AssertionExpectedCode: 200,
		TimeoutSeconds:        30,
	}

	ctx := context.Background()
	res := verifier.Verify(ctx, lease)

	// Connection layer MUST be verified via PostgreSQL protocol probe (SELECT 1)
	if res.ConnectionStatus != verificationv1.LayerStatusVerified {
		t.Fatalf("expected Connection VERIFIED, got: %s (%s)", res.ConnectionStatus, res.ConnectionFailCode)
	}
}

func TestManagedResourceRealK3sValkeyDependencyVerification(t *testing.T) {
	if os.Getenv("OPSI_E2E_K3S_VALKEY") != "1" {
		t.Skip("set OPSI_E2E_K3S_VALKEY=1 to run Valkey K3s verification")
	}
	requireK3sInfrastructure(t)

	spec := resourcev1.ManagedResourceSpec{
		SchemaVersion: resourcev1.ManagedResourceSpecSchemaVersion,
		ResourceID:    "res-vk-adc05-e2e",
		ProjectID:     "project-e2e",
		EnvironmentID: "env-e2e",
		ResourceType:  resourcev1.TypeRedis,
		Profile:       "single-node-experimental",
		Version:       resourcev1.ValkeyVersion,
		Image:         resourcev1.ValkeyImage,
		CredentialID:  "mrcred-res-vk-adc05-e2e",
		Assignment:    resourcev1.ManagedResourceAssignment{RuntimeID: "runtime-e2e", NodeID: "node-e2e", AgentID: "agent-e2e"},
		Replicas:      1,
		CPUMillicores: 250,
		MemoryBytes:   256 << 20,
		Ports:         []resourcev1.ManagedResourcePort{{Name: "redis", Port: 6379, Protocol: resourcev1.ProtocolRedis}},
		Connection:    resourcev1.ManagedResourceConnection{ServiceName: "opsi-mr-res-vk-adc05-e2e", Host: "opsi-mr-res-vk-adc05-e2e.opsi-project-e2e-env-e2e.svc.cluster.local", Port: 6379, Protocol: resourcev1.ProtocolRedis},
		ConfigurationHash: strings.Repeat("a", 64),
		TopologyRevision:  1,
		TopologyHash:      strings.Repeat("b", 64),
	}
	spec.SpecHash, _ = spec.Hash()
	management := randomManagedCredential(t, spec.CredentialID, resourcev1.CredentialPurposeResourceManagement, spec.ResourceID, spec.ResourceID, "")
	namespace := managedResourceNamespace(spec)
	_, _ = kubectlOutput(context.Background(), "delete", "namespace", namespace, "--ignore-not-found", "--wait=true", "--timeout=2m")
	t.Cleanup(func() {
		_, _ = kubectlOutput(context.Background(), "delete", "namespace", namespace, "--ignore-not-found", "--wait=true", "--timeout=2m")
	})

	reconciler := ManagedResourceReconciler{Timeout: 5 * time.Minute, PollInterval: time.Second}
	first := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{
		Action:     "apply",
		LeaseToken: "lease-vk-apply",
		Spec:       spec,
		Credential: management,
	})
	if first.Status != "ready" {
		t.Fatalf("first reconcile failed: %+v", first)
	}

	verifier := depverifier.Verifier{
		KubectlPath: "kubectl",
		Timeout:     30 * time.Second,
	}

	lease := cloudrelay.DepVerificationLease{
		ID:                    "lease-adc05-k3s-vk",
		LeaseToken:            "token-adc05-vk",
		ProjectID:             spec.ProjectID,
		EnvironmentID:         spec.EnvironmentID,
		ConsumerApplicationID: "app-adc05-consumer",
		DependencyLogicalName: "cache",
		ProviderKind:          "valkey",
		ProviderServiceName:   spec.Connection.ServiceName,
		ProviderNamespace:     namespace,
		ConsumerServiceKey:    "app-consumer",
		ConsumerNamespace:     namespace,
		BindingUsername:       "opsi",
		BindingPassword:       management.Password,
		AssertionPath:         "/health",
		AssertionExpectedCode: 200,
		TimeoutSeconds:        30,
	}

	ctx := context.Background()
	res := verifier.Verify(ctx, lease)

	// Connection layer MUST be verified via Valkey protocol probe (PING/PONG)
	if res.ConnectionStatus != verificationv1.LayerStatusVerified {
		t.Fatalf("expected Valkey Connection VERIFIED, got: %s (%s)", res.ConnectionStatus, res.ConnectionFailCode)
	}
}
