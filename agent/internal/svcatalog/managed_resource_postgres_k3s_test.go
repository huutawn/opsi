package svcatalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

func TestManagedResourceRealK3sPostgresPersistence(t *testing.T) {
	if os.Getenv("OPSI_E2E_K3S_POSTGRES") != "1" {
		t.Skip("set OPSI_E2E_K3S_POSTGRES=1 with KUBECONFIG pointing to a disposable K3s cluster")
	}
	requireK3sInfrastructure(t)
	spec := resourcev1.ManagedResourceSpec{
		SchemaVersion: resourcev1.ManagedResourceSpecSchemaVersion, ResourceID: "res-postgres-e2e", ProjectID: "project-postgres-e2e", EnvironmentID: "env-postgres-e2e",
		ResourceType: resourcev1.TypePostgres, Profile: "single-node-experimental", Version: resourcev1.PostgresVersion, Image: resourcev1.PostgresImage,
		CredentialID: "mrcred-res-postgres-e2e", Assignment: resourcev1.ManagedResourceAssignment{RuntimeID: "runtime-e2e", NodeID: "node-e2e", AgentID: "agent-e2e"},
		Replicas: 1, CPUMillicores: 250, MemoryBytes: 256 << 20, Ports: []resourcev1.ManagedResourcePort{{Name: "postgres", Port: 5432, Protocol: resourcev1.ProtocolPostgres}},
		Storage:           resourcev1.StorageRequest{Persistent: true, SizeBytes: 1 << 30, PolicyRef: resourcev1.StoragePolicyDefault},
		Connection:        resourcev1.ManagedResourceConnection{ServiceName: "opsi-mr-postgres-e2e", Host: "opsi-mr-postgres-e2e." + managedResourceNamespace(resourcev1.ManagedResourceSpec{ProjectID: "project-postgres-e2e", EnvironmentID: "env-postgres-e2e"}) + ".svc.cluster.local", Port: 5432, Protocol: resourcev1.ProtocolPostgres, Database: "opsi"},
		ConfigurationHash: strings.Repeat("a", 64), TopologyRevision: 1, TopologyHash: strings.Repeat("b", 64),
	}
	spec.SpecHash, _ = spec.Hash()
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	credential := &resourcev1.ManagedResourceCredential{CredentialID: spec.CredentialID, Username: "opsi", Password: "p07b3a-postgres-e2e-secret", Database: "opsi"}
	namespace := managedResourceNamespace(spec)
	_, _ = kubectlOutput(context.Background(), "delete", "namespace", namespace, "--ignore-not-found", "--wait=true", "--timeout=2m")
	defer kubectlOutput(context.Background(), "delete", "namespace", namespace, "--ignore-not-found", "--wait=true", "--timeout=2m")

	reconciler := ManagedResourceReconciler{Timeout: 8 * time.Minute, PollInterval: time.Second}
	apply := func(token string, value resourcev1.ManagedResourceSpec, current ManagedResourceReconciler) cloudrelay.ManagedResourceResult {
		return current.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: token, Spec: value, Credential: credential})
	}
	first := apply("lease-create", spec, reconciler)
	if first.Status != "ready" || first.Evidence == nil || !first.Evidence.AuthReady || !first.Evidence.StorageReady || !first.Evidence.VolumeMounted || first.Evidence.Image != spec.Image {
		t.Fatalf("first reconcile=%+v", first)
	}
	pvcUID := kubectl(t, "get", "pvc", first.Evidence.PVCName, "-n", namespace, "-o", "jsonpath={.metadata.uid}")
	podUIDBefore := kubectl(t, "get", "pod", spec.Connection.ServiceName+"-0", "-n", namespace, "-o", "jsonpath={.metadata.uid}")
	secretHash := postgresSecretDataHash(t, namespace, managedResourceSecretName(spec))
	invalidOutput, invalidErr := runPostgresClient(t, spec, false, "SELECT 1")
	if invalidErr == nil || !strings.Contains(invalidOutput, "password authentication failed") {
		t.Fatalf("invalid auth unexpectedly succeeded: err=%v output=%q", invalidErr, invalidOutput)
	}
	selectOne, err := runPostgresClient(t, spec, true, "SELECT 1")
	if err != nil || strings.TrimSpace(selectOne) != "1" {
		t.Fatalf("SELECT 1 err=%v output=%q", err, selectOne)
	}
	writeSQL := `CREATE TABLE IF NOT EXISTS opsi_p07b3_acceptance (id text PRIMARY KEY, value text NOT NULL); INSERT INTO opsi_p07b3_acceptance(id,value) VALUES ('p07b3a','p07b3a-persisted') ON CONFLICT (id) DO UPDATE SET value=EXCLUDED.value; SELECT value FROM opsi_p07b3_acceptance WHERE id='p07b3a'`
	row, err := runPostgresClient(t, spec, true, writeSQL)
	if err != nil || lastNonEmptyLine(row) != "p07b3a-persisted" {
		t.Fatalf("write/read err=%v output=%q", err, row)
	}

	kubectl(t, "delete", "pod", spec.Connection.ServiceName+"-0", "-n", namespace, "--wait=true", "--timeout=2m")
	recreated := apply("lease-pod-recreate", spec, reconciler)
	if recreated.Status != "ready" {
		t.Fatalf("pod recreation reconcile=%+v", recreated)
	}
	podUIDAfter := kubectl(t, "get", "pod", spec.Connection.ServiceName+"-0", "-n", namespace, "-o", "jsonpath={.metadata.uid}")
	if podUIDBefore == podUIDAfter || pvcUID != kubectl(t, "get", "pvc", recreated.Evidence.PVCName, "-n", namespace, "-o", "jsonpath={.metadata.uid}") || recreated.Evidence.PVName != first.Evidence.PVName {
		t.Fatalf("recreation before=%s after=%s first=%+v recreated=%+v", podUIDBefore, podUIDAfter, first.Evidence, recreated.Evidence)
	}
	assertPostgresAcceptanceRow(t, spec)

	for index := range 3 {
		if replay := apply(fmt.Sprintf("lease-replay-%d", index), spec, reconciler); replay.Status != "ready" {
			t.Fatalf("replay %d=%+v", index, replay)
		}
	}
	assertPostgresObjectCounts(t, spec, 1, 1)
	if postgresSecretDataHash(t, namespace, managedResourceSecretName(spec)) != secretHash {
		t.Fatal("credential Secret changed during same-spec reconciliation")
	}

	restarted := apply("lease-agent-restart", spec, ManagedResourceReconciler{Timeout: 8 * time.Minute, PollInterval: time.Second})
	if restarted.Status != "ready" || restarted.Evidence.PVCName != first.Evidence.PVCName || restarted.Evidence.PVName != first.Evidence.PVName || postgresSecretDataHash(t, namespace, managedResourceSecretName(spec)) != secretHash {
		t.Fatalf("agent restart=%+v", restarted)
	}
	assertPostgresAcceptanceRow(t, spec)

	updatedSpec := spec
	updatedSpec.CPUMillicores = 300
	updatedSpec.SpecHash, _ = updatedSpec.Hash()
	updated := apply("lease-compute-update", updatedSpec, reconciler)
	if updated.Status != "ready" || updated.Evidence.PVCName != first.Evidence.PVCName || updated.Evidence.PVName != first.Evidence.PVName || kubectl(t, "get", "pvc", updated.Evidence.PVCName, "-n", namespace, "-o", "jsonpath={.metadata.uid}") != pvcUID || postgresSecretDataHash(t, namespace, managedResourceSecretName(spec)) != secretHash {
		t.Fatalf("compute update=%+v", updated)
	}
	assertPostgresAcceptanceRow(t, updatedSpec)

	deleted := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "delete", LeaseToken: "lease-delete", Spec: updatedSpec})
	if deleted.Status != "deleted" || deleted.Evidence == nil || !deleted.Evidence.StorageRetained || deleted.Evidence.PVCName != first.Evidence.PVCName || deleted.Evidence.PVCUID != pvcUID || deleted.Evidence.PVUID == "" || deleted.Evidence.ReclaimPolicy == "" || kubectl(t, "get", "pvc", first.Evidence.PVCName, "-n", namespace, "-o", "jsonpath={.metadata.uid}") != pvcUID {
		t.Fatalf("safe delete=%+v", deleted)
	}
	assertPostgresObjectCounts(t, updatedSpec, 0, 1)

	unrelatedPVC, unrelatedUID, unrelatedPod := createUnrelatedPVC(t, namespace, deleted.Evidence.StorageClass, updatedSpec.Image)
	unrelatedPV := kubectl(t, "get", "pvc", unrelatedPVC, "-n", namespace, "-o", "jsonpath={.spec.volumeName}")
	unrelatedPVUID := kubectl(t, "get", "pv", unrelatedPV, "-o", "jsonpath={.metadata.uid}")
	unrelatedReclaim := kubectl(t, "get", "pv", unrelatedPV, "-o", "jsonpath={.spec.persistentVolumeReclaimPolicy}")
	mismatchSpec := resourcev1.RetainedStorageDestroySpec{
		SchemaVersion: resourcev1.RetainedStorageSchemaVersion, RetainedStorageID: "rsto-unrelated", OriginalResourceID: updatedSpec.ResourceID,
		ProjectID: updatedSpec.ProjectID, EnvironmentID: updatedSpec.EnvironmentID, ResourceType: updatedSpec.ResourceType, Namespace: namespace,
		PVCName: unrelatedPVC, PVCUID: unrelatedUID, PVName: unrelatedPV, PVUID: unrelatedPVUID, StorageClass: deleted.Evidence.StorageClass,
		ReclaimPolicy: unrelatedReclaim, StorageHash: deleted.Evidence.StorageHash, Assignment: updatedSpec.Assignment, Revision: 2, Operation: "destroy",
	}
	mismatch := reconciler.ReconcileRetainedStorage(context.Background(), cloudrelay.RetainedStorageLease{LeaseToken: "ownership-mismatch", Spec: mismatchSpec})
	if mismatch.Status != "failed" || mismatch.FailureCode != resourcev1.FailureRetainedStorageOwnership || kubectl(t, "get", "pvc", unrelatedPVC, "-n", namespace, "-o", "jsonpath={.metadata.uid}") != unrelatedUID {
		t.Fatalf("unrelated PVC safety result=%+v", mismatch)
	}
	kubectl(t, "delete", "pod", unrelatedPod, "-n", namespace, "--wait=true", "--timeout=2m")
	kubectl(t, "delete", "pvc", unrelatedPVC, "-n", namespace, "--wait=true", "--timeout=2m")

	destroySpec := resourcev1.RetainedStorageDestroySpec{
		SchemaVersion: resourcev1.RetainedStorageSchemaVersion, RetainedStorageID: "rsto-postgres-e2e", OriginalResourceID: updatedSpec.ResourceID,
		ProjectID: updatedSpec.ProjectID, EnvironmentID: updatedSpec.EnvironmentID, ResourceType: updatedSpec.ResourceType, Namespace: deleted.Evidence.Namespace,
		PVCName: deleted.Evidence.PVCName, PVCUID: deleted.Evidence.PVCUID, PVName: deleted.Evidence.PVName, PVUID: deleted.Evidence.PVUID,
		StorageClass: deleted.Evidence.StorageClass, ReclaimPolicy: deleted.Evidence.ReclaimPolicy, StorageHash: deleted.Evidence.StorageHash,
		Assignment: updatedSpec.Assignment, Revision: 2, Operation: "destroy",
	}
	if destroySpec.ReclaimPolicy != "Delete" {
		t.Fatalf("local-path factual reclaim policy=%s; explicit destruction is unsupported", destroySpec.ReclaimPolicy)
	}
	destroyed := reconciler.ReconcileRetainedStorage(context.Background(), cloudrelay.RetainedStorageLease{LeaseToken: "destroy-storage", Spec: destroySpec})
	if destroyed.Status != "destroyed" || destroyed.Evidence == nil || !destroyed.Evidence.PVCAbsent || !destroyed.Evidence.PVAbsent {
		t.Fatalf("destroyed=%+v", destroyed)
	}
	if pvc, _ := kubectlOutput(context.Background(), "get", "pvc", destroySpec.PVCName, "-n", namespace, "-o", "name", "--ignore-not-found"); strings.TrimSpace(pvc) != "" {
		t.Fatalf("PVC remains after destruction: %s", pvc)
	}
	if pv, _ := kubectlOutput(context.Background(), "get", "pv", destroySpec.PVName, "-o", "name", "--ignore-not-found"); strings.TrimSpace(pv) != "" {
		t.Fatalf("PV remains after destruction: %s", pv)
	}
	if recovered := (ManagedResourceReconciler{Timeout: 8 * time.Minute, PollInterval: time.Second}).ReconcileRetainedStorage(context.Background(), cloudrelay.RetainedStorageLease{LeaseToken: "destroy-recovery", Spec: destroySpec}); recovered.Status != "destroyed" || recovered.Evidence == nil || !recovered.Evidence.PVCAbsent || !recovered.Evidence.PVAbsent {
		t.Fatalf("destroy recovery=%+v", recovered)
	}
	assertPostgresObjectCounts(t, updatedSpec, 0, 0)

	evidence := map[string]any{
		"resource_id": spec.ResourceID, "lifecycle": []string{"unplaced", "planned", "provisioning", "ready", "deleting", "deleted-runtime-storage-retained"},
		"profile": spec.Profile, "version": resourcev1.PostgresVersion, "image": resourcev1.PostgresImage, "namespace": namespace, "statefulset": spec.Connection.ServiceName,
		"service": spec.Connection.ServiceName, "pvc": first.Evidence.PVCName, "pvc_uid": deleted.Evidence.PVCUID, "pv": first.Evidence.PVName, "pv_uid": deleted.Evidence.PVUID, "storage_class": first.Evidence.StorageClass, "reclaim_policy": deleted.Evidence.ReclaimPolicy,
		"requested_bytes": spec.Storage.SizeBytes, "actual_storage": first.Evidence.ActualStorage, "image_id": first.Evidence.ImageID,
		"secret": managedResourceSecretName(spec), "authenticated_readiness": first.Evidence.AuthReady, "invalid_auth": "rejected", "select_1": strings.TrimSpace(selectOne),
		"acceptance_row": lastNonEmptyLine(row), "pod_uid_before": podUIDBefore, "pod_uid_after": podUIDAfter, "pvc_uid_before": pvcUID, "pvc_uid_after": pvcUID,
		"persistence": "PASS", "idempotency": "PASS", "credential_stable": "PASS", "agent_recovery": "PASS", "compute_update": "PASS", "storage_resize": "unsupported", "delete_behavior": "runtime_deleted_pvc_retained", "ownership_mismatch": "REJECTED_UNRELATED_PVC_SURVIVED", "destroy_lifecycle": "destroying_to_destroyed", "pvc_absent": destroyed.Evidence.PVCAbsent, "pv_absent": destroyed.Evidence.PVAbsent, "destroy_recovery": "PASS",
		"spec_hash": spec.SpecHash, "storage_hash": resourcev1.ManagedResourceStorageHash(spec), "storage": spec.Storage,
	}
	writePostgresEvidence(t, credential.Password, evidence)
}

