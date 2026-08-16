package cutover

import (
	"context"
	"strings"
	"testing"

	cutoverv1 "github.com/opsi-dev/opsi/contracts/go/cutoverv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
)

func TestCutoverFinalizeLifecycleAndSourceRevocation(t *testing.T) {
	svc, _, cutover := setupTestCutoverRollbackFixture(t)
	ctx := context.Background()

	// Verify pre-finalize application state (DATABASE binding points to TargetBindingID)
	preConfig, err := svc.Applications.GetServiceConfiguration(cutover.ProjectID, cutover.ApplicationID)
	if err != nil {
		t.Fatalf("failed to get config: %v", err)
	}
	if len(preConfig.ResourceBindings) != 1 || preConfig.ResourceBindings[0].BindingID != cutover.TargetBindingID {
		t.Fatalf("expected pre-finalize config to point to target %s: %+v", cutover.TargetBindingID, preConfig.ResourceBindings)
	}

	// 1. Execute Finalize
	finalization, reused, err := svc.Finalize(ctx, cutover.ProjectID, cutover.ApplicationID, cutover.ID, "user-1", "finalize-key-1")
	if err != nil {
		t.Fatalf("expected successful finalization, got: %v", err)
	}
	if reused {
		t.Fatal("expected reused=false on initial finalization")
	}
	if finalization.Lifecycle != cutoverv1.FinalizationSucceeded {
		t.Fatalf("expected lifecycle %q, got %q", cutoverv1.FinalizationSucceeded, finalization.Lifecycle)
	}
	if !finalization.VerificationSummary.SourceBindingRevoked {
		t.Fatal("expected SourceBindingRevoked=true")
	}
	if !finalization.VerificationSummary.SourceResourceRetained {
		t.Fatal("expected SourceResourceRetained=true")
	}
	if !finalization.VerificationSummary.TargetDBConnected {
		t.Fatal("expected TargetDBConnected=true")
	}
	if err := finalization.ValidateSucceeded(); err != nil {
		t.Fatalf("expected valid succeeded evidence: %v", err)
	}

	// 2. Verify SOURCE binding was revoked / deleted from store
	_, err = svc.Resources.GetBinding(ctx, cutover.ProjectID, cutover.SourceBindingID)
	if err == nil {
		t.Fatal("expected source binding to be deleted from store")
	}

	// 3. Verify TARGET binding remains intact and Ready
	tgtB, err := svc.Resources.GetBinding(ctx, cutover.ProjectID, cutover.TargetBindingID)
	if err != nil || tgtB.Lifecycle != resourcev1.LifecycleReady {
		t.Fatalf("expected target binding to remain ready, got: %v, %v", tgtB, err)
	}

	// 4. Verify SOURCE & TARGET resources remain intact and Ready
	srcR, err := svc.Resources.Get(ctx, cutover.ProjectID, cutover.SourceResourceID)
	if err != nil || srcR.Lifecycle != resourcev1.LifecycleReady {
		t.Fatalf("expected source resource to remain ready, got: %v, %v", srcR, err)
	}
	tgtR, err := svc.Resources.Get(ctx, cutover.ProjectID, cutover.TargetResourceID)
	if err != nil || tgtR.Lifecycle != resourcev1.LifecycleReady {
		t.Fatalf("expected target resource to remain ready, got: %v, %v", tgtR, err)
	}

	// 5. Verify Application configuration was NOT mutated
	cfg, err := svc.Applications.GetServiceConfiguration(cutover.ProjectID, cutover.ApplicationID)
	if err != nil || cfg.Revision != preConfig.Revision || cfg.ResourceBindings[0].BindingID != cutover.TargetBindingID {
		t.Fatalf("expected application configuration unchanged pointing to target: %v", cfg)
	}

	// 6. Verify rollback attempt on finalized Cutover fails closed
	_, _, err = svc.Rollback(ctx, cutover.ProjectID, cutover.ApplicationID, cutover.ID, "user-1", "rollback-after-finalize")
	if err == nil || !strings.Contains(err.Error(), cutoverv1.FailureCutoverFinalized) {
		t.Fatalf("expected rollback rejection with %q, got: %v", cutoverv1.FailureCutoverFinalized, err)
	}
}

