package cutover

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
	backupv1 "github.com/opsi-dev/opsi/contracts/go/backupv1"
	cutoverv1 "github.com/opsi-dev/opsi/contracts/go/cutoverv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	restorev1 "github.com/opsi-dev/opsi/contracts/go/restorev1"
)

type testApplicationAuthority struct {
	apps    map[string]registry.ServiceRecord
	configs map[string]registry.ServiceConfiguration
}

func (a testApplicationAuthority) GetServiceConfiguration(projectID, serviceID string) (registry.ServiceConfiguration, error) {
	if cfg, ok := a.configs[serviceID]; ok {
		return cfg, nil
	}
	return registry.ServiceConfiguration{Revision: 1, StateHash: strings.Repeat("1", 64)}, nil
}

func (a testApplicationAuthority) ApplyServiceConfiguration(projectID, serviceID, actorUserID, key string, request registry.ServiceConfigurationApplyRequest) (registry.ServiceConfigurationApplyResult, error) {
	current := a.configs[serviceID]
	now := time.Now().UTC()
	cfg := registry.ServiceConfiguration{
		ServiceConfigurationDraft: request.Draft,
		Revision:                  current.Revision + 1,
		StateHash:                 strings.Repeat("9", 64),
		AppliedBy:                 actorUserID,
		AppliedAt:                 &now,
	}
	if a.configs != nil {
		a.configs[serviceID] = cfg
	}
	return registry.ServiceConfigurationApplyResult{Configuration: cfg}, nil
}

func (a testApplicationAuthority) ListServices(projectID string) ([]registry.ServiceRecord, error) {
	out := make([]registry.ServiceRecord, 0, len(a.apps))
	for _, app := range a.apps {
		if app.ProjectID == projectID {
			out = append(out, app)
		}
	}
	return out, nil
}

func (a testApplicationAuthority) ApplicationBelongs(_ context.Context, projectID, environmentID, applicationID string) (bool, error) {
	app, ok := a.apps[applicationID]
	return ok && app.ProjectID == projectID && app.EnvironmentID == environmentID, nil
}

type testResourceAuthority struct {
	resources map[string]resourcev1.Resource
	bindings  map[string]resourcev1.Binding
}

func (r testResourceAuthority) Get(_ context.Context, projectID, resourceID string) (resourcev1.Resource, error) {
	if res, ok := r.resources[resourceID]; ok && res.ProjectID == projectID {
		return res, nil
	}
	return resourcev1.Resource{}, ErrNotFound
}

func (r testResourceAuthority) GetBinding(_ context.Context, projectID, bindingID string) (resourcev1.Binding, error) {
	if b, ok := r.bindings[bindingID]; ok && b.ProjectID == projectID {
		return b, nil
	}
	return resourcev1.Binding{}, ErrNotFound
}

func (r testResourceAuthority) ListBindings(_ context.Context, projectID, environmentID string) ([]resourcev1.Binding, error) {
	out := []resourcev1.Binding{}
	for _, b := range r.bindings {
		if b.ProjectID == projectID && (environmentID == "" || b.EnvironmentID == environmentID) {
			out = append(out, b)
		}
	}
	return out, nil
}

func (r testResourceAuthority) DeleteBinding(_ context.Context, projectID, bindingID string) (resourcev1.Binding, error) {
	if b, ok := r.bindings[bindingID]; ok && b.ProjectID == projectID {
		delete(r.bindings, bindingID)
		return b, nil
	}
	return resourcev1.Binding{}, ErrNotFound
}

type testRestoreAuthority struct {
	restores map[string]restorev1.Restore
}

func (r testRestoreAuthority) Get(_ context.Context, projectID, restoreID string) (restorev1.Restore, error) {
	if rst, ok := r.restores[restoreID]; ok && rst.ProjectID == projectID {
		return rst, nil
	}
	return restorev1.Restore{}, ErrNotFound
}

