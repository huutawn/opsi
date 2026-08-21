package svcatalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	backupagent "github.com/opsi-dev/opsi/agent/internal/backup"
	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	"github.com/opsi-dev/opsi/agent/internal/cloudrunner"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
	restoreagent "github.com/opsi-dev/opsi/agent/internal/restore"
	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	cutoverv1 "github.com/opsi-dev/opsi/contracts/go/cutoverv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	restorev1 "github.com/opsi-dev/opsi/contracts/go/restorev1"
)

const postgresBackupSeedScript = `set -eu
role=$1; db=$2
IFS= read -r password
export PGPASSWORD=$password
psql -v ON_ERROR_STOP=1 -qAt -h 127.0.0.1 -U "$role" -d "$db" <<'SQL'
CREATE SEQUENCE IF NOT EXISTS opsi_p07b3c1_sequence;
CREATE TABLE IF NOT EXISTS opsi_p07b3c1_backup_rows (id bigint PRIMARY KEY DEFAULT nextval('opsi_p07b3c1_sequence'), value text NOT NULL);
CREATE INDEX IF NOT EXISTS opsi_p07b3c1_backup_rows_value_idx ON opsi_p07b3c1_backup_rows(value);
TRUNCATE opsi_p07b3c1_backup_rows RESTART IDENTITY;
INSERT INTO opsi_p07b3c1_backup_rows(value) SELECT 'application-row-' || value FROM generate_series(1,128) AS value;
SELECT count(*) FROM opsi_p07b3c1_backup_rows;
SQL`

