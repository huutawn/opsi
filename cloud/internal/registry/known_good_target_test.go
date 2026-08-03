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
	service, _, success := rolloutRegistryFixture(t, "canonical-history")
	id, hash, digest := service.latestKnownGoodLocked(success.ProjectID, success.EnvironmentID, success.RuntimeID, success.ServiceID, success.NodeID, success.AgentID)
	if id != success.KnownGoodID || hash != success.KnownGoodHash || digest != success.CurrentDigest {
		t.Fatalf("successful exact target was not selected: %q %q %q", id, hash, digest)
	}
	if id, hash, digest := service.latestKnownGoodLocked(success.ProjectID, success.EnvironmentID, success.RuntimeID, success.ServiceID, success.NodeID, "agent-other"); id != "" || hash != "" || digest != "" {
		t.Fatalf("same-node different-Agent history was selected: %q %q %q", id, hash, digest)
	}
	if id, hash, digest := service.latestKnownGoodLocked(success.ProjectID, success.EnvironmentID, success.RuntimeID, success.ServiceID, "node-other", success.AgentID); id != "" || hash != "" || digest != "" {
		t.Fatalf("different-node history was selected: %q %q %q", id, hash, digest)
	}

	now := success.UpdatedAt.Add(time.Second)
	rolledBack := canonicalKnownGoodCandidate(t, success, "dep-memory-rolled-back", deploymentv1.RolloutStateRolledBack, now)
	service.deployments[rolledBack.ID] = rolledBack
	for index, state := range []string{deploymentv1.RolloutStateFailed, deploymentv1.RolloutStateRollbackFailed, deploymentv1.RolloutStatePrepared, "cancelled"} {
		candidate := canonicalKnownGoodCandidate(t, success, "dep-memory-excluded-"+state, deploymentv1.RolloutStateSucceeded, now.Add(time.Duration(index+1)*time.Second))
		candidate.Status = state
		candidate.RolloutState = state
		candidate.TerminalResult.Status = state
		candidate.TerminalResult.RolloutState = state
		setKnownGoodCandidateIdentity(&candidate, "c")
		service.deployments[candidate.ID] = candidate
	}
	resultless := canonicalKnownGoodCandidate(t, success, "dep-memory-resultless", deploymentv1.RolloutStateSucceeded, now.Add(5*time.Second))
	setKnownGoodCandidateIdentity(&resultless, "c")
	resultless.TerminalResult = nil
	service.deployments[resultless.ID] = resultless
	preview := canonicalKnownGoodCandidate(t, success, "dep-memory-preview", deploymentv1.RolloutStateSucceeded, now.Add(6*time.Second))
	setKnownGoodCandidateIdentity(&preview, "c")
	preview.Snapshot.Preview = &deploymentv1.PreviewSpec{}
	service.deployments[preview.ID] = preview
	malformed := []DeploymentJob{
		canonicalKnownGoodCandidate(t, success, "dep-memory-legacy-mode", deploymentv1.RolloutStateSucceeded, now.Add(7*time.Second)),
		canonicalKnownGoodCandidate(t, success, "dep-memory-nil-intent", deploymentv1.RolloutStateSucceeded, now.Add(8*time.Second)),
		canonicalKnownGoodCandidate(t, success, "dep-memory-invalid-intent", deploymentv1.RolloutStateSucceeded, now.Add(9*time.Second)),
		canonicalKnownGoodCandidate(t, success, "dep-memory-invalid-snapshot", deploymentv1.RolloutStateSucceeded, now.Add(10*time.Second)),
		canonicalKnownGoodCandidate(t, success, "dep-memory-terminal-mismatch", deploymentv1.RolloutStateSucceeded, now.Add(11*time.Second)),
		canonicalKnownGoodCandidate(t, success, "dep-memory-invalid-job-schema", deploymentv1.RolloutStateSucceeded, now.Add(12*time.Second)),
		canonicalKnownGoodCandidate(t, success, "dep-memory-invalid-result-schema", deploymentv1.RolloutStateSucceeded, now.Add(13*time.Second)),
		canonicalKnownGoodCandidate(t, success, "dep-memory-invalid-hash", deploymentv1.RolloutStateSucceeded, now.Add(14*time.Second)),
		canonicalKnownGoodCandidate(t, success, "dep-memory-invalid-digest", deploymentv1.RolloutStateSucceeded, now.Add(15*time.Second)),
	}
	for index := range malformed {
		setKnownGoodCandidateIdentity(&malformed[index], "c")
	}
	malformed[0].Mode = ""
	malformed[1].RolloutIntent = nil
	malformed[2].RolloutIntent.Target.AgentID = "agent-other"
	malformed[3].Snapshot.SchemaVersion = ""
	malformed[4].KnownGoodID = "known-good-mismatch"
	malformed[5].SchemaVersion = ""
	malformed[6].TerminalResult.SchemaVersion = ""
	malformed[7].TerminalResult.KnownGoodHash = ""
	malformed[8].TerminalResult.CurrentDigest = "sha256:invalid"
	for _, candidate := range malformed {
		service.deployments[candidate.ID] = candidate
	}

	id, hash, digest = service.latestKnownGoodLocked(success.ProjectID, success.EnvironmentID, success.RuntimeID, success.ServiceID, success.NodeID, success.AgentID)
	if id != rolledBack.KnownGoodID || hash != rolledBack.KnownGoodHash || digest != rolledBack.CurrentDigest {
		t.Fatalf("factual rollback was not retained across excluded rows: %q %q %q", id, hash, digest)
	}
}

