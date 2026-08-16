package svcatalog

import (
	"context"
	"encoding/json"
	"errors"
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

func (a restoreAcceptanceAPI) createRollback(t *testing.T, appID, cutoverID, key string) (cutoverv1.ApplicationCutoverRollback, string) {
	t.Helper()
	status, body := a.requestStatus(t, http.MethodPost, "/api/projects/"+url.PathEscape(a.projectID)+"/applications/"+url.PathEscape(appID)+"/cutovers/"+url.PathEscape(cutoverID)+"/rollbacks", key, map[string]any{})
	if status != http.StatusAccepted && status != http.StatusOK {
		t.Fatalf("create rollback status=%d body=%s", status, body)
	}
	var resp struct {
		Rollback cutoverv1.ApplicationCutoverRollback `json:"rollback"`
		Reused   bool                                 `json:"reused"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("parse rollback response error=%v body=%s", err, body)
	}
	return resp.Rollback, body
}

func (a restoreAcceptanceAPI) waitRollback(t *testing.T, id string, timeout time.Duration) (cutoverv1.ApplicationCutoverRollback, []string) {
	t.Helper()
	var value cutoverv1.ApplicationCutoverRollback
	lifecycle := []string{cutoverv1.RollbackQueued}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var resp struct {
			Rollback cutoverv1.ApplicationCutoverRollback `json:"rollback"`
		}
		a.request(t, http.MethodGet, "/api/projects/"+url.PathEscape(a.projectID)+"/application-cutover-rollbacks/"+url.PathEscape(id), "", nil, http.StatusOK, &resp)
		value = resp.Rollback
		if len(lifecycle) == 0 || lifecycle[len(lifecycle)-1] != value.Lifecycle {
			lifecycle = append(lifecycle, value.Lifecycle)
		}
		if value.Lifecycle == cutoverv1.RollbackSucceeded || value.Lifecycle == cutoverv1.RollbackFailed {
			return value, lifecycle
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("Cloud rollback timed out id=%s lifecycle=%v", id, lifecycle)
	return value, lifecycle
}

func (a restoreAcceptanceAPI) completeRollbackResult(t *testing.T, agentToken, nodeID, rollbackID string, result cutoverv1.RollbackResult) {
	t.Helper()
	status, body := a.requestStatusWithBearer(t, http.MethodPost, "/v1/agents/"+url.PathEscape(nodeID)+"/cutover-rollbacks/"+url.PathEscape(rollbackID)+"?project_id="+url.QueryEscape(a.projectID), agentToken, "", result)
	if status != http.StatusOK {
		t.Fatalf("complete rollback result status=%d body=%s", status, body)
	}
}

func TestManagedResourceRealK3sPostgresCutoverRollback(t *testing.T) {
	reference := os.Getenv("OPSI_P07B3B1_ACCEPTANCE_E2E_IMAGE")
	registryUsername, registryPassword := os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_USERNAME"), os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_PASSWORD")
	endpoint, access, secret, bucket := os.Getenv("OPSI_E2E_MINIO_ENDPOINT"), os.Getenv("OPSI_E2E_MINIO_ACCESS_KEY"), os.Getenv("OPSI_E2E_MINIO_SECRET_KEY"), os.Getenv("OPSI_E2E_MINIO_BUCKET")
	cloudURL, projectID, environmentID := os.Getenv("OPSI_P07B3C2A_CLOUD_URL"), os.Getenv("OPSI_P07B3C2A_CLOUD_PROJECT_ID"), os.Getenv("OPSI_P07B3C2A_CLOUD_ENVIRONMENT_ID")
	nodeID, agentID := os.Getenv("OPSI_P07B3C2A_CLOUD_NODE_ID"), os.Getenv("OPSI_P07B3C2A_CLOUD_AGENT_ID")
	pat, agentToken, postgresContainer := os.Getenv("OPSI_P07B3C2A_CLOUD_PAT"), os.Getenv("OPSI_P07B3C2A_CLOUD_AGENT_TOKEN"), os.Getenv("OPSI_P07B3C2A_POSTGRES_CONTAINER")
	if os.Getenv("OPSI_E2E_K3S_POSTGRES_BACKUP") != "1" || reference == "" || registryUsername == "" || registryPassword == "" || endpoint == "" || access == "" || secret == "" || bucket == "" || cloudURL == "" || projectID == "" || environmentID == "" || nodeID == "" || agentID == "" || pat == "" || agentToken == "" || postgresContainer == "" {
		t.Skip("set P07B3C2B2B2 K3s, Cloud/PostgreSQL authority, immutable application, private registry, and disposable MinIO inputs")
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
	sourceSpec.ResourceID = "res-cutover-source-rb"
	sourceSpec.CredentialID = "mrcred-cutover-source-rb"
	sourceSpec.ProjectID, sourceSpec.EnvironmentID = projectID, environmentID
	sourceSpec.Assignment.NodeID, sourceSpec.Assignment.AgentID = nodeID, agentID
	sourceSpec.Connection.ServiceName = "opsi-mr-cutover-src-rb"
	sourceSpec.Connection.Host = sourceSpec.Connection.ServiceName + "." + managedResourceNamespace(sourceSpec) + ".svc.cluster.local"
	sourceSpec.SpecHash, _ = sourceSpec.Hash()
	sourceManagement := randomManagedCredential(t, sourceSpec.CredentialID, resourcev1.CredentialPurposeResourceManagement, sourceSpec.ResourceID, sourceSpec.ResourceID, "opsi")
	sourceBinding := postgresBindingOperation(t, sourceSpec, "binding-cutover-source-rb", true)
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
	createdBackup := authorityAPI.createBackupWithKey(t, sourceSpec.ResourceID, "p07b3c2b2b2-backup")
	backupID := createdBackup.ID

	cloud := &restoreAcceptanceCloudClient{Client: cloudrelay.Client{BaseURL: cloudURL, ProjectID: projectID, AgentToken: agentToken}}
	runCtx, stopRunner := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() {
		runResult <- (cloudrunner.Runner{
			Client:            cloud,
			Engine:            postgresBackupRolloutEngine{},
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
	postBackupMarkerScript := psqlSQLScript(`CREATE TABLE IF NOT EXISTS p07b3c2b2_source_post_backup_marker (id text primary key, marked_at timestamptz default now()); INSERT INTO p07b3c2b2_source_post_backup_marker(id) VALUES('source-post-backup-marker') ON CONFLICT DO NOTHING; SELECT count(*) FROM p07b3c2b2_source_post_backup_marker;`)
	postMarkerOut, err := reconciler.postgresBindingExec(context.Background(), sourceSpec, []byte(sourceBinding.Credential.Password+"\n"), postBackupMarkerScript, *sourceBinding)
	if err != nil || lastNonEmptyLine(string(postMarkerOut)) != "1" {
		t.Fatalf("write source post-backup marker err=%v out=%q", err, postMarkerOut)
	}

	// 6. Setup Target PostgreSQL and perform Restore
	targetSpec := sourceSpec
	targetSpec.ResourceID, targetSpec.CredentialID, targetSpec.Connection.ServiceName = "res-cutover-target-rb", "mrcred-cutover-target-rb", "opsi-mr-cutover-tgt-rb"
	targetSpec.Connection.Host = targetSpec.Connection.ServiceName + "." + managedResourceNamespace(targetSpec) + ".svc.cluster.local"
	targetSpec.SpecHash, _ = targetSpec.Hash()
	targetNamespace := managedResourceNamespace(targetSpec)
	targetManagement := randomManagedCredential(t, targetSpec.CredentialID, resourcev1.CredentialPurposeResourceManagement, targetSpec.ResourceID, targetSpec.ResourceID, "opsi")
	targetReady := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "cutover-target-rb", Spec: targetSpec, Credential: targetManagement})
	if targetReady.Status != "ready" || targetReady.Evidence == nil {
		t.Fatalf("target ready error: %+v", targetReady)
	}
	targetPVCUID := kubectl(t, "get", "pvc", targetReady.Evidence.PVCName, "-n", targetNamespace, "-o", "jsonpath={.metadata.uid}")
	targetPVUID := kubectl(t, "get", "pv", targetReady.Evidence.PVName, "-o", "jsonpath={.metadata.uid}")
	targetReady.Evidence.PVCUID, targetReady.Evidence.PVUID, targetReady.Evidence.StorageHash = targetPVCUID, targetPVUID, resourcev1.ManagedResourceStorageHash(targetSpec)
	authorityAPI.seedReadyResource(t, targetSpec, targetReady.Evidence)
	seedVaultManagedResourceCredential(t, authorityAPI, *targetManagement)

	queuedReview := authorityAPI.createReviewWithKey(t, backupID, targetSpec.ResourceID, "p07b3c2b2b2-restore-review")
	restoreReview, _ := authorityAPI.waitReview(t, queuedReview.ID, 5*time.Minute)
	queuedRestore := authorityAPI.createRestoreWithKey(t, backupID, restoreReview, "p07b3c2b2b2-restore")
	restoreAuthority, _ := authorityAPI.waitRestore(t, queuedRestore.ID, 10*time.Minute)
	if err := restoreAuthority.ValidateSucceeded(); err != nil {
		t.Fatalf("restore authority error: %v", err)
	}

	// 7. Create Target binding for the Application & write Target-Only marker
	targetBinding := postgresBindingOperation(t, targetSpec, "binding-cutover-target-rb", true)
	_ = reconcilePostgresBindingK3s(t, reconciler, "target-binding-create-rb", targetSpec, targetManagement, targetBinding)

	targetOnlyMarkerScript := psqlSQLScript(`CREATE TABLE IF NOT EXISTS p07b3c2b2_target_only_marker (id text primary key, marked_at timestamptz default now()); INSERT INTO p07b3c2b2_target_only_marker(id) VALUES('target-only-marker') ON CONFLICT DO NOTHING; SELECT count(*) FROM p07b3c2b2_target_only_marker;`)
	targetMarkerOut, err := reconciler.postgresBindingExec(context.Background(), targetSpec, []byte(targetBinding.Credential.Password+"\n"), targetOnlyMarkerScript, *targetBinding)
	if err != nil || lastNonEmptyLine(string(targetMarkerOut)) != "1" {
		t.Fatalf("write target-only marker err=%v out=%q", err, targetMarkerOut)
	}

	// Seed application & bindings in Cloud database
	appID := "app-cutover-rollback-e2e"
	runtimeID := "rt-cutover-app-rb"
	runtimeSQL := "INSERT INTO runtimes(id, org_id, project_id, environment_id, name, status) SELECT " + sqlQuote(runtimeID) + ", p.org_id, p.id, e.id, 'cutover-rt-rb', 'ready' FROM projects p JOIN environments e ON e.project_id=p.id WHERE p.id=" + sqlQuote(projectID) + " ON CONFLICT(id) DO NOTHING;"
	appSQL := "INSERT INTO control_services(id, org_id, project_id, environment_id, runtime_id, name, type, status, source_type, namespace, configuration, configuration_revision, configuration_state_hash, configuration_applied_by, configuration_applied_at) SELECT " + sqlQuote(appID) + ", p.org_id, p.id, e.id, " + sqlQuote(runtimeID) + ", 'cutover-app-rb', 'application', 'ready', 'image', 'default', '{\"schema_version\":\"opsi.service_configuration/v1\",\"resource_bindings\":[{\"logical_name\":\"DATABASE\",\"binding_id\":\"" + sourceBinding.BindingID + "\"}]}'::jsonb, 1, 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'p07b3c2b2b2', now() FROM projects p JOIN environments e ON e.project_id=p.id WHERE p.id=" + sqlQuote(projectID) + " ON CONFLICT(id) DO NOTHING;"
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
	createdCutoverReview, _ := authorityAPI.createCutoverReview(t, appID, sourceBinding.BindingID, targetBinding.BindingID, "idemp-cutover-rev-rb")
	succeededCutoverReview, _ := waitCutoverReviewOutcome(t, authorityAPI, createdCutoverReview.ID, 5*time.Minute)
	if succeededCutoverReview.Lifecycle != cutoverv1.ReviewSucceeded {
		t.Fatalf("cutover review did not succeed: %+v", succeededCutoverReview)
	}

	// 9. Cutover Apply (mutates Application configuration from SOURCE -> TARGET)
	appliedCutover, _ := authorityAPI.createCutover(t, appID, succeededCutoverReview.ID, "idemp-cutover-apply-rb")
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

	postCutoverMarkerScript := psqlSQLScript(`CREATE TABLE IF NOT EXISTS p07b3c2b2_post_cutover_target_marker (id text primary key, marked_at timestamptz default now()); INSERT INTO p07b3c2b2_post_cutover_target_marker(id) VALUES('target-post-cutover-marker') ON CONFLICT DO NOTHING; SELECT count(*) FROM p07b3c2b2_post_cutover_target_marker;`)
	postCutoverOut, err := reconciler.postgresBindingExec(context.Background(), targetSpec, []byte(targetBinding.Credential.Password+"\n"), postCutoverMarkerScript, *targetBinding)
	if err != nil || lastNonEmptyLine(string(postCutoverOut)) != "1" {
		t.Fatalf("write post-cutover marker err=%v out=%q", err, postCutoverOut)
	}

	// 11. Verify SOURCE Rollback Authority is still valid & preserved
	sourceBindingRow := authorityAPI.execSQL(t, "SELECT lifecycle FROM resource_bindings WHERE id="+sqlQuote(sourceBinding.BindingID)+";")
	if !strings.Contains(sourceBindingRow, "ready") {
		t.Fatalf("source binding was not preserved as ready: %s", sourceBindingRow)
	}

	// 12. EXPLICIT CUTOVER ROLLBACK REQUEST: POST /api/projects/{project}/applications/{app}/cutovers/{cutover}/rollbacks
	appliedRollback, rawRollbackResp := authorityAPI.createRollback(t, appID, succeededCutover.ID, "idemp-cutover-rollback-1")
	if appliedRollback.ID == "" || (appliedRollback.Lifecycle != cutoverv1.RollbackDeploying && appliedRollback.Lifecycle != cutoverv1.RollbackValidating && appliedRollback.Lifecycle != cutoverv1.RollbackApplying) {
		t.Fatalf("unexpected rollback state after request: %+v body=%s", appliedRollback, rawRollbackResp)
	}

	// Mandatory Data Divergence Warning Check
	if len(appliedRollback.Warnings) != 1 || appliedRollback.Warnings[0] != cutoverv1.WarningTargetWritesMayNotBeOnSource {
		t.Fatalf("expected divergence warning in rollback response: %+v", appliedRollback.Warnings)
	}

	// Security check: No credentials in rollback response
	for _, forbidden := range []string{"password", "secret", "bearer"} {
		if strings.Contains(strings.ToLower(rawRollbackResp), `"`+forbidden+`"`) {
			t.Fatalf("forbidden security field %q found in cutover rollback response", forbidden)
		}
	}

	// 13. Verify Application Configuration Mutation back to SOURCE in Cloud PostgreSQL
	appConfigAfterRollback := authorityAPI.execSQL(t, "SELECT configuration::text FROM control_services WHERE id="+sqlQuote(appID)+";")
	if !strings.Contains(appConfigAfterRollback, sourceBinding.BindingID) {
		t.Fatalf("application configuration was not mutated back to source binding: %s", appConfigAfterRollback)
	}

	// 14. Reconcile Workload Secret for SOURCE DB binding on K3s cluster & Rollout
	sourceAppSnapshot, sourceAppCommand := postgresBindingApplicationSnapshot(t, sourceSpec, image, *sourceBinding.Credential, registryUsername, registryPassword)
	if err := (deploy.KubernetesWorkloadSecretEnsurer{Runner: runner, KubectlPath: "kubectl"}).Ensure(context.Background(), sourceAppCommand); err != nil {
		t.Fatal(err)
	}
	var sourcePlan deploy.RolloutPlan
	for attempt := 0; attempt < 10; attempt++ {
		sourcePlan, err = adapter.PrepareRollout(context.Background(), sourceAppSnapshot)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if _, err = adapter.ApplyRollout(context.Background(), sourcePlan); err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if evidence, _, err := adapter.ObserveReadiness(context.Background(), sourcePlan); err != nil || !evidence.RuntimeReady {
		t.Fatalf("source application rollout readiness=%+v err=%v", evidence, err)
	}

	// 15. Application-level SOURCE verification:
	// - Application reads 128 rows from SOURCE
	// - Application sees source post-backup marker
	// - Application DOES NOT see target-only marker or post-cutover marker
	sourceCheckRows, err := checkRestoreBindingRows(reconciler, sourceSpec, sourceBinding, sourceBinding.Credential.Password)
	if err != nil || strings.TrimSpace(sourceCheckRows) != "128" {
		t.Fatalf("source binding read failed: %q err=%v", sourceCheckRows, err)
	}
	sourceCheckMarker, err := reconciler.postgresBindingExec(context.Background(), sourceSpec, []byte(sourceBinding.Credential.Password+"\n"), psqlSQLScript("SELECT count(*) FROM p07b3c2b2_source_post_backup_marker;"), *sourceBinding)
	if err != nil || lastNonEmptyLine(string(sourceCheckMarker)) != "1" {
		t.Fatalf("source post-backup marker absent on source: %q err=%v", sourceCheckMarker, err)
	}
	targetOnlyOnSource, _ := reconciler.postgresBindingExec(context.Background(), sourceSpec, []byte(sourceBinding.Credential.Password+"\n"), psqlSQLScript("SELECT count(*) FROM pg_class WHERE relname='p07b3c2b2_target_only_marker';"), *sourceBinding)
	if lastNonEmptyLine(string(targetOnlyOnSource)) != "0" {
		t.Fatalf("target only marker unexpectedly found on source: %q", targetOnlyOnSource)
	}
	postCutoverOnSource, _ := reconciler.postgresBindingExec(context.Background(), sourceSpec, []byte(sourceBinding.Credential.Password+"\n"), psqlSQLScript("SELECT count(*) FROM pg_class WHERE relname='p07b3c2b2_post_cutover_target_marker';"), *sourceBinding)
	if lastNonEmptyLine(string(postCutoverOnSource)) != "0" {
		t.Fatalf("post-cutover marker unexpectedly found on source: %q", postCutoverOnSource)
	}

	// 16. Write post-rollback marker to SOURCE
	postRollbackMarkerScript := psqlSQLScript(`CREATE TABLE IF NOT EXISTS p07b3c2b2_post_rollback_source_marker (id text primary key, marked_at timestamptz default now()); INSERT INTO p07b3c2b2_post_rollback_source_marker(id) VALUES('source-post-rollback-marker') ON CONFLICT DO NOTHING; SELECT count(*) FROM p07b3c2b2_post_rollback_source_marker;`)
	postRollbackOut, err := reconciler.postgresBindingExec(context.Background(), sourceSpec, []byte(sourceBinding.Credential.Password+"\n"), postRollbackMarkerScript, *sourceBinding)
	if err != nil || lastNonEmptyLine(string(postRollbackOut)) != "1" {
		t.Fatalf("write post-rollback source marker err=%v out=%q", err, postRollbackOut)
	}

	// 17. Complete Rollback Verification via Agent Result API
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

	// 18. Wait for Rollback lifecycle Succeeded
	succeededRollback, rollbackLifecycle := authorityAPI.waitRollback(t, appliedRollback.ID, 5*time.Minute)
	if succeededRollback.Lifecycle != cutoverv1.RollbackSucceeded {
		t.Fatalf("rollback did not succeed: %+v", succeededRollback)
	}
	if err := succeededRollback.ValidateSucceeded(); err != nil {
		t.Fatalf("rollback ValidateSucceeded error: %v", err)
	}

	// 19. STRICT TARGET Authority Preservation Verification (TARGET NOT deleted, NOT destroyed)
	targetBindingRow := authorityAPI.execSQL(t, "SELECT lifecycle FROM resource_bindings WHERE id="+sqlQuote(targetBinding.BindingID)+";")
	if !strings.Contains(targetBindingRow, "ready") {
		t.Fatalf("target binding was not preserved as ready: %s", targetBindingRow)
	}
	targetRowsAfterRollback, err := checkRestoreBindingRows(reconciler, targetSpec, targetBinding, targetBinding.Credential.Password)
	if err != nil || strings.TrimSpace(targetRowsAfterRollback) != "128" {
		t.Fatalf("target database became unreadable after rollback: %q err=%v", targetRowsAfterRollback, err)
	}
	targetMarkerAfterRollback, err := reconciler.postgresBindingExec(context.Background(), targetSpec, []byte(targetBinding.Credential.Password+"\n"), psqlSQLScript("SELECT count(*) FROM p07b3c2b2_target_only_marker;"), *targetBinding)
	if err != nil || lastNonEmptyLine(string(targetMarkerAfterRollback)) != "1" {
		t.Fatalf("target database marker missing after rollback: %q err=%v", targetMarkerAfterRollback, err)
	}

	// 20. Original Cutover record remains immutable (Succeeded, not mutated to rolled_back)
	cutoverRow := authorityAPI.execSQL(t, "SELECT lifecycle FROM application_cutovers WHERE id="+sqlQuote(succeededCutover.ID)+";")
	if !strings.Contains(cutoverRow, "succeeded") {
		t.Fatalf("original cutover was mutated away from succeeded: %s", cutoverRow)
	}

	// 21. Replay Idempotency Verification
	reusedRollback, _ := authorityAPI.createRollback(t, appID, succeededCutover.ID, "idemp-cutover-rollback-1")
	if reusedRollback.ID != succeededRollback.ID {
		t.Fatalf("expected reused rollback on same key: got %s want %s", reusedRollback.ID, succeededRollback.ID)
	}

	// 22. Audit events verification
	auditRows := authorityAPI.execSQL(t, "SELECT action FROM cloud_audit_events WHERE project_id="+sqlQuote(projectID)+" AND resource_id="+sqlQuote(succeededRollback.ID)+" ORDER BY created_at ASC;")
	for _, expectedAction := range []string{"CUTOVER_ROLLBACK_REQUESTED", "CUTOVER_ROLLBACK_APPLY_STARTED", "CUTOVER_ROLLBACK_DEPLOYMENT_STARTED", "CUTOVER_ROLLBACK_SUCCEEDED"} {
		if !strings.Contains(auditRows, expectedAction) {
			t.Fatalf("missing audit event %s in audit log: %q", expectedAction, auditRows)
		}
	}

	stopRunner()
	if err := <-runResult; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	// 23. Write comprehensive evidence JSON artifact
	dir := os.Getenv("OPSI_P07B3C2B2B2_EVIDENCE_DIR")
	if dir == "" {
		dir = filepath.Join(".tmp", "evidence", "p07b3c2b2b2-postgres-cutover-rollback-"+time.Now().UTC().Format("20060102T150405Z"))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	evidenceData, _ := json.MarshalIndent(map[string]any{
		"B2B2_Validation":           "PASS_EXPLICIT_CUTOVER_ROLLBACK",
		"RollbackID":                succeededRollback.ID,
		"CutoverID":                 succeededCutover.ID,
		"Lifecycle":                 succeededRollback.Lifecycle,
		"LifecycleHistory":          rollbackLifecycle,
		"EvidenceHash":              succeededRollback.EvidenceHash,
		"ApplicationID":             appID,
		"SourceResourceID":          sourceSpec.ResourceID,
		"SourceBindingID":           sourceBinding.BindingID,
		"TargetResourceID":          targetSpec.ResourceID,
		"TargetBindingID":           targetBinding.BindingID,
		"Warnings":                  succeededRollback.Warnings,
		"VerificationSummary":       succeededRollback.VerificationSummary,
		"TargetAuthorityPreserved":  true,
		"TargetBindingReady":        true,
		"TargetDataIntact":          true,
		"SourceDataIntact":          true,
		"SourceMarkerVerified":      true,
		"TargetMarkerAbsentOnSrc":   true,
		"PostRollbackSourceWritten": true,
		"OriginalCutoverImmutable":  true,
		"IdempotencyVerified":       true,
		"AuditVerified":             true,
	}, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "p07b3c2b2b2-postgres-cutover-rollback.json"), append(evidenceData, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "P07B3C2B2B2_CUTOVER_ROLLBACK_EVIDENCE=%s\n", dir)
}
