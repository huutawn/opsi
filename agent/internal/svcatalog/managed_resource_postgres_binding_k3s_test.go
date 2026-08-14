package svcatalog

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

const postgresBindingClientScript = `set -eu
role=$1; db=$2
IFS= read -r password
export PGPASSWORD=$password
psql -v ON_ERROR_STOP=1 -h 127.0.0.1 -U "$role" -d "$db" -tAc "SELECT 1"`

const postgresBindingEvidenceScript = `set -eu
role=$1; db=$2
manager=$(cat /run/opsi-postgres/username)
export PGPASSWORD=$(cat /run/opsi-postgres/password)
psql -v ON_ERROR_STOP=1 -h 127.0.0.1 -U "$manager" -d "$db" -tAc "SELECT rolcanlogin::int||':'||rolsuper::int||':'||rolcreatedb::int||':'||rolcreaterole::int||':'||rolreplication::int||':'||rolbypassrls::int FROM pg_roles WHERE rolname='$role'; SELECT has_database_privilege('$role','$db','CONNECT')::int||':'||has_schema_privilege('$role','public','USAGE')::int||':'||has_schema_privilege('$role','public','CREATE')::int; SELECT pg_get_userbyid(relowner) FROM pg_class WHERE relname='opsi_p07b3b1_acceptance'"`

