package registry

import (
	"sync"
	"testing"
	"time"
)

func TestDeploymentLeaseRetryDeadLetterAndStaleResult(t *testing.T) {
	service, projectID := readyRegistry(t)
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	record := createRegistryService(t, service, projectID, "api", "Dockerfile", "deploy/api.yaml", "svc-lease")
	job, err := service.StartDeployment(projectID, record.ID, "user-1", "dep-lease", "req-lease")
	if err != nil {
		t.Fatal(err)
	}
	job.MaxAttempts = 2
	service.deployments[job.ID] = job

	first, ok, err := service.LeaseDeployment(projectID, job.NodeID)
	if err != nil || !ok {
		t.Fatalf("first lease ok=%v err=%v", ok, err)
	}
	if first.LeaseToken == "" || first.Deployment.AttemptCount != 1 {
		t.Fatalf("bad first lease: %+v", first)
	}
	if _, err := service.CompleteDeployment(projectID, job.NodeID, job.ID, "req-stale", DeploymentResult{Status: DeploymentSucceeded, LeaseToken: "old", IntentHash: job.IntentHash}); apiCode(err) != "DEPLOYMENT_STALE_LEASE" {
		t.Fatalf("stale token err = %#v", err)
	}

	now = now.Add(defaultDeploymentLeaseDuration + time.Second)
	if _, err := service.CompleteDeployment(projectID, job.NodeID, job.ID, "req-expired", DeploymentResult{Status: DeploymentSucceeded, LeaseToken: first.LeaseToken, IntentHash: job.IntentHash}); apiCode(err) != "DEPLOYMENT_LEASE_EXPIRED" {
		t.Fatalf("expired lease err = %#v", err)
	}
	second, ok, err := service.LeaseDeployment(projectID, job.NodeID)
	if err != nil || !ok {
		t.Fatalf("second lease ok=%v err=%v", ok, err)
	}
	if second.LeaseToken == first.LeaseToken || second.Deployment.AttemptCount != 2 {
		t.Fatalf("lease was not retried: first=%+v second=%+v", first, second)
	}

	now = now.Add(defaultDeploymentLeaseDuration + time.Second)
	_, ok, err = service.LeaseDeployment(projectID, job.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("dead-lettered job should not lease again")
	}
	dead := service.deployments[job.ID]
	if dead.Status != DeploymentDeadLetter || dead.FailureCode != "DEPLOYMENT_LEASE_ATTEMPTS_EXHAUSTED" {
		t.Fatalf("job not dead-lettered: %+v", dead)
	}
	if _, locked := service.deployLocks[record.ID]; locked {
		t.Fatal("dead-lettered deployment kept service lock")
	}
}

func TestDuplicateDeploymentResultIsIdempotent(t *testing.T) {
	service, projectID := readyRegistry(t)
	record := createRegistryService(t, service, projectID, "api", "Dockerfile", "deploy/api.yaml", "svc-result")
	job, err := service.StartDeployment(projectID, record.ID, "user-1", "dep-result", "req-result")
	if err != nil {
		t.Fatal(err)
	}
	lease, ok, err := service.LeaseDeployment(projectID, job.NodeID)
	if err != nil || !ok {
		t.Fatalf("lease ok=%v err=%v", ok, err)
	}
	result := DeploymentResult{Status: DeploymentSucceeded, LeaseToken: lease.LeaseToken, IntentHash: job.IntentHash, FinalRevisionRef: "rev-1", RollbackEligible: true}
	done, err := service.CompleteDeployment(projectID, job.NodeID, job.ID, "req-result", result)
	if err != nil {
		t.Fatal(err)
	}
	eventCount := len(service.deployEvents[job.ID])
	again, err := service.CompleteDeployment(projectID, job.NodeID, job.ID, "req-result-dup", result)
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != done.Status || len(service.deployEvents[job.ID]) != eventCount {
		t.Fatalf("duplicate result changed state: first=%+v again=%+v events=%d/%d", done, again, eventCount, len(service.deployEvents[job.ID]))
	}
}

func TestConcurrentAgentsCannotLeaseSameDeployment(t *testing.T) {
	service, projectID := readyRegistry(t)
	record := createRegistryService(t, service, projectID, "api", "Dockerfile", "deploy/api.yaml", "svc-concurrent")
	job, err := service.StartDeployment(projectID, record.ID, "user-1", "dep-concurrent", "req-concurrent")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := service.LeaseDeployment(projectID, job.NodeID)
			if err != nil {
				t.Errorf("lease error: %v", err)
			}
			results <- ok
		}()
	}
	wg.Wait()
	close(results)
	var leased int
	for ok := range results {
		if ok {
			leased++
		}
	}
	if leased != 1 {
		t.Fatalf("leased count = %d, want 1", leased)
	}
}

func apiCode(err error) string {
	if err == nil {
		return ""
	}
	if apiErr, ok := err.(APIError); ok {
		return apiErr.Code
	}
	return err.Error()
}
