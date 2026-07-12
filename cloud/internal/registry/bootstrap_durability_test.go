package registry

import (
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