func TestManagedResourceRealK3sPostgresApplicationBinding(t *testing.T) {
	reference := os.Getenv("OPSI_P07B3B1_ACCEPTANCE_E2E_IMAGE")
	registryUsername := os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_USERNAME")
	registryPassword := os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_PASSWORD")
	if os.Getenv("OPSI_E2E_K3S_POSTGRES_BINDING") != "1" || reference == "" || registryUsername == "" || registryPassword == "" {
		t.Skip("set P07B3B1 K3s, immutable fixture, and private registry inputs")
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

	spec := postgresBindingK3sSpec()
	management := randomManagedCredential(t, spec.CredentialID, resourcev1.CredentialPurposeResourceManagement, spec.ResourceID, spec.ResourceID, "opsi")
	bindingA := postgresBindingOperation(t, spec, "binding-postgres-api", true)
	bindingB := postgresBindingOperation(t, spec, "binding-postgres-worker", true)
	namespace := managedResourceNamespace(spec)
	_, _ = kubectlOutput(context.Background(), "delete", "namespace", namespace, "--ignore-not-found", "--wait=true", "--timeout=2m")
	t.Cleanup(func() {
		_, _ = kubectlOutput(context.Background(), "delete", "namespace", namespace, "--ignore-not-found", "--wait=true", "--timeout=2m")
	})

	reconciler := ManagedResourceReconciler{Timeout: 8 * time.Minute, PollInterval: time.Second}
	first := reconcilePostgresBindingK3s(t, reconciler, "binding-create", spec, management, bindingA, bindingB)
	pvcUID := kubectl(t, "get", "pvc", first.Evidence.PVCName, "-n", namespace, "-o", "jsonpath={.metadata.uid}")
	managementSecret := managedResourceSecretName(spec)

	snapshot, command := postgresBindingApplicationSnapshot(t, spec, image, *bindingA.Credential, registryUsername, registryPassword)
	runner := deploy.ExecCommandRunner{}
	if err := (deploy.KubernetesRegistryPullSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}).Ensure(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	workloadSecrets := deploy.KubernetesWorkloadSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}
	if err := workloadSecrets.Ensure(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	bCommand := command
	bCommand.Workload.ServiceKey = "worker"
	bCommand.Workload.SecretReferences = postgresBindingSecretRefs(bindingB.CredentialID)
	bCommand.SecretMaterials = []deploymentv1.SecretMaterial{postgresBindingSecretMaterial(spec, *bindingB.Credential)}
	if err := workloadSecrets.Ensure(context.Background(), bCommand); err != nil {
		t.Fatal(err)
	}
	secretA, secretAUID := bindingSecretIdentity(t, namespace, bindingA.CredentialID)
	secretB, secretBUID := bindingSecretIdentity(t, namespace, bindingB.CredentialID)
	if secretA == managementSecret || secretB == managementSecret || secretA == secretB {
		t.Fatalf("binding Secret isolation failed management=%s a=%s b=%s", managementSecret, secretA, secretB)
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
		t.Fatalf("application readiness=%+v err=%v", readiness, err)
	}
	deploymentName := resourceName(resources, "Deployment")
	podName := kubectl(t, "get", "pod", "-n", namespace, "-l", "opsi.dev/service=api", "-o", "jsonpath={.items[0].metadata.name}")
	applicationImageID := kubectl(t, "get", "pod", podName, "-n", namespace, "-o", "jsonpath={.status.containerStatuses[0].imageID}")
	if !strings.Contains(applicationImageID, image.Digest) {
		t.Fatalf("application imageID=%s digest=%s", applicationImageID, image.Digest)
	}
	logs := kubectl(t, "logs", "pod/"+podName, "-n", namespace, "-c", deploymentv1.ApplicationContainer)
	if !strings.Contains(logs, `acceptance={"select_1":1,"inserted":"inserted","updated":"updated","reconnect":"updated"}`) {
		t.Fatalf("application SQL acceptance evidence missing: %s", logs)
	}

	if output, err := runBindingLogin(reconciler, spec, bindingA, "wrong-password"); err == nil || !strings.Contains(output+err.Error(), "password authentication failed") {
		t.Fatalf("wrong binding password was not rejected err=%v output=%q", err, output)
	}
	if output, err := runBindingLogin(reconciler, spec, bindingA, bindingA.Credential.Password); err != nil || strings.TrimSpace(output) != "1" {
		t.Fatalf("correct binding credential failed err=%v output=%q", err, output)
	}
	roleEvidence := postgresBindingEvidence(t, reconciler, spec, bindingA)
	if len(roleEvidence) != 3 || roleEvidence[0] != "1:0:0:0:0:0" || roleEvidence[1] != "1:1:1" || roleEvidence[2] != bindingA.RoleName {
		t.Fatalf("role evidence=%v", roleEvidence)
	}
	assertPostgresBindingSecretIsolation(t, namespace, deploymentName, managementSecret, secretA, secretB, registryUsername, registryPassword, bindingA.Credential.Password, spec)

	if err := workloadSecrets.Ensure(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	redeployPlan, err := adapter.PrepareRollout(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ApplyRollout(context.Background(), redeployPlan); err != nil {
		t.Fatal(err)
	}
	if evidence, _, err := adapter.ObserveReadiness(context.Background(), redeployPlan); err != nil || !evidence.RuntimeReady {
		t.Fatalf("application redeploy evidence=%+v err=%v", evidence, err)
	}
	_, secretAUIDAfter := bindingSecretIdentity(t, namespace, bindingA.CredentialID)
	if secretAUIDAfter != secretAUID {
		t.Fatal("application binding Secret identity changed on redeploy")
	}

	kubectl(t, "delete", "pod", spec.Connection.ServiceName+"-0", "-n", namespace, "--wait=true", "--timeout=2m")
	recreated := reconcilePostgresBindingK3s(t, reconciler, "postgres-pod-recreate", spec, management, ensureOperation(bindingA), ensureOperation(bindingB))
	if kubectl(t, "get", "pvc", recreated.Evidence.PVCName, "-n", namespace, "-o", "jsonpath={.metadata.uid}") != pvcUID {
		t.Fatal("PVC identity changed after PostgreSQL Pod recreation")
	}
	assertPostgresApplicationReady(t, adapter, snapshot)

	recovered := reconcilePostgresBindingK3s(t, ManagedResourceReconciler{Timeout: 8 * time.Minute, PollInterval: time.Second}, "agent-recovery", spec, management, ensureOperation(bindingA), ensureOperation(bindingB))
	if recovered.Evidence.PVCName != first.Evidence.PVCName {
		t.Fatal("Agent recovery changed PostgreSQL storage identity")
	}
	assertPostgresApplicationReady(t, adapter, snapshot)

	updatedSpec := spec
	updatedSpec.CPUMillicores = 300
	updatedSpec.SpecHash, _ = updatedSpec.Hash()
	updated := reconcilePostgresBindingK3s(t, reconciler, "postgres-compute-update", updatedSpec, management, ensureOperation(bindingA), ensureOperation(bindingB))
	if updated.Evidence.PVCName != first.Evidence.PVCName || kubectl(t, "get", "pvc", updated.Evidence.PVCName, "-n", namespace, "-o", "jsonpath={.metadata.uid}") != pvcUID {
		t.Fatal("CPU update changed PostgreSQL storage identity")
	}
	assertPostgresApplicationReady(t, adapter, snapshot)

	revokedA := *bindingA
	revokedA.Action, revokedA.Credential = resourcev1.PostgresBindingRevoke, nil
	reconcilePostgresBindingK3s(t, reconciler, "binding-a-delete", updatedSpec, management, &revokedA, ensureOperation(bindingB))
	if names := kubectl(t, "get", "secret", "-n", namespace, "-l", "opsi.dev/workload-secret="+managedLabel(bindingA.CredentialID), "-o", "jsonpath={.items[*].metadata.name}"); strings.TrimSpace(names) != "" {
		t.Fatalf("binding A Secret remains: %s", names)
	}
	if _, uid := bindingSecretIdentity(t, namespace, bindingB.CredentialID); uid != secretBUID {
		t.Fatal("binding B Secret changed when binding A was deleted")
	}
	if output, err := runBindingLogin(reconciler, updatedSpec, bindingA, bindingA.Credential.Password); err == nil || !strings.Contains(output+err.Error(), "not permitted to log in") {
		t.Fatalf("old binding credential was not revoked err=%v output=%q", err, output)
	}
	if output, err := runBindingLogin(reconciler, updatedSpec, bindingB, bindingB.Credential.Password); err != nil || strings.TrimSpace(output) != "1" {
		t.Fatalf("binding B was affected by binding A deletion err=%v output=%q", err, output)
	}
	retained := postgresBindingEvidence(t, reconciler, updatedSpec, bindingA)
	if len(retained) != 3 || retained[0] != "0:0:0:0:0:0" || retained[2] != bindingA.RoleName || kubectl(t, "get", "pvc", updated.Evidence.PVCName, "-n", namespace, "-o", "jsonpath={.metadata.uid}") != pvcUID {
		t.Fatalf("revocation/data retention evidence=%v", retained)
	}

	writePostgresBindingEvidence(t, []string{management.Password, registryPassword, bindingA.Credential.Password, bindingB.Credential.Password}, map[string]any{
		"resource_id": spec.ResourceID, "resource_binding_id": bindingA.BindingID, "logical_name": "DATABASE",
		"database_keys":     []string{"DATABASE_HOST", "DATABASE_PORT", "DATABASE_NAME", "DATABASE_USER", "DATABASE_PASSWORD", "DATABASE_URL"},
		"management_secret": managementSecret, "application_binding_secret": secretA, "application_role": bindingA.RoleName,
		"binding_credential_id": bindingA.CredentialID, "second_binding_id": bindingB.BindingID, "second_binding_credential_id": bindingB.CredentialID, "second_binding_secret": secretB, "second_binding_role": bindingB.RoleName,
		"role_attributes": roleEvidence[0], "grants": "CONNECT database; USAGE,CREATE public schema; table/sequence DML; matching manager default privileges", "table_owner": roleEvidence[2],
		"fixture": image.Reference, "application_image_id": applicationImageID, "application_image_id_hash": readiness.ApplicationImageIDHash, "deployment_job_id": snapshot.DeploymentJobID, "deployment_result": "succeeded",
		"sql":                             map[string]any{"select_1": 1, "create_table": "PASS", "insert": "inserted", "read": "inserted", "update": "updated", "reconnect": "updated"},
		"management_credential_isolation": "PASS", "redeploy_credential_stability": "PASS", "pod_recreation": "PASS", "agent_recovery": "PASS", "compute_update": "PASS",
		"multiple_binding_isolation": "PASS", "binding_deletion": "NOLOGIN grants revoked secret removed", "old_credential": "REJECTED", "pvc_uid": pvcUID, "data_retained": "PASS",
	})
}

func postgresBindingK3sSpec() resourcev1.ManagedResourceSpec {
	spec := resourcev1.ManagedResourceSpec{
		SchemaVersion: resourcev1.ManagedResourceSpecSchemaVersion, ResourceID: "res-postgres-binding-e2e", ProjectID: "project-postgres-binding", EnvironmentID: "env-postgres-binding",
		ResourceType: resourcev1.TypePostgres, Profile: "single-node-experimental", Version: resourcev1.PostgresVersion, Image: resourcev1.PostgresImage,
		CredentialID: "mrcred-res-postgres-binding-e2e", Assignment: resourcev1.ManagedResourceAssignment{RuntimeID: "runtime-e2e", NodeID: "node-e2e", AgentID: "agent-e2e"},
		Replicas: 1, CPUMillicores: 250, MemoryBytes: 256 << 20, Ports: []resourcev1.ManagedResourcePort{{Name: "postgres", Port: 5432, Protocol: resourcev1.ProtocolPostgres}},
		Storage:           resourcev1.StorageRequest{Persistent: true, SizeBytes: 1 << 30, PolicyRef: resourcev1.StoragePolicyDefault},
		Connection:        resourcev1.ManagedResourceConnection{ServiceName: "opsi-mr-postgres-binding", Port: 5432, Protocol: resourcev1.ProtocolPostgres, Database: "opsi"},
		ConfigurationHash: strings.Repeat("a", 64), TopologyRevision: 1, TopologyHash: strings.Repeat("b", 64),
	}
	spec.Connection.Host = spec.Connection.ServiceName + "." + managedResourceNamespace(spec) + ".svc.cluster.local"
	spec.SpecHash, _ = spec.Hash()
	return spec
}

func randomManagedCredential(t *testing.T, credentialID, purpose, ownerID, resourceID, username string) *resourcev1.ManagedResourceCredential {
	t.Helper()
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return &resourcev1.ManagedResourceCredential{CredentialID: credentialID, Purpose: purpose, OwnerID: ownerID, ResourceID: resourceID, Username: username, Password: base64.RawURLEncoding.EncodeToString(value), Database: "opsi"}
}

func postgresBindingOperation(t *testing.T, spec resourcev1.ManagedResourceSpec, bindingID string, create bool) *resourcev1.PostgresBindingOperation {
	t.Helper()
	sum := sha256.Sum256([]byte(bindingID))
	role := "opsi_b_" + hex.EncodeToString(sum[:])[:32]
	credentialID := "rbcred-" + bindingID
	credential := randomManagedCredential(t, credentialID, resourcev1.CredentialPurposeResourceBinding, bindingID, spec.ResourceID, role)
	return &resourcev1.PostgresBindingOperation{BindingID: bindingID, CredentialID: credentialID, RoleName: role, Database: spec.Connection.Database, Action: resourcev1.PostgresBindingEnsure, Create: create, Credential: credential}
}

func ensureOperation(operation *resourcev1.PostgresBindingOperation) *resourcev1.PostgresBindingOperation {
	copy := *operation
	copy.Create = false
	return &copy
}

func reconcilePostgresBindingK3s(t *testing.T, reconciler ManagedResourceReconciler, token string, spec resourcev1.ManagedResourceSpec, management *resourcev1.ManagedResourceCredential, operations ...*resourcev1.PostgresBindingOperation) cloudrelay.ManagedResourceResult {
	t.Helper()
	bindings := make([]resourcev1.PostgresBindingOperation, 0, len(operations))
	for _, operation := range operations {
		bindings = append(bindings, *operation)
	}
	result := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: token, Spec: spec, Credential: management, Bindings: bindings})
	if result.Status != "ready" || result.Evidence == nil || !result.Evidence.AuthReady || !result.Evidence.StorageReady || len(result.BindingResults) != len(bindings) {
		t.Fatalf("managed reconcile=%+v", result)
	}
	for _, binding := range result.BindingResults {
		if binding.Status != "ready" {
			t.Fatalf("binding reconcile=%+v", binding)
		}
	}
	return result
}

func postgresBindingApplicationSnapshot(t *testing.T, spec resourcev1.ManagedResourceSpec, image deploymentv1.ImmutableImage, credential resourcev1.ManagedResourceCredential, registryUsername, registryPassword string) (deploymentv1.RuntimeSnapshot, deploymentv1.AgentCommand) {
	t.Helper()
	registry := strings.SplitN(image.Repository, "/", 2)[0]
	registryRef := deploymentv1.RegistryPullCredentialReference{Provider: "local", CredentialID: "p07b3b1-fixture", Registry: registry}
	workload := deploymentv1.WorkloadSpec{
		SchemaVersion: deploymentv1.WorkloadSchemaVersion, ServiceKey: "api", Replicas: 1, ApplicationContainerName: deploymentv1.ApplicationContainer, ContainerPort: 8080,
		ReadinessProbe:               &deploymentv1.Probe{Path: "/health", Port: 8080, InitialDelaySeconds: 1, PeriodSeconds: 1, TimeoutSeconds: 1, FailureThreshold: 3},
		Resources:                    deploymentv1.Resources{Requests: deploymentv1.ResourceValues{CPU: "50m", Memory: "64Mi"}, Limits: deploymentv1.ResourceValues{CPU: "250m", Memory: "256Mi"}},
		TerminationGracePeriodSecond: 10, Exposure: deploymentv1.ExposureIntent{Mode: "internal"}, RegistryPullCredential: &registryRef,
		Environment:      []deploymentv1.EnvironmentVariable{{Name: "DATABASE_HOST", Value: spec.Connection.Host}, {Name: "DATABASE_PORT", Value: "5432"}, {Name: "DATABASE_NAME", Value: spec.Connection.Database}},
		SecretReferences: postgresBindingSecretRefs(credential.CredentialID),
	}
	workloadHash, err := workload.Hash()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := deploymentv1.RuntimeSnapshot{
		SchemaVersion: deploymentv1.RuntimeSnapshotVersion, Target: deploymentv1.RuntimeTarget{ProjectID: spec.ProjectID, EnvironmentID: spec.EnvironmentID, RuntimeID: "runtime-application", ServiceKey: "api", NodeID: "node-e2e", AgentID: "agent-e2e"},
		DeploymentJobID: "deployment-postgres-binding", Image: image, Workload: workload, WorkloadSpecHash: workloadHash,
		Authority: deploymentv1.RuntimeAuthority{TopologyPlanID: "topology-postgres-binding", TopologyRevision: 1, TopologyHash: strings.Repeat("c", 64), DeploymentPolicyID: "policy-postgres-binding", DeploymentPolicyRevision: 1, DeploymentPolicyHash: strings.Repeat("d", 64), RoutingDecisionHash: strings.Repeat("e", 64)},
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	command := snapshot.AgentCommand()
	command.RegistryPullCredential = &deploymentv1.RegistryPullCredential{Reference: registryRef, Username: registryUsername, Password: registryPassword}
	command.SecretMaterials = []deploymentv1.SecretMaterial{postgresBindingSecretMaterial(spec, credential)}
	return snapshot, command
}

func postgresBindingSecretRefs(credentialID string) []deploymentv1.SecretReference {
	return []deploymentv1.SecretReference{{EnvName: "DATABASE_USER", SecretID: credentialID}, {EnvName: "DATABASE_PASSWORD", SecretID: credentialID}, {EnvName: "DATABASE_URL", SecretID: credentialID}}
}

func postgresBindingSecretMaterial(spec resourcev1.ManagedResourceSpec, credential resourcev1.ManagedResourceCredential) deploymentv1.SecretMaterial {
	connectionURL := url.URL{Scheme: "postgres", User: url.UserPassword(credential.Username, credential.Password), Host: fmt.Sprintf("%s:%d", spec.Connection.Host, spec.Connection.Port), Path: "/" + spec.Connection.Database, RawQuery: "sslmode=disable"}
	return deploymentv1.SecretMaterial{SecretID: credential.CredentialID, Values: map[string]string{"DATABASE_USER": credential.Username, "DATABASE_PASSWORD": credential.Password, "DATABASE_URL": connectionURL.String()}}
}

func bindingSecretIdentity(t *testing.T, namespace, credentialID string) (string, string) {
	t.Helper()
	selector := "opsi.dev/workload-secret=" + managedLabel(credentialID)
	name := kubectl(t, "get", "secret", "-n", namespace, "-l", selector, "-o", "jsonpath={.items[0].metadata.name}")
	uid := kubectl(t, "get", "secret", name, "-n", namespace, "-o", "jsonpath={.metadata.uid}")
	return name, uid
}

func resourceName(resources []deploymentv1.ResourceIdentity, kind string) string {
	for _, resource := range resources {
		if resource.Kind == kind {
			return resource.Name
		}
	}
	return ""
}

func runBindingLogin(reconciler ManagedResourceReconciler, spec resourcev1.ManagedResourceSpec, operation *resourcev1.PostgresBindingOperation, password string) (string, error) {
	output, err := reconciler.postgresBindingExec(context.Background(), spec, []byte(password+"\n"), postgresBindingClientScript, *operation)
	return string(output), err
}

func postgresBindingEvidence(t *testing.T, reconciler ManagedResourceReconciler, spec resourcev1.ManagedResourceSpec, operation *resourcev1.PostgresBindingOperation) []string {
	t.Helper()
	output, err := reconciler.postgresBindingExec(context.Background(), spec, nil, postgresBindingEvidenceScript, *operation)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(strings.TrimSpace(string(output)))
}

func assertPostgresApplicationReady(t *testing.T, adapter deploy.ProductionAdapter, snapshot deploymentv1.RuntimeSnapshot) {
	t.Helper()
	plan, err := adapter.PrepareRollout(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	evidence, _, err := adapter.ObserveReadiness(context.Background(), plan)
	if err != nil || !evidence.RuntimeReady {
		t.Fatalf("application did not reconnect evidence=%+v err=%v", evidence, err)
	}
}

func assertPostgresBindingSecretIsolation(t *testing.T, namespace, deploymentName, managementSecret, secretA, secretB, registryUsername, registryPassword, bindingPassword string, spec resourcev1.ManagedResourceSpec) {
	t.Helper()
	application := kubectl(t, "get", "deployment", deploymentName, "-n", namespace, "-o", "json")
	server := kubectl(t, "get", "statefulset", spec.Connection.ServiceName, "-n", namespace, "-o", "json")
	for _, forbidden := range []string{managementSecret, secretB, registryUsername, registryPassword, bindingPassword} {
		if strings.Contains(application, forbidden) {
			t.Fatalf("application Deployment contains forbidden credential authority %q", forbidden)
		}
	}
	if !strings.Contains(application, secretA) {
		t.Fatal("application Deployment does not reference its exact binding Secret")
	}
	for _, forbidden := range []string{secretA, secretB, registryUsername, registryPassword} {
		if strings.Contains(server, forbidden) {
			t.Fatalf("PostgreSQL workload contains application/registry credential %q", forbidden)
		}
	}
}

func writePostgresBindingEvidence(t *testing.T, secrets []string, evidence map[string]any) {
	t.Helper()
	dir := os.Getenv("OPSI_K3S_EVIDENCE_DIR")
	if dir == "" {
		dir = filepath.Join(".tmp", "evidence", "p07b3b1-postgres-binding-"+time.Now().UTC().Format("20060102T150405Z"))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(evidence, "", "  ")
	for _, secret := range secrets {
		if secret != "" && bytes.Contains(data, []byte(secret)) {
			t.Fatal("credential leaked into PostgreSQL binding evidence")
		}
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(dir, "postgres-application-binding.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("P07B3B1_POSTGRES_BINDING_EVIDENCE=%s", dir)
}
