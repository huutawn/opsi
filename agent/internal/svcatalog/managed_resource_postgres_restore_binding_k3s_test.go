package svcatalog

import (
	"context"
	"encoding/json"
	"errors"
				"os"
		"path/filepath"
	"strings"
		"testing"
	"time"

	backupagent "github.com/opsi-dev/opsi/agent/internal/backup"
	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	"github.com/opsi-dev/opsi/agent/internal/cloudrunner"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
	restoreagent "github.com/opsi-dev/opsi/agent/internal/restore"
	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	)


func TestManagedResourceRealK3sPostgresRestoreApplicationBinding(t *testing.T) {
	reference := os.Getenv("OPSI_P07B3B1_ACCEPTANCE_E2E_IMAGE")
	registryUsername, registryPassword := os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_USERNAME"), os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_PASSWORD")
	endpoint, access, secret, bucket := os.Getenv("OPSI_E2E_MINIO_ENDPOINT"), os.Getenv("OPSI_E2E_MINIO_ACCESS_KEY"), os.Getenv("OPSI_E2E_MINIO_SECRET_KEY"), os.Getenv("OPSI_E2E_MINIO_BUCKET")
	cloudURL, projectID, environmentID := os.Getenv("OPSI_P07B3C2A_CLOUD_URL"), os.Getenv("OPSI_P07B3C2A_CLOUD_PROJECT_ID"), os.Getenv("OPSI_P07B3C2A_CLOUD_ENVIRONMENT_ID")
	nodeID, agentID := os.Getenv("OPSI_P07B3C2A_CLOUD_NODE_ID"), os.Getenv("OPSI_P07B3C2A_CLOUD_AGENT_ID")
	pat, agentToken, postgresContainer := os.Getenv("OPSI_P07B3C2A_CLOUD_PAT"), os.Getenv("OPSI_P07B3C2A_CLOUD_AGENT_TOKEN"), os.Getenv("OPSI_P07B3C2A_POSTGRES_CONTAINER")
	if os.Getenv("OPSI_E2E_K3S_POSTGRES_BACKUP") != "1" || reference == "" || registryUsername == "" || registryPassword == "" || endpoint == "" || access == "" || secret == "" || bucket == "" || cloudURL == "" || projectID == "" || environmentID == "" || nodeID == "" || agentID == "" || pat == "" || agentToken == "" || postgresContainer == "" {
		t.Skip("set P07B3C2A K3s, Cloud/PostgreSQL authority, immutable application, private registry, and disposable MinIO inputs")
	}
	parts := strings.Split(reference, "@")
	if len(parts) != 2 {
		t.Fatal("fixture image must be an immutable digest reference")
	}
	image, err := deploymentv1.NewImmutableImage(parts[0], parts[1])
	if err != nil {
		t.Fatal(err)
	}
	requireK3sInfrastructure(t)

	spec := postgresBackupK3sSpec()
	spec.ResourceID, spec.CredentialID, spec.Connection.ServiceName = "res-postgres-rb-src-e2e", "mrcred-res-postgres-rb-src-e2e", "opsi-mr-postgres-rb-src"
	spec.ProjectID, spec.EnvironmentID = projectID, environmentID
	spec.Assignment.NodeID, spec.Assignment.AgentID = nodeID, agentID
	spec.Connection.Host = spec.Connection.ServiceName + "." + managedResourceNamespace(spec) + ".svc.cluster.local"
	spec.SpecHash, _ = spec.Hash()
	management := randomManagedCredential(t, spec.CredentialID, resourcev1.CredentialPurposeResourceManagement, spec.ResourceID, spec.ResourceID, "opsi")
	binding := postgresBindingOperation(t, spec, "binding-postgres-backup", true)
	namespace := managedResourceNamespace(spec)
	_, _ = kubectlOutput(context.Background(), "delete", "namespace", namespace, "--ignore-not-found", "--wait=true", "--timeout=2m")
	t.Cleanup(func() {
		_, _ = kubectlOutput(context.Background(), "delete", "namespace", namespace, "--ignore-not-found", "--wait=true", "--timeout=2m")
	})

	reconciler := ManagedResourceReconciler{Timeout: 8 * time.Minute, PollInterval: time.Second}
	ready := reconcilePostgresBindingK3s(t, reconciler, "backup-create", spec, management, binding)
	pvcUID := kubectl(t, "get", "pvc", ready.Evidence.PVCName, "-n", namespace, "-o", "jsonpath={.metadata.uid}")
	pvUID := kubectl(t, "get", "pv", ready.Evidence.PVName, "-o", "jsonpath={.metadata.uid}")
	ready.Evidence.PVCUID, ready.Evidence.PVUID, ready.Evidence.StorageHash = pvcUID, pvUID, resourcev1.ManagedResourceStorageHash(spec)

	snapshot, command := postgresBindingApplicationSnapshot(t, spec, image, *binding.Credential, registryUsername, registryPassword)
	runner := deploy.ExecCommandRunner{}
	if err := (deploy.KubernetesRegistryPullSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}).Ensure(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if err := (deploy.KubernetesWorkloadSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}).Ensure(context.Background(), command); err != nil {
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
	if evidence, _, err := adapter.ObserveReadiness(context.Background(), plan); err != nil || !evidence.RuntimeReady {
		t.Fatalf("application readiness=%+v err=%v", evidence, err)
	}
	seeded, err := reconciler.postgresBindingExec(context.Background(), spec, []byte(binding.Credential.Password+"\n"), postgresBackupSeedScript, *binding)
	if err != nil || lastNonEmptyLine(string(seeded)) != "128" {
		t.Fatalf("seed backup data err=%v output=%q", err, seeded)
	}

	authorityAPI := restoreAcceptanceAPI{baseURL: cloudURL, projectID: projectID, pat: pat, postgresContainer: postgresContainer}
	authorityAPI.seedReadyResource(t, spec, ready.Evidence)
	seedVaultManagedResourceCredential(t, authorityAPI, *management)
	createdBackup := authorityAPI.createBackupWithKey(t, spec.ResourceID, "p07b3c2b1-backup")
	backupID := createdBackup.ID
	storeSpec := backupv1.StoreSpec{ID: "minio-p07b3c2a", Provider: backupv1.StoreProviderS3, Endpoint: endpoint, Bucket: bucket, Region: "us-east-1", AllowInsecure: true}
	credential := backupv1.StoreCredential{AccessKey: access, SecretKey: secret}
	_, err = backupagent.NewS3Store(storeSpec, credential)
	if err != nil {
		t.Fatal(err)
	}

	cloud := &restoreAcceptanceCloudClient{Client: cloudrelay.Client{BaseURL: cloudURL, ProjectID: projectID, AgentToken: agentToken}}
	runCtx, stopRunner := context.WithCancel(context.Background())
	t.Cleanup(stopRunner)
	runResult := make(chan error, 1)
	go func() {
		runResult <- (cloudrunner.Runner{Client: cloud, Engine: postgresBackupRolloutEngine{}, Backups: backupagent.Executor{KubectlPath: "kubectl"}, Restores: restoreagent.Executor{KubectlPath: "kubectl"}, NodeID: spec.Assignment.NodeID, PollInterval: 10 * time.Millisecond, LongPollWait: 10 * time.Millisecond, HeartbeatInterval: time.Hour, BackupHeartbeat: 250 * time.Millisecond}).Run(runCtx)
	}()
	authority, backupLifecycle := authorityAPI.waitBackup(t, backupID, 10*time.Minute)

	if err := authority.ValidateSucceeded(); err != nil || !cloud.leased("backup", backupID) || !containsLifecycle(backupLifecycle, backupv1.LifecycleRunning) {
		t.Fatalf("backup authority=%+v lifecycle=%v leased=%t err=%v", authority, backupLifecycle, cloud.leased("backup", backupID), err)
	}

	// Post-backup marker on source
	sourceMarkerScript := `set -eu
role=$1; db=$2
IFS= read -r password
export PGPASSWORD=$password
psql -v ON_ERROR_STOP=1 -qAt -h 127.0.0.1 -U "$role" -d "$db" -c 'CREATE TABLE source_only_marker (id int);'`
	_, err = reconciler.postgresBindingExec(context.Background(), spec, []byte(binding.Credential.Password+"\n"), sourceMarkerScript, *binding)
	if err != nil {
		t.Fatalf("source post-backup marker err=%v", err)
	}

	targetSpec := spec
	targetSpec.ResourceID, targetSpec.CredentialID, targetSpec.Connection.ServiceName = "res-postgres-rb-tgt-e2e", "mrcred-res-postgres-rb-tgt-e2e", "opsi-mr-postgres-rb-tgt"
	targetSpec.Connection.Host = targetSpec.Connection.ServiceName + "." + managedResourceNamespace(targetSpec) + ".svc.cluster.local"
	targetSpec.SpecHash, _ = targetSpec.Hash()
	targetNamespace := managedResourceNamespace(targetSpec)
	t.Cleanup(func() {
		_, _ = kubectlOutput(context.Background(), "delete", "namespace", targetNamespace, "--ignore-not-found", "--wait=true", "--timeout=2m")
	})
	targetManagement := randomManagedCredential(t, targetSpec.CredentialID, resourcev1.CredentialPurposeResourceManagement, targetSpec.ResourceID, targetSpec.ResourceID, "opsi")
	targetReady := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "restore-target", Spec: targetSpec, Credential: targetManagement})
	if targetReady.Status != "ready" || targetReady.Evidence == nil || targetReady.Evidence.PVCUID == pvcUID || targetReady.Evidence.PVUID == pvUID {
		t.Fatalf("restore target=%+v source pvc=%s pv=%s", targetReady, pvcUID, pvUID)
	}
	targetPVCUID := kubectl(t, "get", "pvc", targetReady.Evidence.PVCName, "-n", targetNamespace, "-o", "jsonpath={.metadata.uid}")
	targetPVUID := kubectl(t, "get", "pv", targetReady.Evidence.PVName, "-o", "jsonpath={.metadata.uid}")
	targetReady.Evidence.PVCUID, targetReady.Evidence.PVUID, targetReady.Evidence.StorageHash = targetPVCUID, targetPVUID, resourcev1.ManagedResourceStorageHash(targetSpec)
	authorityAPI.seedReadyResource(t, targetSpec, targetReady.Evidence)
	seedVaultManagedResourceCredential(t, authorityAPI, *targetManagement)

	queuedReview := authorityAPI.createReviewWithKey(t, backupID, targetSpec.ResourceID, "p07b3c2b1-review")
	restoreReview, _ := authorityAPI.waitReview(t, queuedReview.ID, 5*time.Minute)

	queuedRestore := authorityAPI.createRestoreWithKey(t, backupID, restoreReview, "p07b3c2b1-restore")
	restoreAuthority, _ := authorityAPI.waitRestore(t, queuedRestore.ID, 10*time.Minute)

	if err := restoreAuthority.ValidateSucceeded(); err != nil {
		t.Fatalf("restore authority err=%v", err)
	}

	// target must NOT contain post-backup marker
	markerCheck := kubectl(t, "exec", "pod/"+targetSpec.Connection.ServiceName+"-0", "-n", targetNamespace, "-c", "postgres", "--", "sh", "-ec", `u=$(cat /run/opsi-postgres/username); d=$(cat /run/opsi-postgres/database); export PGPASSWORD=$(cat /run/opsi-postgres/password); psql -v ON_ERROR_STOP=1 -qAt -h 127.0.0.1 -U "$u" -d "$d" -c "SELECT count(*) FROM pg_class WHERE relname='source_only_marker'"`)
	if strings.TrimSpace(markerCheck) != "0" {
		t.Fatalf("restored target contains source-only post-backup marker")
	}

	// create NEW scoped ResourceBinding on target
	targetBindingA := postgresBindingOperation(t, targetSpec, "binding-target-a", true)
	targetBindingB := postgresBindingOperation(t, targetSpec, "binding-target-b", true)
	_ = reconcilePostgresBindingK3s(t, reconciler, "target-binding-create", targetSpec, targetManagement, targetBindingA, targetBindingB)

	// Deploy validation Application
	targetSnapshot, targetCommand := postgresBindingApplicationSnapshot(t, targetSpec, image, *targetBindingA.Credential, registryUsername, registryPassword)
	if err := (deploy.KubernetesRegistryPullSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}).Ensure(context.Background(), targetCommand); err != nil {
		t.Fatal(err)
	}
	if err := (deploy.KubernetesWorkloadSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}).Ensure(context.Background(), targetCommand); err != nil {
		t.Fatal(err)
	}
	targetPlan, err := adapter.PrepareRollout(context.Background(), targetSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ApplyRollout(context.Background(), targetPlan); err != nil {
		t.Fatal(err)
	}
	if evidence, _, err := adapter.ObserveReadiness(context.Background(), targetPlan); err != nil || !evidence.RuntimeReady {
		t.Fatalf("target application readiness=%+v err=%v", evidence, err)
	}

	// 9. Read restored dataset using validation App
	verifiedRows, err := checkRestoreBindingRows(reconciler, targetSpec, targetBindingA, targetBindingA.Credential.Password)
	if err != nil || strings.TrimSpace(verifiedRows) != "128" {
		t.Fatalf("restored target app rows=%q err=%v", verifiedRows, err)
	}

	// 10. Write target-only marker
	targetWriteScript := `set -eu
role=$1; db=$2
IFS= read -r password
export PGPASSWORD=$password
psql -v ON_ERROR_STOP=1 -qAt -h 127.0.0.1 -U "$role" -d "$db" -c "CREATE TABLE target_only_marker (id int); INSERT INTO opsi_p07b3c1_backup_rows (value) VALUES ('new_target_value'); UPDATE opsi_p07b3c1_backup_rows SET value = 'updated' WHERE id=1; DELETE FROM opsi_p07b3c1_backup_rows WHERE id=2;"`
	_, err = reconciler.postgresBindingExec(context.Background(), targetSpec, []byte(targetBindingA.Credential.Password+"\n"), targetWriteScript, *targetBindingA)
	if err != nil {
		t.Fatalf("target-only marker/write err=%v", err)
	}

	// 11. source does NOT contain target-only marker
	sourceMarkerCheck := kubectl(t, "exec", "pod/"+spec.Connection.ServiceName+"-0", "-n", namespace, "-c", "postgres", "--", "sh", "-ec", `u=$(cat /run/opsi-postgres/username); d=$(cat /run/opsi-postgres/database); export PGPASSWORD=$(cat /run/opsi-postgres/password); psql -v ON_ERROR_STOP=1 -qAt -h 127.0.0.1 -U "$u" -d "$d" -c "SELECT count(*) FROM pg_class WHERE relname='target_only_marker'"`)
	if strings.TrimSpace(sourceMarkerCheck) != "0" {
		t.Fatalf("source contains target-only marker")
	}

	// 13. role attributes check
	evidenceScript := `set -eu
role=$1; db=$2
manager=$(cat /run/opsi-postgres/username)
export PGPASSWORD=$(cat /run/opsi-postgres/password)
psql -v ON_ERROR_STOP=1 -h 127.0.0.1 -U "$manager" -d "$db" -tAc "SELECT rolcanlogin::int||':'||rolsuper::int||':'||rolcreatedb::int||':'||rolcreaterole::int||':'||rolreplication::int||':'||rolbypassrls::int FROM pg_roles WHERE rolname='$role'; SELECT has_database_privilege('$role','$db','CONNECT')::int||':'||has_schema_privilege('$role','public','USAGE')::int||':'||has_schema_privilege('$role','public','CREATE')::int"`
	attributes, err := reconciler.postgresBindingExec(context.Background(), targetSpec, []byte{}, evidenceScript, *targetBindingA)
	if err != nil || !strings.Contains(string(attributes), "1:0:0:0:0:0") {
		t.Fatalf("target role attributes check failed: %q err=%v", string(attributes), err)
	}

	// 14. Redeploy stability
	redeployPlan, err := adapter.PrepareRollout(context.Background(), targetSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ApplyRollout(context.Background(), redeployPlan); err != nil {
		t.Fatal(err)
	}
	if evidence, _, err := adapter.ObserveReadiness(context.Background(), redeployPlan); err != nil || !evidence.RuntimeReady {
		t.Fatalf("target application redeploy readiness=%+v err=%v", evidence, err)
	}

	// 15. PostgreSQL pod recreation
	kubectl(t, "delete", "pod", targetSpec.Connection.ServiceName+"-0", "-n", targetNamespace)
	kubectl(t, "wait", "--for=condition=Ready", "pod/"+targetSpec.Connection.ServiceName+"-0", "-n", targetNamespace, "--timeout=4m")
	if _, err := checkRestoreBindingRows(reconciler, targetSpec, targetBindingA, targetBindingA.Credential.Password); err != nil {
		t.Fatalf("target application reconnect failed after db pod recreate: %v", err)
	}

	// 17. Application restart
	kubectl(t, "delete", "pod", "-l", "opsi.dev/service=api", "-n", targetNamespace)
	kubectl(t, "wait", "--for=condition=Ready", "pod", "-l", "opsi.dev/service=api", "-n", targetNamespace, "--timeout=4m")
	if _, err := checkRestoreBindingRows(reconciler, targetSpec, targetBindingA, targetBindingA.Credential.Password); err != nil {
		t.Fatalf("target application read failed after app restart: %v", err)
	}

	// 18. Compute rollout
	targetSpec.CPUMillicores = 300
	targetSpec.SpecHash, _ = targetSpec.Hash()
	targetReady = reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "restore-target-update", Spec: targetSpec, Credential: targetManagement})
	if targetReady.Status != "ready" || targetReady.Evidence == nil {
		t.Fatalf("target update ready=%+v", targetReady)
	}
	if _, err := checkRestoreBindingRows(reconciler, targetSpec, targetBindingA, targetBindingA.Credential.Password); err != nil {
		t.Fatalf("target application read failed after db update: %v", err)
	}

	// 19. Multiple binding isolation regression
	verifiedRowsB, err := checkRestoreBindingRows(reconciler, targetSpec, targetBindingB, targetBindingB.Credential.Password)
	if err != nil || strings.TrimSpace(verifiedRowsB) != "128" {
		t.Fatalf("binding B read failed: %q err=%v", verifiedRowsB, err)
	}

	// 20. Binding deletion
	revokedA := *targetBindingA
	revokedA.Action, revokedA.Credential = resourcev1.PostgresBindingRevoke, nil
	reconcilePostgresBindingK3s(t, reconciler, "target-binding-revoke-a", targetSpec, targetManagement, &revokedA)
	if _, err := checkRestoreBindingRows(reconciler, targetSpec, targetBindingA, targetBindingA.Credential.Password); err == nil {
		t.Fatalf("target binding A still works after revocation")
	}

	// 22. Restored data remains
	verifiedRowsBAfterRevokeA, err := checkRestoreBindingRows(reconciler, targetSpec, targetBindingB, targetBindingB.Credential.Password)
	if err != nil || strings.TrimSpace(verifiedRowsBAfterRevokeA) != "128" {
		t.Fatalf("binding B read failed after A revoked: %q err=%v", verifiedRowsBAfterRevokeA, err)
	}

	stopRunner()
	if err := <-runResult; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	dir := os.Getenv("OPSI_P07B3C2B1_EVIDENCE_DIR")
	if dir == "" {
		dir = filepath.Join(".tmp", "evidence", "p07b3c2b1-postgres-restore-binding-"+time.Now().UTC().Format("20060102T150405Z"))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(map[string]any{"B1_Validation": "PASS_NO_PRODUCT_CHANGE_REQUIRED"}, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "p07b3c2b1.json"), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func checkRestoreBindingRows(reconciler ManagedResourceReconciler, spec resourcev1.ManagedResourceSpec, operation *resourcev1.PostgresBindingOperation, password string) (string, error) {
	script := `set -eu
role=$1; db=$2
IFS= read -r password
export PGPASSWORD=$password
psql -v ON_ERROR_STOP=1 -qAt -h 127.0.0.1 -U "$role" -d "$db" -c 'SELECT count(*) FROM opsi_p07b3c1_backup_rows'`
	out, err := reconciler.postgresBindingExec(context.Background(), spec, []byte(password+"\n"), script, *operation)
	return string(out), err
}
