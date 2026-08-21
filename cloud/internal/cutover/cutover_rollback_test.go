package cutover

import (
	"context"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
	cutoverv1 "github.com/opsi-dev/opsi/contracts/go/cutoverv1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
)

func setupTestCutoverRollbackFixture(t *testing.T) (Service, *MemoryStore, cutoverv1.ApplicationCutover) {
	svc, store, review := setupTestCutoverApplyFixture(t)

	applyResult, reused, err := svc.Apply(context.Background(), review.ProjectID, review.ApplicationID, cutoverv1.ApplyRequest{
		CutoverReviewID: review.ID,
	}, "user-1", "apply-key-1")
	if err != nil {
		t.Fatalf("failed to apply cutover: %v", err)
	}
	if reused {
		t.Fatal("expected new cutover, got reused")
	}

	completed, err := svc.CompleteCutover(context.Background(), review.ProjectID, applyResult.ID, cutoverv1.CutoverApplyResult{
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
	if err != nil {
		t.Fatalf("failed to complete cutover: %v", err)
	}

	return svc, store, completed
}

func TestCutoverRollbackLifecycleAndZeroDiffMutation(t *testing.T) {
	svc, _, cutover := setupTestCutoverRollbackFixture(t)

	// Verify pre-rollback application state (DATABASE binding points to TargetBindingID)
	preConfig, err := svc.Applications.GetServiceConfiguration(cutover.ProjectID, cutover.ApplicationID)
	if err != nil {
		t.Fatalf("failed to get config: %v", err)
	}
	if len(preConfig.ResourceBindings) != 1 || preConfig.ResourceBindings[0].BindingID != cutover.TargetBindingID {
		t.Fatalf("expected pre-rollback config to point to target %s: %+v", cutover.TargetBindingID, preConfig.ResourceBindings)
	}

	// 1. Explicit Rollback Request
	rollback, reused, err := svc.Rollback(context.Background(), cutover.ProjectID, cutover.ApplicationID, cutover.ID, "user-1", "rollback-key-1")
	if err != nil {
		t.Fatalf("failed to request rollback: %v", err)
	}
	if reused {
		t.Fatal("expected new rollback, got reused")
	}

	if rollback.Lifecycle != cutoverv1.RollbackDeploying {
		t.Fatalf("expected rollback deploying, got %s", rollback.Lifecycle)
	}
	if rollback.SourceBindingID != cutover.SourceBindingID {
		t.Fatalf("expected source binding %s, got %s", cutover.SourceBindingID, rollback.SourceBindingID)
	}
	if rollback.TargetBindingID != cutover.TargetBindingID {
		t.Fatalf("expected target binding %s, got %s", cutover.TargetBindingID, rollback.TargetBindingID)
	}
	if len(rollback.Warnings) != 1 || rollback.Warnings[0] != cutoverv1.WarningTargetWritesMayNotBeOnSource {
		t.Fatalf("expected divergence warning, got %+v", rollback.Warnings)
	}

	// 2. Verify Post-Rollback Application Configuration Mutation (Zero Unrelated Diffs)
	postConfig, err := svc.Applications.GetServiceConfiguration(cutover.ProjectID, cutover.ApplicationID)
	if err != nil {
		t.Fatalf("failed to get post-rollback config: %v", err)
	}
	if postConfig.Revision <= preConfig.Revision {
		t.Fatalf("expected revision bump, got pre=%d post=%d", preConfig.Revision, postConfig.Revision)
	}
	if len(postConfig.ResourceBindings) != 1 || postConfig.ResourceBindings[0].BindingID != cutover.SourceBindingID {
		t.Fatalf("expected post-rollback config to point to source %s: %+v", cutover.SourceBindingID, postConfig.ResourceBindings)
	}

	// Verify all other configuration remains strictly identical
	if len(postConfig.Environment) != len(preConfig.Environment) ||
		len(postConfig.Bindings) != len(preConfig.Bindings) {
		t.Fatalf("unrelated configuration fields changed: pre=%+v post=%+v", preConfig, postConfig)
	}

	// 3. Complete Rollback with Agent/Verification evidence
	completed, err := svc.CompleteRollback(context.Background(), cutover.ProjectID, rollback.ID, cutoverv1.RollbackResult{
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
	if err != nil {
		t.Fatalf("failed to complete rollback: %v", err)
	}

	if completed.Lifecycle != cutoverv1.RollbackSucceeded {
		t.Fatalf("expected succeeded rollback, got %s", completed.Lifecycle)
	}
	if err := completed.ValidateSucceeded(); err != nil {
		t.Fatalf("expected valid succeeded rollback: %v", err)
	}

	// 4. Verify Original Cutover Remains Immutable
	loadedCutover, err := svc.GetCutover(context.Background(), cutover.ProjectID, cutover.ID)
	if err != nil {
		t.Fatalf("failed to get original cutover: %v", err)
	}
	if loadedCutover.Lifecycle != cutoverv1.CutoverSucceeded {
		t.Fatalf("original cutover must remain succeeded, got %s", loadedCutover.Lifecycle)
	}
}

func TestCutoverRollbackRejectionWhenApplicationStale(t *testing.T) {
	svc, _, cutover := setupTestCutoverRollbackFixture(t)

	// Manually change application config to point to an unrelated binding
	appConfig, _ := svc.Applications.GetServiceConfiguration(cutover.ProjectID, cutover.ApplicationID)
	appConfig.ResourceBindings = []serviceconfigurationv1.ResourceBinding{
		{LogicalName: "DATABASE", BindingID: "bind-other"},
	}
	svc.Applications = testApplicationAuthority{
		apps:    map[string]registry.ServiceRecord{cutover.ApplicationID: {ID: cutover.ApplicationID, Name: "app", ProjectID: cutover.ProjectID}},
		configs: map[string]registry.ServiceConfiguration{cutover.ApplicationID: appConfig},
	}

	_, _, err := svc.Rollback(context.Background(), cutover.ProjectID, cutover.ApplicationID, cutover.ID, "user-1", "rollback-stale-key")
	if err == nil {
		t.Fatal("expected error when application config does not point to target binding")
	}
	if !strings.Contains(err.Error(), cutoverv1.FailureRollbackStaleApplication) {
		t.Fatalf("expected ROLLBACK_STALE_APPLICATION error, got: %v", err)
	}
}

func TestCutoverRollbackReplayIdempotency(t *testing.T) {
	svc, _, cutover := setupTestCutoverRollbackFixture(t)

	r1, reused1, err := svc.Rollback(context.Background(), cutover.ProjectID, cutover.ApplicationID, cutover.ID, "user-1", "same-rollback-key")
	if err != nil {
		t.Fatalf("initial rollback failed: %v", err)
	}
	if reused1 {
		t.Fatal("expected initial rollback not reused")
	}

	r2, reused2, err := svc.Rollback(context.Background(), cutover.ProjectID, cutover.ApplicationID, cutover.ID, "user-1", "same-rollback-key")
	if err != nil {
		t.Fatalf("replay rollback failed: %v", err)
	}
	if !reused2 {
		t.Fatal("expected replay rollback to return reused: true")
	}
	if r1.ID != r2.ID {
		t.Fatalf("expected same rollback ID, got %s and %s", r1.ID, r2.ID)
	}
}

func TestCutoverRollbackRejectionWhenIneligible(t *testing.T) {
	svc, store, review := setupTestCutoverApplyFixture(t)

	// Create a cutover that failed before config mutation
	ineligibleCutover := cutoverv1.ApplicationCutover{
		SchemaVersion:                       cutoverv1.CutoverSchemaVersion,
		ID:                                  "acut-ineligible",
		ProjectID:                           review.ProjectID,
		EnvironmentID:                       review.EnvironmentID,
		ApplicationID:                       review.ApplicationID,
		CutoverReviewID:                     review.ID,
		SourceBindingID:                     review.SourceBindingID,
		TargetBindingID:                     review.TargetBindingID,
		SourceResourceID:                    review.SourceResourceID,
		TargetResourceID:                    review.TargetResourceID,
		Lifecycle:                           cutoverv1.CutoverFailed,
		ResultingApplicationConfigRevision:  0, // zero!
	}
	_, _, _ = store.CreateCutover(context.Background(), ineligibleCutover, "", "")

	_, _, err := svc.Rollback(context.Background(), review.ProjectID, review.ApplicationID, ineligibleCutover.ID, "user-1", "ineligible-key")
	if err == nil {
		t.Fatal("expected error for ineligible cutover")
	}
	if !strings.Contains(err.Error(), cutoverv1.FailureRollbackCutoverIneligible) {
		t.Fatalf("expected ROLLBACK_CUTOVER_INELIGIBLE, got: %v", err)
	}
}