func (r testRestoreAuthority) List(_ context.Context, projectID, backupID, targetID string) ([]restorev1.Restore, error) {
	out := []restorev1.Restore{}
	for _, rst := range r.restores {
		if rst.ProjectID == projectID && (targetID == "" || rst.TargetResourceID == targetID) && (backupID == "" || rst.BackupID == backupID) {
			out = append(out, rst)
		}
	}
	return out, nil
}

func (r testRestoreAuthority) HasActive(_ context.Context, projectID, targetID string) (bool, error) {
	for _, rst := range r.restores {
		if rst.ProjectID == projectID && rst.TargetResourceID == targetID {
			if rst.Lifecycle == restorev1.LifecycleQueued || rst.Lifecycle == restorev1.LifecycleRunning || rst.Lifecycle == restorev1.LifecycleVerifying {
				return true, nil
			}
		}
	}
	return false, nil
}

type testBackupAuthority struct {
	backups map[string]backupv1.Backup
}

func (b testBackupAuthority) Get(_ context.Context, projectID, backupID string) (backupv1.Backup, error) {
	if bak, ok := b.backups[backupID]; ok && bak.ProjectID == projectID {
		return bak, nil
	}
	return backupv1.Backup{}, ErrNotFound
}

func (b testBackupAuthority) HasActive(_ context.Context, projectID, resourceID string) (bool, error) {
	return false, nil
}

type testCredentialAuthority struct {
	credentials map[string]resourcev1.ManagedResourceCredential
}

func (c testCredentialAuthority) Get(_ context.Context, credentialID string) (resourcev1.ManagedResourceCredential, error) {
	if cred, ok := c.credentials[credentialID]; ok {
		return cred, nil
	}
	return resourcev1.ManagedResourceCredential{}, ErrNotFound
}