func TestManagedResourceRealK3sPostgresLogicalBackup(t *testing.T) {
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
	createdBackup := authorityAPI.createBackup(t, spec.ResourceID)
	backupID, objectKey := createdBackup.ID, createdBackup.ObjectKey
	storeSpec := backupv1.StoreSpec{ID: "minio-p07b3c2a", Provider: backupv1.StoreProviderS3, Endpoint: endpoint, Bucket: bucket, Region: "us-east-1", AllowInsecure: true}
	credential := backupv1.StoreCredential{AccessKey: access, SecretKey: secret}
	store, err := backupagent.NewS3Store(storeSpec, credential)
	if err != nil {
		t.Fatal(err)
	}
	partial := []byte("incomplete previous attempt")
	partialSum := sha256.Sum256(partial)
	if _, err := store.Put(context.Background(), objectKey, bytes.NewReader(partial), int64(len(partial)), hex.EncodeToString(partialSum[:]), backupID); err != nil {
		t.Fatal(err)
	}
	trafficStarted := make(chan struct{})
	trafficDone := make(chan struct {
		count int
		err   error
	}, 1)
	trafficCtx, stopTraffic := context.WithCancel(context.Background())
	go func() {
		count := 0
		for {
			output, err := runBindingLogin(reconciler, spec, binding, binding.Credential.Password)
			if err != nil || strings.TrimSpace(output) != "1" {
				trafficDone <- struct {
					count int
					err   error
				}{count, fmt.Errorf("application credential query failed: %w output=%q", err, output)}
				return
			}
			count++
			if count == 1 {
				close(trafficStarted)
			}
			select {
			case <-trafficCtx.Done():
				trafficDone <- struct {
					count int
					err   error
				}{count, nil}
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
	select {
	case <-trafficStarted:
	case outcome := <-trafficDone:
		t.Fatal(outcome.err)
	case <-time.After(2 * time.Minute):
		t.Fatal("application traffic did not start")
	}
	cloud := &restoreAcceptanceCloudClient{Client: cloudrelay.Client{BaseURL: cloudURL, ProjectID: projectID, AgentToken: agentToken}}
	runCtx, stopRunner := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() {
		runResult <- (cloudrunner.Runner{Client: cloud, Engine: postgresBackupRolloutEngine{}, Backups: backupagent.Executor{KubectlPath: "kubectl"}, Restores: restoreagent.Executor{KubectlPath: "kubectl"}, NodeID: spec.Assignment.NodeID, PollInterval: 10 * time.Millisecond, LongPollWait: 10 * time.Millisecond, HeartbeatInterval: time.Hour, BackupHeartbeat: 250 * time.Millisecond}).Run(runCtx)
	}()
	authority, backupLifecycle := authorityAPI.waitBackup(t, backupID, 10*time.Minute)
	stopTraffic()
	traffic := <-trafficDone
	if traffic.err != nil || traffic.count == 0 {
		t.Fatalf("application traffic count=%d err=%v", traffic.count, traffic.err)
	}
	if err := authority.ValidateSucceeded(); err != nil || !cloud.leased("backup", backupID) || !containsLifecycle(backupLifecycle, backupv1.LifecycleRunning) {
		t.Fatalf("backup authority=%+v lifecycle=%v leased=%t err=%v", authority, backupLifecycle, cloud.leased("backup", backupID), err)
	}
	if kubectl(t, "get", "pvc", ready.Evidence.PVCName, "-n", namespace, "-o", "jsonpath={.metadata.uid}") != pvcUID || kubectl(t, "get", "pv", ready.Evidence.PVName, "-o", "jsonpath={.metadata.uid}") != pvUID {
		t.Fatal("backup changed source PVC/PV identity")
	}
	assertPostgresApplicationReady(t, adapter, snapshot)
	// Same-resource review is rejected while the source authority still exists.
	if status, body := authorityAPI.requestStatus(t, http.MethodPost, "/api/projects/"+url.PathEscape(projectID)+"/backups/"+url.PathEscape(backupID)+"/restore-review", "p07b3c2a-same-resource", map[string]string{"target_resource_id": spec.ResourceID}); status != http.StatusConflict || !strings.Contains(body, restorev1.FailureTargetInvalid) {
		t.Fatalf("same-resource review status=%d body=%s", status, body)
	}

	artifact, listing := downloadAndInspectBackup(t, store, objectKey, authority.ArtifactSize, authority.SHA256, spec, "pod/"+spec.Connection.ServiceName+"-0")
	for _, expected := range []string{"TABLE public opsi_p07b3c1_backup_rows", "SEQUENCE public opsi_p07b3c1_sequence", "INDEX public opsi_p07b3c1_backup_rows_value_idx", "TABLE public opsi_p07b3b1_acceptance"} {
		if !strings.Contains(listing, expected) {
			t.Fatalf("archive listing missing %q\n%s", expected, listing)
		}
	}
	if strings.Contains(listing, " ACL ") {
		t.Fatal("archive unexpectedly contains GRANT/ACL entries")
	}
	scanBackupLeaks(t, artifact, []byte(listing), authority, management.Password, binding.Credential.Password, registryPassword, access, secret)

	revoked := *binding
	revoked.Action, revoked.Credential = resourcev1.PostgresBindingRevoke, nil
	reconcilePostgresBindingK3s(t, reconciler, "backup-binding-revoke", spec, management, &revoked)
	deleted := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "delete", LeaseToken: "backup-delete", Spec: spec})
	if deleted.Status != "deleted" || deleted.Evidence == nil || !deleted.Evidence.StorageRetained || deleted.Evidence.PVCUID != pvcUID || deleted.Evidence.PVUID != pvUID {
		t.Fatalf("resource delete=%+v", deleted)
	}
	destroySpec := resourcev1.RetainedStorageDestroySpec{
		SchemaVersion: resourcev1.RetainedStorageSchemaVersion, RetainedStorageID: "rsto-p07b3c1", OriginalResourceID: spec.ResourceID, ProjectID: spec.ProjectID, EnvironmentID: spec.EnvironmentID, ResourceType: spec.ResourceType, Namespace: namespace,
		PVCName: deleted.Evidence.PVCName, PVCUID: deleted.Evidence.PVCUID, PVName: deleted.Evidence.PVName, PVUID: deleted.Evidence.PVUID, StorageClass: deleted.Evidence.StorageClass, ReclaimPolicy: deleted.Evidence.ReclaimPolicy, StorageHash: deleted.Evidence.StorageHash,
		Assignment: spec.Assignment, Revision: 2, Operation: "destroy",
	}
	destroyed := reconciler.ReconcileRetainedStorage(context.Background(), cloudrelay.RetainedStorageLease{LeaseToken: "backup-destroy-storage", Spec: destroySpec})
	if destroyed.Status != "destroyed" || destroyed.Evidence == nil || !destroyed.Evidence.PVCAbsent || !destroyed.Evidence.PVAbsent {
		t.Fatalf("retained storage destroy=%+v", destroyed)
	}

	verifier := "opsi-p07b3c1-verifier"
	kubectl(t, "run", verifier, "-n", namespace, "--image="+resourcev1.PostgresImage, "--restart=Never", "--command", "--", "sleep", "600")
	kubectl(t, "wait", "--for=condition=Ready", "pod/"+verifier, "-n", namespace, "--timeout=4m")
	afterArtifact, afterListing := downloadAndInspectBackup(t, store, objectKey, authority.ArtifactSize, authority.SHA256, spec, "pod/"+verifier)
	if !bytes.Equal(artifact, afterArtifact) || !strings.Contains(afterListing, "TABLE public opsi_p07b3c1_backup_rows") || authority.ObjectKey != objectKey {
		t.Fatal("backup authority/artifact did not survive source storage destruction")
	}
	scanBackupLeaks(t, afterArtifact, []byte(afterListing), authority, management.Password, binding.Credential.Password, registryPassword, access, secret)

	// Restore into a separately provisioned Resource; the source PVC/PV is gone.
	targetSpec := spec
	targetSpec.ResourceID, targetSpec.CredentialID, targetSpec.Connection.ServiceName = "res-postgres-restore-e2e", "mrcred-res-postgres-restore-e2e", "opsi-mr-postgres-restore"
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
	supplemental := restoreSupplementalManifest{SameResource: "PASS"}
	// Seed the durable binding authority and use the real review endpoint.
	seedCloudRestoreBinding(t, authorityAPI, projectID, targetSpec)
	authorityAPI.assertReviewRejected(t, backupID, targetSpec.ResourceID, "p07b3c2a-active-binding", restorev1.FailureTargetHasBindings)
	supplemental.ActiveBinding = "PASS"
	authorityAPI.execSQL(t, "DELETE FROM resource_bindings WHERE project_id="+sqlQuote(projectID)+" AND id='p07b3c2a-restore-binding'")
	seedTargetNonEmpty(t, targetSpec)
	authorityAPI.assertReviewRejected(t, backupID, targetSpec.ResourceID, "p07b3c2a-non-empty", restorev1.FailureTargetNotEmpty)
	supplemental.NonEmptyTarget = "PASS"
	clearTargetNonEmpty(t, targetSpec)
	queuedReview := authorityAPI.createReview(t, backupID, targetSpec.ResourceID)
	restoreReview, reviewLifecycle := authorityAPI.waitReview(t, queuedReview.ID, 5*time.Minute)
	if err := restoreReview.ValidateSucceeded(); err != nil || !cloud.leased("restore_review", restoreReview.ID) || restoreReview.BackupID != backupID || restoreReview.TargetResourceID != targetSpec.ResourceID || restoreReview.TargetNodeID != targetSpec.Assignment.NodeID || restoreReview.TargetSpecRevision != targetSpec.TopologyRevision || restoreReview.TargetSpecHash != targetSpec.SpecHash || restoreReview.TargetDatabase != targetSpec.Connection.Database || restoreReview.TargetDatabaseOID == "" || restoreReview.TargetPVCUID != targetReady.Evidence.PVCUID || restoreReview.TargetPVCName != targetReady.Evidence.PVCName || restoreReview.TargetPVUID != targetReady.Evidence.PVUID || restoreReview.TargetPVName != targetReady.Evidence.PVName || restoreReview.TargetStorageHash != targetReady.Evidence.StorageHash || restoreReview.BackupRevision == "" || restoreReview.PristineEvidenceHash == "" {
		t.Fatalf("restore review id=%s lifecycle=%v leased=%t err=%v", restoreReview.ID, reviewLifecycle, cloud.leased("restore_review", restoreReview.ID), err)
	}
	authorityAPI.assertDurableReview(t, restoreReview)
	stopRunner()
	if err := <-runResult; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	queuedRestore := authorityAPI.createRestore(t, backupID, restoreReview)
	barrier := &restorePreMutationBarrier{inner: restoreagent.Executor{KubectlPath: "kubectl"}, entered: make(chan struct{}), release: make(chan struct{})}
	recoveryCtx, stopRecovery := context.WithCancel(context.Background())
	recoveryResult := make(chan error, 1)
	go func() {
		recoveryResult <- (cloudrunner.Runner{Client: cloud, Engine: postgresBackupRolloutEngine{}, Backups: backupagent.Executor{KubectlPath: "kubectl"}, Restores: barrier, NodeID: spec.Assignment.NodeID, PollInterval: 10 * time.Millisecond, LongPollWait: 10 * time.Millisecond, HeartbeatInterval: time.Hour, BackupHeartbeat: 250 * time.Millisecond}).Run(recoveryCtx)
	}()
	select {
	case <-barrier.entered:
	case <-time.After(2 * time.Minute):
		t.Fatal("restore pre-mutation barrier was not reached")
	}
	stopRecovery()
	close(barrier.release)
	if err := <-recoveryResult; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Expire only this disposable lease so the restarted Agent can reclaim it immediately.
	authorityAPI.execSQL(t, "UPDATE restores SET lease_expires_at=now() WHERE project_id="+sqlQuote(projectID)+" AND id="+sqlQuote(queuedRestore.ID))
	runCtx, stopRunner = context.WithCancel(context.Background())
	runResult = make(chan error, 1)
	go func() {
		runResult <- (cloudrunner.Runner{Client: cloud, Engine: postgresBackupRolloutEngine{}, Backups: backupagent.Executor{KubectlPath: "kubectl"}, Restores: restoreagent.Executor{KubectlPath: "kubectl"}, NodeID: spec.Assignment.NodeID, PollInterval: 10 * time.Millisecond, LongPollWait: 10 * time.Millisecond, HeartbeatInterval: time.Hour, BackupHeartbeat: 250 * time.Millisecond}).Run(runCtx)
	}()
	restoreAuthority, restoreLifecycle := authorityAPI.waitRestore(t, queuedRestore.ID, 10*time.Minute)
	stopRunner()
	if err := <-runResult; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	supplemental.AgentPreMutationRecovery = "PASS"
	if err := restoreAuthority.ValidateSucceeded(); err != nil || !cloud.leased("restore", restoreAuthority.ID) || restoreAuthority.ReviewID != restoreReview.ID || restoreAuthority.BackupID != backupID {
		t.Fatalf("restore authority id=%s lifecycle=%v leased=%t err=%v", restoreAuthority.ID, restoreLifecycle, cloud.leased("restore", restoreAuthority.ID), err)
	}
	verifiedRows := kubectl(t, "exec", "pod/"+targetSpec.Connection.ServiceName+"-0", "-n", targetNamespace, "-c", "postgres", "--", "sh", "-ec", `u=$(cat /run/opsi-postgres/username); d=$(cat /run/opsi-postgres/database); export PGPASSWORD=$(cat /run/opsi-postgres/password); psql -v ON_ERROR_STOP=1 -qAt -h 127.0.0.1 -U "$u" -d "$d" -c 'SELECT count(*) FROM opsi_p07b3c1_backup_rows'`)
	if strings.TrimSpace(verifiedRows) != "128" {
		t.Fatalf("restored acceptance rows=%q", verifiedRows)
	}
	// Generate a valid custom archive whose CHECK function fails during COPY.
	seedFailingRestoreFixture(t, targetSpec)
	failureRunCtx, stopFailureRunner := context.WithCancel(context.Background())
	failureRunResult := make(chan error, 1)
	go func() {
		failureRunResult <- (cloudrunner.Runner{Client: cloud, Engine: postgresBackupRolloutEngine{}, Backups: backupagent.Executor{KubectlPath: "kubectl"}, Restores: restoreagent.Executor{KubectlPath: "kubectl"}, NodeID: spec.Assignment.NodeID, PollInterval: 10 * time.Millisecond, LongPollWait: 10 * time.Millisecond, HeartbeatInterval: time.Hour, BackupHeartbeat: 250 * time.Millisecond}).Run(failureRunCtx)
	}()
	failingBackup := authorityAPI.createBackupWithKey(t, targetSpec.ResourceID, "p07b3c2a-failing-backup")
	failingBackup, _ = authorityAPI.waitBackup(t, failingBackup.ID, 10*time.Minute)
	failureTargetSpec := targetSpec
	failureTargetSpec.ResourceID, failureTargetSpec.CredentialID, failureTargetSpec.Connection.ServiceName = "res-postgres-restore-failure-e2e", "mrcred-res-postgres-restore-failure-e2e", "opsi-mr-postgres-restore-failure"
	failureTargetSpec.Connection.Host = failureTargetSpec.Connection.ServiceName + "." + managedResourceNamespace(failureTargetSpec) + ".svc.cluster.local"
	failureTargetSpec.SpecHash, _ = failureTargetSpec.Hash()
	failureTargetNamespace := managedResourceNamespace(failureTargetSpec)
	t.Cleanup(func() {
		_, _ = kubectlOutput(context.Background(), "delete", "namespace", failureTargetNamespace, "--ignore-not-found", "--wait=true", "--timeout=2m")
	})
	failureManagement := randomManagedCredential(t, failureTargetSpec.CredentialID, resourcev1.CredentialPurposeResourceManagement, failureTargetSpec.ResourceID, failureTargetSpec.ResourceID, "opsi")
	failureReady := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "restore-failure-target", Spec: failureTargetSpec, Credential: failureManagement})
	if failureReady.Status != "ready" || failureReady.Evidence == nil {
		t.Fatalf("failing restore target=%+v", failureReady)
	}
	failureReady.Evidence.PVCUID = kubectl(t, "get", "pvc", failureReady.Evidence.PVCName, "-n", failureTargetNamespace, "-o", "jsonpath={.metadata.uid}")
	failureReady.Evidence.PVUID = kubectl(t, "get", "pv", failureReady.Evidence.PVName, "-o", "jsonpath={.metadata.uid}")
	failureReady.Evidence.StorageHash = resourcev1.ManagedResourceStorageHash(failureTargetSpec)
	authorityAPI.seedReadyResource(t, failureTargetSpec, failureReady.Evidence)
	failureReview := authorityAPI.createReviewWithKey(t, failingBackup.ID, failureTargetSpec.ResourceID, "p07b3c2a-failing-review")
	failureReview, _ = authorityAPI.waitReview(t, failureReview.ID, 5*time.Minute)
	failingRestore := authorityAPI.createRestoreWithKey(t, failingBackup.ID, failureReview, "p07b3c2a-failing-restore")
	failingAuthority := authorityAPI.waitRestoreOutcome(t, failingRestore.ID, 10*time.Minute)
	stopFailureRunner()
	if err := <-failureRunResult; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if failingAuthority.Lifecycle != restorev1.LifecycleFailed || failingAuthority.FailureCode != restorev1.FailureExecution || !failingAuthority.RollbackConfirmed || !failingAuthority.TargetPristineAfterFailure {
		t.Fatalf("failing restore authority=%+v", failingAuthority)
	}
	remaining := kubectl(t, "exec", "pod/"+failureTargetSpec.Connection.ServiceName+"-0", "-n", failureTargetNamespace, "-c", "postgres", "--", "sh", "-ec", `u=$(cat /run/opsi-postgres/username); d=$(cat /run/opsi-postgres/database); export PGPASSWORD=$(cat /run/opsi-postgres/password); psql -v ON_ERROR_STOP=1 -qAt -h 127.0.0.1 -U "$u" -d "$d" -c "SELECT count(*) FROM pg_class WHERE relname='p07b3c2a_failure_rows'"`)
	if strings.TrimSpace(remaining) != "0" {
		t.Fatalf("failing restore left target objects=%q", remaining)
	}
	supplemental.TransactionalRollback = "PASS"
	writePostgresBackupEvidence(t, []string{management.Password, targetManagement.Password, binding.Credential.Password, registryPassword, access, secret}, map[string]any{
		"backup_id": backupID, "resource_id": spec.ResourceID, "backup_type": authority.BackupType, "lifecycle": []string{"queued", "leased", "running", "succeeded"},
		"source_postgresql_version": authority.SourcePostgresVersion, "pg_dump_version": authority.PGDumpVersion, "format": authority.Format, "dump_options": authority.DumpOptions,
		"archive_policy": map[string]any{"schema_data": "included", "ownership_restore": "suppressed (--no-owner)", "acl_grant_restore": "excluded (--no-privileges)", "binding_role_credentials": "not included; Opsi reconciliation remains authoritative"},
		"store_provider": authority.StoreID, "object_endpoint": endpoint, "object_bucket": bucket, "object_key": objectKey, "object_etag": authority.ObjectETag, "object_version_id": authority.ObjectVersionID,
		"artifact_size": authority.ArtifactSize, "sha256": authority.SHA256, "remote_checksum_verification": "PASS", "pg_restore_list": "PASS", "expected_objects": []string{"opsi_p07b3c1_backup_rows", "opsi_p07b3c1_sequence", "opsi_p07b3c1_backup_rows_value_idx", "opsi_p07b3b1_acceptance"},
		"application_rows": 128, "application_live_queries_during_backup": traffic.count, "application_available": "PASS", "resource_state_during_backup": "ready", "pvc_uid_before": pvcUID, "pvc_uid_after_backup": pvcUID, "pv_uid_before": pvUID, "pv_uid_after_backup": pvUID,
		"agent_recovery": "same Backup ID replaced one incomplete object before success", "concurrent_backup_policy": "BACKUP_ALREADY_RUNNING (Cloud authority tests)",
		"resource_destroy": "runtime deleted, retained PVC explicitly destroyed", "pvc_absent": destroyed.Evidence.PVCAbsent, "pv_absent": destroyed.Evidence.PVAbsent, "backup_record_survives_resource_delete": "PASS (PostgreSQL authority test)", "artifact_survives_pvc_destroy": "PASS",
		"credential_leak_scan": "PASS", "restore_performed": true, "restore_id": restoreAuthority.ID, "review_id": restoreReview.ID, "review_lifecycle": reviewLifecycle, "durable_review_match": "PASS", "target_resource_id": targetSpec.ResourceID, "target_postgresql_version": targetSpec.Version, "target_pvc_uid": targetReady.Evidence.PVCUID, "target_pv_uid": targetReady.Evidence.PVUID, "source_target_storage_identity_different": true, "restore_lifecycle": restoreLifecycle, "pg_restore_version": restoreAuthority.PGRestoreVersion, "restore_options": restoreAuthority.RestoreOptions, "restored_rows": 128,
	})
	writeRestoreReviewInputsEvidence(t, authority, targetSpec)
	writeRestoreAuthorityEvidence(t, restoreReview, restoreAuthority)
	supplemental.AgentInTransactionRecovery = "NOT_EXERCISED_REAL"
	supplemental.CloudReviewDurability = "NOT_EXERCISED_REAL"
	supplemental.CloudRestoreDurability = "NOT_EXERCISED_REAL"
	supplemental.SucceededImmutability = "NOT_EXERCISED_REAL"
	writeRestoreSupplementalManifest(t, supplemental)
}

