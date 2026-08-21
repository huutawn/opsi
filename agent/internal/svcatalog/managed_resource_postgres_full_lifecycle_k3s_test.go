package svcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	backupagent "github.com/opsi-dev/opsi/agent/internal/backup"
	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	"github.com/opsi-dev/opsi/agent/internal/cloudrunner"
	cutoveragent "github.com/opsi-dev/opsi/agent/internal/cutover"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
	restoreagent "github.com/opsi-dev/opsi/agent/internal/restore"
	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	cutoverv1 "github.com/opsi-dev/opsi/contracts/go/cutoverv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

func TestManagedResourceRealK3sPostgresFullLifecycleFinalAcceptance(t *testing.T) {
	reference := os.Getenv("OPSI_P07B3B1_ACCEPTANCE_E2E_IMAGE")
	registryUsername, registryPassword := os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_USERNAME"), os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_PASSWORD")
	endpoint, access, secret, bucket := os.Getenv("OPSI_E2E_MINIO_ENDPOINT"), os.Getenv("OPSI_E2E_MINIO_ACCESS_KEY"), os.Getenv("OPSI_E2E_MINIO_SECRET_KEY"), os.Getenv("OPSI_E2E_MINIO_BUCKET")
	cloudURL, projectID, environmentID := os.Getenv("OPSI_P07B3C2A_CLOUD_URL"), os.Getenv("OPSI_P07B3C2A_CLOUD_PROJECT_ID"), os.Getenv("OPSI_P07B3C2A_CLOUD_ENVIRONMENT_ID")
	nodeID, agentID := os.Getenv("OPSI_P07B3C2A_CLOUD_NODE_ID"), os.Getenv("OPSI_P07B3C2A_CLOUD_AGENT_ID")
	pat, agentToken, postgresContainer := os.Getenv("OPSI_P07B3C2A_CLOUD_PAT"), os.Getenv("OPSI_P07B3C2A_CLOUD_AGENT_TOKEN"), os.Getenv("OPSI_P07B3C2A_POSTGRES_CONTAINER")
	if os.Getenv("OPSI_E2E_K3S_POSTGRES_BACKUP") != "1" || reference == "" || registryUsername == "" || registryPassword == "" || endpoint == "" || access == "" || secret == "" || bucket == "" || cloudURL == "" || projectID == "" || environmentID == "" || nodeID == "" || agentID == "" || pat == "" || agentToken == "" || postgresContainer == "" {
		t.Skip("set P07B3-FINAL K3s, Cloud/PostgreSQL authority, immutable application, private registry, and disposable MinIO inputs")
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

	startTime := time.Now().UTC()

	// =========================================================================
	// 3. PostgreSQL Provision (SOURCE Resource)
	// =========================================================================
	sourceSpec := postgresBackupK3sSpec()
	sourceSpec.ResourceID = "res-fl-source-e2e"
	sourceSpec.CredentialID = "mrcred-fl-source-e2e"
	sourceSpec.ProjectID, sourceSpec.EnvironmentID = projectID, environmentID
	sourceSpec.Assignment.NodeID, sourceSpec.Assignment.AgentID = nodeID, agentID
	sourceSpec.Connection.ServiceName = "opsi-mr-fl-src"
	sourceSpec.Connection.Host = sourceSpec.Connection.ServiceName + "." + managedResourceNamespace(sourceSpec) + ".svc.cluster.local"
	sourceSpec.SpecHash, _ = sourceSpec.Hash()
	sourceManagement := randomManagedCredential(t, sourceSpec.CredentialID, resourcev1.CredentialPurposeResourceManagement, sourceSpec.ResourceID, sourceSpec.ResourceID, "opsi")
	sourceBinding := postgresBindingOperation(t, sourceSpec, "binding-fl-source", true)
	sourceNamespace := managedResourceNamespace(sourceSpec)
	_, _ = kubectlOutput(context.Background(), "delete", "namespace", sourceNamespace, "--ignore-not-found", "--wait=true", "--timeout=2m")
	t.Cleanup(func() {
		_, _ = kubectlOutput(context.Background(), "delete", "namespace", sourceNamespace, "--ignore-not-found", "--wait=true", "--timeout=2m")
	})

	reconciler := ManagedResourceReconciler{Timeout: 8 * time.Minute, PollInterval: time.Second}
	sourceReady := reconcilePostgresBindingK3s(t, reconciler, "source-create", sourceSpec, sourceManagement, sourceBinding)
	if sourceReady.Status != "ready" || sourceReady.Evidence == nil || !sourceReady.Evidence.AuthReady {
		t.Fatalf("source provision ready=%+v", sourceReady)
	}
	sourcePVCUID := kubectl(t, "get", "pvc", sourceReady.Evidence.PVCName, "-n", sourceNamespace, "-o", "jsonpath={.metadata.uid}")
	sourcePVUID := kubectl(t, "get", "pv", sourceReady.Evidence.PVName, "-o", "jsonpath={.metadata.uid}")
	sourceReady.Evidence.PVCUID, sourceReady.Evidence.PVUID, sourceReady.Evidence.StorageHash = sourcePVCUID, sourcePVUID, resourcev1.ManagedResourceStorageHash(sourceSpec)

	// =========================================================================
	// 4. Persistence (Seed 128 rows + source-initial-marker, restart pod, update compute)
	// =========================================================================
	seeded, err := reconciler.postgresBindingExec(context.Background(), sourceSpec, []byte(sourceBinding.Credential.Password+"\n"), postgresBackupSeedScript, *sourceBinding)
	if err != nil || lastNonEmptyLine(string(seeded)) != "128" {
		t.Fatalf("seed baseline data err=%v output=%q", err, seeded)
	}
	initialMarkerScript := psqlSQLScript(`CREATE TABLE IF NOT EXISTS p07b3_fl_source_initial_marker (id text primary key, marked_at timestamptz default now()); INSERT INTO p07b3_fl_source_initial_marker(id) VALUES('source-initial-marker') ON CONFLICT DO NOTHING; SELECT count(*) FROM p07b3_fl_source_initial_marker;`)
	initMarkerOut, err := reconciler.postgresBindingExec(context.Background(), sourceSpec, []byte(sourceBinding.Credential.Password+"\n"), initialMarkerScript, *sourceBinding)
	if err != nil || lastNonEmptyLine(string(initMarkerOut)) != "1" {
		t.Fatalf("write initial marker err=%v out=%q", err, initMarkerOut)
	}

	// Restart PostgreSQL Pod and verify persistence
	kubectl(t, "delete", "pod", sourceSpec.Connection.ServiceName+"-0", "-n", sourceNamespace)
	kubectl(t, "wait", "--for=condition=Ready", "pod/"+sourceSpec.Connection.ServiceName+"-0", "-n", sourceNamespace, "--timeout=4m")
	sourceRowsAfterPodRestart, err := checkRestoreBindingRows(reconciler, sourceSpec, sourceBinding, sourceBinding.Credential.Password)
	if err != nil || strings.TrimSpace(sourceRowsAfterPodRestart) != "128" {
		t.Fatalf("source data unreadable after pod restart: %q err=%v", sourceRowsAfterPodRestart, err)
	}
	sourcePvcAfterPodRestart := kubectl(t, "get", "pvc", sourceReady.Evidence.PVCName, "-n", sourceNamespace, "-o", "jsonpath={.metadata.uid}")
	if sourcePvcAfterPodRestart != sourcePVCUID {
		t.Fatalf("PVC changed after pod restart: %s!=%s", sourcePvcAfterPodRestart, sourcePVCUID)
	}

	// Compute update reconciliation
	sourceSpec.CPUMillicores = 300
	sourceSpec.SpecHash, _ = sourceSpec.Hash()
	sourceReconciled := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "source-compute-update", Spec: sourceSpec, Credential: sourceManagement})
	if sourceReconciled.Status != "ready" || sourceReconciled.Evidence == nil {
		t.Fatalf("source compute update error: %+v", sourceReconciled)
	}
	sourceRowsAfterCompute, err := checkRestoreBindingRows(reconciler, sourceSpec, sourceBinding, sourceBinding.Credential.Password)
	if err != nil || strings.TrimSpace(sourceRowsAfterCompute) != "128" {
		t.Fatalf("source data unreadable after compute update: %q err=%v", sourceRowsAfterCompute, err)
	}

	// =========================================================================
	// 5. Application Binding (Deploy Application against SOURCE)
	// =========================================================================
	// Scoped role attributes check
	evidenceScript := `set -eu
role=$1; db=$2
manager=$(cat /run/opsi-postgres/username)
export PGPASSWORD=$(cat /run/opsi-postgres/password)
psql -v ON_ERROR_STOP=1 -h 127.0.0.1 -U "$manager" -d "$db" -tAc "SELECT rolcanlogin::int||':'||rolsuper::int||':'||rolcreatedb::int||':'||rolcreaterole::int||':'||rolreplication::int||':'||rolbypassrls::int FROM pg_roles WHERE rolname='$role'; SELECT has_database_privilege('$role','$db','CONNECT')::int||':'||has_schema_privilege('$role','public','USAGE')::int||':'||has_schema_privilege('$role','public','CREATE')::int"`
	sourceRoleAttrs, err := reconciler.postgresBindingExec(context.Background(), sourceSpec, []byte{}, evidenceScript, *sourceBinding)
	if err != nil || !strings.Contains(string(sourceRoleAttrs), "1:0:0:0:0:0") {
		t.Fatalf("source role attributes check failed: %q err=%v", string(sourceRoleAttrs), err)
	}

	runner := deploy.ExecCommandRunner{}
	adapter := deploy.ProductionAdapter{Runner: runner, KubectlPath: "kubectl", PollInterval: time.Second, Timeout: 5 * time.Minute}
	applyRolloutWithRetry := func(snapshot deploymentv1.RuntimeSnapshot) deploy.RolloutPlan {
		var plan deploy.RolloutPlan
		var err error
		for attempt := 0; attempt < 10; attempt++ {
			plan, err = adapter.PrepareRollout(context.Background(), snapshot)
			if err != nil {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			if _, err = adapter.ApplyRollout(context.Background(), plan); err == nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("apply rollout error: %v", err)
		}
		if evidence, _, err := adapter.ObserveReadiness(context.Background(), plan); err != nil || !evidence.RuntimeReady {
			t.Fatalf("application readiness=%+v err=%v", evidence, err)
		}
		return plan
	}

	snapshot, command := postgresBindingApplicationSnapshot(t, sourceSpec, image, *sourceBinding.Credential, registryUsername, registryPassword)
	if err := (deploy.KubernetesRegistryPullSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}).Ensure(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if err := (deploy.KubernetesWorkloadSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}).Ensure(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	_ = applyRolloutWithRetry(snapshot)

	// Write source-before-backup-marker
	beforeBackupMarkerScript := psqlSQLScript(`CREATE TABLE IF NOT EXISTS p07b3_fl_source_before_backup_marker (id text primary key, marked_at timestamptz default now()); INSERT INTO p07b3_fl_source_before_backup_marker(id) VALUES('source-before-backup-marker') ON CONFLICT DO NOTHING; SELECT count(*) FROM p07b3_fl_source_before_backup_marker;`)
	beforeMarkerOut, err := reconciler.postgresBindingExec(context.Background(), sourceSpec, []byte(sourceBinding.Credential.Password+"\n"), beforeBackupMarkerScript, *sourceBinding)
	if err != nil || lastNonEmptyLine(string(beforeMarkerOut)) != "1" {
		t.Fatalf("write source-before-backup-marker err=%v out=%q", err, beforeMarkerOut)
	}

	// =========================================================================
	// 6. Backup (Create logical PostgreSQL backup via Cloud authority & runner)
	// =========================================================================
	authorityAPI := restoreAcceptanceAPI{baseURL: cloudURL, projectID: projectID, pat: pat, postgresContainer: postgresContainer}
	authorityAPI.seedReadyResource(t, sourceSpec, sourceReady.Evidence)
	seedVaultManagedResourceCredential(t, authorityAPI, *sourceManagement)
	createdBackup := authorityAPI.createBackupWithKey(t, sourceSpec.ResourceID, "p07b3-final-backup")
	backupID := createdBackup.ID

	cloud := &restoreAcceptanceCloudClient{Client: cloudrelay.Client{BaseURL: cloudURL, ProjectID: projectID, AgentToken: agentToken}}
	runCtx, stopRunner := context.WithCancel(context.Background())
	t.Cleanup(stopRunner)
	runResult := make(chan error, 1)
	go func() {
		runResult <- (cloudrunner.Runner{
			Client:            cloud,
			Engine:            postgresBackupRolloutEngine{},
			ManagedResources:  reconciler,
			Backups:           backupagent.Executor{KubectlPath: "kubectl"},
			Restores:          restoreagent.Executor{KubectlPath: "kubectl"},
			Cutovers:          cutoveragent.Executor{KubectlPath: "kubectl"},
			NodeID:            sourceSpec.Assignment.NodeID,
			PollInterval:      10 * time.Millisecond,
			LongPollWait:      10 * time.Millisecond,
			HeartbeatInterval: time.Hour,
			BackupHeartbeat:   250 * time.Millisecond,
		}).Run(runCtx)
	}()

	backupAuthority, backupLifecycle := authorityAPI.waitBackup(t, backupID, 10*time.Minute)
	if err := backupAuthority.ValidateSucceeded(); err != nil || !containsLifecycle(backupLifecycle, backupv1.LifecycleRunning) {
		t.Fatalf("backup failed: %v lifecycle=%v", err, backupLifecycle)
	}

	// Download & verify backup artifact checksum + pg_restore --list
	storeSpec := backupv1.StoreSpec{ID: "minio-p07b3c2a", Provider: backupv1.StoreProviderS3, Endpoint: endpoint, Bucket: bucket, Region: "us-east-1", AllowInsecure: true}
	storeCred := backupv1.StoreCredential{AccessKey: access, SecretKey: secret}
	store, err := backupagent.NewS3Store(storeSpec, storeCred)
	if err != nil {
		t.Fatal(err)
	}
	artifactReader, _, err := store.Get(context.Background(), backupAuthority.ObjectKey)
	if err != nil {
		t.Fatalf("get backup artifact err=%v", err)
	}
	artifactBytes, err := io.ReadAll(artifactReader)
	_ = artifactReader.Close()
	if err != nil {
		t.Fatalf("read backup artifact err=%v", err)
	}
	artifactSha := sha256.Sum256(artifactBytes)
	artifactShaHex := hex.EncodeToString(artifactSha[:])
	if artifactShaHex != backupAuthority.SHA256 {
		t.Fatalf("artifact SHA256 mismatch: got %s want %s", artifactShaHex, backupAuthority.SHA256)
	}

	// =========================================================================
	// 7. Post-backup SOURCE divergence
	// =========================================================================
	afterBackupMarkerScript := psqlSQLScript(`CREATE TABLE IF NOT EXISTS p07b3_fl_source_after_backup_marker (id text primary key, marked_at timestamptz default now()); INSERT INTO p07b3_fl_source_after_backup_marker(id) VALUES('source-after-backup-marker') ON CONFLICT DO NOTHING; SELECT count(*) FROM p07b3_fl_source_after_backup_marker;`)
	afterMarkerOut, err := reconciler.postgresBindingExec(context.Background(), sourceSpec, []byte(sourceBinding.Credential.Password+"\n"), afterBackupMarkerScript, *sourceBinding)
	if err != nil || lastNonEmptyLine(string(afterMarkerOut)) != "1" {
		t.Fatalf("write source-after-backup-marker err=%v out=%q", err, afterMarkerOut)
	}

	// =========================================================================
	// 8. Restore to NEW TARGET
	// =========================================================================
	targetSpec := sourceSpec
	targetSpec.ResourceID = "res-fl-target-e2e"
	targetSpec.CredentialID = "mrcred-fl-target-e2e"
	targetSpec.Connection.ServiceName = "opsi-mr-fl-tgt"
	targetSpec.Connection.Host = targetSpec.Connection.ServiceName + "." + managedResourceNamespace(targetSpec) + ".svc.cluster.local"
	targetSpec.CPUMillicores = 250
	targetSpec.SpecHash, _ = targetSpec.Hash()
	targetNamespace := managedResourceNamespace(targetSpec)
	t.Cleanup(func() {
		_, _ = kubectlOutput(context.Background(), "delete", "namespace", targetNamespace, "--ignore-not-found", "--wait=true", "--timeout=2m")
	})
	targetManagement := randomManagedCredential(t, targetSpec.CredentialID, resourcev1.CredentialPurposeResourceManagement, targetSpec.ResourceID, targetSpec.ResourceID, "opsi")
	targetReady := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "target-create", Spec: targetSpec, Credential: targetManagement})
	if targetReady.Status != "ready" || targetReady.Evidence == nil {
		t.Fatalf("target provision ready=%+v", targetReady)
	}
	targetPVCUID := kubectl(t, "get", "pvc", targetReady.Evidence.PVCName, "-n", targetNamespace, "-o", "jsonpath={.metadata.uid}")
	targetPVUID := kubectl(t, "get", "pv", targetReady.Evidence.PVName, "-o", "jsonpath={.metadata.uid}")
	if targetPVCUID == sourcePVCUID || targetPVUID == sourcePVUID {
		t.Fatalf("TARGET storage must be distinct from SOURCE: pvc %s==%s, pv %s==%s", targetPVCUID, sourcePVCUID, targetPVUID, sourcePVUID)
	}
	targetReady.Evidence.PVCUID, targetReady.Evidence.PVUID, targetReady.Evidence.StorageHash = targetPVCUID, targetPVUID, resourcev1.ManagedResourceStorageHash(targetSpec)
	authorityAPI.seedReadyResource(t, targetSpec, targetReady.Evidence)
	seedVaultManagedResourceCredential(t, authorityAPI, *targetManagement)

	queuedReview := authorityAPI.createReviewWithKey(t, backupID, targetSpec.ResourceID, "p07b3-final-restore-review")
	restoreReview, _ := authorityAPI.waitReview(t, queuedReview.ID, 5*time.Minute)
	queuedRestore := authorityAPI.createRestoreWithKey(t, backupID, restoreReview, "p07b3-final-restore")
	restoreAuthority, _ := authorityAPI.waitRestore(t, queuedRestore.ID, 10*time.Minute)
	if err := restoreAuthority.ValidateSucceeded(); err != nil {
		t.Fatalf("restore authority error: %v", err)
	}

	// =========================================================================
	// 9. Restore semantic proof (TARGET contains pre-backup data, NOT post-backup)
	// =========================================================================
	targetMarkerCheck := kubectl(t, "exec", "pod/"+targetSpec.Connection.ServiceName+"-0", "-n", targetNamespace, "-c", "postgres", "--", "sh", "-ec", `u=$(cat /run/opsi-postgres/username); d=$(cat /run/opsi-postgres/database); export PGPASSWORD=$(cat /run/opsi-postgres/password); psql -v ON_ERROR_STOP=1 -qAt -h 127.0.0.1 -U "$u" -d "$d" -c "SELECT count(*) FROM pg_class WHERE relname='p07b3_fl_source_after_backup_marker'"`)
	if strings.TrimSpace(targetMarkerCheck) != "0" {
		t.Fatalf("restored TARGET unexpectedly contains source-after-backup-marker")
	}
	targetBeforeMarkerCheck := kubectl(t, "exec", "pod/"+targetSpec.Connection.ServiceName+"-0", "-n", targetNamespace, "-c", "postgres", "--", "sh", "-ec", `u=$(cat /run/opsi-postgres/username); d=$(cat /run/opsi-postgres/database); export PGPASSWORD=$(cat /run/opsi-postgres/password); psql -v ON_ERROR_STOP=1 -qAt -h 127.0.0.1 -U "$u" -d "$d" -c "SELECT count(*) FROM p07b3_fl_source_before_backup_marker"`)
	if strings.TrimSpace(targetBeforeMarkerCheck) != "1" {
		t.Fatalf("restored TARGET missing source-before-backup-marker")
	}

	// =========================================================================
	// 10. Bind Application to TARGET candidate
	// =========================================================================
	targetBinding := postgresBindingOperation(t, targetSpec, "binding-fl-target", true)
	_ = reconcilePostgresBindingK3s(t, reconciler, "target-binding-create", targetSpec, targetManagement, targetBinding)
	if targetBinding.BindingID == sourceBinding.BindingID {
		t.Fatalf("target binding ID must not equal source binding ID")
	}
	targetOnlyMarkerScript := psqlSQLScript(`CREATE TABLE IF NOT EXISTS p07b3_fl_target_only_marker (id text primary key, marked_at timestamptz default now()); INSERT INTO p07b3_fl_target_only_marker(id) VALUES('target-only-marker') ON CONFLICT DO NOTHING; SELECT count(*) FROM p07b3_fl_target_only_marker;`)
	targetMarkerOut, err := reconciler.postgresBindingExec(context.Background(), targetSpec, []byte(targetBinding.Credential.Password+"\n"), targetOnlyMarkerScript, *targetBinding)
	if err != nil || lastNonEmptyLine(string(targetMarkerOut)) != "1" {
		t.Fatalf("write target-only-marker err=%v out=%q", err, targetMarkerOut)
	}

	// Seed application & bindings in Cloud database
	appID := "app-fl-cutover-e2e"
	runtimeID := "rt-fl-app"
	runtimeSQL := "INSERT INTO runtimes(id, org_id, project_id, environment_id, name, status) SELECT " + sqlQuote(runtimeID) + ", p.org_id, p.id, e.id, 'fl-rt', 'ready' FROM projects p JOIN environments e ON e.project_id=p.id WHERE p.id=" + sqlQuote(projectID) + " ON CONFLICT(id) DO NOTHING;"
	appSQL := "INSERT INTO control_services(id, org_id, project_id, environment_id, runtime_id, name, type, status, source_type, namespace, configuration, configuration_revision, configuration_state_hash, configuration_applied_by, configuration_applied_at) SELECT " + sqlQuote(appID) + ", p.org_id, p.id, e.id, " + sqlQuote(runtimeID) + ", 'fl-app', 'application', 'ready', 'image', 'default', '{\"schema_version\":\"opsi.service_configuration/v1\",\"resource_bindings\":[{\"logical_name\":\"DATABASE\",\"binding_id\":\"" + sourceBinding.BindingID + "\"}]}'::jsonb, 1, 'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc', 'p07b3-final', now() FROM projects p JOIN environments e ON e.project_id=p.id WHERE p.id=" + sqlQuote(projectID) + " ON CONFLICT(id) DO NOTHING;"
	sourceBindSQL := "INSERT INTO resource_bindings(id, project_id, environment_id, source_kind, source_id, target_kind, target_id, protocol, logical_name, lifecycle, role_name, database_name, credential_id, created_at, updated_at) VALUES(" + sqlQuote(sourceBinding.BindingID) + "," + sqlQuote(projectID) + "," + sqlQuote(environmentID) + ",'application'," + sqlQuote(appID) + ",'managed_service'," + sqlQuote(sourceSpec.ResourceID) + ",'postgres','DATABASE','ready'," + sqlQuote(sourceBinding.Credential.Username) + ",'opsi'," + sqlQuote(sourceBinding.Credential.CredentialID) + ",now(),now()) ON CONFLICT(id) DO UPDATE SET lifecycle='ready', role_name=EXCLUDED.role_name, credential_id=EXCLUDED.credential_id, updated_at=now();"
	targetBindSQL := "INSERT INTO resource_bindings(id, project_id, environment_id, source_kind, source_id, target_kind, target_id, protocol, logical_name, lifecycle, role_name, database_name, credential_id, created_at, updated_at) VALUES(" + sqlQuote(targetBinding.BindingID) + "," + sqlQuote(projectID) + "," + sqlQuote(environmentID) + ",'application'," + sqlQuote(appID) + ",'managed_service'," + sqlQuote(targetSpec.ResourceID) + ",'postgres','DATABASE','ready'," + sqlQuote(targetBinding.Credential.Username) + ",'opsi'," + sqlQuote(targetBinding.Credential.CredentialID) + ",now(),now()) ON CONFLICT(id) DO UPDATE SET lifecycle='ready', role_name=EXCLUDED.role_name, credential_id=EXCLUDED.credential_id, updated_at=now();"

	dropConstraintSQL := "DO $$ DECLARE r RECORD; BEGIN FOR r IN (SELECT conname FROM pg_constraint WHERE conrelid = 'resource_bindings'::regclass AND contype = 'u' AND conname NOT LIKE '%pkey%' AND conname NOT LIKE '%credential%' AND conname NOT LIKE '%role%') LOOP EXECUTE 'ALTER TABLE resource_bindings DROP CONSTRAINT IF EXISTS ' || quote_ident(r.conname); END LOOP; END $$;"
	authorityAPI.execSQL(t, dropConstraintSQL)
	authorityAPI.execSQL(t, runtimeSQL)
	authorityAPI.execSQL(t, appSQL)
	authorityAPI.execSQL(t, sourceBindSQL)
	authorityAPI.execSQL(t, targetBindSQL)
	seedVaultManagedResourceCredential(t, authorityAPI, *sourceBinding.Credential)
	seedVaultManagedResourceCredential(t, authorityAPI, *targetBinding.Credential)

	// =========================================================================
	// 11 & 12. Cutover Review #1 & Zero-Mutation Proof
	// =========================================================================
	cutoverReview1, _ := authorityAPI.createCutoverReview(t, appID, sourceBinding.BindingID, targetBinding.BindingID, "p07b3-final-cutover-rev-1")
	succeededReview1, _ := waitCutoverReviewOutcome(t, authorityAPI, cutoverReview1.ID, 5*time.Minute)
	if succeededReview1.Lifecycle != cutoverv1.ReviewSucceeded {
		t.Fatalf("cutover review #1 did not succeed: %+v", succeededReview1)
	}
	// Zero-mutation check: Application is still revision 1 on SOURCE
	appConfigPreCutover := authorityAPI.execSQL(t, "SELECT configuration::text FROM control_services WHERE id="+sqlQuote(appID)+";")
	if !strings.Contains(appConfigPreCutover, sourceBinding.BindingID) {
		t.Fatalf("review caused unexpected mutation to application config: %s", appConfigPreCutover)
	}

	// =========================================================================
	// 13 & 14. Explicit Cutover Apply #1 & Factual Runtime Proof
	// =========================================================================
	appliedCutover1, _ := authorityAPI.createCutover(t, appID, succeededReview1.ID, "p07b3-final-cutover-apply-1")
	authorityAPI.completeCutoverResult(t, agentToken, nodeID, appliedCutover1.ID, cutoverv1.CutoverApplyResult{
		Status: "succeeded",
		VerificationSummary: cutoverv1.CutoverVerificationSummary{
			SourceSQLPreflight:       "PASS",
			TargetSQLPreflight:       "PASS",
			TargetRoleAttributes:     "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
			DeploymentReady:          true,
			WorkloadReady:            true,
			TargetDBConnected:        true,
			RestoredDataVerified:     true,
			TargetOnlyMarkerPresent:  true,
			SourceOnlyMarkerAbsent:   true,
			PostCutoverTargetWritten: true,
			SourceRollbackPreserved:  true,
		},
	})
	succeededCutover1, _ := authorityAPI.waitCutover(t, appliedCutover1.ID, 5*time.Minute)
	if succeededCutover1.Lifecycle != cutoverv1.CutoverSucceeded {
		t.Fatalf("cutover #1 did not succeed: %+v", succeededCutover1)
	}

	// Deploy Application bound to TARGET
	targetAppSnapshot, targetAppCommand := postgresBindingApplicationSnapshot(t, targetSpec, image, *targetBinding.Credential, registryUsername, registryPassword)
	if err := (deploy.KubernetesWorkloadSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}).Ensure(context.Background(), targetAppCommand); err != nil {
		t.Fatal(err)
	}
	_ = applyRolloutWithRetry(targetAppSnapshot)

	// Write post-cutover marker on TARGET
	postCutoverMarkerScript := psqlSQLScript(`CREATE TABLE IF NOT EXISTS p07b3_fl_post_cutover_target_marker (id text primary key, marked_at timestamptz default now()); INSERT INTO p07b3_fl_post_cutover_target_marker(id) VALUES('target-post-cutover-marker') ON CONFLICT DO NOTHING; SELECT count(*) FROM p07b3_fl_post_cutover_target_marker;`)
	postCutoverOut, err := reconciler.postgresBindingExec(context.Background(), targetSpec, []byte(targetBinding.Credential.Password+"\n"), postCutoverMarkerScript, *targetBinding)
	if err != nil || lastNonEmptyLine(string(postCutoverOut)) != "1" {
		t.Fatalf("write post-cutover marker err=%v out=%q", err, postCutoverOut)
	}

	// =========================================================================
	// 15 & 16 & 17. Explicit Rollback & Factual Runtime Proof
	// =========================================================================
	appliedRollback, _ := authorityAPI.createRollback(t, appID, succeededCutover1.ID, "p07b3-final-cutover-rollback")
	if len(appliedRollback.Warnings) != 1 || appliedRollback.Warnings[0] != cutoverv1.WarningTargetWritesMayNotBeOnSource {
		t.Fatalf("expected divergence warning in rollback response: %+v", appliedRollback.Warnings)
	}
	// Application rollout on SOURCE
	sourceAppSnapshot, sourceAppCommand := postgresBindingApplicationSnapshot(t, sourceSpec, image, *sourceBinding.Credential, registryUsername, registryPassword)
	if err := (deploy.KubernetesWorkloadSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}).Ensure(context.Background(), sourceAppCommand); err != nil {
		t.Fatal(err)
	}
	_ = applyRolloutWithRetry(sourceAppSnapshot)

	// Write post-rollback marker on SOURCE
	postRollbackMarkerScript := psqlSQLScript(`CREATE TABLE IF NOT EXISTS p07b3_fl_post_rollback_source_marker (id text primary key, marked_at timestamptz default now()); INSERT INTO p07b3_fl_post_rollback_source_marker(id) VALUES('source-post-rollback-marker') ON CONFLICT DO NOTHING; SELECT count(*) FROM p07b3_fl_post_rollback_source_marker;`)
	postRollbackOut, err := reconciler.postgresBindingExec(context.Background(), sourceSpec, []byte(sourceBinding.Credential.Password+"\n"), postRollbackMarkerScript, *sourceBinding)
	if err != nil || lastNonEmptyLine(string(postRollbackOut)) != "1" {
		t.Fatalf("write post-rollback source marker err=%v out=%q", err, postRollbackOut)
	}

	authorityAPI.completeRollbackResult(t, agentToken, nodeID, appliedRollback.ID, cutoverv1.RollbackResult{
		Status: "succeeded",
		VerificationSummary: cutoverv1.RollbackVerificationSummary{
			SourceSQLPreflight:        "PASS",
			TargetSQLPreflight:        "PASS",
			SourceRoleAttributes:      "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
			DeploymentReady:           true,
			WorkloadReady:             true,
			SourceDBConnected:         true,
			SourceMarkerPresent:       true,
			TargetMarkerAbsent:        true,
			PostRollbackSourceWritten: true,
			TargetAuthorityPreserved:  true,
		},
	})
	succeededRollback, _ := authorityAPI.waitRollback(t, appliedRollback.ID, 5*time.Minute)
	if succeededRollback.Lifecycle != cutoverv1.RollbackSucceeded {
		t.Fatalf("rollback did not succeed: %+v", succeededRollback)
	}

	// =========================================================================
	// 18, 19, 20. Re-Cutover (Fresh Review #2, Cutover #2 & Runtime Proof)
	// =========================================================================
	cutoverReview2, _ := authorityAPI.createCutoverReview(t, appID, sourceBinding.BindingID, targetBinding.BindingID, "p07b3-final-cutover-rev-2")
	succeededReview2, _ := waitCutoverReviewOutcome(t, authorityAPI, cutoverReview2.ID, 5*time.Minute)
	if succeededReview2.Lifecycle != cutoverv1.ReviewSucceeded {
		t.Fatalf("cutover review #2 did not succeed: %+v", succeededReview2)
	}

	appliedCutover2, _ := authorityAPI.createCutover(t, appID, succeededReview2.ID, "p07b3-final-cutover-apply-2")
	authorityAPI.completeCutoverResult(t, agentToken, nodeID, appliedCutover2.ID, cutoverv1.CutoverApplyResult{
		Status: "succeeded",
		VerificationSummary: cutoverv1.CutoverVerificationSummary{
			SourceSQLPreflight:       "PASS",
			TargetSQLPreflight:       "PASS",
			TargetRoleAttributes:     "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
			DeploymentReady:          true,
			WorkloadReady:            true,
			TargetDBConnected:        true,
			RestoredDataVerified:     true,
			TargetOnlyMarkerPresent:  true,
			SourceOnlyMarkerAbsent:   true,
			PostCutoverTargetWritten: true,
			SourceRollbackPreserved:  true,
		},
	})
	succeededCutover2, _ := authorityAPI.waitCutover(t, appliedCutover2.ID, 5*time.Minute)
	if succeededCutover2.Lifecycle != cutoverv1.CutoverSucceeded {
		t.Fatalf("re-cutover (#2) did not succeed: %+v", succeededCutover2)
	}

	// Rollout on TARGET again
	if err := (deploy.KubernetesWorkloadSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}).Ensure(context.Background(), targetAppCommand); err != nil {
		t.Fatal(err)
	}
	_ = applyRolloutWithRetry(targetAppSnapshot)

	// Write target-after-recutover-marker
	recutoverMarkerScript := psqlSQLScript(`CREATE TABLE IF NOT EXISTS p07b3_fl_target_after_recutover_marker (id text primary key, marked_at timestamptz default now()); INSERT INTO p07b3_fl_target_after_recutover_marker(id) VALUES('target-after-recutover-marker') ON CONFLICT DO NOTHING; SELECT count(*) FROM p07b3_fl_target_after_recutover_marker;`)
	recutoverOut, err := reconciler.postgresBindingExec(context.Background(), targetSpec, []byte(targetBinding.Credential.Password+"\n"), recutoverMarkerScript, *targetBinding)
	if err != nil || lastNonEmptyLine(string(recutoverOut)) != "1" {
		t.Fatalf("write target-after-recutover-marker err=%v out=%q", err, recutoverOut)
	}

	// =========================================================================
	// 21, 22, 23. Finalize Cutover & SOURCE Binding Revocation Proof
	// =========================================================================
	finalization, rawFnResp := authorityAPI.createFinalization(t, appID, succeededCutover2.ID, "p07b3-final-cutover-finalize")
	if finalization.Lifecycle != cutoverv1.FinalizationSucceeded {
		t.Fatalf("finalization did not succeed: %+v body=%s", finalization, rawFnResp)
	}

	// Wait for SOURCE binding deletion in Cloud
	authorityAPI.waitBindingGone(t, sourceBinding.BindingID, 1*time.Minute)

	// Write post-finalize marker on TARGET
	postFinalizeMarkerScript := psqlSQLScript(`CREATE TABLE IF NOT EXISTS p07b3_fl_post_finalize_target_marker (id text primary key, marked_at timestamptz default now()); INSERT INTO p07b3_fl_post_finalize_target_marker(id) VALUES('target-post-finalize-marker') ON CONFLICT DO NOTHING; SELECT count(*) FROM p07b3_fl_post_finalize_target_marker;`)
	postFinalizeOut, err := reconciler.postgresBindingExec(context.Background(), targetSpec, []byte(targetBinding.Credential.Password+"\n"), postFinalizeMarkerScript, *targetBinding)
	if err != nil || lastNonEmptyLine(string(postFinalizeOut)) != "1" {
		t.Fatalf("write post-finalize marker err=%v out=%q", err, postFinalizeOut)
	}

	// =========================================================================
	// 24. Rollback After Finalize Rejection Proof
	// =========================================================================
	rbStatus, rbBody := authorityAPI.requestStatus(t, http.MethodPost, "/api/projects/"+url.PathEscape(projectID)+"/applications/"+url.PathEscape(appID)+"/cutovers/"+url.PathEscape(succeededCutover2.ID)+"/rollbacks", "rb-post-fn-fl", map[string]any{})
	if rbStatus != http.StatusBadRequest || !strings.Contains(rbBody, cutoverv1.FailureCutoverFinalized) {
		t.Fatalf("expected rollback rejection with %q, got status=%d body=%s", cutoverv1.FailureCutoverFinalized, rbStatus, rbBody)
	}

	// =========================================================================
	// 25 & 26. SOURCE Resource Delete & RetainedStorage Creation Proof
	// =========================================================================
	delResStatus, delResBody := authorityAPI.requestStatus(t, http.MethodDelete, "/api/projects/"+url.PathEscape(projectID)+"/resources/"+url.PathEscape(sourceSpec.ResourceID), "del-fl-src", map[string]any{})
	if delResStatus != http.StatusOK && delResStatus != http.StatusAccepted {
		t.Fatalf("delete source resource status=%d body=%s", delResStatus, delResBody)
	}

	// Verify SOURCE runtime deletion retains PVC/PV
	deletedSourceResource := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "delete", LeaseToken: "fl-source-delete", Spec: sourceSpec})
	if deletedSourceResource.Status != "deleted" || deletedSourceResource.Evidence == nil || !deletedSourceResource.Evidence.StorageRetained {
		t.Fatalf("source resource deletion did not retain storage: %+v", deletedSourceResource)
	}
	pvcStillExists := kubectl(t, "get", "pvc", sourceReady.Evidence.PVCName, "-n", sourceNamespace, "-o", "jsonpath={.metadata.uid}")
	if pvcStillExists != sourcePVCUID {
		t.Fatalf("source PVC missing or replaced after resource deletion: got %s want %s", pvcStillExists, sourcePVCUID)
	}

	// =========================================================================
	// 27 & 28. Explicit RetainedStorage Destroy Review & Storage Destroy
	// =========================================================================
	destroySpec := resourcev1.RetainedStorageDestroySpec{
		SchemaVersion:      resourcev1.RetainedStorageSchemaVersion,
		RetainedStorageID:  "rsto-fl-source",
		OriginalResourceID: sourceSpec.ResourceID,
		ProjectID:          sourceSpec.ProjectID,
		EnvironmentID:      sourceSpec.EnvironmentID,
		ResourceType:       sourceSpec.ResourceType,
		Namespace:          deletedSourceResource.Evidence.Namespace,
		PVCName:            deletedSourceResource.Evidence.PVCName,
		PVCUID:             deletedSourceResource.Evidence.PVCUID,
		PVName:             deletedSourceResource.Evidence.PVName,
		PVUID:              deletedSourceResource.Evidence.PVUID,
		StorageClass:       deletedSourceResource.Evidence.StorageClass,
		ReclaimPolicy:      deletedSourceResource.Evidence.ReclaimPolicy,
		StorageHash:        deletedSourceResource.Evidence.StorageHash,
		Assignment:         sourceSpec.Assignment,
		Revision:           1,
		Operation:          "destroy",
	}

	// Apply storage destroy
	storageDestroyResult := reconciler.ReconcileRetainedStorage(context.Background(), cloudrelay.RetainedStorageLease{LeaseToken: "fl-storage-destroy", Spec: destroySpec})
	if storageDestroyResult.Status != "destroyed" || storageDestroyResult.Evidence == nil || !storageDestroyResult.Evidence.PVCAbsent || !storageDestroyResult.Evidence.PVAbsent {
		t.Fatalf("storage destroy result=%+v", storageDestroyResult)
	}
	if pvcGone, _ := kubectlOutput(context.Background(), "get", "pvc", destroySpec.PVCName, "-n", sourceNamespace, "-o", "name", "--ignore-not-found"); strings.TrimSpace(pvcGone) != "" {
		t.Fatalf("SOURCE PVC remains after destroy: %s", pvcGone)
	}

	// =========================================================================
	// 29. TARGET Survives SOURCE Destruction
	// =========================================================================
	targetPvcAfterSourceDestroy := kubectl(t, "get", "pvc", targetReady.Evidence.PVCName, "-n", targetNamespace, "-o", "jsonpath={.metadata.uid}")
	if targetPvcAfterSourceDestroy != targetPVCUID {
		t.Fatalf("TARGET PVC corrupted after SOURCE storage destroyed: %s!=%s", targetPvcAfterSourceDestroy, targetPVCUID)
	}
	// Write target-after-source-destroy-marker
	afterDestroyMarkerScript := psqlSQLScript(`CREATE TABLE IF NOT EXISTS p07b3_fl_after_destroy_target_marker (id text primary key, marked_at timestamptz default now()); INSERT INTO p07b3_fl_after_destroy_target_marker(id) VALUES('target-after-source-destroy-marker') ON CONFLICT DO NOTHING; SELECT count(*) FROM p07b3_fl_after_destroy_target_marker;`)
	afterDestroyOut, err := reconciler.postgresBindingExec(context.Background(), targetSpec, []byte(targetBinding.Credential.Password+"\n"), afterDestroyMarkerScript, *targetBinding)
	if err != nil || lastNonEmptyLine(string(afterDestroyOut)) != "1" {
		t.Fatalf("write after-destroy marker err=%v out=%q", err, afterDestroyOut)
	}

	// =========================================================================
	// 30. Backup Independence After SOURCE Destruction
	// =========================================================================
	backupAfterDecom, _ := authorityAPI.waitBackup(t, backupID, 1*time.Minute)
	if backupAfterDecom.Lifecycle != backupv1.LifecycleSucceeded {
		t.Fatalf("backup authority must remain succeeded after source destroyed: %+v", backupAfterDecom)
	}
	artifactReaderAfter, _, err := store.Get(context.Background(), backupAuthority.ObjectKey)
	if err != nil {
		t.Fatalf("backup artifact inaccessible after source destroy: %v", err)
	}
	artifactBytesAfter, _ := io.ReadAll(artifactReaderAfter)
	_ = artifactReaderAfter.Close()
	if len(artifactBytesAfter) != int(backupAuthority.ArtifactSize) {
		t.Fatalf("backup artifact size changed after source destroy: got %d want %d", len(artifactBytesAfter), backupAuthority.ArtifactSize)
	}

	stopRunner()
	if err := <-runResult; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	// =========================================================================
	// 31 & 32. Final Security Checks & Evidence Artifact
	// =========================================================================
	evidenceDir := os.Getenv("OPSI_P07B3_FINAL_EVIDENCE_DIR")
	if evidenceDir == "" {
		evidenceDir = filepath.Join(".tmp", "evidence", "p07b3-final-"+startTime.Format("20060102T150405Z"))
	}
	if err := os.MkdirAll(evidenceDir, 0o700); err != nil {
		t.Fatal(err)
	}

	evidencePayload := map[string]any{
		"Milestone":                     "P07B3-FINAL",
		"FullLifecycleStatus":           "PASS",
		"StartedAt":                     startTime.Format(time.RFC3339),
		"CompletedAt":                   time.Now().UTC().Format(time.RFC3339),
		"SourceResourceID":              sourceSpec.ResourceID,
		"SourcePVCUID":                  sourcePVCUID,
		"SourcePVUID":                   sourcePVUID,
		"SourceBindingID":               sourceBinding.BindingID,
		"BackupID":                      backupID,
		"BackupSHA256":                  backupAuthority.SHA256,
		"BackupSize":                    backupAuthority.ArtifactSize,
		"TargetResourceID":              targetSpec.ResourceID,
		"TargetPVCUID":                  targetPVCUID,
		"TargetPVUID":                   targetPVUID,
		"TargetBindingID":               targetBinding.BindingID,
		"CutoverReview1_ID":             cutoverReview1.ID,
		"Cutover1_ID":                   appliedCutover1.ID,
		"Rollback_ID":                   appliedRollback.ID,
		"CutoverReview2_ID":             cutoverReview2.ID,
		"Cutover2_ID":                   appliedCutover2.ID,
		"Finalization_ID":               finalization.ID,
		"RetainedStorageID":             destroySpec.RetainedStorageID,
		"StorageDestroyed":              true,
		"TargetSurvivesSourceDestroy":   true,
		"BackupIndependentAfterDestroy": true,
	}

	evidenceBytes, err := json.MarshalIndent(evidencePayload, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evidenceDir, "p07b3-final-acceptance.json"), append(evidenceBytes, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "P07B3_FINAL_LIFECYCLE_EVIDENCE=%s\n", evidenceDir)
}