func createUnrelatedPVC(t *testing.T, namespace, storageClass, image string) (string, string, string) {
	t.Helper()
	name := fmt.Sprintf("unrelated-pvc-%d", time.Now().UnixNano())
	pvc := map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolumeClaim",
		"metadata": map[string]any{"name": name, "namespace": namespace, "labels": map[string]any{"app.kubernetes.io/managed-by": "unrelated"}},
		"spec":     map[string]any{"accessModes": []any{"ReadWriteOnce"}, "storageClassName": storageClass, "resources": map[string]any{"requests": map[string]any{"storage": "16Mi"}}},
	}
	data, _ := json.Marshal(pvc)
	if out, err := kubectlInput(context.Background(), data, "create", "-f", "-"); err != nil {
		t.Fatalf("create unrelated PVC: %v\n%s", err, out)
	}
	podName := name + "-consumer"
	pod := map[string]any{
		"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": podName, "namespace": namespace},
		"spec": map[string]any{"restartPolicy": "Never", "containers": []any{map[string]any{"name": "consumer", "image": image, "command": []any{"sh", "-ec", "sleep 600"}, "volumeMounts": []any{map[string]any{"name": "data", "mountPath": "/data"}}}}, "volumes": []any{map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": name}}}},
	}
	data, _ = json.Marshal(pod)
	if out, err := kubectlInput(context.Background(), data, "create", "-f", "-"); err != nil {
		t.Fatalf("create unrelated PVC consumer: %v\n%s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if out, err := kubectlOutput(ctx, "wait", "--for=jsonpath={.status.phase}=Bound", "pvc/"+name, "-n", namespace, "--timeout=2m"); err != nil {
		t.Fatalf("wait unrelated PVC: %v\n%s", err, out)
	}
	return name, kubectl(t, "get", "pvc", name, "-n", namespace, "-o", "jsonpath={.metadata.uid}"), podName
}

func runPostgresClient(t *testing.T, spec resourcev1.ManagedResourceSpec, valid bool, sql string) (string, error) {
	t.Helper()
	name := fmt.Sprintf("postgres-client-%d", time.Now().UnixNano())
	password := map[string]any{"name": "PGPASSWORD", "value": "invalid-password"}
	if valid {
		password = map[string]any{"name": "PGPASSWORD", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": managedResourceSecretName(spec), "key": "password"}}}
	}
	pod := map[string]any{
		"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": name, "namespace": managedResourceNamespace(spec)},
		"spec": map[string]any{"restartPolicy": "Never", "containers": []any{map[string]any{
			"name": "client", "image": spec.Image, "command": []any{"sh", "-ec", `psql -v ON_ERROR_STOP=1 -tAc "$SQL"`},
			"env": []any{
				map[string]any{"name": "PGHOST", "value": spec.Connection.Host},
				map[string]any{"name": "PGUSER", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": managedResourceSecretName(spec), "key": "username"}}},
				password,
				map[string]any{"name": "PGDATABASE", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": managedResourceSecretName(spec), "key": "database"}}},
				map[string]any{"name": "PGSSLMODE", "value": "disable"},
				map[string]any{"name": "SQL", "value": sql},
			},
		}}},
	}
	data, _ := json.Marshal(pod)
	if out, err := kubectlInput(context.Background(), data, "create", "-f", "-"); err != nil {
		t.Fatalf("create PostgreSQL client: %v\n%s", err, out)
	}
	defer kubectlOutput(context.Background(), "delete", "pod", name, "-n", managedResourceNamespace(spec), "--ignore-not-found", "--wait=true", "--timeout=2m")
	phase := "Failed"
	if valid {
		phase = "Succeeded"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	waitOutput, waitErr := kubectlOutput(ctx, "wait", "--for=jsonpath={.status.phase}="+phase, "pod/"+name, "-n", managedResourceNamespace(spec), "--timeout=4m")
	logs, logsErr := kubectlOutput(context.Background(), "logs", "pod/"+name, "-n", managedResourceNamespace(spec), "-c", "client")
	if logsErr != nil {
		return logs, logsErr
	}
	if waitErr != nil {
		return logs + waitOutput, waitErr
	}
	if valid {
		return logs, nil
	}
	return logs, errors.New("invalid PostgreSQL credential rejected")
}

func assertPostgresAcceptanceRow(t *testing.T, spec resourcev1.ManagedResourceSpec) {
	t.Helper()
	row, err := runPostgresClient(t, spec, true, `SELECT value FROM opsi_p07b3_acceptance WHERE id='p07b3a'`)
	if err != nil || strings.TrimSpace(row) != "p07b3a-persisted" {
		t.Fatalf("persisted row err=%v output=%q", err, row)
	}
}

func lastNonEmptyLine(value string) string {
	lines := strings.FieldsFunc(value, func(r rune) bool { return r == '\r' || r == '\n' })
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[len(lines)-1])
}

func assertPostgresObjectCounts(t *testing.T, spec resourcev1.ManagedResourceSpec, runtimeCount, pvcCount int) {
	t.Helper()
	namespace := managedResourceNamespace(spec)
	selector := selectorString(managedResourceOwnershipLabels(spec))
	for _, kind := range []string{"statefulset", "service", "secret"} {
		out := kubectl(t, "get", kind, "-n", namespace, "-l", selector, "-o", "jsonpath={.items[*].metadata.name}")
		count := len(strings.Fields(out))
		if count != runtimeCount {
			t.Fatalf("%s count=%d want=%d names=%q", kind, count, runtimeCount, out)
		}
	}
	out := kubectl(t, "get", "pvc", "-n", namespace, "-l", selector, "-o", "jsonpath={.items[*].metadata.name}")
	if count := len(strings.Fields(out)); count != pvcCount {
		t.Fatalf("PVC count=%d want=%d names=%q", count, pvcCount, out)
	}
}

func postgresSecretDataHash(t *testing.T, namespace, name string) string {
	t.Helper()
	raw := kubectl(t, "get", "secret", name, "-n", namespace, "-o", "json")
	var object map[string]any
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(object["data"])
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func kubectlInput(ctx context.Context, input []byte, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "kubectl", args...)
	command.Stdin = bytes.NewReader(input)
	out, err := command.CombinedOutput()
	return string(out), err
}

func writePostgresEvidence(t *testing.T, password string, evidence map[string]any) {
	t.Helper()
	dir := os.Getenv("OPSI_K3S_EVIDENCE_DIR")
	if dir == "" {
		dir = filepath.Join(".tmp", "evidence", "p07b3a-postgres-"+time.Now().UTC().Format("20060102T150405Z"))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(evidence, "", "  ")
	if bytes.Contains(data, []byte(password)) {
		t.Fatal("credential leaked into PostgreSQL evidence")
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(dir, "postgres-persistence.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("P07B3A_POSTGRES_EVIDENCE=%s", dir)
}
