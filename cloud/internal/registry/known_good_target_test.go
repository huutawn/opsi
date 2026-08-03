package registry

import (
	"strings"
	"testing"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

func TestMemoryDeploymentKnownGoodDoesNotCrossRuntimeTarget(t *testing.T) {
	service, projectID, base := rolloutRegistryFixture(t, "target-scope")
	now := base.UpdatedAt.Add(time.Second)
	service.now = func() time.Time { return now }

	node, agent := replaceMemoryRuntimeTarget(t, service, projectID, base)
	snapshot := snapshotForRuntimeTarget(base, node.ID, agent.ID, "new-target")
	job, _, err := service.StartImmutableDeployment(snapshot, "user-1", "new-target", "new-target")
	if err != nil {
		t.Fatal(err)
	}
	assertNoPreviousKnownGood(t, job)
}

func TestMemoryFailedPreMutationDoesNotPoisonKnownGood(t *testing.T) {
	service, projectID, base := rolloutRegistryFixture(t, "failed-poison")
	now := base.UpdatedAt.Add(time.Second)
	service.now = func() time.Time { return now }

	agent, err := service.RegisterAgent(projectID, base.NodeID, "sha256:new-agent", "new-agent-hash", "v2", "new-agent", map[string]any{"deploy": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordAgentHeartbeat(projectID, base.NodeID, AgentHeartbeat{Version: "v2", NodeReady: true, K3SStatus: "ready", Capabilities: map[string]any{"deploy": true}}); err != nil {
		t.Fatal(err)
	}

	poisonedSnapshot := snapshotForRuntimeTarget(base, base.NodeID, agent.ID, "failed-target")
	poisoned, _, err := service.StartImmutableDeployment(poisonedSnapshot, "user-1", "failed-target", "failed-target")
	if err != nil {
		t.Fatal(err)
	}
	assertNoPreviousKnownGood(t, poisoned)
	lease := leaseRollout(t, service, projectID, poisoned)
	if _, err := service.CompleteDeployment(projectID, poisoned.NodeID, poisoned.ID, "failed-target", preMutationRolloutResult(lease, deploymentv1.RolloutCodeOwnershipConflict)); err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Second)
	nextSnapshot := snapshotForRuntimeTarget(base, base.NodeID, agent.ID, "after-failure")
	next, _, err := service.StartImmutableDeployment(nextSnapshot, "user-1", "after-failure", "after-failure")
	if err != nil {
		t.Fatal(err)
	}
	assertNoPreviousKnownGood(t, next)
}

func TestMemoryKnownGoodAcceptsOnlyExactFactualSuccessOrRollback(t *testing.T) {
	service := NewService()
	target := DeploymentJob{ProjectID: "project", EnvironmentID: "environment", RuntimeID: "runtime", ServiceID: "service", NodeID: "node", AgentID: "agent"}
	now := time.Date(2026, 8, 3, 2, 0, 0, 0, time.UTC)

	success := knownGoodCandidate(target, "success", deploymentv1.RolloutStateSucceeded, now)
	service.deployments[success.ID] = success
	id, hash, digest := service.latestKnownGoodLocked(target.ProjectID, target.EnvironmentID, target.RuntimeID, target.ServiceID, target.NodeID, target.AgentID)
	if id != success.KnownGoodID || hash != success.KnownGoodHash || digest != success.CurrentDigest {
		t.Fatalf("successful exact target was not selected: %q %q %q", id, hash, digest)
	}

	rolledBack := knownGoodCandidate(target, "rolled-back", deploymentv1.RolloutStateRolledBack, now.Add(time.Second))
	service.deployments[rolledBack.ID] = rolledBack
	for index, state := range []string{deploymentv1.RolloutStateFailed, deploymentv1.RolloutStateRollbackFailed, deploymentv1.RolloutStatePrepared, "cancelled"} {
		candidate := knownGoodCandidate(target, "excluded-"+state, state, now.Add(time.Duration(index+2)*time.Second))
		service.deployments[candidate.ID] = candidate
	}
	resultless := knownGoodCandidate(target, "resultless", deploymentv1.RolloutStateSucceeded, now.Add(6*time.Second))
	resultless.TerminalResult = nil
	service.deployments[resultless.ID] = resultless
	preview := knownGoodCandidate(target, "preview", deploymentv1.RolloutStateSucceeded, now.Add(7*time.Second))
	preview.Snapshot.Preview = &deploymentv1.PreviewSpec{}
	service.deployments[preview.ID] = preview
	malformed := knownGoodCandidate(target, "malformed", deploymentv1.RolloutStateSucceeded, now.Add(8*time.Second))
	malformed.TerminalResult.KnownGoodHash = ""
	service.deployments[malformed.ID] = malformed

	id, hash, digest = service.latestKnownGoodLocked(target.ProjectID, target.EnvironmentID, target.RuntimeID, target.ServiceID, target.NodeID, target.AgentID)
	if id != rolledBack.KnownGoodID || hash != rolledBack.KnownGoodHash || digest != rolledBack.CurrentDigest {
		t.Fatalf("factual rollback was not retained across excluded rows: %q %q %q", id, hash, digest)
	}
}

func TestMemoryExposureKnownGoodDoesNotCrossAgentTarget(t *testing.T) {
	service, projectID, base := rolloutRegistryFixture(t, "exposure-target")
	now := base.UpdatedAt.Add(time.Second)
	service.now = func() time.Time { return now }
	agent, err := service.RegisterAgent(projectID, base.NodeID, "sha256:exposure-agent", "exposure-agent-hash", "v2", "exposure-agent", map[string]any{"deploy": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordAgentHeartbeat(projectID, base.NodeID, AgentHeartbeat{Version: "v2", NodeReady: true, K3SStatus: "ready", Capabilities: map[string]any{"deploy": true}}); err != nil {
		t.Fatal(err)
	}

	legacyBase := legacyBaseForRuntimeTarget(base, base.NodeID, agent.ID, "dep-exposure-new-target", now)
	legacyBase.TerminalResult = &deploymentv1.AgentResult{Status: deploymentv1.RolloutStateSucceeded, RolloutState: deploymentv1.RolloutStateSucceeded}
	service.deployments[legacyBase.ID] = legacyBase
	request := rolloutExposureRequest(t, legacyBase, "dep-exposure-target-scope", "target.example.com", "/")
	job, _, err := service.StartExposureRollout(projectID, "user-1", "exposure-target-scope", "exposure-target-scope", request)
	if err != nil {
		t.Fatal(err)
	}
	assertNoPreviousKnownGood(t, job)
}

func replaceMemoryRuntimeTarget(t *testing.T, service *Service, projectID string, base DeploymentJob) (Node, Agent) {
	t.Helper()
	if _, _, err := service.MarkNodeOffline(projectID, base.NodeID, "user-1", "retire-old-target", "retire-old-target"); err != nil {
		t.Fatal(err)
	}
	node, err := service.UpsertNode(projectID, "replacement", "server", NodeHealthy, "203.0.113.11", "", "replacement-node")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := service.RegisterAgent(projectID, node.ID, "sha256:replacement", "replacement-hash", "v2", "replacement-agent", map[string]any{"deploy": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordAgentHeartbeat(projectID, node.ID, AgentHeartbeat{Version: "v2", NodeReady: true, K3SStatus: "ready", Capabilities: map[string]any{"deploy": true}}); err != nil {
		t.Fatal(err)
	}
	return node, agent
}

func snapshotForRuntimeTarget(base DeploymentJob, nodeID, agentID, key string) deploymentv1.JobSnapshot {
	snapshot := *base.Snapshot
	snapshot.PayloadHash = "payload-" + key
	snapshot.Authority.BuildRecord.ID = "br-" + key
	snapshot.Authority.NodeID = nodeID
	snapshot.Authority.AgentID = agentID
	return snapshot
}

func knownGoodCandidate(target DeploymentJob, id, state string, updatedAt time.Time) DeploymentJob {
	character := "a"
	if state == deploymentv1.RolloutStateRolledBack {
		character = "b"
	}
	knownID, knownHash := knownGoodIdentity(character)
	digest := "sha256:" + strings.Repeat(character, 64)
	result := &deploymentv1.AgentResult{Status: state, RolloutState: state, KnownGoodID: knownID, KnownGoodHash: knownHash, CurrentDigest: digest}
	return DeploymentJob{ID: "dep-" + id, ProjectID: target.ProjectID, EnvironmentID: target.EnvironmentID, RuntimeID: target.RuntimeID, ServiceID: target.ServiceID, NodeID: target.NodeID, AgentID: target.AgentID, Status: state, RolloutState: state, Snapshot: &deploymentv1.JobSnapshot{}, TerminalResult: result, KnownGoodID: knownID, KnownGoodHash: knownHash, CurrentDigest: digest, UpdatedAt: updatedAt}
}

func legacyBaseForRuntimeTarget(base DeploymentJob, nodeID, agentID, id string, updatedAt time.Time) DeploymentJob {
	snapshot := snapshotForRuntimeTarget(base, nodeID, agentID, id)
	legacy := base
	legacy.ID = id
	legacy.NodeID = nodeID
	legacy.AgentID = agentID
	legacy.IdempotencyKey = id
	legacy.PayloadHash = "payload-" + id
	legacy.Snapshot = &snapshot
	legacy.TerminalResult = nil
	legacy.RolloutIntent = nil
	legacy.RolloutState = deploymentv1.RolloutStateSucceeded
	legacy.RolloutStateHash = ""
	legacy.CurrentDigest = ""
	legacy.PreviousDigest = ""
	legacy.KnownGoodID = ""
	legacy.KnownGoodHash = ""
	legacy.CreatedAt = updatedAt
	legacy.UpdatedAt = updatedAt
	return legacy
}

func assertNoPreviousKnownGood(t *testing.T, job DeploymentJob) {
	t.Helper()
	if job.RolloutIntent == nil || job.RolloutIntent.PreviousKnownGoodID != "" || job.RolloutIntent.PreviousKnownGoodHash != "" || job.RolloutIntent.PreviousDigest != "" || job.KnownGoodID != "" || job.KnownGoodHash != "" || job.PreviousDigest != "" {
		t.Fatalf("unexpected previous known-good: %+v", job)
	}
}

func knownGoodIdentity(character string) (string, string) {
	return "known-good-" + character, strings.Repeat(character, 64)
}
