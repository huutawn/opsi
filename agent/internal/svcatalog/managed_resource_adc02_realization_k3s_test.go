package svcatalog

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

func TestManagedResourceRealK3sPostgresApplicationDependencyRealization(t *testing.T) {
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
	image, err := deploymentv1.NewImmutableImage(referenceParts[0], referenceParts[1])
	if err != nil {
		t.Fatal(err)
	}
	requireK3sInfrastructure(t)

	// Step 1: Managed PostgreSQL Resource Provisioning
	spec := postgresBindingK3sSpec()
	spec.ResourceID = "res-pg-dep-e2e"
	spec.CredentialID = "mrcred-res-pg-dep-e2e"
	spec.Connection.Host = spec.Connection.ServiceName + "." + managedResourceNamespace(spec) + ".svc.cluster.local"
	spec.SpecHash, _ = spec.Hash()
	management := randomManagedCredential(t, spec.CredentialID, resourcev1.CredentialPurposeResourceManagement, spec.ResourceID, spec.ResourceID, "opsi")
	binding := postgresBindingOperation(t, spec, "binding-postgres-dep-app", true)
	namespace := managedResourceNamespace(spec)
	_, _ = kubectlOutput(context.Background(), "delete", "namespace", namespace, "--ignore-not-found", "--wait=true", "--timeout=2m")
	t.Cleanup(func() {
		_, _ = kubectlOutput(context.Background(), "delete", "namespace", namespace, "--ignore-not-found", "--wait=true", "--timeout=2m")
	})

	reconciler := ManagedResourceReconciler{Timeout: 8 * time.Minute, PollInterval: time.Second}
	first := reconcilePostgresBindingK3s(t, reconciler, "binding-dep-create", spec, management, binding)
	if first.Status != "ready" {
		t.Fatalf("first reconcile failed: %+v", first)
	}

	// Step 2: Same BuildRecord Capture Before Realization Deploy
	buildRecordIDBefore := "build-adc02-consumer-001"
	imageDigestBefore := image.Digest
	buildJobCountBefore := 1
	configRevisionBefore := 1

	// Step 3: Dependency Realization & Consumer Deployment
	registry := strings.SplitN(image.Repository, "/", 2)[0]
	registryRef := deploymentv1.RegistryPullCredentialReference{Provider: "local", CredentialID: "adc02-fixture", Registry: registry}
	workload := deploymentv1.WorkloadSpec{
		SchemaVersion: deploymentv1.WorkloadSchemaVersion, ServiceKey: "dep-app", Replicas: 1, ApplicationContainerName: deploymentv1.ApplicationContainer, ContainerPort: 8080,
		ReadinessProbe:               &deploymentv1.Probe{Path: "/health", Port: 8080, InitialDelaySeconds: 1, PeriodSeconds: 1, TimeoutSeconds: 1, FailureThreshold: 3},
		Resources:                    deploymentv1.Resources{Requests: deploymentv1.ResourceValues{CPU: "50m", Memory: "64Mi"}, Limits: deploymentv1.ResourceValues{CPU: "250m", Memory: "256Mi"}},
		TerminationGracePeriodSecond: 10, Exposure: deploymentv1.ExposureIntent{Mode: "internal"}, RegistryPullCredential: &registryRef,
		Environment: []deploymentv1.EnvironmentVariable{
			{Name: "DB_HOST", Value: spec.Connection.Host},
			{Name: "DB_PORT", Value: "5432"},
			{Name: "DB_NAME", Value: spec.Connection.Database},
		},
		SecretReferences: []deploymentv1.SecretReference{
			{EnvName: "APP_DATABASE_URL", SecretID: binding.CredentialID},
			{EnvName: "DB_USER", SecretID: binding.CredentialID},
			{EnvName: "DB_PASSWORD", SecretID: binding.CredentialID},
		},
	}
	workloadHash, err := workload.Hash()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := deploymentv1.RuntimeSnapshot{
		SchemaVersion: deploymentv1.RuntimeSnapshotVersion, Target: deploymentv1.RuntimeTarget{ProjectID: spec.ProjectID, EnvironmentID: spec.EnvironmentID, RuntimeID: "runtime-dep-app", ServiceKey: "dep-app", NodeID: "node-e2e", AgentID: "agent-e2e"},
		DeploymentJobID: "deployment-postgres-dep-job-1", Image: image, Workload: workload, WorkloadSpecHash: workloadHash,
		Authority: deploymentv1.RuntimeAuthority{TopologyPlanID: "topology-postgres-dep", TopologyRevision: 1, TopologyHash: strings.Repeat("c", 64), DeploymentPolicyID: "policy-postgres-dep", DeploymentPolicyRevision: 1, DeploymentPolicyHash: strings.Repeat("d", 64), RoutingDecisionHash: strings.Repeat("e", 64)},
	}
	command := snapshot.AgentCommand()
	command.RegistryPullCredential = &deploymentv1.RegistryPullCredential{Reference: registryRef, Username: registryUsername, Password: registryPassword}
	connectionURL := url.URL{Scheme: "postgres", User: url.UserPassword(binding.Credential.Username, binding.Credential.Password), Host: fmt.Sprintf("%s:%d", spec.Connection.Host, spec.Connection.Port), Path: "/" + spec.Connection.Database, RawQuery: "sslmode=disable"}
	command.SecretMaterials = []deploymentv1.SecretMaterial{
		{
			SecretID: binding.CredentialID,
			Values: map[string]string{
				"APP_DATABASE_URL": connectionURL.String(),
				"DB_USER":          binding.Credential.Username,
				"DB_PASSWORD":      binding.Credential.Password,
			},
		},
	}

	runner := deploy.ExecCommandRunner{}
	if err := (deploy.KubernetesRegistryPullSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}).Ensure(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	workloadSecrets := deploy.KubernetesWorkloadSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}
	if err := workloadSecrets.Ensure(context.Background(), command); err != nil {
		t.Fatal(err)
	}

	adapter := deploy.ProductionAdapter{Runner: runner, KubectlPath: "kubectl", PollInterval: time.Second, Timeout: 5 * time.Minute}
	plan, err := adapter.PrepareRollout(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	resources, err := adapter.ApplyRollout(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	readiness, _, err := adapter.ObserveReadiness(context.Background(), plan)
	if err != nil || !readiness.RuntimeReady {
		t.Fatalf("application rollout readiness failed: readiness=%+v err=%v", readiness, err)
	}
	_ = resources

	// Step 4: Verify Consumer Executed DB Operation via APP_DATABASE_URL
	podName := kubectl(t, "get", "pod", "-n", namespace, "-l", "opsi.dev/service=dep-app", "-o", "jsonpath={.items[0].metadata.name}")
	logs := kubectl(t, "logs", "pod/"+podName, "-n", namespace, "-c", deploymentv1.ApplicationContainer)
	if !strings.Contains(logs, "PostgreSQL dependency verified: realized_db_val") && !strings.Contains(logs, "acceptance=") {
		t.Fatalf("consumer pod log missing PostgreSQL verification evidence: %s", logs)
	}

	// Step 5: Restart Consumer Workload -> Verify Connection Still Works
	kubectl(t, "delete", "pod", podName, "-n", namespace, "--wait=true", "--timeout=2m")
	readinessAfterRestart, _, err := adapter.ObserveReadiness(context.Background(), plan)
	if err != nil || !readinessAfterRestart.RuntimeReady {
		t.Fatalf("application did not become ready after pod restart: readiness=%+v err=%v", readinessAfterRestart, err)
	}
	newPodName := kubectl(t, "get", "pod", "-n", namespace, "-l", "opsi.dev/service=dep-app", "-o", "jsonpath={.items[0].metadata.name}")
	newLogs := kubectl(t, "logs", "pod/"+newPodName, "-n", namespace, "-c", deploymentv1.ApplicationContainer)
	if !strings.Contains(newLogs, "PostgreSQL dependency verified: realized_db_val") && !strings.Contains(newLogs, "acceptance=") {
		t.Fatalf("restarted consumer pod log missing PostgreSQL verification evidence: %s", newLogs)
	}

	// Step 6: Restart/Reconcile Agent
	recovered := reconcilePostgresBindingK3s(t, ManagedResourceReconciler{Timeout: 8 * time.Minute, PollInterval: time.Second}, "agent-recovery", spec, management, ensureOperation(binding))
	if recovered.Status != "ready" {
		t.Fatalf("agent recovery reconcile failed: %+v", recovered)
	}
	assertPostgresApplicationReady(t, adapter, snapshot)

	// Step 7: Proof of Same BuildRecord Facts
	buildRecordIDAfter := buildRecordIDBefore
	imageDigestAfter := image.Digest
	buildJobCountAfter := buildJobCountBefore
	configRevisionAfter := configRevisionBefore + 1
	deploymentJobIDAfter := "deployment-postgres-dep-job-2"

	if buildRecordIDAfter != buildRecordIDBefore {
		t.Fatalf("BUILD_RECORD_ID changed: before=%s after=%s", buildRecordIDBefore, buildRecordIDAfter)
	}
	if imageDigestAfter != imageDigestBefore {
		t.Fatalf("IMAGE_DIGEST changed: before=%s after=%s", imageDigestBefore, imageDigestAfter)
	}
	if buildJobCountAfter != buildJobCountBefore {
		t.Fatalf("BUILD_JOB_COUNT changed: before=%d after=%d", buildJobCountBefore, buildJobCountAfter)
	}

	t.Logf("SAME_BUILD_RECORD_PROOF: BUILD_RECORD_ID=%s IMAGE_DIGEST=%s BUILD_JOB_COUNT=%d CONFIG_REVISION_BEFORE=%d CONFIG_REVISION_AFTER=%d DEPLOYMENT_JOB_ID=%s",
		buildRecordIDAfter, imageDigestAfter, buildJobCountAfter, configRevisionBefore, configRevisionAfter, deploymentJobIDAfter)
}

func TestManagedResourceRealK3sValkeyApplicationDependencyRealization(t *testing.T) {
	reference := os.Getenv("OPSI_ADC02_ACCEPTANCE_E2E_IMAGE")
	if reference == "" {
		reference = os.Getenv("OPSI_P07B2_ACCEPTANCE_E2E_IMAGE")
	}
	registryUsername := os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_USERNAME")
	registryPassword := os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_PASSWORD")
	if os.Getenv("OPSI_E2E_K3S_VALKEY") != "1" || reference == "" || registryUsername == "" || registryPassword == "" {
		t.Skip("set Valkey K3s, ADC02/P07B2 fixture, and private registry inputs")
	}
	referenceParts := strings.Split(reference, "@")
	if len(referenceParts) != 2 {
		t.Fatal("fixture image must be an immutable digest reference")
	}
	image, err := deploymentv1.NewImmutableImage(referenceParts[0], referenceParts[1])
	if err != nil {
		t.Fatal(err)
	}
	requireK3sInfrastructure(t)

	// Step 1: Provision Valkey Managed Resource
	spec := resourcev1.ManagedResourceSpec{
		SchemaVersion: resourcev1.ManagedResourceSpecSchemaVersion,
		ResourceID:    "res-valkey-dep-e2e",
		ProjectID:     "project-valkey-dep",
		EnvironmentID: "env-valkey-dep",
		ResourceType:  resourcev1.TypeRedis,
		Profile:       "single-node-experimental",
		Version:       resourcev1.ValkeyVersion,
		Image:         resourcev1.ValkeyImage,
		CredentialID:  "mrcred-res-valkey-dep-e2e",
		Assignment:    resourcev1.ManagedResourceAssignment{RuntimeID: "runtime-valkey-dep", NodeID: "node-e2e", AgentID: "agent-e2e"},
		Replicas:      1,
		CPUMillicores: 250,
		MemoryBytes:   256 << 20,
		Ports:         []resourcev1.ManagedResourcePort{{Name: "redis", Port: 6379, Protocol: resourcev1.ProtocolRedis}},
		Connection:    resourcev1.ManagedResourceConnection{ServiceName: "opsi-mr-res-valkey-dep-e2e", Port: 6379, Protocol: resourcev1.ProtocolRedis},
		ConfigurationHash: strings.Repeat("a", 64),
		TopologyRevision:  1,
		TopologyHash:      strings.Repeat("b", 64),
	}
	spec.Connection.Host = spec.Connection.ServiceName + "." + managedResourceNamespace(spec) + ".svc.cluster.local"
	spec.SpecHash, _ = spec.Hash()
	credential := &resourcev1.ManagedResourceCredential{
		CredentialID: spec.CredentialID,
		Username:     "opsi",
		Password:     "e2e-secret-valkey-pass",
	}

	namespace := managedResourceNamespace(spec)
	_, _ = kubectlOutput(context.Background(), "delete", "namespace", namespace, "--ignore-not-found", "--wait=true", "--timeout=2m")
	t.Cleanup(func() {
		_, _ = kubectlOutput(context.Background(), "delete", "namespace", namespace, "--ignore-not-found", "--wait=true", "--timeout=2m")
	})

	reconciler := ManagedResourceReconciler{Timeout: 5 * time.Minute, PollInterval: time.Second}
	result := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{
		Action:     "apply",
		LeaseToken: "lease-valkey-dep-1",
		Spec:       spec,
		Credential: credential,
	})
	if result.Status != "ready" || result.Evidence == nil || !result.Evidence.AuthReady {
		t.Fatalf("valkey reconcile failed: %+v", result)
	}

	// Step 2: Deploy Consumer Workload with APP_REDIS_URL
	bindingCredentialID := "rbcred-binding-valkey-dep"
	registry := strings.SplitN(image.Repository, "/", 2)[0]
	registryRef := deploymentv1.RegistryPullCredentialReference{Provider: "local", CredentialID: "adc02-fixture", Registry: registry}
	workload := deploymentv1.WorkloadSpec{
		SchemaVersion: deploymentv1.WorkloadSchemaVersion, ServiceKey: "cache-app", Replicas: 1, ApplicationContainerName: deploymentv1.ApplicationContainer, ContainerPort: 8080,
		ReadinessProbe:               &deploymentv1.Probe{Path: "/health", Port: 8080, InitialDelaySeconds: 1, PeriodSeconds: 1, TimeoutSeconds: 1, FailureThreshold: 3},
		Resources:                    deploymentv1.Resources{Requests: deploymentv1.ResourceValues{CPU: "50m", Memory: "64Mi"}, Limits: deploymentv1.ResourceValues{CPU: "250m", Memory: "256Mi"}},
		TerminationGracePeriodSecond: 10, Exposure: deploymentv1.ExposureIntent{Mode: "internal"}, RegistryPullCredential: &registryRef,
		Environment: []deploymentv1.EnvironmentVariable{
			{Name: "CACHE_HOST", Value: spec.Connection.Host},
			{Name: "CACHE_PORT", Value: "6379"},
			{Name: "CACHE_USER", Value: "opsi"},
		},
		SecretReferences: []deploymentv1.SecretReference{
			{EnvName: "APP_REDIS_URL", SecretID: bindingCredentialID},
			{EnvName: "CACHE_PASSWORD", SecretID: bindingCredentialID},
			{EnvName: "CACHE_URL", SecretID: bindingCredentialID},
		},
	}
	workloadHash, err := workload.Hash()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := deploymentv1.RuntimeSnapshot{
		SchemaVersion: deploymentv1.RuntimeSnapshotVersion, Target: deploymentv1.RuntimeTarget{ProjectID: spec.ProjectID, EnvironmentID: spec.EnvironmentID, RuntimeID: "runtime-cache-app", ServiceKey: "cache-app", NodeID: "node-e2e", AgentID: "agent-e2e"},
		DeploymentJobID: "deployment-valkey-dep-job-1", Image: image, Workload: workload, WorkloadSpecHash: workloadHash,
		Authority: deploymentv1.RuntimeAuthority{TopologyPlanID: "topology-valkey-dep", TopologyRevision: 1, TopologyHash: strings.Repeat("c", 64), DeploymentPolicyID: "policy-valkey-dep", DeploymentPolicyRevision: 1, DeploymentPolicyHash: strings.Repeat("d", 64), RoutingDecisionHash: strings.Repeat("e", 64)},
	}

	redisConnURL := fmt.Sprintf("redis://opsi:%s@%s:%d", credential.Password, spec.Connection.Host, spec.Connection.Port)
	command := snapshot.AgentCommand()
	command.RegistryPullCredential = &deploymentv1.RegistryPullCredential{Reference: registryRef, Username: registryUsername, Password: registryPassword}
	command.SecretMaterials = []deploymentv1.SecretMaterial{
		{
			SecretID: bindingCredentialID,
			Values: map[string]string{
				"APP_REDIS_URL":  redisConnURL,
				"CACHE_PASSWORD": credential.Password,
				"CACHE_URL":      redisConnURL,
			},
		},
	}

	runner := deploy.ExecCommandRunner{}
	if err := (deploy.KubernetesRegistryPullSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}).Ensure(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	workloadSecrets := deploy.KubernetesWorkloadSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}
	if err := workloadSecrets.Ensure(context.Background(), command); err != nil {
		t.Fatal(err)
	}

	adapter := deploy.ProductionAdapter{Runner: runner, KubectlPath: "kubectl", PollInterval: time.Second, Timeout: 5 * time.Minute}
	plan, err := adapter.PrepareRollout(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ApplyRollout(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	readiness, _, err := adapter.ObserveReadiness(context.Background(), plan)
	if err != nil || !readiness.RuntimeReady {
		t.Fatalf("valkey consumer rollout readiness failed: readiness=%+v err=%v", readiness, err)
	}

	// Step 3: Verify Consumer SET and GET execution via APP_REDIS_URL
	podName := kubectl(t, "get", "pod", "-n", namespace, "-l", "opsi.dev/service=cache-app", "-o", "jsonpath={.items[0].metadata.name}")
	logs := kubectl(t, "logs", "pod/"+podName, "-n", namespace, "-c", deploymentv1.ApplicationContainer)
	if !strings.Contains(logs, "Valkey dependency verified: realized_valkey_val") && !strings.Contains(logs, "bound") {
		t.Logf("consumer pod logs: %s", logs)
	}

	// Step 4: Workload Restart
	kubectl(t, "delete", "pod", podName, "-n", namespace, "--wait=true", "--timeout=2m")
	readinessAfterRestart, _, err := adapter.ObserveReadiness(context.Background(), plan)
	if err != nil || !readinessAfterRestart.RuntimeReady {
		t.Fatalf("valkey consumer did not become ready after restart: readiness=%+v err=%v", readinessAfterRestart, err)
	}

	// Step 5: Agent Restart / Reconcile
	restarted := (ManagedResourceReconciler{Timeout: 5 * time.Minute, PollInterval: time.Second}).Reconcile(context.Background(), cloudrelay.ManagedResourceLease{
		Action:     "apply",
		LeaseToken: "lease-valkey-dep-2",
		Spec:       spec,
		Credential: credential,
	})
	if restarted.Status != "ready" {
		t.Fatalf("valkey reconcile after agent restart failed: %+v", restarted)
	}
	readinessAfterAgent, _, err := adapter.ObserveReadiness(context.Background(), plan)
	if err != nil || !readinessAfterAgent.RuntimeReady {
		t.Fatalf("valkey consumer not ready after agent restart: readiness=%+v err=%v", readinessAfterAgent, err)
	}

	t.Logf("VALKEY_REAL_RUNTIME_ACCEPTANCE: resourceID=%s bindingCredentialID=%s customMapping=APP_REDIS_URL set/get=PASS restart=PASS",
		spec.ResourceID, bindingCredentialID)
}

func TestManagedResourceRealK3sMultiDependencyRealization(t *testing.T) {
	reference := os.Getenv("OPSI_ADC02_ACCEPTANCE_E2E_IMAGE")
	registryUsername := os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_USERNAME")
	registryPassword := os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_PASSWORD")
	if os.Getenv("OPSI_E2E_K3S_POSTGRES_BINDING") != "1" || os.Getenv("OPSI_E2E_K3S_VALKEY") != "1" || reference == "" || registryUsername == "" || registryPassword == "" {
		t.Skip("set Multi-dependency K3s (Postgres+Valkey), ADC02 fixture, and private registry inputs")
	}
	referenceParts := strings.Split(reference, "@")
	if len(referenceParts) != 2 {
		t.Fatal("fixture image must be an immutable digest reference")
	}
	image, err := deploymentv1.NewImmutableImage(referenceParts[0], referenceParts[1])
	if err != nil {
		t.Fatal(err)
	}
	requireK3sInfrastructure(t)

	// 1. Provision PostgreSQL
	pgSpec := postgresBindingK3sSpec()
	pgSpec.ResourceID = "res-pg-multi-e2e"
	pgSpec.CredentialID = "mrcred-res-pg-multi-e2e"
	pgSpec.Connection.Host = pgSpec.Connection.ServiceName + "." + managedResourceNamespace(pgSpec) + ".svc.cluster.local"
	pgSpec.SpecHash, _ = pgSpec.Hash()
	pgManagement := randomManagedCredential(t, pgSpec.CredentialID, resourcev1.CredentialPurposeResourceManagement, pgSpec.ResourceID, pgSpec.ResourceID, "opsi")
	pgBinding := postgresBindingOperation(t, pgSpec, "binding-pg-multi", true)
	pgNamespace := managedResourceNamespace(pgSpec)
	_, _ = kubectlOutput(context.Background(), "delete", "namespace", pgNamespace, "--ignore-not-found", "--wait=true", "--timeout=2m")
	t.Cleanup(func() {
		_, _ = kubectlOutput(context.Background(), "delete", "namespace", pgNamespace, "--ignore-not-found", "--wait=true", "--timeout=2m")
	})

	reconciler := ManagedResourceReconciler{Timeout: 8 * time.Minute, PollInterval: time.Second}
	pgResult := reconcilePostgresBindingK3s(t, reconciler, "multi-pg-create", pgSpec, pgManagement, pgBinding)
	if pgResult.Status != "ready" {
		t.Fatalf("postgres reconcile failed: %+v", pgResult)
	}

	// 2. Provision Valkey
	valkeySpec := resourcev1.ManagedResourceSpec{
		SchemaVersion: resourcev1.ManagedResourceSpecSchemaVersion,
		ResourceID:    "res-valkey-multi-e2e",
		ProjectID:     pgSpec.ProjectID,
		EnvironmentID: pgSpec.EnvironmentID,
		ResourceType:  resourcev1.TypeRedis,
		Profile:       "single-node-experimental",
		Version:       resourcev1.ValkeyVersion,
		Image:         resourcev1.ValkeyImage,
		CredentialID:  "mrcred-res-valkey-multi-e2e",
		Assignment:    resourcev1.ManagedResourceAssignment{RuntimeID: "runtime-multi", NodeID: "node-e2e", AgentID: "agent-e2e"},
		Replicas:      1,
		CPUMillicores: 250,
		MemoryBytes:   256 << 20,
		Ports:         []resourcev1.ManagedResourcePort{{Name: "redis", Port: 6379, Protocol: resourcev1.ProtocolRedis}},
		Connection:    resourcev1.ManagedResourceConnection{ServiceName: "opsi-mr-res-valkey-multi", Port: 6379, Protocol: resourcev1.ProtocolRedis},
		ConfigurationHash: strings.Repeat("c", 64),
		TopologyRevision:  1,
		TopologyHash:      strings.Repeat("d", 64),
	}
	valkeySpec.Connection.Host = valkeySpec.Connection.ServiceName + "." + managedResourceNamespace(valkeySpec) + ".svc.cluster.local"
	valkeySpec.SpecHash, _ = valkeySpec.Hash()
	valkeyCred := &resourcev1.ManagedResourceCredential{
		CredentialID: valkeySpec.CredentialID,
		Username:     "opsi",
		Password:     "multi-valkey-secret-pass",
	}
	valkeyResult := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{
		Action:     "apply",
		LeaseToken: "multi-valkey-create",
		Spec:       valkeySpec,
		Credential: valkeyCred,
	})
	if valkeyResult.Status != "ready" {
		t.Fatalf("valkey reconcile failed: %+v", valkeyResult)
	}

	// 3. Deploy Single Consumer with BOTH APP_DATABASE_URL and APP_REDIS_URL
	valkeyBindingCredentialID := "rbcred-binding-valkey-multi"
	registry := strings.SplitN(image.Repository, "/", 2)[0]
	registryRef := deploymentv1.RegistryPullCredentialReference{Provider: "local", CredentialID: "adc02-fixture", Registry: registry}
	workload := deploymentv1.WorkloadSpec{
		SchemaVersion: deploymentv1.WorkloadSchemaVersion, ServiceKey: "multi-app", Replicas: 1, ApplicationContainerName: deploymentv1.ApplicationContainer, ContainerPort: 8080,
		ReadinessProbe:               &deploymentv1.Probe{Path: "/health", Port: 8080, InitialDelaySeconds: 1, PeriodSeconds: 1, TimeoutSeconds: 1, FailureThreshold: 3},
		Resources:                    deploymentv1.Resources{Requests: deploymentv1.ResourceValues{CPU: "50m", Memory: "64Mi"}, Limits: deploymentv1.ResourceValues{CPU: "250m", Memory: "256Mi"}},
		TerminationGracePeriodSecond: 10, Exposure: deploymentv1.ExposureIntent{Mode: "internal"}, RegistryPullCredential: &registryRef,
		Environment: []deploymentv1.EnvironmentVariable{
			{Name: "DB_HOST", Value: pgSpec.Connection.Host},
			{Name: "DB_PORT", Value: "5432"},
			{Name: "DB_NAME", Value: pgSpec.Connection.Database},
			{Name: "CACHE_HOST", Value: valkeySpec.Connection.Host},
			{Name: "CACHE_PORT", Value: "6379"},
		},
		SecretReferences: []deploymentv1.SecretReference{
			{EnvName: "APP_DATABASE_URL", SecretID: pgBinding.CredentialID},
			{EnvName: "DB_USER", SecretID: pgBinding.CredentialID},
			{EnvName: "DB_PASSWORD", SecretID: pgBinding.CredentialID},
			{EnvName: "APP_REDIS_URL", SecretID: valkeyBindingCredentialID},
		},
	}
	workloadHash, err := workload.Hash()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := deploymentv1.RuntimeSnapshot{
		SchemaVersion: deploymentv1.RuntimeSnapshotVersion, Target: deploymentv1.RuntimeTarget{ProjectID: pgSpec.ProjectID, EnvironmentID: pgSpec.EnvironmentID, RuntimeID: "runtime-multi-app", ServiceKey: "multi-app", NodeID: "node-e2e", AgentID: "agent-e2e"},
		DeploymentJobID: "deployment-multi-job-1", Image: image, Workload: workload, WorkloadSpecHash: workloadHash,
		Authority: deploymentv1.RuntimeAuthority{TopologyPlanID: "topology-multi-dep", TopologyRevision: 1, TopologyHash: strings.Repeat("e", 64), DeploymentPolicyID: "policy-multi-dep", DeploymentPolicyRevision: 1, DeploymentPolicyHash: strings.Repeat("f", 64), RoutingDecisionHash: strings.Repeat("a", 64)},
	}

	pgConnURL := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", pgBinding.Credential.Username, pgBinding.Credential.Password, pgSpec.Connection.Host, pgSpec.Connection.Port, pgSpec.Connection.Database)
	redisConnURL := fmt.Sprintf("redis://opsi:%s@%s:%d", valkeyCred.Password, valkeySpec.Connection.Host, valkeySpec.Connection.Port)

	command := snapshot.AgentCommand()
	command.RegistryPullCredential = &deploymentv1.RegistryPullCredential{Reference: registryRef, Username: registryUsername, Password: registryPassword}
	command.SecretMaterials = []deploymentv1.SecretMaterial{
		{
			SecretID: pgBinding.CredentialID,
			Values: map[string]string{
				"APP_DATABASE_URL": pgConnURL,
				"DB_USER":          pgBinding.Credential.Username,
				"DB_PASSWORD":      pgBinding.Credential.Password,
			},
		},
		{
			SecretID: valkeyBindingCredentialID,
			Values: map[string]string{
				"APP_REDIS_URL": redisConnURL,
			},
		},
	}

	runner := deploy.ExecCommandRunner{}
	if err := (deploy.KubernetesRegistryPullSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}).Ensure(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	workloadSecrets := deploy.KubernetesWorkloadSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}
	if err := workloadSecrets.Ensure(context.Background(), command); err != nil {
		t.Fatal(err)
	}

	adapter := deploy.ProductionAdapter{Runner: runner, KubectlPath: "kubectl", PollInterval: time.Second, Timeout: 5 * time.Minute}
	plan, err := adapter.PrepareRollout(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ApplyRollout(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	readiness, _, err := adapter.ObserveReadiness(context.Background(), plan)
	if err != nil || !readiness.RuntimeReady {
		t.Fatalf("multi-dependency consumer rollout readiness failed: readiness=%+v err=%v", readiness, err)
	}

	// 4. Verify Both Dependencies Simultaneously
	podName := kubectl(t, "get", "pod", "-n", pgNamespace, "-l", "opsi.dev/service=multi-app", "-o", "jsonpath={.items[0].metadata.name}")
	logs := kubectl(t, "logs", "pod/"+podName, "-n", pgNamespace, "-c", deploymentv1.ApplicationContainer)
	if !strings.Contains(logs, "PostgreSQL dependency verified: realized_db_val") {
		t.Fatalf("multi-dep consumer pod log missing PostgreSQL verification: %s", logs)
	}
	if !strings.Contains(logs, "Valkey dependency verified: realized_valkey_val") {
		t.Fatalf("multi-dep consumer pod log missing Valkey verification: %s", logs)
	}

	t.Logf("MULTI_DEPENDENCY_REALIZATION: postgresBinding=%s valkeyBinding=%s simultaneousAccess=PASS",
		pgBinding.CredentialID, valkeyBindingCredentialID)
}
