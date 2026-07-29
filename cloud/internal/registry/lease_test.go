package registry

import (
	"strings"
	"sync"
	"testing"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

func TestLegacyDeploymentIsRetiredWithoutBlockingCanonicalLease(t *testing.T) {
	service, projectID := readyRegistry(t)
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	record := createRegistryService(t, service, projectID, "api", "Dockerfile", "deploy/api.yaml", "svc-retired")
	nodes, err := service.ListNodes(projectID)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("nodes err=%v nodes=%+v", err, nodes)
	}
	legacy := DeploymentJob{
		ID: "dep-legacy", OrgID: record.OrgID, ProjectID: projectID,
		EnvironmentID: record.EnvironmentID, RuntimeID: record.RuntimeID,
		ServiceID: record.ID, Status: DeploymentQueued, NodeID: nodes[0].ID,
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute),
	}
	service.deployments[legacy.ID] = legacy

	snapshot := immutableSnapshot(t, service, projectID, record.ID, "canonical-behind-legacy")
	canonical, _, err := service.StartImmutableDeployment(snapshot, "user-1", "dep-canonical", "req-canonical")
	if err != nil {
		t.Fatal(err)
	}
	lease, ok, err := service.LeaseDeployment(projectID, nodes[0].ID)
	if err != nil || !ok {
		t.Fatalf("canonical lease ok=%v err=%v", ok, err)
	}
	if lease.Deployment.ID != canonical.ID || lease.Command == nil {
		t.Fatalf("legacy job reached Agent or canonical command missing: %+v", lease)
	}
	retired := service.deployments[legacy.ID]
	if retired.Status != DeploymentFailed || retired.FailureCode != LegacyDeploymentRetired || retired.FinishedAt == nil {
		t.Fatalf("legacy job was not retired deterministically: %+v", retired)
	}
	if lock := service.deployLocks[record.ID]; lock.DeploymentID != canonical.ID {
		t.Fatalf("legacy retirement released the canonical deployment lock: %+v", lock)
	}
	if events := service.deployEvents[legacy.ID]; len(events) != 1 || events[0].MessageRedacted != "legacy deployment jobs are retired" {
		t.Fatalf("legacy retirement evidence is missing or unsafe: %+v", events)
	}
	if _, _, err := service.RetryDeployment(projectID, legacy.ID, "retry-legacy", "req-retry"); apiCode(err) != LegacyDeploymentRetired {
		t.Fatalf("legacy retry err=%v", err)
	}
	if _, err := service.RollbackDeployment(projectID, legacy.ID, "user-1", "rollback-legacy", "req-rollback"); apiCode(err) != LegacyDeploymentRetired {
		t.Fatalf("legacy rollback err=%v", err)
	}
}

func TestDuplicateDeploymentResultIsIdempotent(t *testing.T) {
	service, projectID := readyRegistry(t)
	record := createRegistryService(t, service, projectID, "api", "Dockerfile", "deploy/api.yaml", "svc-result")
	snapshot := immutableSnapshot(t, service, projectID, record.ID, "result")
	job, _, err := service.StartImmutableDeployment(snapshot, "user-1", "dep-result", "req-result")
	if err != nil {
		t.Fatal(err)
	}
	lease, ok, err := service.LeaseDeployment(projectID, job.NodeID)
	if err != nil || !ok {
		t.Fatalf("lease ok=%v err=%v", ok, err)
	}
	reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateApplying, "1", "")
	reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateWaiting, "2", "")
	reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateSucceeded, "3", "")
	result := rolloutResult(lease, deploymentv1.RolloutStateSucceeded, "3", job.DesiredDigest, job.RolloutIntent.RolloutID, strings.Repeat("a", 64), "")
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
	snapshot := immutableSnapshot(t, service, projectID, record.ID, "concurrent")
	job, _, err := service.StartImmutableDeployment(snapshot, "user-1", "dep-concurrent", "req-concurrent")
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
