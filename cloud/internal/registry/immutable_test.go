package registry

import (
	"strings"
	"sync"
	"testing"
	"time"

	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

func immutableSnapshot(t *testing.T, service *Service, projectID, serviceID, key string) deploymentv1.JobSnapshot {
	t.Helper()
	record := service.services[serviceID]
	nodes, err := service.ListNodes(projectID)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("nodes: %v %+v", err, nodes)
	}
	agent := service.agents[nodes[0].AgentID]
	spec := deploymentv1.WorkloadSpec{SchemaVersion: deploymentv1.WorkloadSchemaVersion, ServiceKey: record.Name, Replicas: 1, ApplicationContainerName: deploymentv1.ApplicationContainer, ContainerPort: 8080, Resources: deploymentv1.Resources{Requests: deploymentv1.ResourceValues{CPU: "100m", Memory: "128Mi"}, Limits: deploymentv1.ResourceValues{CPU: "500m", Memory: "512Mi"}}, TerminationGracePeriodSecond: 30, Exposure: deploymentv1.ExposureIntent{Mode: "internal"}}
	specHash, err := spec.Hash()
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	image, err := deploymentv1.NewImmutableImage("ghcr.io/example/"+record.Name, digest)
	if err != nil {
		t.Fatal(err)
	}
	return deploymentv1.JobSnapshot{SchemaVersion: deploymentv1.JobSchemaVersion, ProjectID: projectID, Image: image, Workload: spec, SpecHash: specHash, PayloadHash: "payload-" + key, Authority: deploymentv1.AuthoritySnapshot{BuildRecord: buildrecordv1.Record{SchemaVersion: buildrecordv1.SchemaVersion, ID: "br-" + key, ProjectID: projectID, ServiceID: serviceID, ServiceKey: record.Name, ActiveBindingID: "binding-1", Build: buildrecordv1.BuildMetadata{OCIRepository: image.Repository, OCIDigest: image.Digest, Status: "succeeded"}}, TopologyPlanID: "topology-" + key, TopologyRevision: 1, TopologyHash: strings.Repeat("1", 64), DeploymentPolicyID: "policy-" + key, DeploymentPolicyRevision: 1, DeploymentPolicyHash: strings.Repeat("2", 64), RoutingDecisionHash: strings.Repeat("3", 64), EnvironmentID: record.EnvironmentID, RuntimeID: record.RuntimeID, NodeID: nodes[0].ID, AgentID: agent.ID}}
}

