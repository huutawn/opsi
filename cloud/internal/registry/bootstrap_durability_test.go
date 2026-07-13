package registry

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBootstrapLeaseHeartbeatRenewalAndValidation(t *testing.T) {
	service, projectID, session := newBootstrapDurabilityFixture(t)
	now := service.clock()
	lease, ok, err := service.LeaseNextBootstrapSession("worker-1", now, 90*time.Second)
	if err != nil || !ok {
		t.Fatalf("lease ok=%v err=%v", ok, err)
	}
	renewAt := now.Add(20 * time.Second)
	renewed, err := service.RenewBootstrapLease(projectID, session.ID, "worker-1", lease.LeaseToken, renewAt, 90*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.AttemptCount != 1 || renewed.LeaseHeartbeatAt == nil || !renewed.LeaseHeartbeatAt.Equal(renewAt) || renewed.LeaseExpiresAt == nil || !renewed.LeaseExpiresAt.Equal(renewAt.Add(90*time.Second)) {
		t.Fatalf("renewed session=%+v", renewed)
	}
	if _, err := service.RenewBootstrapLease(projectID, session.ID, "worker-2", lease.LeaseToken, renewAt, 90*time.Second); apiErrorCode(err) != "BOOTSTRAP_LEASE_OWNER_MISMATCH" {
		t.Fatalf("wrong owner err=%v", err)
	}
	if _, err := service.RenewBootstrapLease(projectID, session.ID, "worker-1", "wrong", renewAt, 90*time.Second); apiErrorCode(err) != "BOOTSTRAP_LEASE_INVALID" {
		t.Fatalf("wrong token err=%v", err)
	}
	if _, err := service.RenewBootstrapLease(projectID, session.ID, "worker-1", lease.LeaseToken, renewed.LeaseExpiresAt.Add(time.Nanosecond), 90*time.Second); apiErrorCode(err) != "BOOTSTRAP_LEASE_EXPIRED" {
		t.Fatalf("expired heartbeat err=%v", err)
	}
}

func TestBootstrapRetryFailureBackoffAndCompletion(t *testing.T) {
	service, projectID, session := newBootstrapDurabilityFixture(t)
	now := service.clock()
	lease, _, _ := service.LeaseNextBootstrapSession("worker-1", now, 90*time.Second)
	failed, err := service.FinishBootstrapSessionForLease(projectID, session.ID, "worker-1", lease.LeaseToken, BootstrapFinishResult{Status: "failed", FailureCode: "BOOTSTRAP_CONNECT_FAILED", MessageRedacted: "temporary network timeout", Retryable: true}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != BootstrapRetryWait || failed.NextAttemptAt == nil || !failed.NextAttemptAt.Equal(now.Add(time.Second).Add(bootstrapRetryDelay(1))) || failed.LeaseTokenHash != "" {
		t.Fatalf("retry state=%+v", failed)
	}
	if _, ok, err := service.LeaseNextBootstrapSession("worker-2", failed.NextAttemptAt.Add(-time.Nanosecond), 90*time.Second); err != nil || ok {
		t.Fatalf("leased before backoff ok=%v err=%v", ok, err)
	}
	second, ok, err := service.LeaseNextBootstrapSession("worker-2", *failed.NextAttemptAt, 90*time.Second)
	if err != nil || !ok || second.Session.AttemptCount != 2 {
		t.Fatalf("second lease=%+v ok=%v err=%v", second, ok, err)
	}
	completed, err := service.FinishBootstrapSessionForLease(projectID, session.ID, "worker-2", second.LeaseToken, BootstrapFinishResult{Status: "completed"}, failed.NextAttemptAt.Add(time.Second))
	if err != nil || completed.Status != "completed" || completed.LeaseOwner != "" || completed.FinishedAt == nil {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
}

func TestBootstrapExpiredLeaseRecoveryAndAttemptExhaustion(t *testing.T) {
	service, _, session := newBootstrapDurabilityFixture(t)
	now := service.clock()
	lease, _, _ := service.LeaseNextBootstrapSession("worker-1", now, 30*time.Second)
	summary, err := service.RecoverExpiredBootstrapLeases(now.Add(30 * time.Second))
	if err != nil || len(summary.Recovered) != 1 {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	recovered := summary.Recovered[0]
	if recovered.Status != BootstrapRetryWait || recovered.LastFailureCode != "BOOTSTRAP_LEASE_EXPIRED" || recovered.NextAttemptAt == nil || recovered.LeaseTokenHash != "" {
		t.Fatalf("recovered=%+v", recovered)
	}
	second, ok, err := service.LeaseNextBootstrapSession("worker-2", *recovered.NextAttemptAt, 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("second lease ok=%v err=%v", ok, err)
	}
	service.mu.Lock()
	stored := service.bootstraps[session.ID]
	stored.MaxAttempts = 2
	service.bootstraps[session.ID] = stored
	service.mu.Unlock()
	summary, err = service.RecoverExpiredBootstrapLeases(second.LeaseExpiresAt)
	if err != nil || len(summary.DeadLettered) != 1 {
		t.Fatalf("dead-letter summary=%+v err=%v", summary, err)
	}
	dead := summary.DeadLettered[0]
	if dead.Status != BootstrapDeadLetter || dead.DeadLetteredAt == nil || dead.AttemptCount != 2 {
		t.Fatalf("dead-letter=%+v", dead)
	}
	if _, ok, err := service.LeaseNextBootstrapSession("worker-3", second.LeaseExpiresAt.Add(time.Hour), 30*time.Second); err != nil || ok {
		t.Fatalf("dead-letter leased ok=%v err=%v lease=%+v", ok, err, lease)
	}
}

func TestBootstrapPermanentFailureDeadLettersImmediately(t *testing.T) {
	service, projectID, session := newBootstrapDurabilityFixture(t)
	now := service.clock()
	lease, _, _ := service.LeaseNextBootstrapSession("worker-1", now, 90*time.Second)
	dead, err := service.FinishBootstrapSessionForLease(projectID, session.ID, "worker-1", lease.LeaseToken, BootstrapFinishResult{Status: "failed", FailureCode: "SSH_AUTH_METHOD_UNSUPPORTED", MessageRedacted: "private key mode is unsupported"}, now.Add(time.Second))
	if err != nil || dead.Status != BootstrapDeadLetter || dead.DeadLetteredAt == nil || dead.NextAttemptAt != nil {
		t.Fatalf("dead=%+v err=%v", dead, err)
	}
}

func TestBootstrapManualRetryIsIdempotentAndDeadLetterOnly(t *testing.T) {
	service, projectID, session := newBootstrapDurabilityFixture(t)
	now := service.clock()
	if _, err := service.ManualRetryBootstrapSession(projectID, session.ID, "retry-1", now); apiErrorCode(err) != "BOOTSTRAP_NOT_DEAD_LETTER" {
		t.Fatalf("non-dead-letter err=%v", err)
	}
	service.mu.Lock()
	stored := service.bootstraps[session.ID]
	stored.Status = BootstrapDeadLetter
	stored.AttemptCount = 3
	stored.DeadLetteredAt = &now
	service.bootstraps[session.ID] = stored
	service.mu.Unlock()
	first, err := service.ManualRetryBootstrapSession(projectID, session.ID, "retry-1", now.Add(time.Second))
	if err != nil || !first.Applied || first.Session.Status != BootstrapPending || first.Session.AttemptCount != 0 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := service.ManualRetryBootstrapSession(projectID, session.ID, "retry-1", now.Add(2*time.Second))
	if err != nil || second.Applied || second.Session.ID != first.Session.ID {
		t.Fatalf("duplicate=%+v err=%v", second, err)
	}
}

func TestConcurrentBootstrapRecoveryTransitionsOnce(t *testing.T) {
	service, _, _ := newBootstrapDurabilityFixture(t)
	now := service.clock()
	lease, _, _ := service.LeaseNextBootstrapSession("worker-1", now, time.Second)
	var wg sync.WaitGroup
	results := make(chan BootstrapRecoverySummary, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			summary, _ := service.RecoverExpiredBootstrapLeases(lease.LeaseExpiresAt)
			results <- summary
		}()
	}
	wg.Wait()
	close(results)
	recovered := 0
	for result := range results {
		recovered += len(result.Recovered)
	}
	if recovered != 1 {
		t.Fatalf("recovery transitions=%d want=1", recovered)
	}
}

func TestBootstrapRetryDelayIsBounded(t *testing.T) {
	if bootstrapRetryDelay(1) != 5*time.Second || bootstrapRetryDelay(2) != 10*time.Second || bootstrapRetryDelay(100) != 5*time.Minute {
		t.Fatalf("unexpected retry delays: %s %s %s", bootstrapRetryDelay(1), bootstrapRetryDelay(2), bootstrapRetryDelay(100))
	}
}

func TestBootstrapCheckpointTransitionsAndLeaseValidation(t *testing.T) {
	service, projectID, session := newBootstrapDurabilityFixture(t)
	now := service.clock()
	lease, ok, err := service.LeaseNextBootstrapSession("worker-1", now, 90*time.Second)
	if err != nil || !ok {
		t.Fatalf("lease ok=%v err=%v", ok, err)
	}
	initial := testBootstrapCheckpoint(0)
	initialized, err := service.UpdateBootstrapCheckpointForLease(projectID, session.ID, "worker-1", lease.LeaseToken, initial, now.Add(time.Second))
	if err != nil || initialized.Checkpoint.NextStepIndex != 0 || initialized.Checkpoint.UpdatedAt == nil {
		t.Fatalf("initialized=%+v err=%v", initialized.Checkpoint, err)
	}
	eventsAfterInitialize := len(service.events[session.ID])
	replayed, err := service.UpdateBootstrapCheckpointForLease(projectID, session.ID, "worker-1", lease.LeaseToken, initial, now.Add(2*time.Second))
	if err != nil || len(service.events[session.ID]) != eventsAfterInitialize || !replayed.Checkpoint.UpdatedAt.Equal(*initialized.Checkpoint.UpdatedAt) {
		t.Fatalf("replay=%+v events=%d err=%v", replayed.Checkpoint, len(service.events[session.ID]), err)
	}
	different := initial
	different.PlanFingerprint = strings.Repeat("b", 64)
	if _, err := service.UpdateBootstrapCheckpointForLease(projectID, session.ID, "worker-1", lease.LeaseToken, different, now.Add(2*time.Second)); apiErrorCode(err) != "BOOTSTRAP_PLAN_MISMATCH" {
		t.Fatalf("different initialization err=%v", err)
	}
	advancedRequest := testBootstrapCheckpoint(1)
	advanced, err := service.UpdateBootstrapCheckpointForLease(projectID, session.ID, "worker-1", lease.LeaseToken, advancedRequest, now.Add(3*time.Second))
	if err != nil || advanced.Checkpoint.NextStepIndex != 1 || advanced.Checkpoint.LastCompletedStep != "preflight" {
		t.Fatalf("advanced=%+v err=%v", advanced.Checkpoint, err)
	}
	eventsAfterAdvance := len(service.events[session.ID])
	advanceTime := *advanced.Checkpoint.UpdatedAt
	advancedReplay, err := service.UpdateBootstrapCheckpointForLease(projectID, session.ID, "worker-1", lease.LeaseToken, advancedRequest, now.Add(4*time.Second))
	if err != nil || len(service.events[session.ID]) != eventsAfterAdvance || !advancedReplay.Checkpoint.UpdatedAt.Equal(advanceTime) {
		t.Fatalf("advance replay=%+v events=%d err=%v", advancedReplay.Checkpoint, len(service.events[session.ID]), err)
	}

	for name, checkpoint := range map[string]BootstrapCheckpoint{
		"regression":            testBootstrapCheckpoint(0),
		"jump":                  testBootstrapCheckpoint(3),
		"plan version mismatch": func() BootstrapCheckpoint { c := testBootstrapCheckpoint(2); c.PlanVersion = "other-v1"; return c }(),
		"fingerprint mismatch": func() BootstrapCheckpoint {
			c := testBootstrapCheckpoint(2)
			c.PlanFingerprint = strings.Repeat("b", 64)
			return c
		}(),
		"schema mismatch": func() BootstrapCheckpoint { c := testBootstrapCheckpoint(2); c.SchemaVersion = 2; return c }(),
		"negative index":  func() BootstrapCheckpoint { c := testBootstrapCheckpoint(0); c.NextStepIndex = -1; return c }(),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.UpdateBootstrapCheckpointForLease(projectID, session.ID, "worker-1", lease.LeaseToken, checkpoint, now.Add(5*time.Second)); apiErrorCode(err) != "BOOTSTRAP_CHECKPOINT_INVALID" && apiErrorCode(err) != "BOOTSTRAP_PLAN_MISMATCH" {
				t.Fatalf("err=%v", err)
			}
		})
	}
	if _, err := service.UpdateBootstrapCheckpointForLease(projectID, session.ID, "worker-2", lease.LeaseToken, testBootstrapCheckpoint(2), now.Add(5*time.Second)); apiErrorCode(err) != "BOOTSTRAP_LEASE_OWNER_MISMATCH" {
		t.Fatalf("wrong worker err=%v", err)
	}
	if _, err := service.UpdateBootstrapCheckpointForLease(projectID, session.ID, "worker-1", "wrong", testBootstrapCheckpoint(2), now.Add(5*time.Second)); apiErrorCode(err) != "BOOTSTRAP_LEASE_INVALID" {
		t.Fatalf("wrong token err=%v", err)
	}
	if _, err := service.UpdateBootstrapCheckpointForLease(projectID, session.ID, "worker-1", lease.LeaseToken, testBootstrapCheckpoint(2), lease.LeaseExpiresAt); apiErrorCode(err) != "BOOTSTRAP_LEASE_EXPIRED" {
		t.Fatalf("expired lease err=%v", err)
	}
}

func TestBootstrapCheckpointMetadataPreservesV1AndAcceptsV2(t *testing.T) {
	if FirstServerBootstrapPlanVersion != "first-server-v1" {
		t.Fatalf("v1 metadata changed: %q", FirstServerBootstrapPlanVersion)
	}
	want := []string{"preflight", "install_k3s", "install_agent", "register_agent"}
	if got := FirstServerBootstrapPlanV2StepIDs(); !slices.Equal(got, want) {
		t.Fatalf("v2 step IDs=%v", got)
	}
	v2 := BootstrapCheckpoint{
		SchemaVersion:     BootstrapCheckpointSchemaVersion,
		PlanVersion:       FirstServerBootstrapPlanVersionV2,
		PlanFingerprint:   strings.Repeat("b", 64),
		NextStepIndex:     2,
		LastCompletedStep: "install_k3s",
	}
	if err := validateBootstrapCheckpointFormat(v2); err != nil {
		t.Fatalf("v2 checkpoint rejected: %v", err)
	}
	v1 := testBootstrapCheckpoint(1)
	requested := v2
	requested.NextStepIndex = 1
	requested.LastCompletedStep = "preflight"
	if _, err := validateBootstrapCheckpointTransition(v1, requested); apiErrorCode(err) != "BOOTSTRAP_PLAN_MISMATCH" {
		t.Fatalf("v1 to v2 transition err=%v", err)
	}
}

func TestBootstrapCheckpointSurvivesRetryRecoveryNewWorkerAndFinish(t *testing.T) {
	service, projectID, session := newBootstrapDurabilityFixture(t)
	now := service.clock()
	first, _, _ := service.LeaseNextBootstrapSession("worker-1", now, 90*time.Second)
	_, _ = service.UpdateBootstrapCheckpointForLease(projectID, session.ID, "worker-1", first.LeaseToken, testBootstrapCheckpoint(0), now.Add(time.Second))
	checkpointed, err := service.UpdateBootstrapCheckpointForLease(projectID, session.ID, "worker-1", first.LeaseToken, testBootstrapCheckpoint(2), now.Add(2*time.Second))
	if apiErrorCode(err) != "BOOTSTRAP_CHECKPOINT_INVALID" {
		t.Fatalf("jump should fail err=%v checkpoint=%+v", err, checkpointed.Checkpoint)
	}
	_, _ = service.UpdateBootstrapCheckpointForLease(projectID, session.ID, "worker-1", first.LeaseToken, testBootstrapCheckpoint(1), now.Add(2*time.Second))
	checkpointed, err = service.UpdateBootstrapCheckpointForLease(projectID, session.ID, "worker-1", first.LeaseToken, testBootstrapCheckpoint(2), now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	failed, err := service.FinishBootstrapSessionForLease(projectID, session.ID, "worker-1", first.LeaseToken, BootstrapFinishResult{Status: "failed", FailureCode: "BOOTSTRAP_CLOUD_TEMPORARY", MessageRedacted: "temporary", Retryable: true}, now.Add(4*time.Second))
	if err != nil || failed.Checkpoint != checkpointed.Checkpoint {
		t.Fatalf("retry checkpoint=%+v err=%v", failed.Checkpoint, err)
	}
	second, ok, err := service.LeaseNextBootstrapSession("worker-2", *failed.NextAttemptAt, 90*time.Second)
	if err != nil || !ok || second.Session.Checkpoint.NextStepIndex != 2 {
		t.Fatalf("second lease=%+v ok=%v err=%v", second, ok, err)
	}
	summary, err := service.RecoverExpiredBootstrapLeases(second.LeaseExpiresAt)
	if err != nil || len(summary.Recovered) != 1 || summary.Recovered[0].Checkpoint.NextStepIndex != 2 {
		t.Fatalf("recovery=%+v err=%v", summary, err)
	}
	third, ok, err := service.LeaseNextBootstrapSession("worker-3", *summary.Recovered[0].NextAttemptAt, 90*time.Second)
	if err != nil || !ok || third.Session.Checkpoint.NextStepIndex != 2 {
		t.Fatalf("third lease=%+v ok=%v err=%v", third, ok, err)
	}
	finished, err := service.FinishBootstrapSessionForLease(projectID, session.ID, "worker-3", third.LeaseToken, BootstrapFinishResult{Status: "completed"}, summary.Recovered[0].NextAttemptAt.Add(time.Second))
	if err != nil || finished.Checkpoint.NextStepIndex != 2 {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
}

func TestBootstrapCheckpointSurvivesManualRetry(t *testing.T) {
	service, projectID, session := newBootstrapDurabilityFixture(t)
	now := service.clock()
	lease, _, _ := service.LeaseNextBootstrapSession("worker-1", now, 90*time.Second)
	_, _ = service.UpdateBootstrapCheckpointForLease(projectID, session.ID, "worker-1", lease.LeaseToken, testBootstrapCheckpoint(0), now.Add(time.Second))
	checkpointed, _ := service.UpdateBootstrapCheckpointForLease(projectID, session.ID, "worker-1", lease.LeaseToken, testBootstrapCheckpoint(1), now.Add(2*time.Second))
	dead, err := service.FinishBootstrapSessionForLease(projectID, session.ID, "worker-1", lease.LeaseToken, BootstrapFinishResult{Status: "failed", FailureCode: "BOOTSTRAP_PLAN_MISMATCH", MessageRedacted: "plan mismatch", Retryable: false}, now.Add(3*time.Second))
	if err != nil || dead.Status != BootstrapDeadLetter {
		t.Fatalf("dead=%+v err=%v", dead, err)
	}
	retried, err := service.ManualRetryBootstrapSession(projectID, session.ID, "retry-checkpoint", now.Add(4*time.Second))
	if err != nil || retried.Session.Checkpoint != checkpointed.Checkpoint {
		t.Fatalf("manual retry checkpoint=%+v err=%v", retried.Session.Checkpoint, err)
	}
}

func testBootstrapCheckpoint(nextIndex int) BootstrapCheckpoint {
	checkpoint := BootstrapCheckpoint{
		SchemaVersion:   BootstrapCheckpointSchemaVersion,
		PlanVersion:     FirstServerBootstrapPlanVersion,
		PlanFingerprint: strings.Repeat("a", 64),
		NextStepIndex:   nextIndex,
	}
	if nextIndex > 0 && nextIndex <= len(BootstrapStepIDs(FirstServerBootstrapPlanVersion)) {
		checkpoint.LastCompletedStep = BootstrapStepIDs(FirstServerBootstrapPlanVersion)[nextIndex-1]
	}
	return checkpoint
}

func newBootstrapDurabilityFixture(t *testing.T) (*Service, string, BootstrapSession) {
	t.Helper()
	service := NewService()
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	project, err := service.CreateProject("org-1", "Demo", "demo", "", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateBootstrapSession(project.ID, "first_server", "203.0.113.10", "root", "password", "", "boot-key", 22)
	if err != nil {
		t.Fatal(err)
	}
	return service, project.ID, session
}
