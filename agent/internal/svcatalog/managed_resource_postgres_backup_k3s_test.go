package svcatalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	backupagent "github.com/opsi-dev/opsi/agent/internal/backup"
	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	"github.com/opsi-dev/opsi/agent/internal/cloudrunner"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
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
	if os.Getenv("OPSI_E2E_K3S_POSTGRES_BACKUP") != "1" || reference == "" || registryUsername == "" || registryPassword == "" || endpoint == "" || access == "" || secret == "" || bucket == "" {
		t.Skip("set P07B3C1 K3s, immutable application, private registry, and disposable MinIO inputs")
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

	backupID := "bkp_p07b3c1_k3s"
	objectKey := fmt.Sprintf("projects/%s/environments/%s/resources/%s/backups/%s.dump", spec.ProjectID, spec.EnvironmentID, spec.ResourceID, backupID)
	storeSpec := backupv1.StoreSpec{ID: "minio-p07b3c1", Provider: backupv1.StoreProviderS3, Endpoint: endpoint, Bucket: bucket, Region: "us-east-1", AllowInsecure: true}
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
	lease := backupv1.Lease{
		LeaseToken: "bkplease_p07b3c1", SourceSpec: spec, Store: storeSpec, Credential: credential,
		Backup: backupv1.Backup{
			SchemaVersion: backupv1.SchemaVersion, ID: backupID, ProjectID: spec.ProjectID, EnvironmentID: spec.EnvironmentID, SourceResourceID: spec.ResourceID, SourceNodeID: spec.Assignment.NodeID,
			ResourceType: resourcev1.TypePostgres, BackupType: backupv1.BackupTypePostgresLogical, SourceDatabase: backupv1.CanonicalDatabase, SourcePostgresVersion: spec.Version,
			SourceSpecRevision: spec.TopologyRevision, SourceSpecHash: spec.SpecHash, SourcePVCName: ready.Evidence.PVCName, SourcePVCUID: pvcUID, SourcePVName: ready.Evidence.PVName, SourcePVUID: pvUID, SourceStorageHash: ready.Evidence.StorageHash,
			Format: backupv1.FormatCustom, DumpOptions: backupv1.CanonicalDumpOptions(), Lifecycle: backupv1.LifecycleLeased, StoreID: storeSpec.ID, ObjectKey: objectKey, RequestedBy: "p07b3c1-e2e", RequestedAt: time.Now().UTC(), CreatedAt: time.Now().UTC(), AttemptCount: 2,
		},
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
	cloud := &postgresBackupCloudClient{lease: &cloudrelay.JobLease{Kind: "backup", Backup: &lease}, done: make(chan struct{})}
	runCtx, stopRunner := context.WithCancel(context.Background())
	cloud.cancel = stopRunner
	runResult := make(chan error, 1)
	go func() {
		runResult <- (cloudrunner.Runner{Client: cloud, Engine: postgresBackupRolloutEngine{}, Backups: backupagent.Executor{KubectlPath: "kubectl"}, NodeID: spec.Assignment.NodeID, PollInterval: 10 * time.Millisecond, LongPollWait: 10 * time.Millisecond, HeartbeatInterval: time.Hour, BackupHeartbeat: 250 * time.Millisecond}).Run(runCtx)
	}()
	select {
	case <-cloud.done:
	case <-time.After(10 * time.Minute):
		stopRunner()
		t.Fatal("Agent backup job timed out")
	}
	stopRunner()
	if err := <-runResult; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	backupResults := cloud.results()
	if len(backupResults) < 2 || backupResults[0].Status != backupv1.LifecycleRunning || backupResults[len(backupResults)-1].Status != backupv1.LifecycleSucceeded {
		t.Fatalf("Agent backup lifecycle=%+v", backupResults)
	}
	result := backupResults[len(backupResults)-1]
	stopTraffic()
	traffic := <-trafficDone
	if traffic.err != nil || traffic.count == 0 {
		t.Fatalf("application traffic count=%d err=%v", traffic.count, traffic.err)
	}
	if result.Status != backupv1.LifecycleSucceeded || !result.ArchiveVerified || result.ArtifactSize <= 0 || len(result.SHA256) != 64 || !strings.Contains(result.PGDumpVersion, "(PostgreSQL) "+resourcev1.PostgresVersion) || !strings.HasPrefix(result.SourcePostgresVersion, resourcev1.PostgresVersion) {
		t.Fatalf("backup result=%+v", result)
	}
	completed := time.Now().UTC()
	authority := lease.Backup
	authority.Lifecycle, authority.ArtifactSize, authority.SHA256, authority.PGDumpVersion, authority.SourcePostgresVersion, authority.ArchiveVerified, authority.ObjectETag, authority.ObjectVersionID, authority.CompletedAt = backupv1.LifecycleSucceeded, result.ArtifactSize, result.SHA256, result.PGDumpVersion, result.SourcePostgresVersion, true, result.ObjectETag, result.ObjectVersionID, &completed
	if err := authority.ValidateSucceeded(); err != nil {
		t.Fatal(err)
	}
	if kubectl(t, "get", "pvc", ready.Evidence.PVCName, "-n", namespace, "-o", "jsonpath={.metadata.uid}") != pvcUID || kubectl(t, "get", "pv", ready.Evidence.PVName, "-o", "jsonpath={.metadata.uid}") != pvUID {
		t.Fatal("backup changed source PVC/PV identity")
	}
	assertPostgresApplicationReady(t, adapter, snapshot)

	artifact, listing := downloadAndInspectBackup(t, store, objectKey, result.ArtifactSize, result.SHA256, spec, "pod/"+spec.Connection.ServiceName+"-0")
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
	afterArtifact, afterListing := downloadAndInspectBackup(t, store, objectKey, result.ArtifactSize, result.SHA256, spec, "pod/"+verifier)
	if !bytes.Equal(artifact, afterArtifact) || !strings.Contains(afterListing, "TABLE public opsi_p07b3c1_backup_rows") || authority.SHA256 != result.SHA256 || authority.ObjectKey != objectKey {
		t.Fatal("backup authority/artifact did not survive source storage destruction")
	}
	scanBackupLeaks(t, afterArtifact, []byte(afterListing), authority, management.Password, binding.Credential.Password, registryPassword, access, secret)
	writePostgresBackupEvidence(t, []string{management.Password, binding.Credential.Password, registryPassword, access, secret}, map[string]any{
		"backup_id": backupID, "resource_id": spec.ResourceID, "backup_type": authority.BackupType, "lifecycle": []string{"queued", "leased", "running", "succeeded"},
		"source_postgresql_version": result.SourcePostgresVersion, "pg_dump_version": result.PGDumpVersion, "format": authority.Format, "dump_options": authority.DumpOptions,
		"archive_policy": map[string]any{"schema_data": "included", "ownership_restore": "suppressed (--no-owner)", "acl_grant_restore": "excluded (--no-privileges)", "binding_role_credentials": "not included; Opsi reconciliation remains authoritative"},
		"store_provider": authority.StoreID, "object_endpoint": endpoint, "object_bucket": bucket, "object_key": objectKey, "object_etag": authority.ObjectETag, "object_version_id": authority.ObjectVersionID,
		"artifact_size": authority.ArtifactSize, "sha256": authority.SHA256, "remote_checksum_verification": "PASS", "pg_restore_list": "PASS", "expected_objects": []string{"opsi_p07b3c1_backup_rows", "opsi_p07b3c1_sequence", "opsi_p07b3c1_backup_rows_value_idx", "opsi_p07b3b1_acceptance"},
		"application_rows": 128, "application_live_queries_during_backup": traffic.count, "application_available": "PASS", "resource_state_during_backup": "ready", "pvc_uid_before": pvcUID, "pvc_uid_after_backup": pvcUID, "pv_uid_before": pvUID, "pv_uid_after_backup": pvUID,
		"agent_recovery": "same Backup ID replaced one incomplete object before success", "concurrent_backup_policy": "BACKUP_ALREADY_RUNNING (Cloud authority tests)",
		"resource_destroy": "runtime deleted, retained PVC explicitly destroyed", "pvc_absent": destroyed.Evidence.PVCAbsent, "pv_absent": destroyed.Evidence.PVAbsent, "backup_record_survives_resource_delete": "PASS (PostgreSQL authority test)", "artifact_survives_pvc_destroy": "PASS",
		"credential_leak_scan": "PASS", "restore_performed": false,
	})
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

type postgresBackupCloudClient struct {
	mu     sync.Mutex
	lease  *cloudrelay.JobLease
	values []backupv1.Result
	done   chan struct{}
	cancel context.CancelFunc
	once   sync.Once
}

func (c *postgresBackupCloudClient) PollJob(ctx context.Context, _ string, _ time.Duration) (*cloudrelay.JobLease, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lease != nil {
		lease := c.lease
		c.lease = nil
		return lease, nil
	}
	return nil, ctx.Err()
}

func (c *postgresBackupCloudClient) CompleteBackup(_ context.Context, _, _ string, result backupv1.Result) error {
	c.mu.Lock()
	c.values = append(c.values, result)
	c.mu.Unlock()
	if result.Status == backupv1.LifecycleSucceeded || result.Status == backupv1.LifecycleFailed {
		c.once.Do(func() {
			close(c.done)
			c.cancel()
		})
	}
	return nil
}

func (c *postgresBackupCloudClient) results() []backupv1.Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]backupv1.Result(nil), c.values...)
}

