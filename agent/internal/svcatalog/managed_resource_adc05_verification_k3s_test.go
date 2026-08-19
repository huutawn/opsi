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
		AssertionPath:         "/health",
		AssertionExpectedCode: 200,
		TimeoutSeconds:        30,
	}

	ctx := context.Background()
	res := verifier.Verify(ctx, lease)

	// Connection layer MUST be verified via PostgreSQL probe
	if res.ConnectionStatus != verificationv1.LayerStatusVerified {
		t.Fatalf("expected Connection VERIFIED, got: %s (%s)", res.ConnectionStatus, res.ConnectionFailCode)
	}
}