func TestMemoryKnownGoodRejectsLegacyExactTargetHistory(t *testing.T) {
	service, _, base := rolloutRegistryFixture(t, "legacy-history")
	legacy := legacyKnownGoodCandidate(base, "legacy-history", deploymentv1.RolloutStateSucceeded, base.UpdatedAt.Add(time.Second))
	legacy.SchemaVersion = deploymentv1.JobSchemaVersion
	service.deployments[legacy.ID] = legacy

	id, hash, digest := service.latestKnownGoodLocked(base.ProjectID, base.EnvironmentID, base.RuntimeID, base.ServiceID, base.NodeID, base.AgentID)
	if id != base.KnownGoodID || hash != base.KnownGoodHash || digest != base.CurrentDigest {
		t.Fatalf("legacy exact-target row became known-good authority: %q %q %q", id, hash, digest)
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
	legacyBase.KnownGoodID, legacyBase.KnownGoodHash = knownGoodIdentity("c")
	legacyBase.CurrentDigest = "sha256:" + strings.Repeat("c", 64)
	legacyBase.TerminalResult = &deploymentv1.AgentResult{Status: deploymentv1.RolloutStateSucceeded, RolloutState: deploymentv1.RolloutStateSucceeded, KnownGoodID: legacyBase.KnownGoodID, KnownGoodHash: legacyBase.KnownGoodHash, CurrentDigest: legacyBase.CurrentDigest}
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

func legacyKnownGoodCandidate(target DeploymentJob, id, state string, updatedAt time.Time) DeploymentJob {
	character := "a"
	if state == deploymentv1.RolloutStateRolledBack {
		character = "b"
	}
	knownID, knownHash := knownGoodIdentity(character)
	digest := "sha256:" + strings.Repeat(character, 64)
	result := &deploymentv1.AgentResult{Status: state, RolloutState: state, KnownGoodID: knownID, KnownGoodHash: knownHash, CurrentDigest: digest}
	return DeploymentJob{ID: "dep-" + id, ProjectID: target.ProjectID, EnvironmentID: target.EnvironmentID, RuntimeID: target.RuntimeID, ServiceID: target.ServiceID, NodeID: target.NodeID, AgentID: target.AgentID, Status: state, RolloutState: state, Snapshot: &deploymentv1.JobSnapshot{}, TerminalResult: result, KnownGoodID: knownID, KnownGoodHash: knownHash, CurrentDigest: digest, UpdatedAt: updatedAt}
}

func canonicalKnownGoodCandidate(t *testing.T, base DeploymentJob, id, state string, updatedAt time.Time) DeploymentJob {
	t.Helper()
	job := base
	job.ID = id
	job.IdempotencyKey = id
	job.PayloadHash = "payload-" + id
	job.CreatedAt = updatedAt.Add(-time.Second)
	job.UpdatedAt = updatedAt
	job.FinishedAt = &updatedAt
	snapshot := *base.Snapshot
	job.Snapshot = &snapshot
	exposure, err := exposureForDeployment(base.ExposureSpec, id)
	if err != nil {
		t.Fatal(err)
	}
	previousID, previousHash, previousDigest := base.RolloutIntent.PreviousKnownGoodID, base.RolloutIntent.PreviousKnownGoodHash, base.RolloutIntent.PreviousDigest
	if state == deploymentv1.RolloutStateRolledBack {
		previousID, previousHash = knownGoodIdentity("b")
		previousDigest = "sha256:" + strings.Repeat("b", 64)
	}
	intent, err := buildRolloutIntent(job, exposure, previousID, previousHash, previousDigest, "", "", deploymentv1.RolloutOperationApply, job.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	job.Action = intent.Operation
	job.ExposureSpec = exposure
	job.IntentHash = intent.IntentHash
	job.RolloutIntent = &intent
	result := *base.TerminalResult
	result.Status = state
	result.RolloutID = intent.RolloutID
	result.RolloutState = state
	result.IntentHash = intent.IntentHash
	result.WorkloadSpecHash = intent.Desired.WorkloadSpecHash
	result.ExposureSpecHash = intent.Desired.ExposureSpecHash
	result.DesiredDigest = intent.Desired.Image.Digest
	result.PreviousDigest = intent.PreviousDigest
	result.Attempt = intent.Attempt
	if state == deploymentv1.RolloutStateRolledBack {
		result.CurrentDigest = intent.PreviousDigest
		result.KnownGoodID = intent.PreviousKnownGoodID
		result.KnownGoodHash = intent.PreviousKnownGoodHash
	} else {
		result.CurrentDigest = intent.Desired.Image.Digest
	}
	job.Status = state
	job.RolloutState = state
	job.RolloutStateHash = result.StateHash
	job.DesiredDigest = result.DesiredDigest
	job.CurrentDigest = result.CurrentDigest
	job.PreviousDigest = result.PreviousDigest
	job.KnownGoodID = result.KnownGoodID
	job.KnownGoodHash = result.KnownGoodHash
	job.ReadinessEvidenceHash = result.ReadinessEvidenceHash
	job.FailureCode = result.FailureCode
	job.FailureMessageRedacted = result.FailureMessageRedacted
	job.TerminalResult = &result
	if job.RolloutVersion == 0 {
		job.RolloutVersion = 1
	}
	return job
}

func setKnownGoodCandidateIdentity(job *DeploymentJob, character string) {
	job.KnownGoodID, job.KnownGoodHash = knownGoodIdentity(character)
	if job.TerminalResult != nil {
		job.TerminalResult.KnownGoodID = job.KnownGoodID
		job.TerminalResult.KnownGoodHash = job.KnownGoodHash
	}
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
