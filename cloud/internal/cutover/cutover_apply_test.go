package cutover

import (
	"context"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
	cutoverv1 "github.com/opsi-dev/opsi/contracts/go/cutoverv1"
	deploymentpolicyv1 "github.com/opsi-dev/opsi/contracts/go/deploymentpolicyv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

type testDeploymentAuthority struct {
	jobs []registry.DeploymentJob
}

func (d *testDeploymentAuthority) ListDeployments(projectID string) ([]registry.DeploymentJob, error) {
	return d.jobs, nil
}

func (d *testDeploymentAuthority) GetDeployment(projectID, deploymentID string) (registry.DeploymentJob, error) {
	for _, j := range d.jobs {
		if j.ID == deploymentID {
			return j, nil
		}
	}
	return registry.DeploymentJob{}, ErrNotFound
}

func (d *testDeploymentAuthority) StartImmutableDeployment(snapshot deploymentv1.JobSnapshot, requestedBy, key, requestID string) (registry.DeploymentJob, bool, error) {
	job := registry.DeploymentJob{
		ID:        "dep-cutover-1",
		ProjectID: snapshot.ProjectID,
		ServiceID: snapshot.Authority.BuildRecord.ServiceID,
		Status:    deploymentv1.StateSucceeded,
		Snapshot:  &snapshot,
	}
	d.jobs = append(d.jobs, job)
	return job, false, nil
}

func setupTestCutoverApplyFixture(t *testing.T) (Service, *MemoryStore, cutoverv1.ApplicationCutoverReview) {
	svc, store, app, sourceRes, sourceBinding, targetRes, targetBinding, restore, backup := setupTestService(t)

	// Ensure app configuration has DATABASE ResourceBinding
	appConfig := app.Configuration
	appConfig.ResourceBindings = []serviceconfigurationv1.ResourceBinding{
		{LogicalName: "DATABASE", BindingID: sourceBinding.ID},
	}
	app.Configuration = appConfig
	svc.Applications = testApplicationAuthority{
		apps:    map[string]registry.ServiceRecord{app.ID: app},
		configs: map[string]registry.ServiceConfiguration{app.ID: appConfig},
	}

	record := buildrecordv1.Record{
		ID:         "br-1",
		ProjectID:  app.ProjectID,
		ServiceID:  app.ID,
		ServiceKey: app.Name,
		Build: buildrecordv1.BuildMetadata{
			OCIRepository: "registry:5000/opsi/app",
			OCIDigest:     "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		},
	}

	preJob := registry.DeploymentJob{
		ID:        "dep-pre-1",
		ProjectID: app.ProjectID,
		ServiceID: app.ID,
		Status:    deploymentv1.StateSucceeded,
		Snapshot: &deploymentv1.JobSnapshot{
			ProjectID: app.ProjectID,
			Image: deploymentv1.ImmutableImage{
				Repository: record.Build.OCIRepository,
				Digest:     record.Build.OCIDigest,
			},
			Authority: deploymentv1.AuthoritySnapshot{
				BuildRecord: record,
			},
		},
	}

	depAuth := &testDeploymentAuthority{jobs: []registry.DeploymentJob{preJob}}
	svc.Deployments = depAuth

	reviewRes, _, err := svc.Review(context.Background(), app.ProjectID, app.ID, cutoverv1.ReviewRequest{
		SourceBindingID: sourceBinding.ID,
		TargetBindingID: targetBinding.ID,
	}, "user-1", "review-key-1")
	if err != nil {
		t.Fatalf("failed to create review: %v", err)
	}

	lease, ok, err := svc.LeaseReview(context.Background(), app.ProjectID, "node-1")
	if err != nil || !ok {
		t.Fatalf("failed to lease review: %v", err)
	}

	completedReview, err := svc.CompleteReview(context.Background(), app.ProjectID, lease.Review.ID, cutoverv1.ReviewResult{
		Status:               "succeeded",
		LeaseToken:           lease.LeaseToken,
		SourceSQLPreflight:   "PASS",
		TargetSQLPreflight:   "PASS",
		TargetRoleAttributes: "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
		ValidationSummary: cutoverv1.ValidationSummary{
			SourceSQLPreflight:   "PASS",
			TargetSQLPreflight:   "PASS",
			TargetRoleAttributes: "LOGIN,NOSUPERUSER,NOCREATEDB,NOCREATEROLE,NOREPLICATION,NOBYPASSRLS",
			SourceBindingReady:   true,
			TargetBindingReady:   true,
			TargetRestoreReady:   true,
			TargetPVCUID:         "pvc-tgt-uid",
			TargetPVUID:          "pv-tgt-uid",
			TargetStorageHash:    resourcev1.ManagedResourceStorageHash(targetRes.Runtime.Spec),
		},
	})
	if err != nil {
		t.Fatalf("failed to complete review: %v", err)
	}

	_ = reviewRes
	_ = sourceRes
	_ = backup
	_ = restore
	return svc, store, completedReview
}

type testTopologyAuthority struct {
	plan topologyv1.Plan
}

func (t testTopologyAuthority) Get(_ context.Context, _ string) (topologyv1.Plan, error) {
	return t.plan, nil
}

type testPolicyAuthority struct {
	policy deploymentpolicyv1.Policy
}

func (p testPolicyAuthority) Route(_ context.Context, _ string, _ deploymentpolicyv1.RoutingRequest) (deploymentpolicyv1.RoutingDecision, error) {
	return deploymentpolicyv1.RoutingDecision{DecisionCode: "DEPLOYMENT_ALLOWED"}, nil
}

func (p testPolicyAuthority) Get(_ context.Context, _, _ string) (deploymentpolicyv1.Policy, error) {
	return p.policy, nil
}

func TestCutoverApplySuccessLifecycleAndZeroDiff(t *testing.T) {
	svc, store, review := setupTestCutoverApplyFixture(t)
	ctx := context.Background()

	plan := topologyv1.Plan{
		Assignments: []topologyv1.Assignment{
			{ServiceKey: "web", EnvironmentID: "env-1", RuntimeID: "rt-1", Replicas: 1, CPURequestMillicores: 100, MemoryRequestBytes: 128 << 20, Exposure: topologyv1.ExposureIntent{Mode: "internal"}},
		},
	}
	svc.Topology = testTopologyAuthority{plan: plan}
	svc.Policies = testPolicyAuthority{policy: deploymentpolicyv1.Policy{ID: "pol-1"}}

	// Apply cutover
	cutover, reused, err := svc.Apply(ctx, "proj-1", "app-1", cutoverv1.ApplyRequest{CutoverReviewID: review.ID}, "user-1", "apply-key-1")
	if err != nil {
		t.Fatalf("expected apply to succeed, got %v", err)
	}
	if reused {
		t.Fatal("expected first apply to not be reused")
	}
	if cutover.Lifecycle != cutoverv1.CutoverDeploying {
		t.Fatalf("expected lifecycle deploying, got %s", cutover.Lifecycle)
	}
	if cutover.PreCutoverApplicationConfigRevision != 1 {
		t.Fatalf("expected pre revision 1, got %d", cutover.PreCutoverApplicationConfigRevision)
	}
	if cutover.ResultingApplicationConfigRevision != 2 {
		t.Fatalf("expected resulting revision 2, got %d", cutover.ResultingApplicationConfigRevision)
	}
	if cutover.DeploymentJobID != "dep-cutover-1" {
		t.Fatalf("expected deployment job ID dep-cutover-1, got %s", cutover.DeploymentJobID)
	}

	// Verify application configuration was mutated: DATABASE now references target binding
	cfg, err := svc.Applications.GetServiceConfiguration("proj-1", "app-1")
	if err != nil {
		t.Fatalf("failed to get config: %v", err)
	}
	if cfg.Revision != 2 {
		t.Fatalf("expected config revision 2, got %d", cfg.Revision)
	}
	if len(cfg.ResourceBindings) != 1 || cfg.ResourceBindings[0].BindingID != review.TargetBindingID || cfg.ResourceBindings[0].LogicalName != "DATABASE" {
		t.Fatalf("expected DATABASE -> %s, got %+v", review.TargetBindingID, cfg.ResourceBindings)
	}

	// Complete cutover verification
	result := cutoverv1.CutoverApplyResult{
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
	}
	completed, err := svc.CompleteCutover(ctx, "proj-1", cutover.ID, result)
	if err != nil {
		t.Fatalf("failed to complete cutover: %v", err)
	}
	if completed.Lifecycle != cutoverv1.CutoverSucceeded {
		t.Fatalf("expected succeeded, got %s", completed.Lifecycle)
	}
	if err := completed.ValidateSucceeded(); err != nil {
		t.Fatalf("cutover validate succeeded failed: %v", err)
	}

	// Idempotent replay of apply
	replayCutover, replayReused, err := svc.Apply(ctx, "proj-1", "app-1", cutoverv1.ApplyRequest{CutoverReviewID: review.ID}, "user-1", "apply-key-1")
	if err != nil {
		t.Fatalf("expected replay to succeed, got %v", err)
	}
	if !replayReused {
		t.Fatal("expected replay to be reused")
	}
	if replayCutover.ID != cutover.ID {
		t.Fatalf("expected replay cutover ID %s, got %s", cutover.ID, replayCutover.ID)
	}

	// Replay with different payload should conflict
	_, _, err = svc.Apply(ctx, "proj-1", "app-1", cutoverv1.ApplyRequest{CutoverReviewID: "acrv-different"}, "user-1", "apply-key-1")
	if err == nil {
		t.Fatal("expected conflict with different payload on same key")
	}

	// Succeeded cutover is immutable
	_, err = store.UpdateCutover(ctx, cutoverv1.ApplicationCutover{
		ID:        cutover.ID,
		ProjectID: "proj-1",
		Lifecycle: cutoverv1.CutoverFailed,
	})
	if err == nil {
		t.Fatal("expected error updating succeeded cutover")
	}
}

func TestCutoverApplyRejectsStaleReviewAndNotReady(t *testing.T) {
	svc, store, review := setupTestCutoverApplyFixture(t)
	ctx := context.Background()

	// 1. Stale Review: Mutate source resource spec hash (both spec and observed evidence so resource is ready, but hash changed)
	sourceRes, _ := svc.Resources.Get(ctx, "proj-1", review.SourceResourceID)
	mutatedSource := sourceRes
	mutatedSource.Runtime.Spec.SpecHash = strings.Repeat("9", 64)
	mutatedSource.Runtime.Evidence.ObservedSpecHash = strings.Repeat("9", 64)
	svc.Resources = testResourceAuthority{
		resources: map[string]resourcev1.Resource{
			review.SourceResourceID: mutatedSource,
			review.TargetResourceID: svc.Resources.(testResourceAuthority).resources[review.TargetResourceID],
		},
		bindings: svc.Resources.(testResourceAuthority).bindings,
	}

	_, _, err := svc.Apply(ctx, "proj-1", "app-1", cutoverv1.ApplyRequest{CutoverReviewID: review.ID}, "user-1", "key-stale-1")
	if err == nil || !strings.Contains(err.Error(), "CUTOVER_STALE_REVIEW") {
		t.Fatalf("expected CUTOVER_STALE_REVIEW, got %v", err)
	}

	// 2. Review Not Ready (e.g. queued or failed review)
	failedReview := review
	failedReview.ID = "acrv-failed"
	failedReview.Lifecycle = cutoverv1.ReviewFailed
	_, _, _ = store.CreateReview(ctx, failedReview, "key-failed-rev", "hash-1")

	_, _, err = svc.Apply(ctx, "proj-1", "app-1", cutoverv1.ApplyRequest{CutoverReviewID: failedReview.ID}, "user-1", "key-not-ready")
	if err == nil || !strings.Contains(err.Error(), "CUTOVER_REVIEW_NOT_READY") {
		t.Fatalf("expected CUTOVER_REVIEW_NOT_READY, got %v", err)
	}
}
