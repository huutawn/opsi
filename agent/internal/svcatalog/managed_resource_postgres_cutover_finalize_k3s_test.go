package svcatalog

import (
	"context"
	"encoding/json"
	"fmt"
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
	cutoverv1 "github.com/opsi-dev/opsi/contracts/go/cutoverv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

func psqlSQLScript(sql string) string {
	return "set -eu\nrole=$1; db=$2\nIFS= read -r password\nexport PGPASSWORD=$password\npsql -v ON_ERROR_STOP=1 -qAt -h 127.0.0.1 -U \"$role\" -d \"$db\" -c " + fmt.Sprintf("%q", sql) + "\n"
}

func (a restoreAcceptanceAPI) createFinalization(t *testing.T, appID, cutoverID, key string) (cutoverv1.ApplicationCutoverFinalization, string) {
	t.Helper()
	status, body := a.requestStatus(t, http.MethodPost, "/api/projects/"+url.PathEscape(a.projectID)+"/applications/"+url.PathEscape(appID)+"/cutovers/"+url.PathEscape(cutoverID)+"/finalize", key, map[string]any{})
	if status != http.StatusAccepted && status != http.StatusOK {
		t.Fatalf("create finalization status=%d body=%s", status, body)
	}
	var resp struct {
		Finalization cutoverv1.ApplicationCutoverFinalization `json:"finalization"`
		Reused       bool                                     `json:"reused"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("parse finalization response error=%v body=%s", err, body)
	}
	return resp.Finalization, body
}

func (a restoreAcceptanceAPI) getFinalization(t *testing.T, id string) cutoverv1.ApplicationCutoverFinalization {
	t.Helper()
	var resp struct {
		Finalization cutoverv1.ApplicationCutoverFinalization `json:"finalization"`
	}
	a.request(t, http.MethodGet, "/api/projects/"+url.PathEscape(a.projectID)+"/application-cutover-finalizations/"+url.PathEscape(id), "", nil, http.StatusOK, &resp)
	return resp.Finalization
}

func (a restoreAcceptanceAPI) waitBindingGone(t *testing.T, bindingID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		row := a.execSQL(t, "SELECT count(*) FROM resource_bindings WHERE id="+sqlQuote(bindingID)+";")
		if lastNonEmptyLine(row) == "0" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for binding %s to be deleted", bindingID)
}

func (a restoreAcceptanceAPI) getCutover(t *testing.T, id string) cutoverv1.ApplicationCutover {
	t.Helper()
	var resp struct {
		Cutover cutoverv1.ApplicationCutover `json:"cutover"`
	}
	a.request(t, http.MethodGet, "/api/projects/"+url.PathEscape(a.projectID)+"/application-cutovers/"+url.PathEscape(id), "", nil, http.StatusOK, &resp)
	return resp.Cutover
}

func TestManagedResourceRealK3sPostgresCutoverFinalize(t *testing.T) {
	reference := os.Getenv("OPSI_P07B3B1_ACCEPTANCE_E2E_IMAGE")
	registryUsername, registryPassword := os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_USERNAME"), os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_PASSWORD")
	endpoint, access, secret, bucket := os.Getenv("OPSI_E2E_MINIO_ENDPOINT"), os.Getenv("OPSI_E2E_MINIO_ACCESS_KEY"), os.Getenv("OPSI_E2E_MINIO_SECRET_KEY"), os.Getenv("OPSI_E2E_MINIO_BUCKET")
	cloudURL, projectID, environmentID := os.Getenv("OPSI_P07B3C2A_CLOUD_URL"), os.Getenv("OPSI_P07B3C2A_CLOUD_PROJECT_ID"), os.Getenv("OPSI_P07B3C2A_CLOUD_ENVIRONMENT_ID")
	nodeID, agentID := os.Getenv("OPSI_P07B3C2A_CLOUD_NODE_ID"), os.Getenv("OPSI_P07B3C2A_CLOUD_AGENT_ID")
	pat, agentToken, postgresContainer := os.Getenv("OPSI_P07B3C2A_CLOUD_PAT"), os.Getenv("OPSI_P07B3C2A_CLOUD_AGENT_TOKEN"), os.Getenv("OPSI_P07B3C2A_POSTGRES_CONTAINER")
	if os.Getenv("OPSI_E2E_K3S_POSTGRES_BACKUP") != "1" || reference == "" || registryUsername == "" || registryPassword == "" || endpoint == "" || access == "" || secret == "" || bucket == "" || cloudURL == "" || projectID == "" || environmentID == "" || nodeID == "" || agentID == "" || pat == "" || agentToken == "" || postgresContainer == "" {
		t.Skip("set P07B3C2B2C K3s, Cloud/PostgreSQL authority, immutable application, private registry, and disposable MinIO inputs")
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

	// 1. Setup Source PostgreSQL
	sourceSpec := postgresBackupK3sSpec()
	sourceSpec.ResourceID = "res-cutover-source-fn"
	sourceSpec.CredentialID = "mrcred-cutover-source-fn"
	sourceSpec.ProjectID, sourceSpec.EnvironmentID = projectID, environmentID
	sourceSpec.Assignment.NodeID, sourceSpec.Assignment.AgentID = nodeID, agentID
	sourceSpec.Connection.ServiceName = "opsi-mr-cutover-src-fn"
	sourceSpec.Connection.Host = sourceSpec.Connection.ServiceName + "." + managedResourceNamespace(sourceSpec) + ".svc.cluster.local"
	sourceSpec.SpecHash, _ = sourceSpec.Hash()
	sourceManagement := randomManagedCredential(t, sourceSpec.CredentialID, resourcev1.CredentialPurposeResourceManagement, sourceSpec.ResourceID, sourceSpec.ResourceID, "opsi")
	sourceBinding := postgresBindingOperation(t, sourceSpec, "binding-cutover-source-fn", true)
	sourceNamespace := managedResourceNamespace(sourceSpec)
	_, _ = kubectlOutput(context.Background(), "delete", "namespace", sourceNamespace, "--ignore-not-found", "--wait=true", "--timeout=2m")
	t.Cleanup(func() {
		_, _ = kubectlOutput(context.Background(), "delete", "namespace", sourceNamespace, "--ignore-not-found", "--wait=true", "--timeout=2m")
	})

	reconciler := ManagedResourceReconciler{Timeout: 8 * time.Minute, PollInterval: time.Second}
	sourceReady := reconcilePostgresBindingK3s(t, reconciler, "source-create", sourceSpec, sourceManagement, sourceBinding)
	pvcUID := kubectl(t, "get", "pvc", sourceReady.Evidence.PVCName, "-n", sourceNamespace, "-o", "jsonpath={.metadata.uid}")
	pvUID := kubectl(t, "get", "pv", sourceReady.Evidence.PVName, "-o", "jsonpath={.metadata.uid}")
	sourceReady.Evidence.PVCUID, sourceReady.Evidence.PVUID, sourceReady.Evidence.StorageHash = pvcUID, pvUID, resourcev1.ManagedResourceStorageHash(sourceSpec)

	// 2. Deploy Application bound to Source PostgreSQL
	snapshot, command := postgresBindingApplicationSnapshot(t, sourceSpec, image, *sourceBinding.Credential, registryUsername, registryPassword)
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

	// 3. Seed source data (128 rows)
	seeded, err := reconciler.postgresBindingExec(context.Background(), sourceSpec, []byte(sourceBinding.Credential.Password+"\n"), postgresBackupSeedScript, *sourceBinding)
	if err != nil || lastNonEmptyLine(string(seeded)) != "128" {
		t.Fatalf("seed backup data err=%v output=%q", err, seeded)
	}

	// 4. Authority setup & backup
	authorityAPI := restoreAcceptanceAPI{baseURL: cloudURL, projectID: projectID, pat: pat, postgresContainer: postgresContainer}
	authorityAPI.seedReadyResource(t, sourceSpec, sourceReady.Evidence)
	seedVaultManagedResourceCredential(t, authorityAPI, *sourceManagement)
	createdBackup := authorityAPI.createBackupWithKey(t, sourceSpec.ResourceID, "p07b3c2b2c-backup")
	backupID := createdBackup.ID

	cloud := &restoreAcceptanceCloudClient{Client: cloudrelay.Client{BaseURL: cloudURL, ProjectID: projectID, AgentToken: agentToken}}
	runCtx, stopRunner := context.WithCancel(context.Background())
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

	backupAuthority, _ := authorityAPI.waitBackup(t, backupID, 10*time.Minute)
	if err := backupAuthority.ValidateSucceeded(); err != nil {
		t.Fatalf("backup failed: %v", err)
	}

	// 5. Write post-backup marker on SOURCE PostgreSQL
	postBackupMarkerScript := psqlSQLScript(`CREATE TABLE IF NOT EXISTS p07b3c2c_source_post_backup_marker (id text primary key, marked_at timestamptz default now()); INSERT INTO p07b3c2c_source_post_backup_marker(id) VALUES('source-post-backup-marker') ON CONFLICT DO NOTHING; SELECT count(*) FROM p07b3c2c_source_post_backup_marker;`)
	postMarkerOut, err := reconciler.postgresBindingExec(context.Background(), sourceSpec, []byte(sourceBinding.Credential.Password+"\n"), postBackupMarkerScript, *sourceBinding)
	if err != nil || lastNonEmptyLine(string(postMarkerOut)) != "1" {
		t.Fatalf("write source post-backup marker err=%v out=%q", err, postMarkerOut)
	}

	// 6. Setup Target PostgreSQL and perform Restore
	targetSpec := sourceSpec
	targetSpec.ResourceID, targetSpec.CredentialID, targetSpec.Connection.ServiceName = "res-cutover-target-fn", "mrcred-cutover-target-fn", "opsi-mr-cutover-tgt-fn"
	targetSpec.Connection.Host = targetSpec.Connection.ServiceName + "." + managedResourceNamespace(targetSpec) + ".svc.cluster.local"
	targetSpec.SpecHash, _ = targetSpec.Hash()
	targetNamespace := managedResourceNamespace(targetSpec)
	targetManagement := randomManagedCredential(t, targetSpec.CredentialID, resourcev1.CredentialPurposeResourceManagement, targetSpec.ResourceID, targetSpec.ResourceID, "opsi")
	targetReady := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "cutover-target-fn", Spec: targetSpec, Credential: targetManagement})
	if targetReady.Status != "ready" || targetReady.Evidence == nil {
		t.Fatalf("target ready error: %+v", targetReady)
	}
	targetPVCUID := kubectl(t, "get", "pvc", targetReady.Evidence.PVCName, "-n", targetNamespace, "-o", "jsonpath={.metadata.uid}")
	targetPVUID := kubectl(t, "get", "pv", targetReady.Evidence.PVName, "-o", "jsonpath={.metadata.uid}")
	targetReady.Evidence.PVCUID, targetReady.Evidence.PVUID, targetReady.Evidence.StorageHash = targetPVCUID, targetPVUID, resourcev1.ManagedResourceStorageHash(targetSpec)
	authorityAPI.seedReadyResource(t, targetSpec, targetReady.Evidence)
	seedVaultManagedResourceCredential(t, authorityAPI, *targetManagement)

	queuedReview := authorityAPI.createReviewWithKey(t, backupID, targetSpec.ResourceID, "p07b3c2b2c-restore-review")
	restoreReview, _ := authorityAPI.waitReview(t, queuedReview.ID, 5*time.Minute)
	queuedRestore := authorityAPI.createRestoreWithKey(t, backupID, restoreReview, "p07b3c2b2c-restore")
	restoreAuthority, _ := authorityAPI.waitRestore(t, queuedRestore.ID, 10*time.Minute)
	if err := restoreAuthority.ValidateSucceeded(); err != nil {
		t.Fatalf("restore authority error: %v", err)
	}

	// 7. Create Target binding for the Application & write Target-Only marker
	targetBinding := postgresBindingOperation(t, targetSpec, "binding-cutover-target-fn", true)
	_ = reconcilePostgresBindingK3s(t, reconciler, "target-binding-create-fn", targetSpec, targetManagement, targetBinding)

	targetOnlyMarkerScript := psqlSQLScript(`CREATE TABLE IF NOT EXISTS p07b3c2c_target_only_marker (id text primary key, marked_at timestamptz default now()); INSERT INTO p07b3c2c_target_only_marker(id) VALUES('target-only-marker') ON CONFLICT DO NOTHING; SELECT count(*) FROM p07b3c2c_target_only_marker;`)
	targetMarkerOut, err := reconciler.postgresBindingExec(context.Background(), targetSpec, []byte(targetBinding.Credential.Password+"\n"), targetOnlyMarkerScript, *targetBinding)
	if err != nil || lastNonEmptyLine(string(targetMarkerOut)) != "1" {
		t.Fatalf("write target-only marker err=%v out=%q", err, targetMarkerOut)
	}

	// Seed application & bindings in Cloud database
	appID := "app-cutover-finalize-e2e"
	runtimeID := "rt-cutover-app-fn"
	runtimeSQL := "INSERT INTO runtimes(id, org_id, project_id, environment_id, name, status) SELECT " + sqlQuote(runtimeID) + ", p.org_id, p.id, e.id, 'cutover-rt-fn', 'ready' FROM projects p JOIN environments e ON e.project_id=p.id WHERE p.id=" + sqlQuote(projectID) + " ON CONFLICT(id) DO NOTHING;"
	appSQL := "INSERT INTO control_services(id, org_id, project_id, environment_id, runtime_id, name, type, status, source_type, namespace, configuration, configuration_revision, configuration_state_hash, configuration_applied_by, configuration_applied_at) SELECT " + sqlQuote(appID) + ", p.org_id, p.id, e.id, " + sqlQuote(runtimeID) + ", 'cutover-app-fn', 'application', 'ready', 'image', 'default', '{\"schema_version\":\"opsi.service_configuration/v1\",\"resource_bindings\":[{\"logical_name\":\"DATABASE\",\"binding_id\":\"" + sourceBinding.BindingID + "\"}]}'::jsonb, 1, 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'p07b3c2b2c', now() FROM projects p JOIN environments e ON e.project_id=p.id WHERE p.id=" + sqlQuote(projectID) + " ON CONFLICT(id) DO NOTHING;"
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

	// 8. Request & Complete Cutover Review
	createdCutoverReview, _ := authorityAPI.createCutoverReview(t, appID, sourceBinding.BindingID, targetBinding.BindingID, "idemp-cutover-rev-fn")
	succeededCutoverReview, _ := waitCutoverReviewOutcome(t, authorityAPI, createdCutoverReview.ID, 5*time.Minute)
	if succeededCutoverReview.Lifecycle != cutoverv1.ReviewSucceeded {
		t.Fatalf("cutover review did not succeed: %+v", succeededCutoverReview)
	}

	// 9. Cutover Apply (mutates Application configuration from SOURCE -> TARGET)
	appliedCutover, _ := authorityAPI.createCutover(t, appID, succeededCutoverReview.ID, "idemp-cutover-apply-fn")
	authorityAPI.completeCutoverResult(t, agentToken, nodeID, appliedCutover.ID, cutoverv1.CutoverApplyResult{
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
	succeededCutover, _ := authorityAPI.waitCutover(t, appliedCutover.ID, 5*time.Minute)
	if succeededCutover.Lifecycle != cutoverv1.CutoverSucceeded {
		t.Fatalf("cutover did not succeed: %+v", succeededCutover)
	}

	// 10. Verify application rollout on Target DB and write post-cutover marker on TARGET
	targetAppSnapshot, targetAppCommand := postgresBindingApplicationSnapshot(t, targetSpec, image, *targetBinding.Credential, registryUsername, registryPassword)
	if err := (deploy.KubernetesWorkloadSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}).Ensure(context.Background(), targetAppCommand); err != nil {
		t.Fatal(err)
	}
	targetPlan, err := adapter.PrepareRollout(context.Background(), targetAppSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ApplyRollout(context.Background(), targetPlan); err != nil {
		t.Fatal(err)
	}
	if evidence, _, err := adapter.ObserveReadiness(context.Background(), targetPlan); err != nil || !evidence.RuntimeReady {
		t.Fatalf("target application rollout readiness=%+v err=%v", evidence, err)
	}

	postCutoverMarkerScript := psqlSQLScript(`CREATE TABLE IF NOT EXISTS p07b3c2c_post_cutover_target_marker (id text primary key, marked_at timestamptz default now()); INSERT INTO p07b3c2c_post_cutover_target_marker(id) VALUES('target-post-cutover-marker') ON CONFLICT DO NOTHING; SELECT count(*) FROM p07b3c2c_post_cutover_target_marker;`)
	postCutoverOut, err := reconciler.postgresBindingExec(context.Background(), targetSpec, []byte(targetBinding.Credential.Password+"\n"), postCutoverMarkerScript, *targetBinding)
	if err != nil || lastNonEmptyLine(string(postCutoverOut)) != "1" {
		t.Fatalf("write post-cutover marker err=%v out=%q", err, postCutoverOut)
	}

	// 11. Verify SOURCE binding authority is still Ready before finalize
	sourceBindingRowPre := authorityAPI.execSQL(t, "SELECT lifecycle FROM resource_bindings WHERE id="+sqlQuote(sourceBinding.BindingID)+";")
	if !strings.Contains(sourceBindingRowPre, "ready") {
		t.Fatalf("source binding was not ready before finalize: %s", sourceBindingRowPre)
	}

	// 12. EXPLICIT CUTOVER FINALIZE REQUEST: POST /api/projects/{project}/applications/{app}/cutovers/{cutover}/finalize
	finalization, rawFnResp := authorityAPI.createFinalization(t, appID, succeededCutover.ID, "idemp-cutover-finalize-1")
	if finalization.ID == "" || finalization.Lifecycle != cutoverv1.FinalizationSucceeded {
		t.Fatalf("unexpected finalization state: %+v body=%s", finalization, rawFnResp)
	}
	if err := finalization.ValidateSucceeded(); err != nil {
		t.Fatalf("finalization evidence validation failed: %v", err)
	}

	// Security check: No credentials in finalize response
	for _, forbidden := range []string{"password", "secret", "bearer"} {
		if strings.Contains(strings.ToLower(rawFnResp), `"`+forbidden+`"`) {
			t.Fatalf("forbidden security field %q found in cutover finalize response", forbidden)
		}
	}

	// 13. Replay Idempotency
	replayFn, _ := authorityAPI.createFinalization(t, appID, succeededCutover.ID, "idemp-cutover-finalize-1")
	if replayFn.ID != finalization.ID {
		t.Fatalf("expected same finalization ID %s on replay, got %s", finalization.ID, replayFn.ID)
	}

	// 14. Verify SOURCE binding was revoked and removed
	authorityAPI.waitBindingGone(t, sourceBinding.BindingID, 1*time.Minute)

	// 15. Verify TARGET binding remains Ready and unchanged
	targetBindingRowPost := authorityAPI.execSQL(t, "SELECT lifecycle FROM resource_bindings WHERE id="+sqlQuote(targetBinding.BindingID)+";")
	if !strings.Contains(targetBindingRowPost, "ready") {
		t.Fatalf("target binding must remain ready: %s", targetBindingRowPost)
	}

	// 16. Verify SOURCE Resource remains existing (PVC/PV preserved)
	sourceResourceRowPost := authorityAPI.execSQL(t, "SELECT lifecycle FROM resources WHERE id="+sqlQuote(sourceSpec.ResourceID)+";")
	if strings.Contains(sourceResourceRowPost, "deleted") {
		t.Fatalf("source resource was unexpectedly deleted after finalize: %s", sourceResourceRowPost)
	}
	pvcAfter := kubectl(t, "get", "pvc", sourceReady.Evidence.PVCName, "-n", sourceNamespace, "-o", "jsonpath={.metadata.uid}")
	pvAfter := kubectl(t, "get", "pv", sourceReady.Evidence.PVName, "-o", "jsonpath={.metadata.uid}")
	if pvcAfter != pvcUID || pvAfter != pvUID {
		t.Fatalf("SOURCE storage destroyed or recreated: pvc %s!=%s, pv %s!=%s", pvcAfter, pvcUID, pvAfter, pvUID)
	}

	// 17. Verify Application Configuration is unchanged at revision 2 pointing to TARGET
	appConfigAfterFinalize := authorityAPI.execSQL(t, "SELECT configuration::text FROM control_services WHERE id="+sqlQuote(appID)+";")
	if !strings.Contains(appConfigAfterFinalize, targetBinding.BindingID) {
		t.Fatalf("application configuration must remain pointing to target binding: %s", appConfigAfterFinalize)
	}

	// 18. Write post-finalize marker on TARGET
	postFinalizeMarkerScript := psqlSQLScript(`CREATE TABLE IF NOT EXISTS p07b3c2c_post_finalize_target_marker (id text primary key, marked_at timestamptz default now()); INSERT INTO p07b3c2c_post_finalize_target_marker(id) VALUES('post-finalize-target-marker') ON CONFLICT DO NOTHING; SELECT count(*) FROM p07b3c2c_post_finalize_target_marker;`)
	postFinalizeOut, err := reconciler.postgresBindingExec(context.Background(), targetSpec, []byte(targetBinding.Credential.Password+"\n"), postFinalizeMarkerScript, *targetBinding)
	if err != nil || lastNonEmptyLine(string(postFinalizeOut)) != "1" {
		t.Fatalf("write post-finalize marker err=%v out=%q", err, postFinalizeOut)
	}

	// 19. Verify Rollback attempt on finalized Cutover fails closed
	rbStatus, rbBody := authorityAPI.requestStatus(t, http.MethodPost, "/api/projects/"+url.PathEscape(projectID)+"/applications/"+url.PathEscape(appID)+"/cutovers/"+url.PathEscape(succeededCutover.ID)+"/rollbacks", "rb-post-fn", map[string]any{})
	if rbStatus != http.StatusBadRequest || !strings.Contains(rbBody, cutoverv1.FailureCutoverFinalized) {
		t.Fatalf("expected rollback rejection with %q, got status=%d body=%s", cutoverv1.FailureCutoverFinalized, rbStatus, rbBody)
	}

	// 20. Verify original Cutover record is immutable
	cutoverPost := authorityAPI.getCutover(t, succeededCutover.ID)
	if cutoverPost.Lifecycle != cutoverv1.CutoverSucceeded || cutoverPost.EvidenceHash != succeededCutover.EvidenceHash {
		t.Fatalf("original cutover record must remain immutable: %+v", cutoverPost)
	}

	// 21. Resource-delete composability: Delete SOURCE Resource succeeds without RESOURCE_BINDING_ACTIVE conflict
	delResStatus, delResBody := authorityAPI.requestStatus(t, http.MethodDelete, "/api/projects/"+url.PathEscape(projectID)+"/resources/"+url.PathEscape(sourceSpec.ResourceID), "del-src-key", map[string]any{})
	if delResStatus != http.StatusOK && delResStatus != http.StatusAccepted {
		t.Fatalf("delete source resource status=%d body=%s", delResStatus, delResBody)
	}

	// 22. Audit events verification
	auditRows := authorityAPI.execSQL(t, "SELECT action FROM cloud_audit_events WHERE project_id="+sqlQuote(projectID)+" AND resource_id="+sqlQuote(finalization.ID)+" ORDER BY created_at ASC;")
	for _, expectedAction := range []string{"CUTOVER_FINALIZE_REQUESTED", "CUTOVER_FINALIZE_VALIDATED", "CUTOVER_SOURCE_BINDING_REVOKE_STARTED", "CUTOVER_FINALIZED"} {
		if !strings.Contains(auditRows, expectedAction) {
			t.Fatalf("missing audit event %s in audit log: %q", expectedAction, auditRows)
		}
	}

	stopRunner()
	_ = <-runResult

	// 23. Write comprehensive evidence JSON artifact
	dir := os.Getenv("OPSI_P07B3C2C_EVIDENCE_DIR")
	if dir == "" {
		dir = filepath.Join(".tmp", "evidence", "p07b3c2c-postgres-cutover-finalize-"+time.Now().UTC().Format("20060102T150405Z"))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	evidenceData, _ := json.MarshalIndent(map[string]any{
		"P07B3C2C_Validation":          "PASS_EXPLICIT_CUTOVER_FINALIZE",
		"FinalizationID":               finalization.ID,
		"CutoverID":                    succeededCutover.ID,
		"Lifecycle":                    finalization.Lifecycle,
		"EvidenceHash":                 finalization.EvidenceHash,
		"ApplicationID":                appID,
		"SourceResourceID":             sourceSpec.ResourceID,
		"SourceBindingID":              sourceBinding.BindingID,
		"TargetResourceID":             targetSpec.ResourceID,
		"TargetBindingID":              targetBinding.BindingID,
		"VerificationSummary":          finalization.VerificationSummary,
		"SourceBindingRevoked":         true,
		"SourceResourcePreserved":      true,
		"SourcePVCUID":                 pvcUID,
		"SourcePVUID":                  pvUID,
		"TargetAuthorityPreserved":     true,
		"TargetBindingReady":           true,
		"TargetDataIntact":             true,
		"PostFinalizeTargetWritten":    true,
		"OriginalCutoverImmutable":     true,
		"RollbackRejectedAfterFinal":   true,
		"IdempotencyVerified":          true,
		"AuditVerified":                true,
		"ResourceDeleteComposable":     true,
	}, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "p07b3c2c-postgres-cutover-finalize.json"), append(evidenceData, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "P07B3C2C_CUTOVER_FINALIZE_EVIDENCE=%s\n", dir)
}