func TestImmutableDeploymentStateMachineAndIdempotency(t *testing.T) {
	service, projectID := readyRegistry(t)
	created := createRegistryService(t, service, projectID, "api", "Dockerfile", "deploy/api", "svc-api")
	snapshot := immutableSnapshot(t, service, projectID, created.ID, "one")
	job, reused, err := service.StartImmutableDeployment(snapshot, "user-1", "deploy-one", "req-1")
	if err != nil || reused || job.Status != deploymentv1.StateQueued {
		t.Fatalf("create job=%+v reused=%v err=%v", job, reused, err)
	}
	if job.Mode != "rollout" || job.RolloutIntent == nil || job.RolloutIntent.Desired.Image.Digest != snapshot.Image.Digest {
		t.Fatalf("BuildRecord deployment did not create a canonical rollout: mode=%q intent=%+v", job.Mode, job.RolloutIntent)
	}
	replay, reused, err := service.StartImmutableDeployment(snapshot, "user-1", "deploy-one", "req-2")
	if err != nil || !reused || replay.ID != job.ID || replay.Status != deploymentv1.StateQueued {
		t.Fatalf("replay=%+v reused=%v err=%v", replay, reused, err)
	}
	node := service.nodes[job.NodeID]
	node.Status = NodeOffline
	service.nodes[job.NodeID] = node
	replay, reused, err = service.StartImmutableDeployment(snapshot, "user-1", "deploy-one", "req-authority-changed")
	if err != nil || !reused || replay.ID != job.ID {
		t.Fatalf("exact replay re-resolved mutable authority: replay=%+v reused=%v err=%v", replay, reused, err)
	}
	node.Status = NodeHealthy
	service.nodes[job.NodeID] = node
	lease, ok, err := service.LeaseDeployment(projectID, job.NodeID)
	if err != nil || !ok || lease.Command == nil || lease.Deployment.Status != deploymentv1.StateLeased {
		t.Fatalf("lease=%+v ok=%v err=%v", lease, ok, err)
	}
	if _, err := service.ProgressImmutableDeployment(projectID, job.NodeID, job.ID, "progress", rolloutProgress(lease, deploymentv1.RolloutStateWaiting, "0", "")); err == nil {
		t.Fatal("accepted non-monotonic progress")
	}
	reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateApplying, "1", "")
	reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateWaiting, "2", "")
	reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateSucceeded, "3", "")
	result := rolloutResult(lease, deploymentv1.RolloutStateSucceeded, "3", job.DesiredDigest, job.RolloutIntent.RolloutID, strings.Repeat("a", 64), "")
	spoofed := result
	spoofed.RolloutResult = cloneRolloutResult(result.RolloutResult)
	spoofed.RolloutResult.CurrentDigest = "sha256:" + strings.Repeat("f", 64)
	if _, err := service.CompleteDeployment(projectID, job.NodeID, job.ID, "spoofed", spoofed); apiCode(err) != "DEPLOYMENT_RESULT_MISMATCH" {
		t.Fatalf("accepted mismatched current digest: %v", err)
	}
	finished, err := service.CompleteDeployment(projectID, job.NodeID, job.ID, "complete", result)
	if err != nil || finished.Status != deploymentv1.StateSucceeded || finished.TerminalResult == nil {
		t.Fatalf("finish=%+v err=%v", finished, err)
	}
	finishedAgain, err := service.CompleteDeployment(projectID, job.NodeID, job.ID, "stale", result)
	if err != nil || finishedAgain.ID != job.ID || finishedAgain.Status != deploymentv1.StateSucceeded {
		t.Fatalf("terminal replay=%+v err=%v", finishedAgain, err)
	}
	staleTerminal := result
	staleTerminal.RolloutResult = cloneRolloutResult(result.RolloutResult)
	staleTerminal.RolloutResult.StateHash = strings.Repeat("f", 64)
	if _, err := service.CompleteDeployment(projectID, job.NodeID, job.ID, "stale-different", staleTerminal); apiCode(err) != "DEPLOYMENT_TERMINAL_IMMUTABLE" {
		t.Fatalf("stale result changed terminal job: %v", err)
	}
	events, err := service.DeploymentEvents(projectID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.SchemaVersion != deploymentv1.RolloutEventVersion && event.SchemaVersion != deploymentv1.EventSchemaVersion {
			t.Fatalf("rollout event omitted versioned contract: %+v", event)
		}
	}
}