func setupTestService(t *testing.T) (Service, *MemoryStore, registry.ServiceRecord, resourcev1.Resource, resourcev1.Binding, resourcev1.Resource, resourcev1.Binding, restorev1.Restore, backupv1.Backup) {
	now := time.Now().UTC().Add(-time.Hour)
	completedNow := now.Add(5 * time.Minute)

	app := registry.ServiceRecord{
		ID:            "app-1",
		ProjectID:     "proj-1",
		EnvironmentID: "env-1",
		Name:          "web",
		ContainerPort: 8080,
		HealthPath:    "/health",
		Replicas:      1,
		Configuration: registry.ServiceConfiguration{Revision: 1, StateHash: strings.Repeat("1", 64)},
	}

	sourceSpec := resourcev1.ManagedResourceSpec{
		SchemaVersion:     resourcev1.ManagedResourceSpecSchemaVersion,
		ResourceID:        "res-source",
		ProjectID:         "proj-1",
		EnvironmentID:     "env-1",
		ResourceType:      resourcev1.TypePostgres,
		Profile:           "single-node-experimental",
		Version:           resourcev1.PostgresVersion,
		Image:             resourcev1.PostgresImage,
		Assignment:        resourcev1.ManagedResourceAssignment{RuntimeID: "rt-1", NodeID: "node-1", AgentID: "agent-1"},
		Replicas:          1,
		CPUMillicores:     250,
		MemoryBytes:       256 << 20,
		Storage:           resourcev1.StorageRequest{Persistent: true, SizeBytes: 10 << 30, PolicyRef: resourcev1.StoragePolicyDefault},
		Connection:        resourcev1.ManagedResourceConnection{Host: "source.svc", Port: 5432, Protocol: resourcev1.ProtocolPostgres, ServiceName: "source-pg", Database: "opsi"},
		Ports:             []resourcev1.ManagedResourcePort{{Name: "postgres", Port: 5432, Protocol: resourcev1.ProtocolPostgres}},
		CredentialID:      "cred-src-mgmt",
		ConfigurationHash: strings.Repeat("2", 64),
		TopologyRevision:  1,
		TopologyHash:      strings.Repeat("3", 64),
	}
	sourceSpec.SpecHash, _ = sourceSpec.Hash()

	sourceResource := resourcev1.Resource{
		SchemaVersion: resourcev1.SchemaVersion,
		ID:            "res-source",
		ProjectID:     "proj-1",
		EnvironmentID: "env-1",
		Type:          resourcev1.TypePostgres,
		Kind:          resourcev1.KindManagedService,
		Lifecycle:     resourcev1.LifecycleReady,
		Runtime: &resourcev1.ManagedResourceRuntime{
			Spec: sourceSpec,
			Evidence: &resourcev1.ManagedResourceEvidence{
				ObservedSpecHash: sourceSpec.SpecHash,
				WorkloadReady:    true,
				PodReady:         true,
				ServiceReady:     true,
				SecretReady:      true,
				AuthReady:        true,
				StorageReady:     true,
				VolumeMounted:    true,
				PVCUID:           "pvc-src-uid",
				PVUID:            "pv-src-uid",
				StorageHash:      resourcev1.ManagedResourceStorageHash(sourceSpec),
			},
		},
	}

	sourceBinding := resourcev1.Binding{
		SchemaVersion: resourcev1.SchemaVersion,
		ID:            "bind-source",
		ProjectID:     "proj-1",
		EnvironmentID: "env-1",
		Source:        resourcev1.EndpointReference{Kind: resourcev1.KindApplication, ID: app.ID},
		Target:        resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: sourceResource.ID},
		Protocol:      resourcev1.ProtocolPostgres,
		LogicalName:   "DATABASE",
		Lifecycle:     resourcev1.LifecycleReady,
		RoleName:      "rb_source_user",
		Database:      "opsi",
		CredentialID:  "cred-src-binding",
	}

	targetSpec := sourceSpec
	targetSpec.ResourceID = "res-target"
	targetSpec.Connection.ServiceName = "target-pg"
	targetSpec.Connection.Host = "target.svc"
	targetSpec.CredentialID = "cred-tgt-mgmt"
	targetSpec.SpecHash, _ = targetSpec.Hash()

	targetResource := resourcev1.Resource{
		SchemaVersion: resourcev1.SchemaVersion,
		ID:            "res-target",
		ProjectID:     "proj-1",
		EnvironmentID: "env-1",
		Type:          resourcev1.TypePostgres,
		Kind:          resourcev1.KindManagedService,
		Lifecycle:     resourcev1.LifecycleReady,
		Runtime: &resourcev1.ManagedResourceRuntime{
			Spec: targetSpec,
			Evidence: &resourcev1.ManagedResourceEvidence{
				ObservedSpecHash: targetSpec.SpecHash,
				WorkloadReady:    true,
				PodReady:         true,
				ServiceReady:     true,
				SecretReady:      true,
				AuthReady:        true,
				StorageReady:     true,
				VolumeMounted:    true,
				PVCUID:           "pvc-tgt-uid",
				PVUID:            "pv-tgt-uid",
				StorageHash:      resourcev1.ManagedResourceStorageHash(targetSpec),
			},
		},
	}

	targetBinding := resourcev1.Binding{
		SchemaVersion: resourcev1.SchemaVersion,
		ID:            "bind-target",
		ProjectID:     "proj-1",
		EnvironmentID: "env-1",
		Source:        resourcev1.EndpointReference{Kind: resourcev1.KindApplication, ID: app.ID},
		Target:        resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: targetResource.ID},
		Protocol:      resourcev1.ProtocolPostgres,
		LogicalName:   "DATABASE",
		Lifecycle:     resourcev1.LifecycleReady,
		RoleName:      "rb_target_user",
		Database:      "opsi",
		CredentialID:  "cred-tgt-binding",
	}

	backup := backupv1.Backup{
		SchemaVersion:         backupv1.SchemaVersion,
		ID:                    "bak-1",
		ProjectID:             "proj-1",
		EnvironmentID:         "env-1",
		SourceResourceID:      sourceResource.ID,
		SourcePostgresVersion: resourcev1.PostgresVersion,
		Lifecycle:             backupv1.LifecycleSucceeded,
		SHA256:                strings.Repeat("a", 64),
		ArtifactSize:          1024,
		CreatedAt:             now,
		CompletedAt:           &completedNow,
	}

	restoreReview := restorev1.Review{
		SchemaVersion:        restorev1.SchemaVersion,
		ID:                   "rrv-1",
		ProjectID:            "proj-1",
		EnvironmentID:        "env-1",
		BackupID:             backup.ID,
		SourceResourceID:     sourceResource.ID,
		TargetResourceID:     targetResource.ID,
		TargetNodeID:         "node-1",
		TargetSpecHash:       targetSpec.SpecHash,
		TargetPVCUID:         "pvc-tgt-uid",
		TargetDatabase:       "opsi",
		TargetDatabaseOID:    "16384",
		Pristine:             true,
		Objects:              restorev1.ObjectSummary{Schemas: 1},
		Lifecycle:            restorev1.ReviewSucceeded,
		ReviewedAt:           &completedNow,
	}
	restoreReview.PristineEvidenceHash = restorev1.PristineEvidenceHash(restoreReview)

	restore := restorev1.Restore{
		SchemaVersion:        restorev1.SchemaVersion,
		ID:                   "rst-1",
		ProjectID:            "proj-1",
		EnvironmentID:        "env-1",
		ReviewID:             restoreReview.ID,
		BackupID:             backup.ID,
		SourceResourceID:     sourceResource.ID,
		TargetResourceID:     targetResource.ID,
		TargetNodeID:         "node-1",
		ArtifactSHA256:       backup.SHA256,
		ArtifactSize:         backup.ArtifactSize,
		TargetSpecHash:       targetSpec.SpecHash,
		TargetStorageHash:    resourcev1.ManagedResourceStorageHash(targetSpec),
		PristineEvidenceHash: restoreReview.PristineEvidenceHash,
		PGRestoreVersion:     "pg_restore (PostgreSQL) 16.2",
		ArchiveVerified:      true,
		Lifecycle:            restorev1.LifecycleSucceeded,
		VerifyingAt:          &completedNow,
		CompletedAt:          &completedNow,
		VerificationMetadata: map[string]string{
			"connectivity": "authenticated",
			"transaction":  "committed",
		},
	}

	apps := testApplicationAuthority{
		apps:    map[string]registry.ServiceRecord{app.ID: app},
		configs: map[string]registry.ServiceConfiguration{app.ID: app.Configuration},
	}
	resAuth := testResourceAuthority{
		resources: map[string]resourcev1.Resource{sourceResource.ID: sourceResource, targetResource.ID: targetResource},
		bindings:  map[string]resourcev1.Binding{sourceBinding.ID: sourceBinding, targetBinding.ID: targetBinding},
	}
	restoresAuth := testRestoreAuthority{restores: map[string]restorev1.Restore{restore.ID: restore}}
	backupsAuth := testBackupAuthority{backups: map[string]backupv1.Backup{backup.ID: backup}}
	credsAuth := testCredentialAuthority{
		credentials: map[string]resourcev1.ManagedResourceCredential{
			sourceBinding.CredentialID:  {CredentialID: sourceBinding.CredentialID, Username: sourceBinding.RoleName, Password: "secret_src_password", Database: "opsi"},
			targetBinding.CredentialID:  {CredentialID: targetBinding.CredentialID, Username: targetBinding.RoleName, Password: "secret_tgt_password", Database: "opsi"},
			sourceSpec.CredentialID:     {CredentialID: sourceSpec.CredentialID, Username: "opsi_manager", Password: "mgmt_src_password", Database: "opsi"},
			targetSpec.CredentialID:     {CredentialID: targetSpec.CredentialID, Username: "opsi_manager", Password: "mgmt_tgt_password", Database: "opsi"},
		},
	}

	store := NewMemoryStore()
	svc := Service{
		Store:        store,
		Applications: apps,
		Resources:    resAuth,
		Restores:     restoresAuth,
		Backups:      backupsAuth,
		Credentials:  credsAuth,
	}
	return svc, store, app, sourceResource, sourceBinding, targetResource, targetBinding, restore, backup
}

