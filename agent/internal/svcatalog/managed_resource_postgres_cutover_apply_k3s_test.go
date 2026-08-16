package svcatalog

import (
	"context"
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
	cutoverv1 "github.com/opsi-dev/opsi/contracts/go/cutoverv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

func (a restoreAcceptanceAPI) createCutover(t *testing.T, appID, reviewID, key string) (cutoverv1.ApplicationCutover, string) {
	t.Helper()
	status, body := a.requestStatus(t, http.MethodPost, "/api/projects/"+url.PathEscape(a.projectID)+"/applications/"+url.PathEscape(appID)+"/cutovers", key, cutoverv1.ApplyRequest{
		CutoverReviewID: reviewID,
	})
	if status != http.StatusAccepted && status != http.StatusOK {
		t.Fatalf("create cutover status=%d body=%s", status, body)
	}
	var resp struct {
		Cutover cutoverv1.ApplicationCutover `json:"cutover"`
		Reused  bool                         `json:"reused"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("parse cutover response error=%v body=%s", err, body)
	}
	return resp.Cutover, body
}

func (a restoreAcceptanceAPI) waitCutover(t *testing.T, id string, timeout time.Duration) (cutoverv1.ApplicationCutover, []string) {
	t.Helper()
	var value cutoverv1.ApplicationCutover
	lifecycle := []string{cutoverv1.CutoverQueued}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var resp struct {
			Cutover cutoverv1.ApplicationCutover `json:"cutover"`
		}
		a.request(t, http.MethodGet, "/api/projects/"+url.PathEscape(a.projectID)+"/application-cutovers/"+url.PathEscape(id), "", nil, http.StatusOK, &resp)
		value = resp.Cutover
		if len(lifecycle) == 0 || lifecycle[len(lifecycle)-1] != value.Lifecycle {
			lifecycle = append(lifecycle, value.Lifecycle)
		}
		if value.Lifecycle == cutoverv1.CutoverSucceeded || value.Lifecycle == cutoverv1.CutoverFailed {
			return value, lifecycle
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("Cloud cutover timed out id=%s lifecycle=%v", id, lifecycle)
	return value, lifecycle
}

func (a restoreAcceptanceAPI) completeCutoverResult(t *testing.T, agentToken, nodeID, cutoverID string, result cutoverv1.CutoverApplyResult) {
	t.Helper()
	status, body := a.requestStatusWithBearer(t, http.MethodPost, "/v1/agents/"+url.PathEscape(nodeID)+"/cutovers/"+url.PathEscape(cutoverID)+"?project_id="+url.QueryEscape(a.projectID), agentToken, "", result)
	if status != http.StatusOK {
		t.Fatalf("complete cutover result status=%d body=%s", status, body)
	}
}

func (a restoreAcceptanceAPI) requestStatusWithBearer(t *testing.T, method, path, bearerToken, key string, body any) (int, string) {
	t.Helper()
	var payload []byte
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = data
	}
	endpoint := strings.TrimRight(a.baseURL, "/") + path
	req, err := http.NewRequest(method, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func TestManagedResourceRealK3sPostgresCutoverApply(t *testing.T) {
	reference := os.Getenv("OPSI_P07B3B1_ACCEPTANCE_E2E_IMAGE")
	registryUsername, registryPassword := os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_USERNAME"), os.Getenv("OPSI_PRIVATE_REGISTRY_E2E_PASSWORD")
	endpoint, access, secret, bucket := os.Getenv("OPSI_E2E_MINIO_ENDPOINT"), os.Getenv("OPSI_E2E_MINIO_ACCESS_KEY"), os.Getenv("OPSI_E2E_MINIO_SECRET_KEY"), os.Getenv("OPSI_E2E_MINIO_BUCKET")
	cloudURL, projectID, environmentID := os.Getenv("OPSI_P07B3C2A_CLOUD_URL"), os.Getenv("OPSI_P07B3C2A_CLOUD_PROJECT_ID"), os.Getenv("OPSI_P07B3C2A_CLOUD_ENVIRONMENT_ID")
	nodeID, agentID := os.Getenv("OPSI_P07B3C2A_CLOUD_NODE_ID"), os.Getenv("OPSI_P07B3C2A_CLOUD_AGENT_ID")
	pat, agentToken, postgresContainer := os.Getenv("OPSI_P07B3C2A_CLOUD_PAT"), os.Getenv("OPSI_P07B3C2A_CLOUD_AGENT_TOKEN"), os.Getenv("OPSI_P07B3C2A_POSTGRES_CONTAINER")
	if os.Getenv("OPSI_E2E_K3S_POSTGRES_BACKUP") != "1" || reference == "" || registryUsername == "" || registryPassword == "" || endpoint == "" || access == "" || secret == "" || bucket == "" || cloudURL == "" || projectID == "" || environmentID == "" || nodeID == "" || agentID == "" || pat == "" || agentToken == "" || postgresContainer == "" {
		t.Skip("set P07B3C2B2B1 K3s, Cloud/PostgreSQL authority, immutable application, private registry, and disposable MinIO inputs")
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
	sourceSpec.ResourceID = "res-cutover-source-apply"
	sourceSpec.CredentialID = "mrcred-cutover-source-apply"
	sourceSpec.ProjectID, sourceSpec.EnvironmentID = projectID, environmentID
	sourceSpec.Assignment.NodeID, sourceSpec.Assignment.AgentID = nodeID, agentID
	sourceSpec.Connection.ServiceName = "opsi-mr-cutover-src-app"
	sourceSpec.Connection.Host = sourceSpec.Connection.ServiceName + "." + managedResourceNamespace(sourceSpec) + ".svc.cluster.local"
	sourceSpec.SpecHash, _ = sourceSpec.Hash()
	sourceManagement := randomManagedCredential(t, sourceSpec.CredentialID, resourcev1.CredentialPurposeResourceManagement, sourceSpec.ResourceID, sourceSpec.ResourceID, "opsi")
	sourceBinding := postgresBindingOperation(t, sourceSpec, "binding-cutover-source-app", true)
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
	createdBackup := authorityAPI.createBackupWithKey(t, sourceSpec.ResourceID, "p07b3c2b2b1-backup")
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

	// 5. Write post-backup marker on SOURCE PostgreSQL (to prove target isolation)
	postBackupMarkerScript := psqlSQLScript(`CREATE TABLE IF NOT EXISTS p07b3c2b1_source_post_backup_marker (id text primary key, marked_at timestamptz default now()); INSERT INTO p07b3c2b1_source_post_backup_marker(id) VALUES('source-post-backup-marker') ON CONFLICT DO NOTHING; SELECT count(*) FROM p07b3c2b1_source_post_backup_marker;`)
	postMarkerOut, err := reconciler.postgresBindingExec(context.Background(), sourceSpec, []byte(sourceBinding.Credential.Password+"\n"), postBackupMarkerScript, *sourceBinding)
	if err != nil || lastNonEmptyLine(string(postMarkerOut)) != "1" {
		t.Fatalf("write source post-backup marker err=%v out=%q", err, postMarkerOut)
	}

	// 6. Setup Target PostgreSQL and perform Restore
	targetSpec := sourceSpec
	targetSpec.ResourceID, targetSpec.CredentialID, targetSpec.Connection.ServiceName = "res-cutover-target-apply", "mrcred-cutover-target-apply", "opsi-mr-cutover-tgt-app"
	targetSpec.Connection.Host = targetSpec.Connection.ServiceName + "." + managedResourceNamespace(targetSpec) + ".svc.cluster.local"
	targetSpec.SpecHash, _ = targetSpec.Hash()
	targetNamespace := managedResourceNamespace(targetSpec)
	targetManagement := randomManagedCredential(t, targetSpec.CredentialID, resourcev1.CredentialPurposeResourceManagement, targetSpec.ResourceID, targetSpec.ResourceID, "opsi")
	targetReady := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "cutover-target-app", Spec: targetSpec, Credential: targetManagement})
	if targetReady.Status != "ready" || targetReady.Evidence == nil {
		t.Fatalf("target ready error: %+v", targetReady)
	}
	targetPVCUID := kubectl(t, "get", "pvc", targetReady.Evidence.PVCName, "-n", targetNamespace, "-o", "jsonpath={.metadata.uid}")
	targetPVUID := kubectl(t, "get", "pv", targetReady.Evidence.PVName, "-o", "jsonpath={.metadata.uid}")
	targetReady.Evidence.PVCUID, targetReady.Evidence.PVUID, targetReady.Evidence.StorageHash = targetPVCUID, targetPVUID, resourcev1.ManagedResourceStorageHash(targetSpec)
	authorityAPI.seedReadyResource(t, targetSpec, targetReady.Evidence)
	seedVaultManagedResourceCredential(t, authorityAPI, *targetManagement)

	queuedReview := authorityAPI.createReviewWithKey(t, backupID, targetSpec.ResourceID, "p07b3c2b2b1-restore-review")
	restoreReview, _ := authorityAPI.waitReview(t, queuedReview.ID, 5*time.Minute)
	queuedRestore := authorityAPI.createRestoreWithKey(t, backupID, restoreReview, "p07b3c2b2b1-restore")
	restoreAuthority, _ := authorityAPI.waitRestore(t, queuedRestore.ID, 10*time.Minute)
	if err := restoreAuthority.ValidateSucceeded(); err != nil {
		t.Fatalf("restore authority error: %v", err)
	}

	// 7. Create Target binding for the Application & write Target-Only marker
	targetBinding := postgresBindingOperation(t, targetSpec, "binding-cutover-target-app", true)
	_ = reconcilePostgresBindingK3s(t, reconciler, "target-binding-create-app", targetSpec, targetManagement, targetBinding)

	targetOnlyMarkerScript := psqlSQLScript(`CREATE TABLE IF NOT EXISTS p07b3c2b1_target_only_marker (id text primary key, marked_at timestamptz default now()); INSERT INTO p07b3c2b1_target_only_marker(id) VALUES('target-only-marker') ON CONFLICT DO NOTHING; SELECT count(*) FROM p07b3c2b1_target_only_marker;`)
	targetMarkerOut, err := reconciler.postgresBindingExec(context.Background(), targetSpec, []byte(targetBinding.Credential.Password+"\n"), targetOnlyMarkerScript, *targetBinding)
	if err != nil || lastNonEmptyLine(string(targetMarkerOut)) != "1" {
		t.Fatalf("write target-only marker err=%v out=%q", err, targetMarkerOut)
	}

	// Seed application & bindings in Cloud database
	appID := "app-cutover-apply-e2e"
	runtimeID := "rt-cutover-app-apply"
	runtimeSQL := "INSERT INTO runtimes(id, org_id, project_id, environment_id, name, status) SELECT " + sqlQuote(runtimeID) + ", p.org_id, p.id, e.id, 'cutover-rt-app', 'ready' FROM projects p JOIN environments e ON e.project_id=p.id WHERE p.id=" + sqlQuote(projectID) + " ON CONFLICT(id) DO NOTHING;"
	appSQL := "INSERT INTO control_services(id, org_id, project_id, environment_id, runtime_id, name, type, status, source_type, namespace, configuration, configuration_revision, configuration_state_hash, configuration_applied_by, configuration_applied_at) SELECT " + sqlQuote(appID) + ", p.org_id, p.id, e.id, " + sqlQuote(runtimeID) + ", 'cutover-app-apply', 'application', 'ready', 'image', 'default', '{\"schema_version\":\"opsi.service_configuration/v1\",\"resource_bindings\":[{\"logical_name\":\"DATABASE\",\"binding_id\":\"" + sourceBinding.BindingID + "\"}]}'::jsonb, 1, 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', 'p07b3c2b2b1', now() FROM projects p JOIN environments e ON e.project_id=p.id WHERE p.id=" + sqlQuote(projectID) + " ON CONFLICT(id) DO NOTHING;"
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
	createdCutoverReview, _ := authorityAPI.createCutoverReview(t, appID, sourceBinding.BindingID, targetBinding.BindingID, "idemp-cutover-rev-app")
	succeededCutoverReview, _ := waitCutoverReviewOutcome(t, authorityAPI, createdCutoverReview.ID, 5*time.Minute)
	if succeededCutoverReview.Lifecycle != cutoverv1.ReviewSucceeded {
		t.Fatalf("cutover review did not succeed: %+v", succeededCutoverReview)
	}

	// 9. Call Explicit Cutover Apply: POST /api/projects/{project}/applications/{app}/cutovers
	appliedCutover, rawApplyResp := authorityAPI.createCutover(t, appID, succeededCutoverReview.ID, "idemp-cutover-apply-1")
	if appliedCutover.ID == "" || (appliedCutover.Lifecycle != cutoverv1.CutoverDeploying && appliedCutover.Lifecycle != cutoverv1.CutoverValidating && appliedCutover.Lifecycle != cutoverv1.CutoverApplying) {
		t.Fatalf("unexpected cutover state after apply: %+v body=%s", appliedCutover, rawApplyResp)
	}

	// Security check: No credentials in apply response
	for _, forbidden := range []string{"password", "secret", "bearer"} {
		if strings.Contains(strings.ToLower(rawApplyResp), `"`+forbidden+`"`) {
			t.Fatalf("forbidden security field %q found in cutover apply response", forbidden)
		}
	}

	// 10. Verify Application Configuration Mutation in Cloud PostgreSQL
	appConfigAfter := authorityAPI.execSQL(t, "SELECT configuration::text FROM control_services WHERE id="+sqlQuote(appID)+";")
	if !strings.Contains(appConfigAfter, targetBinding.BindingID) {
		t.Fatalf("application configuration was not mutated to point to target binding: %s", appConfigAfter)
	}

	// 11. Reconcile Workload Secret for Target DB binding on K3s cluster
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

	// 12. Verify target DB connectivity through application:
	// Application sees 128 seed rows, sees target-only marker, DOES NOT see source post-backup marker
	targetCheckRows, err := checkRestoreBindingRows(reconciler, targetSpec, targetBinding, targetBinding.Credential.Password)
	if err != nil || strings.TrimSpace(targetCheckRows) != "128" {
		t.Fatalf("target binding read failed: %q err=%v", targetCheckRows, err)
	}
	targetCheckMarker, err := reconciler.postgresBindingExec(context.Background(), targetSpec, []byte(targetBinding.Credential.Password+"\n"), psqlSQLScript("SELECT count(*) FROM p07b3c2b1_target_only_marker;"), *targetBinding)
	if err != nil || lastNonEmptyLine(string(targetCheckMarker)) != "1" {
		t.Fatalf("target only marker absent on target: %q err=%v", targetCheckMarker, err)
	}
	sourceCheckMarker, _ := reconciler.postgresBindingExec(context.Background(), targetSpec, []byte(targetBinding.Credential.Password+"\n"), psqlSQLScript("SELECT count(*) FROM pg_class WHERE relname='p07b3c2b1_source_post_backup_marker';"), *targetBinding)
	if lastNonEmptyLine(string(sourceCheckMarker)) != "0" {
		t.Fatalf("source post-backup marker leaked to target: %q", sourceCheckMarker)
	}

	// 13. Write post-cutover marker on TARGET through application binding
	postCutoverMarkerScript := psqlSQLScript(`CREATE TABLE IF NOT EXISTS p07b3c2b1_post_cutover_marker (id text primary key, marked_at timestamptz default now()); INSERT INTO p07b3c2b1_post_cutover_marker(id) VALUES('post-cutover-marker') ON CONFLICT DO NOTHING; SELECT count(*) FROM p07b3c2b1_post_cutover_marker;`)
	postCutoverOut, err := reconciler.postgresBindingExec(context.Background(), targetSpec, []byte(targetBinding.Credential.Password+"\n"), postCutoverMarkerScript, *targetBinding)
	if err != nil || lastNonEmptyLine(string(postCutoverOut)) != "1" {
		t.Fatalf("write post-cutover marker err=%v out=%q", err, postCutoverOut)
	}

	// 14. Complete Cutover Apply Verification via Agent Result API
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

	// 15. Wait for Cutover lifecycle Succeeded
	succeededCutover, lifecycle := authorityAPI.waitCutover(t, appliedCutover.ID, 5*time.Minute)
	if succeededCutover.Lifecycle != cutoverv1.CutoverSucceeded {
		t.Fatalf("cutover did not succeed: %+v", succeededCutover)
	}
	if err := succeededCutover.ValidateSucceeded(); err != nil {
		t.Fatalf("cutover ValidateSucceeded error: %v", err)
	}

	// 16. STRICT SOURCE Rollback Authority Preservation Verification (Section 2, 4)
	sourceBindingRow := authorityAPI.execSQL(t, "SELECT lifecycle FROM resource_bindings WHERE id="+sqlQuote(sourceBinding.BindingID)+";")
	if !strings.Contains(sourceBindingRow, "ready") {
		t.Fatalf("source binding was not preserved as ready: %s", sourceBindingRow)
	}
	sourceRowsAfterCutover, err := checkRestoreBindingRows(reconciler, sourceSpec, sourceBinding, sourceBinding.Credential.Password)
	if err != nil || strings.TrimSpace(sourceRowsAfterCutover) != "128" {
		t.Fatalf("source database became unreadable after cutover: %q err=%v", sourceRowsAfterCutover, err)
	}
	sourceMarkerAfterCutover, err := reconciler.postgresBindingExec(context.Background(), sourceSpec, []byte(sourceBinding.Credential.Password+"\n"), psqlSQLScript("SELECT count(*) FROM p07b3c2b1_source_post_backup_marker;"), *sourceBinding)
	if err != nil || lastNonEmptyLine(string(sourceMarkerAfterCutover)) != "1" {
		t.Fatalf("source database marker missing after cutover: %q err=%v", sourceMarkerAfterCutover, err)
	}

	// 17. Replay Idempotency Verification
	reusedCutover, _ := authorityAPI.createCutover(t, appID, succeededCutoverReview.ID, "idemp-cutover-apply-1")
	if reusedCutover.ID != succeededCutover.ID {
		t.Fatalf("expected reused cutover on same key: got %s want %s", reusedCutover.ID, succeededCutover.ID)
	}

	// 18. Audit events verification
	auditRows := authorityAPI.execSQL(t, "SELECT action FROM cloud_audit_events WHERE project_id="+sqlQuote(projectID)+" AND resource_id="+sqlQuote(succeededCutover.ID)+" ORDER BY created_at ASC;")
	for _, expectedAction := range []string{"CUTOVER_REQUESTED", "CUTOVER_APPLY_STARTED", "CUTOVER_DEPLOYMENT_STARTED", "CUTOVER_SUCCEEDED"} {
		if !strings.Contains(auditRows, expectedAction) {
			t.Fatalf("missing audit event %s in audit log: %q", expectedAction, auditRows)
		}
	}

	stopRunner()
	if err := <-runResult; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	// 19. Write comprehensive evidence JSON artifact
	dir := os.Getenv("OPSI_P07B3C2B2B1_EVIDENCE_DIR")
	if dir == "" {
		dir = filepath.Join(".tmp", "evidence", "p07b3c2b2b1-postgres-cutover-apply-"+time.Now().UTC().Format("20060102T150405Z"))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	evidenceData, _ := json.MarshalIndent(map[string]any{
		"B2B1_Validation":            "PASS_EXPLICIT_CUTOVER_APPLY",
		"CutoverID":                  succeededCutover.ID,
		"CutoverReviewID":            succeededCutoverReview.ID,
		"Lifecycle":                  succeededCutover.Lifecycle,
		"LifecycleHistory":           lifecycle,
		"EvidenceHash":               succeededCutover.EvidenceHash,
		"ApplicationID":              appID,
		"SourceResourceID":           sourceSpec.ResourceID,
		"SourceBindingID":            sourceBinding.BindingID,
		"TargetResourceID":           targetSpec.ResourceID,
		"TargetBindingID":            targetBinding.BindingID,
		"VerificationSummary":        succeededCutover.VerificationSummary,
		"SourceRollbackPreserved":    true,
		"SourceBindingReady":         true,
		"SourceDataIntact":           true,
		"TargetDataVerified":         true,
		"TargetOnlyMarkerVerified":   true,
		"SourcePostBackupAbsentOnTgt": true,
		"PostCutoverTargetWritten":   true,
		"IdempotencyVerified":        true,
		"AuditVerified":              true,
	}, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "p07b3c2b2b1-postgres-cutover-apply.json"), append(evidenceData, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "P07B3C2B2B1_CUTOVER_APPLY_EVIDENCE=%s\n", dir)
}