func TestCutoverFinalizeRejectionWhenApplicationStale(t *testing.T) {
	svc, _, cutover := setupTestCutoverRollbackFixture(t)
	ctx := context.Background()

	// Mutate application configuration so DATABASE points to SOURCE instead of TARGET (stale)
	cfg, _ := svc.Applications.GetServiceConfiguration(cutover.ProjectID, cutover.ApplicationID)
	cfg.ResourceBindings = []serviceconfigurationv1.ResourceBinding{
		{LogicalName: "DATABASE", BindingID: cutover.SourceBindingID},
	}
	_ = svc.Applications.(testApplicationAuthority).configs
	svc.Applications.(testApplicationAuthority).configs[cutover.ApplicationID] = cfg

	_, _, err := svc.Finalize(ctx, cutover.ProjectID, cutover.ApplicationID, cutover.ID, "user-1", "finalize-stale")
	if err == nil || !strings.Contains(err.Error(), cutoverv1.FailureFinalizeStale) {
		t.Fatalf("expected error containing %q, got: %v", cutoverv1.FailureFinalizeStale, err)
	}

	// Verify source binding was NOT revoked
	_, err = svc.Resources.GetBinding(ctx, cutover.ProjectID, cutover.SourceBindingID)
	if err != nil {
		t.Fatalf("expected source binding to still exist: %v", err)
	}
}

func TestCutoverFinalizeReplayIdempotency(t *testing.T) {
	svc, _, cutover := setupTestCutoverRollbackFixture(t)
	ctx := context.Background()

	fn1, reused1, err := svc.Finalize(ctx, cutover.ProjectID, cutover.ApplicationID, cutover.ID, "user-1", "same-key")
	if err != nil || reused1 {
		t.Fatalf("first call failed: reused=%v, err=%v", reused1, err)
	}

	fn2, reused2, err := svc.Finalize(ctx, cutover.ProjectID, cutover.ApplicationID, cutover.ID, "user-1", "same-key")
	if err != nil || !reused2 {
		t.Fatalf("replay call failed: reused=%v, err=%v", reused2, err)
	}

	if fn1.ID != fn2.ID {
		t.Fatalf("expected same finalization ID %q, got %q", fn1.ID, fn2.ID)
	}
}

func TestCutoverFinalizeRejectionWhenRolledBack(t *testing.T) {
	svc, store, cutover := setupTestCutoverRollbackFixture(t)
	ctx := context.Background()

	// Add a succeeded rollback record
	rollback := cutoverv1.ApplicationCutoverRollback{
		SchemaVersion:   cutoverv1.RollbackSchemaVersion,
		ID:              "acrb-1",
		ProjectID:       cutover.ProjectID,
		ApplicationID:   cutover.ApplicationID,
		CutoverID:       cutover.ID,
		SourceBindingID: cutover.SourceBindingID,
		TargetBindingID: cutover.TargetBindingID,
		Lifecycle:       cutoverv1.RollbackSucceeded,
	}
	_, _, _ = store.CreateRollback(ctx, rollback, "rb-key", strings.Repeat("r", 64))

	_, _, err := svc.Finalize(ctx, cutover.ProjectID, cutover.ApplicationID, cutover.ID, "user-1", "fn-rolledback")
	if err == nil || !strings.Contains(err.Error(), cutoverv1.FailureFinalizeNotActive) {
		t.Fatalf("expected error containing %q, got: %v", cutoverv1.FailureFinalizeNotActive, err)
	}
}

func TestCutoverFinalizeTargetUnavailable(t *testing.T) {
	svc, _, cutover := setupTestCutoverRollbackFixture(t)
	ctx := context.Background()

	// Mark target resource not ready
	targetRes, _ := svc.Resources.Get(ctx, cutover.ProjectID, cutover.TargetResourceID)
	targetRes.Lifecycle = resourcev1.LifecycleFailed
	svc.Resources.(testResourceAuthority).resources[cutover.TargetResourceID] = targetRes

	_, _, err := svc.Finalize(ctx, cutover.ProjectID, cutover.ApplicationID, cutover.ID, "user-1", "fn-target-unavail")
	if err == nil || !strings.Contains(err.Error(), cutoverv1.FailureFinalizeTargetUnavailable) {
		t.Fatalf("expected error containing %q, got: %v", cutoverv1.FailureFinalizeTargetUnavailable, err)
	}
}