func TestCutoverReviewLifecycleAndStaleness(t *testing.T) {
	svc, _, app, sourceResource, sourceBinding, targetResource, targetBinding, restore, _ := setupTestService(t)

	// 1. Create Review
	created, reused, err := svc.Review(context.Background(), app.ProjectID, app.ID, cutoverv1.ReviewRequest{
		SourceBindingID: sourceBinding.ID,
		TargetBindingID: targetBinding.ID,
	}, "user-dev", "idemp-key-1")
	if err != nil || reused {
		t.Fatalf("create review err=%v reused=%t", err, reused)
	}
	if created.Lifecycle != cutoverv1.ReviewQueued {
		t.Fatalf("expected lifecycle queued, got %s", created.Lifecycle)
	}
	if len(created.Warnings) == 0 || created.Warnings[0] != cutoverv1.WarningNotContinuouslySynchronized {
		t.Fatalf("expected warning TARGET_NOT_CONTINUOUSLY_SYNCHRONIZED, got %v", created.Warnings)
	}

	// 2. Lease Review
	lease, ok, err := svc.LeaseReview(context.Background(), app.ProjectID, "node-1")
	if err != nil || !ok {
		t.Fatalf("lease review err=%v ok=%t", err, ok)
	}
	if lease.Review.ID != created.ID || lease.LeaseToken == "" || lease.SourceCredential == nil || lease.TargetCredential == nil {
		t.Fatalf("invalid lease: %+v", lease)
	}

	// 3. Complete Review with Succeeded Preflight
	res, err := svc.CompleteReview(context.Background(), app.ProjectID, created.ID, cutoverv1.ReviewResult{
		Status:               cutoverv1.ReviewSucceeded,
		LeaseToken:           lease.LeaseToken,
		SourceSQLPreflight:   "PASS",
		TargetSQLPreflight:   "PASS",
		TargetRoleAttributes: "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
		ValidationSummary: cutoverv1.ValidationSummary{
			SourceSQLPreflight:   "PASS",
			TargetSQLPreflight:   "PASS",
			TargetRoleAttributes: "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
		},
	})
	if err != nil {
		t.Fatalf("complete review err=%v", err)
	}
	if res.Lifecycle != cutoverv1.ReviewSucceeded {
		t.Fatalf("review not succeeded: lifecycle=%s", res.Lifecycle)
	}
	if err := res.ValidateSucceeded(); err != nil {
		t.Fatalf("validation failed: %v", err)
	}

	// 4. Validate Staleness: Baseline matches -> PASS
	if err := svc.ValidateStale(context.Background(), res, app, sourceResource, sourceBinding, targetResource, targetBinding, restore); err != nil {
		t.Fatalf("expected review to be fresh, got: %v", err)
	}

	// 5. Change Application config revision -> CUTOVER_STALE_REVIEW
	mutatedApp := app
	mutatedApp.Configuration.Revision = 2
	mutatedApp.Configuration.StateHash = strings.Repeat("9", 64)
	appAuth := svc.Applications.(testApplicationAuthority)
	appAuth.configs[app.ID] = mutatedApp.Configuration
	svc.Applications = appAuth

	if err := svc.ValidateStale(context.Background(), res, mutatedApp, sourceResource, sourceBinding, targetResource, targetBinding, restore); err == nil {
		t.Fatal("expected stale error after application configuration change")
	} else {
		var cutoverErr Error
		if !strings.Contains(err.Error(), "application configuration changed") {
			t.Fatalf("unexpected stale err: %v", err)
		}
		_ = cutoverErr
	}
}