func TestImmutableDeploymentExpiredLeaseUsesBoundedBackoff(t *testing.T) {
	service, projectID := readyRegistry(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	created := createRegistryService(t, service, projectID, "api", "Dockerfile", "deploy/api", "svc-backoff")
	snapshot := immutableSnapshot(t, service, projectID, created.ID, "backoff")
	job, _, err := service.StartImmutableDeployment(snapshot, "user-1", "deploy-backoff", "req")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := service.LeaseDeployment(projectID, job.NodeID); err != nil || !ok {
		t.Fatalf("first lease ok=%v err=%v", ok, err)
	}
	now = now.Add(defaultDeploymentLeaseDuration + time.Second)
	if _, ok, err := service.LeaseDeployment(projectID, job.NodeID); err != nil || ok {
		t.Fatalf("lease bypassed retry backoff ok=%v err=%v", ok, err)
	}
	queued := service.deployments[job.ID]
	if queued.Status != deploymentv1.StateQueued || queued.RetryAfter == nil || !queued.RetryAfter.After(now) {
		t.Fatalf("queued retry backoff missing: %+v", queued)
	}
	if lock, ok := service.deployLocks[job.ServiceID]; !ok || lock.DeploymentID != job.ID || !lock.ExpiresAt.After(now) {
		t.Fatalf("expired lease lost service ownership: lock=%+v ok=%v", lock, ok)
	}
	if _, _, err := service.StartImmutableDeployment(immutableSnapshot(t, service, projectID, created.ID, "blocked-during-backoff"), "user-1", "deploy-blocked-during-backoff", "blocked"); apiCode(err) != "DEPLOYMENT_LOCKED" {
		t.Fatalf("concurrent deployment during backoff err=%v", err)
	}
	if _, _, err := service.CancelDeployment(projectID, job.ID, "cancel-requeued", "cancel-requeued"); apiCode(err) != "CANCEL_UNSAFE" {
		t.Fatalf("requeued leased deployment cancellation err=%v", err)
	}
	events, err := service.DeploymentEvents(projectID, job.ID)
	if err != nil || events[len(events)-1].SchemaVersion != deploymentv1.EventSchemaVersion || events[len(events)-1].Attempt != 1 {
		t.Fatalf("versioned lease-expiry event missing: events=%+v err=%v", events, err)
	}
	now = *queued.RetryAfter
	if _, ok, err := service.LeaseDeployment(projectID, job.NodeID); err != nil || !ok {
		t.Fatalf("lease did not resume after backoff ok=%v err=%v", ok, err)
	}
}

func TestImmutableDeploymentCancellationIsSafeOnlyBeforeLease(t *testing.T) {
	service, projectID := readyRegistry(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	created := createRegistryService(t, service, projectID, "api", "Dockerfile", "deploy/api", "svc-cancel")
	snapshot := immutableSnapshot(t, service, projectID, created.ID, "cancel")
	job, _, err := service.StartImmutableDeployment(snapshot, "user-1", "deploy-cancel", "req")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(31 * time.Minute)
	if _, _, err := service.StartImmutableDeployment(immutableSnapshot(t, service, projectID, created.ID, "ttl-blocked"), "user-1", "deploy-ttl-blocked", "ttl-blocked"); apiCode(err) != "DEPLOYMENT_LOCKED" {
		t.Fatalf("queued deployment lost ownership after lock TTL: %v", err)
	}
	cancelled, reused, err := service.CancelDeployment(projectID, job.ID, "cancel-key", "cancel")
	if err != nil || reused || cancelled.Status != deploymentv1.StateCancelled {
		t.Fatalf("cancelled=%+v err=%v", cancelled, err)
	}
	replayed, reused, err := service.CancelDeployment(projectID, job.ID, "cancel-key", "cancel-replay")
	if err != nil || !reused || replayed.ID != job.ID || replayed.Status != deploymentv1.StateCancelled {
		t.Fatalf("cancel replay=%+v reused=%v err=%v", replayed, reused, err)
	}
	secondSnapshot := immutableSnapshot(t, service, projectID, created.ID, "leased")
	second, _, err := service.StartImmutableDeployment(secondSnapshot, "user-1", "deploy-leased", "req-2")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.CancelDeployment(projectID, second.ID, "cancel-key", "cancel-conflict"); apiCode(err) != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("cross-job cancel replay err=%v", err)
	}
	if _, ok, err := service.LeaseDeployment(projectID, second.NodeID); err != nil || !ok {
		t.Fatalf("lease ok=%v err=%v", ok, err)
	}
	if _, _, err := service.CancelDeployment(projectID, second.ID, "unsafe-key", "unsafe"); apiCode(err) != "CANCEL_UNSAFE" {
		t.Fatalf("unsafe cancel err=%v", err)
	}
}

func TestImmutableDeploymentExpiredLeaseRetryKeepsJobID(t *testing.T) {
	service, projectID := readyRegistry(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	created := createRegistryService(t, service, projectID, "api", "Dockerfile", "deploy/api", "svc-expire")
	snapshot := immutableSnapshot(t, service, projectID, created.ID, "expire")
	job, _, err := service.StartImmutableDeployment(snapshot, "user-1", "deploy-expire", "req")
	if err != nil {
		t.Fatal(err)
	}
	job.MaxAttempts = 1
	service.deployments[job.ID] = job
	lease, ok, err := service.LeaseDeployment(projectID, job.NodeID)
	if err != nil || !ok {
		t.Fatalf("lease ok=%v err=%v", ok, err)
	}
	reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateApplying, "1", "")
	reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateWaiting, "2", "")
	reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateFailed, "3", deploymentv1.RolloutCodeNoKnownGood)
	now = now.Add(defaultDeploymentLeaseDuration + time.Second)
	if _, err := service.CompleteDeployment(projectID, job.NodeID, job.ID, "expired", DeploymentResult{SchemaVersion: deploymentv1.ResultSchemaVersion, Status: deploymentv1.StateFailed, LeaseToken: lease.LeaseToken, SpecHash: snapshot.SpecHash, ApplicationImage: snapshot.Image.Reference}); apiCode(err) != "DEPLOYMENT_LEASE_EXPIRED" {
		t.Fatalf("expired result err=%v", err)
	}
	if _, ok, err := service.LeaseDeployment(projectID, job.NodeID); err != nil || ok {
		t.Fatalf("exhausted lease ok=%v err=%v", ok, err)
	}
	failed := service.deployments[job.ID]
	if failed.Status != deploymentv1.StateFailed || failed.FailureCode != "DEPLOYMENT_LEASE_ATTEMPTS_EXHAUSTED" || failed.TerminalResult != nil {
		t.Fatalf("failed job=%+v", failed)
	}
	if lock, ok := service.deployLocks[job.ServiceID]; !ok || lock.DeploymentID != job.ID {
		t.Fatalf("lease exhaustion lost service ownership: lock=%+v ok=%v", lock, ok)
	}
	if _, _, err := service.StartImmutableDeployment(immutableSnapshot(t, service, projectID, created.ID, "blocked-after-exhaustion"), "user-1", "deploy-blocked-after-exhaustion", "blocked"); apiCode(err) != "DEPLOYMENT_LOCKED" || !strings.Contains(err.Error(), job.ID) {
		t.Fatalf("lease-exhausted deployment allowed replacement: %v", err)
	}
	retried, reused, err := service.RetryDeployment(projectID, job.ID, "retry-one", "retry")
	if err != nil || reused || retried.ID != job.ID || retried.Status != deploymentv1.StateQueued || retried.MaxAttempts <= retried.AttemptCount || retried.NodeID != job.NodeID || retried.AgentID != job.AgentID {
		t.Fatalf("retry=%+v reused=%v err=%v", retried, reused, err)
	}
	replay, reused, err := service.RetryDeployment(projectID, job.ID, "retry-one", "retry-replay")
	if err != nil || !reused || replay.ID != job.ID {
		t.Fatalf("retry replay=%+v reused=%v err=%v", replay, reused, err)
	}
}

