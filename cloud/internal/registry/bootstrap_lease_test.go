package registry

import (
	"sync"
	"testing"
	"time"
)

func TestConcurrentBootstrapLeaseClaimsExactlyOnce(t *testing.T) {
	service := NewService()
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	project, err := service.CreateProject("org-1", "Demo", "demo", "", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	oldest, err := service.CreateBootstrapSession(project.ID, "first_server", "203.0.113.10", "root", "password", "", "boot-1", 22)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		lease BootstrapSessionLease
		ok    bool
		err   error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, workerID := range []string{"worker-1", "worker-2"} {
		wg.Add(1)
		go func(workerID string) {
			defer wg.Done()
			lease, ok, err := service.LeaseNextBootstrapSession(workerID, now, 15*time.Minute)
			results <- result{lease: lease, ok: ok, err: err}
		}(workerID)
	}
	wg.Wait()
	close(results)
	successes, empty := 0, 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if !result.ok {
			empty++
			continue
		}
		successes++
		if result.lease.Session.ID != oldest.ID {
			t.Fatalf("leased session=%s want=%s", result.lease.Session.ID, oldest.ID)
		}
		if result.lease.LeaseToken == "" || result.lease.Session.LeaseTokenHash == result.lease.LeaseToken {
			t.Fatal("raw lease token was not separated from stored hash")
		}
	}
	if successes != 1 || empty != 1 {
		t.Fatalf("successes=%d empty=%d", successes, empty)
	}
}

func TestBootstrapLeaseOldestEligibleAndOwnerValidation(t *testing.T) {
	service := NewService()
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	project, _ := service.CreateProject("org-1", "Demo", "demo", "", "project-key")
	oldest, _ := service.CreateBootstrapSession(project.ID, "first_server", "203.0.113.10", "root", "password", "", "boot-1", 22)
	now = now.Add(time.Second)
	_, _ = service.CreateBootstrapSession(project.ID, "first_server", "203.0.113.11", "root", "password", "", "boot-2", 22)
	lease, ok, err := service.LeaseNextBootstrapSession("worker-1", now, 15*time.Minute)
	if err != nil || !ok || lease.Session.ID != oldest.ID {
		t.Fatalf("lease=%+v ok=%v err=%v", lease, ok, err)
	}
	if _, err := service.UpdateBootstrapSessionForLease(project.ID, oldest.ID, "worker-2", lease.LeaseToken, "connecting", "connecting", now); apiErrorCode(err) != "BOOTSTRAP_LEASE_OWNER_MISMATCH" {
		t.Fatalf("owner mismatch err=%v", err)
	}
	if _, err := service.UpdateBootstrapSessionForLease(project.ID, oldest.ID, "worker-1", "wrong", "connecting", "connecting", now); apiErrorCode(err) != "BOOTSTRAP_LEASE_INVALID" {
		t.Fatalf("invalid token err=%v", err)
	}
}

func apiErrorCode(err error) string {
	if apiErr, ok := err.(APIError); ok {
		return apiErr.Code
	}
	return ""
}