func TestCutoverReviewIdempotency(t *testing.T) {
	svc, _, app, _, sourceBinding, _, targetBinding, _, _ := setupTestService(t)

	// Same request + key -> reused review
	req := cutoverv1.ReviewRequest{SourceBindingID: sourceBinding.ID, TargetBindingID: targetBinding.ID}
	r1, reused1, err := svc.Review(context.Background(), app.ProjectID, app.ID, req, "user-1", "idem-1")
	if err != nil || reused1 {
		t.Fatalf("r1 err=%v reused=%t", err, reused1)
	}

	r2, reused2, err := svc.Review(context.Background(), app.ProjectID, app.ID, req, "user-1", "idem-1")
	if err != nil || !reused2 || r1.ID != r2.ID {
		t.Fatalf("r2 err=%v reused=%t id1=%s id2=%s", err, reused2, r1.ID, r2.ID)
	}

	// Different request + same key -> 409 conflict
	reqDifferent := cutoverv1.ReviewRequest{SourceBindingID: sourceBinding.ID, TargetBindingID: "bind-other"}
	_, _, err = svc.Review(context.Background(), app.ProjectID, app.ID, reqDifferent, "user-1", "idem-1")
	if err == nil {
		t.Fatal("expected conflict error for different payload with same key")
	}
}