func TestCanonicalDeploymentOwnershipOnlyReleasesExactConfirmedReset(t *testing.T) {
	job := DeploymentJob{Mode: "rollout", ProjectID: "project-1", RuntimeID: "runtime-1", NodeID: "node-1", AgentID: "agent-1", Status: deploymentv1.StateFailed}
	node := Node{ID: job.NodeID, ProjectID: job.ProjectID, RuntimeID: job.RuntimeID, AgentID: job.AgentID, Status: NodeOffline, FailureCode: "OPERATOR_CONFIRMED_TARGET_RESET"}
	agent := Agent{ID: job.AgentID, ProjectID: job.ProjectID, RuntimeID: job.RuntimeID, NodeID: job.NodeID, Status: "revoked"}
	if canonicalDeploymentOwnsService(job, node, agent) {
		t.Fatal("exact operator-confirmed target retirement retained ownership")
	}

	cases := map[string]struct {
		node  Node
		agent Agent
	}{
		"ordinary offline":        {node: func() Node { copy := node; copy.FailureCode = ""; return copy }(), agent: agent},
		"agent still active":      {node: node, agent: func() Agent { copy := agent; copy.Status = "active"; return copy }()},
		"different node reset":    {node: func() Node { copy := node; copy.ID = "node-2"; return copy }(), agent: agent},
		"node agent mismatch":     {node: func() Node { copy := node; copy.AgentID = "agent-2"; return copy }(), agent: agent},
		"agent node mismatch":     {node: node, agent: func() Agent { copy := agent; copy.NodeID = "node-2"; return copy }()},
		"ordinary unresolved job": {node: func() Node { copy := node; copy.Status = NodeHealthy; copy.FailureCode = ""; return copy }(), agent: func() Agent { copy := agent; copy.Status = "active"; return copy }()},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if !canonicalDeploymentOwnsService(job, test.node, test.agent) {
				t.Fatal("non-canonical reset evidence released ownership")
			}
		})
	}
}