func (*postgresBackupCloudClient) CompleteDeployment(context.Context, string, string, cloudrelay.DeploymentResult) error {
	return nil
}
func (*postgresBackupCloudClient) ProgressDeployment(context.Context, string, string, deploymentv1.Progress) error {
	return nil
}
func (*postgresBackupCloudClient) CompleteNodeLifecycle(context.Context, string, string, cloudrelay.NodeLifecycleResult) error {
	return nil
}
func (*postgresBackupCloudClient) CompleteManagedResource(context.Context, string, string, cloudrelay.ManagedResourceResult) error {
	return nil
}
func (*postgresBackupCloudClient) CompleteRetainedStorage(context.Context, string, string, cloudrelay.RetainedStorageResult) error {
	return nil
}
func (*postgresBackupCloudClient) Heartbeat(context.Context, string, cloudrelay.Heartbeat) error {
	return nil
}

type postgresBackupRolloutEngine struct{}

func (postgresBackupRolloutEngine) ReconcilePending(context.Context, deploy.ProgressFunc) ([]deploymentv1.RolloutRecord, error) {
	return nil, nil
}
func (postgresBackupRolloutEngine) ReconcileRollout(context.Context, deploymentv1.RolloutIntent, deploy.ProgressFunc) (deploymentv1.RolloutRecord, error) {
	return deploymentv1.RolloutRecord{}, nil
}