func TestCutoverReviewInvalidTargetCases(t *testing.T) {
	svc, _, app, sourceResource, sourceBinding, targetResource, targetBinding, restore, _ := setupTestService(t)

	// Target Restore failed
	badRestore := restore
	badRestore.Lifecycle = restorev1.LifecycleFailed
	restoresAuth := testRestoreAuthority{restores: map[string]restorev1.Restore{badRestore.ID: badRestore}}
	svcBadRestore := svc
	svcBadRestore.Restores = restoresAuth

	_, _, err := svcBadRestore.Review(context.Background(), app.ProjectID, app.ID, cutoverv1.ReviewRequest{
		SourceBindingID: sourceBinding.ID,
		TargetBindingID: targetBinding.ID,
	}, "user-1", "")
	if err == nil {
		t.Fatal("expected error when target restore is failed")
	}

	// Target Resource not ready
	badTargetRes := targetResource
	badTargetRes.Lifecycle = resourcev1.LifecycleProvisioning
	resAuth := testResourceAuthority{
		resources: map[string]resourcev1.Resource{sourceResource.ID: sourceResource, badTargetRes.ID: badTargetRes},
		bindings:  map[string]resourcev1.Binding{sourceBinding.ID: sourceBinding, targetBinding.ID: targetBinding},
	}
	svcBadTarget := svc
	svcBadTarget.Resources = resAuth

	_, _, err = svcBadTarget.Review(context.Background(), app.ProjectID, app.ID, cutoverv1.ReviewRequest{
		SourceBindingID: sourceBinding.ID,
		TargetBindingID: targetBinding.ID,
	}, "user-1", "")
	if err == nil {
		t.Fatal("expected error when target resource is not ready")
	}

	// Target binding belongs to different Application
	badTargetBinding := targetBinding
	badTargetBinding.Source.ID = "other-app"
	resAuth.bindings[badTargetBinding.ID] = badTargetBinding
	svcBadBinding := svc
	svcBadBinding.Resources = resAuth

	_, _, err = svcBadBinding.Review(context.Background(), app.ProjectID, app.ID, cutoverv1.ReviewRequest{
		SourceBindingID: sourceBinding.ID,
		TargetBindingID: badTargetBinding.ID,
	}, "user-1", "")
	if err == nil {
		t.Fatal("expected error when target binding belongs to different application")
	}

	// Source binding == Target binding
	_, _, err = svc.Review(context.Background(), app.ProjectID, app.ID, cutoverv1.ReviewRequest{
		SourceBindingID: sourceBinding.ID,
		TargetBindingID: sourceBinding.ID,
	}, "user-1", "")
	if err == nil {
		t.Fatal("expected error when source binding == target binding")
	}
}