func TestOperatorConfirmedTargetResetReleasesDeploymentOwnership(t *testing.T) {
	service, projectID := readyRegistry(t)
	now := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	created := createRegistryService(t, service, projectID, "api", "Dockerfile", "deploy/api", "svc-reset")
	snapshot := immutableSnapshot(t, service, projectID, created.ID, "reset")
	oldJob, _, err := service.StartImmutableDeployment(snapshot, "user-1", "deploy-reset", "create-old")
	if err != nil {
		t.Fatal(err)
	}
	oldJob.MaxAttempts = 1
	service.deployments[oldJob.ID] = oldJob
	if _, ok, err := service.LeaseDeployment(projectID, oldJob.NodeID); err != nil || !ok {
		t.Fatalf("old lease ok=%v err=%v", ok, err)
	}
	now = now.Add(defaultDeploymentLeaseDuration + time.Second)
	if _, ok, err := service.LeaseDeployment(projectID, oldJob.NodeID); err != nil || ok {
		t.Fatalf("lease exhaustion ok=%v err=%v", ok, err)
	}
	exhausted := service.deployments[oldJob.ID]
	if exhausted.Status != deploymentv1.StateFailed || exhausted.FailureCode != "DEPLOYMENT_LEASE_ATTEMPTS_EXHAUSTED" || exhausted.TerminalResult != nil {
		t.Fatalf("exhausted old job=%+v", exhausted)
	}
	eventsBefore, err := service.DeploymentEvents(projectID, oldJob.ID)
	if err != nil {
		t.Fatal(err)
	}
	offline, err := service.MarkNodeOffline(projectID, oldJob.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if offline.Status != NodeOffline || offline.FailureCode != "OPERATOR_CONFIRMED_TARGET_RESET" || service.agents[oldJob.AgentID].Status != "revoked" {
		t.Fatalf("reset evidence node=%+v agent=%+v", offline, service.agents[oldJob.AgentID])
	}

	now = now.Add(time.Second)
	replacementNode, err := service.UpsertNode(projectID, "vps-2", "server", NodeHealthy, "203.0.113.11", "", "replacement-node")
	if err != nil {
		t.Fatal(err)
	}
	replacementAgent, err := service.RegisterAgent(projectID, replacementNode.ID, "sha256:replacement", "replacement", "v1", "replacement-agent", map[string]any{"deploy": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordAgentHeartbeat(projectID, replacementNode.ID, AgentHeartbeat{Version: "v1", NodeReady: true, K3SStatus: "ready", Capabilities: map[string]any{"deploy": true}}); err != nil {
		t.Fatal(err)
	}
	replacement := snapshot
	replacement.PayloadHash = "payload-replacement"
	replacement.Authority.BuildRecord.ID = "br-replacement"
	replacement.Authority.NodeID = replacementNode.ID
	replacement.Authority.AgentID = replacementAgent.ID
	var newJob DeploymentJob
	var createErr, retryErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		newJob, _, createErr = service.StartImmutableDeployment(replacement, "user-1", "deploy-after-reset", "after-reset")
	}()
	go func() {
		defer wait.Done()
		_, _, retryErr = service.RetryDeployment(projectID, oldJob.ID, "retry-retired", "retry-retired")
	}()
	wait.Wait()
	if createErr != nil || newJob.NodeID != replacementNode.ID || newJob.AgentID != replacementAgent.ID || apiCode(retryErr) != "RETRY_TARGET_RETIRED" {
		t.Fatalf("replacement deployment=%+v createErr=%v retryErr=%v", newJob, createErr, retryErr)
	}
	persisted := service.deployments[oldJob.ID]
	if persisted.TerminalResult != nil || persisted.IntentHash != exhausted.IntentHash || persisted.RolloutStateHash != exhausted.RolloutStateHash || persisted.FailureCode != exhausted.FailureCode {
		t.Fatalf("old deployment evidence changed: before=%+v after=%+v", exhausted, persisted)
	}
	eventsAfter, err := service.DeploymentEvents(projectID, oldJob.ID)
	if err != nil || len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("old deployment events changed before=%d after=%d err=%v", len(eventsBefore), len(eventsAfter), err)
	}
	if _, ok, err := service.LeaseDeployment(projectID, replacementNode.ID); err != nil || !ok {
		t.Fatalf("replacement lease ok=%v err=%v", ok, err)
	}
	if _, ok, err := service.LeaseDeployment(projectID, oldJob.NodeID); err != nil || ok {
		t.Fatalf("old job leased after target retirement ok=%v err=%v", ok, err)
	}
}