type restoreSupplementalManifest struct {
	ActiveBinding              string `json:"active_binding"`
	NonEmptyTarget             string `json:"non_empty_target"`
	SameResource               string `json:"same_resource"`
	TransactionalRollback      string `json:"transactional_rollback"`
	AgentPreMutationRecovery   string `json:"agent_pre_mutation_recovery"`
	AgentInTransactionRecovery string `json:"agent_in_transaction_recovery"`
	CloudReviewDurability      string `json:"cloud_review_durability"`
	CloudRestoreDurability     string `json:"cloud_restore_durability"`
	SucceededImmutability      string `json:"succeeded_immutability"`
}

type restorePreMutationBarrier struct {
	inner   restoreagent.Executor
	entered chan struct{}
	release chan struct{}
}

func (b *restorePreMutationBarrier) Review(ctx context.Context, lease restorev1.ReviewLease) restorev1.ReviewResult {
	return b.inner.Review(ctx, lease)
}

func (b *restorePreMutationBarrier) Execute(ctx context.Context, lease restorev1.Lease) restorev1.Result {
	select {
	case <-b.entered:
	default:
		close(b.entered)
	}
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return b.inner.Execute(ctx, lease)
}

func writeRestoreSupplementalManifest(t *testing.T, manifest restoreSupplementalManifest) {
	t.Helper()
	dir := os.Getenv("OPSI_P07B3C2A_EVIDENCE_DIR")
	if dir == "" {
		dir = filepath.Join(".tmp", "evidence", "p07b3c2a-postgres-restore-"+time.Now().UTC().Format("20060102T150405Z"))
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "supplemental-gate-results.json"), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	notes := "agent_in_transaction_recovery=NOT_EXERCISED_REAL: the test-only barrier is before Executor.Execute; interrupting the pg_restore child after transaction start would require a production process hook, so real transactional rollback is covered separately without claiming interruption recovery.\n"
	if err := os.WriteFile(filepath.Join(dir, "supplemental-gate-notes.txt"), []byte(notes), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeRestoreAuthorityEvidence(t *testing.T, review restorev1.Review, restore restorev1.Restore) {
	t.Helper()
	dir := os.Getenv("OPSI_P07B3C2A_EVIDENCE_DIR")
	if dir == "" {
		dir = filepath.Join(".tmp", "evidence", "p07b3c2a-postgres-restore-"+time.Now().UTC().Format("20060102T150405Z"))
	}
	data, err := json.MarshalIndent(map[string]any{"review_id": review.ID, "restore_id": restore.ID, "review": review, "restore": restore}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "restore-authority.json"), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func seedCloudRestoreBinding(t *testing.T, api restoreAcceptanceAPI, projectID string, target resourcev1.ManagedResourceSpec) {
	t.Helper()
	statement := "INSERT INTO runtimes(id,org_id,project_id,environment_id,name,status) SELECT 'rt-p07b3c2a-binding',p.org_id,p.id,e.id,'p07b3c2a-binding','ready' FROM projects p JOIN environments e ON e.project_id=p.id WHERE p.id=" + sqlQuote(projectID) + " ON CONFLICT(id) DO NOTHING;" +
		"INSERT INTO control_services(id,org_id,project_id,environment_id,runtime_id,name,type,status,source_type,namespace) SELECT 'svc-p07b3c2a-binding',p.org_id,p.id,e.id,'rt-p07b3c2a-binding','p07b3c2a-binding','application','ready','container','p07b3c2a' FROM projects p JOIN environments e ON e.project_id=p.id WHERE p.id=" + sqlQuote(projectID) + " ON CONFLICT(id) DO NOTHING;" +
		"INSERT INTO resource_bindings(id,project_id,environment_id,source_kind,source_id,target_kind,target_id,protocol,logical_name,lifecycle,credential_id,role_name,database_name,runtime_references,created_at,updated_at) SELECT 'p07b3c2a-restore-binding',p.id,e.id,'application','svc-p07b3c2a-binding','managed_service'," + sqlQuote(target.ResourceID) + ",'postgres','p07b3c2a-restore-binding','ready','rbcred-p07b3c2a','opsi_b_p07b3c2a','opsi','[]'::jsonb,now(),now() FROM projects p JOIN environments e ON e.id=" + sqlQuote(target.EnvironmentID) + " WHERE p.id=" + sqlQuote(projectID) + " ON CONFLICT(id) DO UPDATE SET lifecycle='ready';"
	api.execSQL(t, statement)
}

func postgresBackupK3sSpec() resourcev1.ManagedResourceSpec {
	spec := postgresBindingK3sSpec()
	spec.ResourceID, spec.ProjectID, spec.EnvironmentID = "res-postgres-backup-e2e", "project-postgres-backup", "env-postgres-backup"
	spec.CredentialID, spec.Connection.ServiceName = "mrcred-res-postgres-backup-e2e", "opsi-mr-postgres-backup"
	spec.Connection.Host = spec.Connection.ServiceName + "." + managedResourceNamespace(spec) + ".svc.cluster.local"
	spec.SpecHash, _ = spec.Hash()
	return spec
}

func downloadAndInspectBackup(t *testing.T, store *backupagent.S3Store, key string, size int64, sha string, spec resourcev1.ManagedResourceSpec, pod string) ([]byte, string) {
	t.Helper()
	body, info, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	artifact, err := io.ReadAll(io.LimitReader(body, size+1))
	if err != nil || int64(len(artifact)) != size || info.Size != size {
		t.Fatalf("download size=%d info=%+v err=%v", len(artifact), info, err)
	}
	sum := sha256.Sum256(artifact)
	if hex.EncodeToString(sum[:]) != sha || info.SHA256 != sha {
		t.Fatal("remote backup checksum mismatch")
	}
	listing, err := kubectlInput(context.Background(), artifact, "exec", "-i", pod, "-n", managedResourceNamespace(spec), "--", "pg_restore", "--list")
	if err != nil || strings.TrimSpace(listing) == "" {
		t.Fatalf("pg_restore --list err=%v output=%s", err, listing)
	}
	return artifact, listing
}

func scanBackupLeaks(t *testing.T, artifact, listing []byte, authority backupv1.Backup, secrets ...string) {
	t.Helper()
	metadata, _ := json.Marshal(authority)
	for _, secret := range secrets {
		if secret != "" && (bytes.Contains(artifact, []byte(secret)) || bytes.Contains(listing, []byte(secret)) || bytes.Contains(metadata, []byte(secret))) {
			t.Fatal("control-plane credential leaked into backup artifact/listing/metadata")
		}
	}
}

func writePostgresBackupEvidence(t *testing.T, secrets []string, evidence map[string]any) {
	t.Helper()
	dir := os.Getenv("OPSI_P07B3C1_EVIDENCE_DIR")
	if dir == "" {
		dir = filepath.Join(".tmp", "evidence", "p07b3c1-postgres-backup-"+time.Now().UTC().Format("20060102T150405Z"))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(evidence, "", "  ")
	for _, secret := range secrets {
		if secret != "" && bytes.Contains(data, []byte(secret)) {
			t.Fatal("credential leaked into PostgreSQL backup evidence")
		}
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(dir, "postgres-logical-backup.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("P07B3C1_POSTGRES_BACKUP_EVIDENCE=%s", dir)
}

func writeRestoreReviewInputsEvidence(t *testing.T, backup backupv1.Backup, target resourcev1.ManagedResourceSpec) {
	t.Helper()
	dir := os.Getenv("OPSI_P07B3C2A_EVIDENCE_DIR")
	if dir == "" {
		dir = filepath.Join(".tmp", "evidence", "p07b3c2a-postgres-restore-"+time.Now().UTC().Format("20060102T150405Z"))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(map[string]any{
		"backup_id": backup.ID, "backup_lifecycle": backup.Lifecycle, "backup_source_postgresql_version": backup.SourcePostgresVersion,
		"backup_source_profile": backup.SourceProfile, "backup_source_image": backup.SourceImage,
		"target_resource_id": target.ResourceID, "target_postgresql_version": target.Version, "target_profile": target.Profile, "target_image": target.Image,
	}, "", "  ")
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(dir, "restore-review-inputs.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("P07B3C2A_RESTORE_EVIDENCE=%s", dir)
}

type restoreAcceptanceCloudClient struct {
	cloudrelay.Client
	mu         sync.Mutex
	leasedJobs map[string]bool
}

func (c *restoreAcceptanceCloudClient) PollJob(ctx context.Context, nodeID string, wait time.Duration) (*cloudrelay.JobLease, error) {
	job, err := c.Client.PollJob(ctx, nodeID, wait)
	if job == nil || err != nil {
		return job, err
	}
	id, kind := "", job.Kind
	switch {
	case job.Backup != nil:
		id = job.Backup.Backup.ID
	case job.RestoreReview != nil:
		id = job.RestoreReview.Review.ID
	case job.Restore != nil:
		id = job.Restore.Restore.ID
	case job.CutoverReview != nil:
		id = job.CutoverReview.Review.ID
	}
	if id != "" {
		c.mu.Lock()
		if c.leasedJobs == nil {
			c.leasedJobs = map[string]bool{}
		}
		c.leasedJobs[kind+"\x00"+id] = true
		c.mu.Unlock()
	}
	return job, nil
}

func (c *restoreAcceptanceCloudClient) leased(kind, id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.leasedJobs[kind+"\x00"+id]
}

type restoreAcceptanceAPI struct {
	baseURL, projectID, pat, postgresContainer string
}

func (a restoreAcceptanceAPI) request(t *testing.T, method, path, key string, body any, want int, dst any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(data))
	}
	req, err := http.NewRequestWithContext(context.Background(), method, strings.TrimRight(a.baseURL, "/")+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+a.pat)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("X-Request-ID", "req-"+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != want {
		t.Fatalf("Cloud %s %s status=%d want=%d body=%s", method, path, resp.StatusCode, want, strings.TrimSpace(string(data)))
	}
	if dst != nil {
		if err := json.Unmarshal(data, dst); err != nil {
			t.Fatalf("Cloud %s %s response decode: %v", method, path, err)
		}
	}
}

func (a restoreAcceptanceAPI) requestStatus(t *testing.T, method, path, key string, body any) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, strings.TrimRight(a.baseURL, "/")+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+a.pat)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("X-Request-ID", "req-"+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(data)
}

func (a restoreAcceptanceAPI) assertReviewRejected(t *testing.T, backupID, targetID, key, code string) {
	t.Helper()
	status, body := a.requestStatus(t, http.MethodPost, "/api/projects/"+url.PathEscape(a.projectID)+"/backups/"+url.PathEscape(backupID)+"/restore-review", key, map[string]string{"target_resource_id": targetID})
	if status == http.StatusConflict {
		if !strings.Contains(body, code) {
			t.Fatalf("review rejection status=%d body=%s", status, body)
		}
		return
	}
	if status != http.StatusAccepted {
		t.Fatalf("review rejection status=%d body=%s", status, body)
	}
	var response struct {
		Review restorev1.Review `json:"review"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatal(err)
	}
	review := a.waitReviewOutcome(t, response.Review.ID, 5*time.Minute)
	if review.Lifecycle != restorev1.ReviewFailed || review.FailureCode != code {
		t.Fatalf("review rejection=%+v", review)
	}
}

func (a restoreAcceptanceAPI) createBackup(t *testing.T, resourceID string) backupv1.Backup {
	return a.createBackupWithKey(t, resourceID, "p07b3c2a-backup")
}

func (a restoreAcceptanceAPI) createBackupWithKey(t *testing.T, resourceID, key string) backupv1.Backup {
	var response struct {
		Backup backupv1.Backup `json:"backup"`
	}
	a.request(t, http.MethodPost, "/api/projects/"+url.PathEscape(a.projectID)+"/resources/"+url.PathEscape(resourceID)+"/backups", key, map[string]any{}, http.StatusAccepted, &response)
	if response.Backup.ID == "" || response.Backup.Lifecycle != backupv1.LifecycleQueued {
		t.Fatalf("Cloud backup authority=%+v", response.Backup)
	}
	return response.Backup
}

func (a restoreAcceptanceAPI) createReview(t *testing.T, backupID, targetID string) restorev1.Review {
	return a.createReviewWithKey(t, backupID, targetID, "p07b3c2a-review")
}

func (a restoreAcceptanceAPI) createReviewWithKey(t *testing.T, backupID, targetID, key string) restorev1.Review {
	var response struct {
		Review restorev1.Review `json:"review"`
	}
	a.request(t, http.MethodPost, "/api/projects/"+url.PathEscape(a.projectID)+"/backups/"+url.PathEscape(backupID)+"/restore-review", key, map[string]string{"target_resource_id": targetID}, http.StatusAccepted, &response)
	if response.Review.ID == "" || response.Review.BackupID != backupID || response.Review.TargetResourceID != targetID {
		t.Fatalf("Cloud review authority=%+v", response.Review)
	}
	return response.Review
}

func (a restoreAcceptanceAPI) createRestore(t *testing.T, backupID string, review restorev1.Review) restorev1.Restore {
	return a.createRestoreWithKey(t, backupID, review, "p07b3c2a-restore")
}

func (a restoreAcceptanceAPI) createRestoreWithKey(t *testing.T, backupID string, review restorev1.Review, key string) restorev1.Restore {
	var response struct {
		Restore restorev1.Restore `json:"restore"`
	}
	a.request(t, http.MethodPost, "/api/projects/"+url.PathEscape(a.projectID)+"/backups/"+url.PathEscape(backupID)+"/restores", key, restorev1.CreateRequest{TargetResourceID: review.TargetResourceID, ReviewID: review.ID}, http.StatusAccepted, &response)
	if response.Restore.ID == "" || response.Restore.ReviewID != review.ID || response.Restore.BackupID != backupID {
		t.Fatalf("Cloud restore authority=%+v", response.Restore)
	}
	return response.Restore
}

func (a restoreAcceptanceAPI) waitBackup(t *testing.T, id string, timeout time.Duration) (backupv1.Backup, []string) {
	var value backupv1.Backup
	lifecycle := []string{backupv1.LifecycleQueued}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		a.request(t, http.MethodGet, "/api/projects/"+url.PathEscape(a.projectID)+"/backups/"+url.PathEscape(id), "", nil, http.StatusOK, &value)
		if len(lifecycle) == 0 || lifecycle[len(lifecycle)-1] != value.Lifecycle {
			lifecycle = append(lifecycle, value.Lifecycle)
		}
		if value.Lifecycle == backupv1.LifecycleSucceeded || value.Lifecycle == backupv1.LifecycleFailed {
			if value.Lifecycle != backupv1.LifecycleSucceeded {
				t.Fatalf("Cloud backup failed id=%s code=%s", id, value.FailureCode)
			}
			return value, lifecycle
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("Cloud backup timed out id=%s lifecycle=%v", id, lifecycle)
	return value, lifecycle
}

func (a restoreAcceptanceAPI) waitReview(t *testing.T, id string, timeout time.Duration) (restorev1.Review, []string) {
	var value restorev1.Review
	lifecycle := []string{restorev1.ReviewQueued}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		a.request(t, http.MethodGet, "/api/projects/"+url.PathEscape(a.projectID)+"/restore-reviews/"+url.PathEscape(id), "", nil, http.StatusOK, &value)
		if len(lifecycle) == 0 || lifecycle[len(lifecycle)-1] != value.Lifecycle {
			lifecycle = append(lifecycle, value.Lifecycle)
		}
		if value.Lifecycle == restorev1.ReviewSucceeded || value.Lifecycle == restorev1.ReviewFailed {
			if value.Lifecycle != restorev1.ReviewSucceeded {
				t.Fatalf("Cloud restore review failed id=%s code=%s", id, value.FailureCode)
			}
			return value, lifecycle
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("Cloud restore review timed out id=%s lifecycle=%v", id, lifecycle)
	return value, lifecycle
}

func (a restoreAcceptanceAPI) waitReviewOutcome(t *testing.T, id string, timeout time.Duration) restorev1.Review {
	t.Helper()
	var value restorev1.Review
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		a.request(t, http.MethodGet, "/api/projects/"+url.PathEscape(a.projectID)+"/restore-reviews/"+url.PathEscape(id), "", nil, http.StatusOK, &value)
		if value.Lifecycle == restorev1.ReviewSucceeded || value.Lifecycle == restorev1.ReviewFailed {
			return value
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("Cloud restore review timed out id=%s", id)
	return value
}

func (a restoreAcceptanceAPI) createCutoverReview(t *testing.T, appID, sourceBindingID, targetBindingID, key string) (cutoverv1.ApplicationCutoverReview, string) {
	t.Helper()
	status, body := a.requestStatus(t, http.MethodPost, "/api/projects/"+url.PathEscape(a.projectID)+"/applications/"+url.PathEscape(appID)+"/cutover-reviews", key, cutoverv1.ReviewRequest{
		SourceBindingID: sourceBindingID,
		TargetBindingID: targetBindingID,
	})
	if status != http.StatusAccepted {
		t.Fatalf("create cutover review status=%d body=%s", status, body)
	}
	var resp struct {
		Review cutoverv1.ApplicationCutoverReview `json:"review"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Review, body
}

func (a restoreAcceptanceAPI) waitCutoverReview(t *testing.T, id string, timeout time.Duration) (cutoverv1.ApplicationCutoverReview, []string) {
	t.Helper()
	var value cutoverv1.ApplicationCutoverReview
	lifecycle := []string{cutoverv1.ReviewQueued}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		a.request(t, http.MethodGet, "/api/projects/"+url.PathEscape(a.projectID)+"/application-cutover-reviews/"+url.PathEscape(id), "", nil, http.StatusOK, &value)
		if len(lifecycle) == 0 || lifecycle[len(lifecycle)-1] != value.Lifecycle {
			lifecycle = append(lifecycle, value.Lifecycle)
		}
		if value.Lifecycle == cutoverv1.ReviewSucceeded || value.Lifecycle == cutoverv1.ReviewFailed {
			if value.Lifecycle != cutoverv1.ReviewSucceeded {
				t.Fatalf("Cloud cutover review failed id=%s code=%s msg=%s", id, value.FailureCode, value.FailureMessageRedacted)
			}
			return value, lifecycle
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("Cloud cutover review timed out id=%s lifecycle=%v", id, lifecycle)
	return value, lifecycle
}

func (a restoreAcceptanceAPI) waitRestore(t *testing.T, id string, timeout time.Duration) (restorev1.Restore, []string) {
	var value restorev1.Restore
	lifecycle := []string{restorev1.LifecycleQueued}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		a.request(t, http.MethodGet, "/api/projects/"+url.PathEscape(a.projectID)+"/restores/"+url.PathEscape(id), "", nil, http.StatusOK, &value)
		if len(lifecycle) == 0 || lifecycle[len(lifecycle)-1] != value.Lifecycle {
			lifecycle = append(lifecycle, value.Lifecycle)
		}
		if value.Lifecycle == restorev1.LifecycleSucceeded || value.Lifecycle == restorev1.LifecycleFailed {
			if value.Lifecycle != restorev1.LifecycleSucceeded {
				t.Fatalf("Cloud restore failed id=%s code=%s", id, value.FailureCode)
			}
			return value, lifecycle
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("Cloud restore timed out id=%s lifecycle=%v", id, lifecycle)
	return value, lifecycle
}

func (a restoreAcceptanceAPI) waitRestoreOutcome(t *testing.T, id string, timeout time.Duration) restorev1.Restore {
	t.Helper()
	var value restorev1.Restore
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		a.request(t, http.MethodGet, "/api/projects/"+url.PathEscape(a.projectID)+"/restores/"+url.PathEscape(id), "", nil, http.StatusOK, &value)
		if value.Lifecycle == restorev1.LifecycleSucceeded || value.Lifecycle == restorev1.LifecycleFailed {
			return value
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("Cloud restore timed out id=%s", id)
	return value
}

func (a restoreAcceptanceAPI) seedReadyResource(t *testing.T, spec resourcev1.ManagedResourceSpec, evidence *resourcev1.ManagedResourceEvidence) {
	t.Helper()
	if evidence == nil {
		t.Fatal("ready resource evidence is required")
	}
	copyEvidence := *evidence
	copyEvidence.ObservedSpecHash = spec.SpecHash
	value := resourcev1.Resource{SchemaVersion: resourcev1.SchemaVersion, ID: spec.ResourceID, ProjectID: spec.ProjectID, EnvironmentID: spec.EnvironmentID, Name: spec.ResourceID, Kind: resourcev1.KindManagedService, Provider: "opsi", Type: resourcev1.TypePostgres, Lifecycle: resourcev1.LifecycleReady, CreatedBy: "p07b3c2a-harness", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Managed: &resourcev1.ManagedSpec{Type: resourcev1.TypePostgres, Replicas: spec.Replicas, CPUMillicores: spec.CPUMillicores, MemoryBytes: spec.MemoryBytes, Storage: spec.Storage, ConnectionPolicy: resourcev1.ExposurePolicy{Mode: "internal"}}, Runtime: &resourcev1.ManagedResourceRuntime{Spec: spec, Evidence: &copyEvidence}}
	managed, _ := json.Marshal(value.Managed)
	runtime, _ := json.Marshal(value.Runtime)
	sql := "INSERT INTO resources(id,project_id,environment_id,name,kind,provider,type,lifecycle,managed_spec,external_spec,internal_name,created_by,created_at,updated_at,runtime_state) VALUES(" + sqlQuote(value.ID) + "," + sqlQuote(value.ProjectID) + "," + sqlQuote(value.EnvironmentID) + "," + sqlQuote(value.Name) + ",'managed_service','opsi','postgres','ready',convert_from(decode(" + sqlQuote(base64.StdEncoding.EncodeToString(managed)) + ",'base64'),'UTF8')::jsonb,'null'::jsonb,''," + sqlQuote(value.CreatedBy) + ",now(),now(),convert_from(decode(" + sqlQuote(base64.StdEncoding.EncodeToString(runtime)) + ",'base64'),'UTF8')::jsonb) ON CONFLICT(id) DO UPDATE SET lifecycle='ready',managed_spec=EXCLUDED.managed_spec,runtime_state=EXCLUDED.runtime_state,updated_at=now();"
	a.execSQL(t, sql)
	var loaded resourcev1.Resource
	a.request(t, http.MethodGet, "/api/projects/"+url.PathEscape(a.projectID)+"/resources/"+url.PathEscape(value.ID), "", nil, http.StatusOK, &loaded)
	if loaded.Lifecycle != resourcev1.LifecycleReady || loaded.Runtime == nil || loaded.Runtime.Evidence == nil || loaded.Runtime.Spec.Connection.Database != backupv1.CanonicalDatabase || loaded.Runtime.Evidence.PVCUID == "" || loaded.Runtime.Evidence.StorageHash == "" {
		database, pvc, storageHash := "", "", ""
		if loaded.Runtime != nil {
			database = loaded.Runtime.Spec.Connection.Database
			if loaded.Runtime.Evidence != nil {
				pvc, storageHash = loaded.Runtime.Evidence.PVCUID, loaded.Runtime.Evidence.StorageHash
			}
		}
		t.Fatalf("seeded Cloud resource is not ready: lifecycle=%s database=%s pvc=%s storage_hash=%s", loaded.Lifecycle, database, pvc, storageHash)
	}
}

func (a restoreAcceptanceAPI) assertDurableReview(t *testing.T, review restorev1.Review) {
	fields := parsePostgresAuthorityRow(a.execSQL(t, "SELECT "+strings.Join(durableReviewAuthorityColumns, ",")+" FROM restore_reviews WHERE project_id="+sqlQuote(a.projectID)+" AND id="+sqlQuote(review.ID)))
	if len(fields) != 15 || fields[0] != review.ID || fields[1] != review.BackupID || fields[2] != review.TargetResourceID || fields[3] != review.TargetNodeID || fields[4] != fmt.Sprint(review.TargetSpecRevision) || fields[5] != review.TargetSpecHash || fields[6] != review.TargetDatabase || fields[7] != review.TargetDatabaseOID || fields[8] != review.TargetPVCUID || fields[9] != review.TargetPVCName || fields[10] != review.TargetPVUID || fields[11] != review.TargetStorageHash || fields[12] != review.BackupRevision || fields[13] != review.PristineEvidenceHash || fields[14] != restorev1.ReviewSucceeded {
		t.Fatalf("durable review mismatch id=%s", review.ID)
	}
}

func (a restoreAcceptanceAPI) execSQL(t *testing.T, statement string) string {
	t.Helper()
	cmd := exec.Command("docker", postgresAuthorityPSQLArgs(a.postgresContainer)...)
	cmd.Stdin = strings.NewReader(statement)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Cloud PostgreSQL authority command failed: %v", err)
	}
	return string(out)
}

func postgresAuthorityPSQLArgs(container string) []string {
	return []string{"exec", "-i", container, "psql", "-U", "opsi", "-d", "opsi", "-v", "ON_ERROR_STOP=1", "-qAt", "-F", "\t"}
}

var durableReviewAuthorityColumns = []string{
	"authority->>'id'", "authority->>'backup_id'", "authority->>'target_resource_id'", "authority->>'target_node_id'", "authority->>'target_spec_revision'",
	"authority->>'target_spec_hash'", "authority->>'target_database'", "authority->>'target_database_oid'", "authority->>'target_pvc_uid'", "authority->>'target_pvc_name'",
	"authority->>'target_pv_uid'", "authority->>'target_storage_hash'", "authority->>'backup_revision'", "authority->>'pristine_evidence_hash'", "lifecycle",
}

func parsePostgresAuthorityRow(record string) []string {
	return strings.Split(strings.TrimSuffix(record, "\n"), "\t")
}

func TestPostgresAuthorityPSQLRecordUsesTabSeparator(t *testing.T) {
	if len(durableReviewAuthorityColumns) != 15 {
		t.Fatalf("durable review selected columns=%d", len(durableReviewAuthorityColumns))
	}
	args := postgresAuthorityPSQLArgs("postgres")
	separator := args[len(args)-1]
	if args[len(args)-2] != "-F" || separator != "\t" {
		t.Fatalf("psql field separator=%q", separator)
	}
	want := []string{
		"rrv_9d08a6a799eb9ac0fd9f91fe899403ae", "bkp_01", "res_01", "node_01", "7",
		"target-spec-hash", "opsi", "16384", "pvc-uid", "pvc-name", "pv-uid", "storage-hash",
		"backup-revision", "pristine-evidence-hash", restorev1.ReviewSucceeded,
	}
	fields := parsePostgresAuthorityRow(strings.Join(want, separator) + "\n")
	if len(fields) != len(durableReviewAuthorityColumns) {
		t.Fatalf("parsed durable review fields=%d", len(fields))
	}
	for index, field := range want {
		if fields[index] != field {
			t.Fatalf("durable review field %d=%q want %q", index, fields[index], field)
		}
	}
}

func sqlQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

func seedTargetNonEmpty(t *testing.T, spec resourcev1.ManagedResourceSpec) {
	t.Helper()
	command := `u=$(cat /run/opsi-postgres/username); d=$(cat /run/opsi-postgres/database); export PGPASSWORD=$(cat /run/opsi-postgres/password); psql -v ON_ERROR_STOP=1 -qAt -h 127.0.0.1 -U "$u" -d "$d" -c 'CREATE TABLE p07b3c2a_controlled_object(id integer PRIMARY KEY); INSERT INTO p07b3c2a_controlled_object VALUES (1); SELECT count(*) FROM p07b3c2a_controlled_object'`
	if got := strings.TrimSpace(kubectl(t, "exec", "pod/"+spec.Connection.ServiceName+"-0", "-n", managedResourceNamespace(spec), "-c", "postgres", "--", "sh", "-ec", command)); got != "1" {
		t.Fatalf("non-empty target seed count=%q", got)
	}
}

func clearTargetNonEmpty(t *testing.T, spec resourcev1.ManagedResourceSpec) {
	t.Helper()
	command := `u=$(cat /run/opsi-postgres/username); d=$(cat /run/opsi-postgres/database); export PGPASSWORD=$(cat /run/opsi-postgres/password); psql -v ON_ERROR_STOP=1 -qAt -h 127.0.0.1 -U "$u" -d "$d" -c 'DROP TABLE p07b3c2a_controlled_object'`
	kubectl(t, "exec", "pod/"+spec.Connection.ServiceName+"-0", "-n", managedResourceNamespace(spec), "-c", "postgres", "--", "sh", "-ec", command)
}

func seedFailingRestoreFixture(t *testing.T, spec resourcev1.ManagedResourceSpec) {
	t.Helper()
	command := `u=$(cat /run/opsi-postgres/username); d=$(cat /run/opsi-postgres/database); export PGPASSWORD=$(cat /run/opsi-postgres/password); psql -v ON_ERROR_STOP=1 -qAt -h 127.0.0.1 -U "$u" -d "$d" <<'SQL'
CREATE OR REPLACE FUNCTION p07b3c2a_restore_failure_check(value integer) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN
  IF value = 2 AND current_setting('opsi.restore_fixture', true) IS DISTINCT FROM 'source' THEN RAISE EXCEPTION 'p07b3c2a deterministic restore fixture failure'; END IF;
  RETURN true;
END
$$;
DROP TABLE IF EXISTS p07b3c2a_failure_rows;
CREATE TABLE p07b3c2a_failure_rows(id integer PRIMARY KEY, value text NOT NULL, CONSTRAINT p07b3c2a_failure_check CHECK (p07b3c2a_restore_failure_check(id)));
SET opsi.restore_fixture='source';
INSERT INTO p07b3c2a_failure_rows VALUES (1, 'before-failure'), (2, 'failure');
SQL`
	kubectl(t, "exec", "pod/"+spec.Connection.ServiceName+"-0", "-n", managedResourceNamespace(spec), "-c", "postgres", "--", "sh", "-ec", command)
}
func containsLifecycle(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

type postgresBackupRolloutEngine struct{}

func (postgresBackupRolloutEngine) ReconcilePending(context.Context, deploy.ProgressFunc) ([]deploymentv1.RolloutRecord, error) {
	return nil, nil
}
func (postgresBackupRolloutEngine) ReconcileRollout(context.Context, deploymentv1.RolloutIntent, deploy.ProgressFunc) (deploymentv1.RolloutRecord, error) {
	return deploymentv1.RolloutRecord{}, nil
}
