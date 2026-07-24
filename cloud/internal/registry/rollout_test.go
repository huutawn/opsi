package registry

import (
	"strings"
	"sync"
	"testing"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
)

func TestExposureRolloutSuccessReplayAndTerminalImmutability(t *testing.T) {
	service, projectID, base := rolloutRegistryFixture(t, "success")
	request := rolloutExposureRequest(t, base, "dep-exposure-a", "api.example.com", "/")
	job, reused, err := service.StartExposureRollout(projectID, "user-1", "exposure-key", "req-create", request)
	if err != nil || reused || job.Mode != "rollout" || job.RolloutIntent == nil || job.RolloutState != deploymentv1.RolloutStatePrepared {
		t.Fatalf("job=%+v reused=%v err=%v", job, reused, err)
	}
	replay, reused, err := service.StartExposureRollout(projectID, "user-1", "exposure-key", "req-replay", request)
	if err != nil || !reused || replay.ID != job.ID || replay.RolloutIntent.IntentHash != job.RolloutIntent.IntentHash {
		t.Fatalf("replay=%+v reused=%v err=%v", replay, reused, err)
	}
	changed := rolloutExposureRequest(t, base, "dep-exposure-other", "other.example.com", "/")
	if _, _, err := service.StartExposureRollout(projectID, "user-1", "exposure-key", "req-conflict", changed); apiCode(err) != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("idempotency conflict err=%v", err)
	}

	lease := leaseRollout(t, service, projectID, job)
	reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStatePrepared, "1", "")
	applying := reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateApplying, "2", "")
	eventCount := len(service.deployEvents[job.ID])
	replayedProgress := reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateApplying, "2", "")
	if replayedProgress.RolloutVersion != applying.RolloutVersion || len(service.deployEvents[job.ID]) != eventCount {
		t.Fatalf("exact progress replay mutated state/history: versions=%d/%d events=%d/%d", applying.RolloutVersion, replayedProgress.RolloutVersion, eventCount, len(service.deployEvents[job.ID]))
	}
	reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateWaiting, "3", "")
	reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateSucceeded, "4", "")
	beforeResult := service.deployments[job.ID]
	if beforeResult.Status != deploymentv1.RolloutStateWaiting || beforeResult.RolloutState != deploymentv1.RolloutStateSucceeded || beforeResult.TerminalResult != nil {
		t.Fatalf("terminal progress incorrectly finalized job: %+v", beforeResult)
	}
	result := rolloutResult(lease, deploymentv1.RolloutStateSucceeded, "4", job.RolloutIntent.Desired.Image.Digest, "known-good-a", strings.Repeat("a", 64), "")
	finished, err := service.CompleteDeployment(projectID, job.NodeID, job.ID, "req-result", result)
	if err != nil || finished.Status != deploymentv1.RolloutStateSucceeded || finished.TerminalResult == nil || finished.CurrentDigest != finished.DesiredDigest {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
	terminalEvents := len(service.deployEvents[job.ID])
	again, err := service.CompleteDeployment(projectID, job.NodeID, job.ID, "req-result-replay", result)
	if err != nil || again.RolloutStateHash != finished.RolloutStateHash || len(service.deployEvents[job.ID]) != terminalEvents {
		t.Fatalf("terminal replay mutated state/history: again=%+v err=%v events=%d/%d", again, err, terminalEvents, len(service.deployEvents[job.ID]))
	}
	mutated := result
	mutated.RolloutResult = cloneRolloutResult(result.RolloutResult)
	mutated.RolloutResult.StateHash = strings.Repeat("f", 64)
	if _, err := service.CompleteDeployment(projectID, job.NodeID, job.ID, "req-terminal-overwrite", mutated); apiCode(err) != "DEPLOYMENT_TERMINAL_IMMUTABLE" {
		t.Fatalf("terminal overwrite err=%v", err)
	}
	mutated = result
	mutated.FailureMessageRedacted = "different sanitized metadata"
	mutated.RolloutResult = cloneRolloutResult(result.RolloutResult)
	mutated.RolloutResult.FailureMessageRedacted = mutated.FailureMessageRedacted
	if _, err := service.CompleteDeployment(projectID, job.NodeID, job.ID, "req-terminal-metadata-overwrite", mutated); apiCode(err) != "DEPLOYMENT_TERMINAL_IMMUTABLE" {
		t.Fatalf("terminal metadata overwrite err=%v", err)
	}
}

func TestExposureAutomaticRollbackKeepsDesiredAndFactualKnownGood(t *testing.T) {
	service, projectID, base := rolloutRegistryFixture(t, "rollback")
	aRequest := rolloutExposureRequest(t, base, "dep-exposure-a", "api.example.com", "/v1")
	aJob, _, err := service.StartExposureRollout(projectID, "user-1", "exposure-a", "req-a", aRequest)
	if err != nil {
		t.Fatal(err)
	}
	aLease := leaseRollout(t, service, projectID, aJob)
	reportRolloutProgress(t, service, projectID, aLease, deploymentv1.RolloutStateApplying, "1", "")
	reportRolloutProgress(t, service, projectID, aLease, deploymentv1.RolloutStateWaiting, "2", "")
	aKnownID, aKnownHash := "known-good-a", strings.Repeat("a", 64)
	aResult := rolloutResult(aLease, deploymentv1.RolloutStateSucceeded, "3", aJob.RolloutIntent.Desired.Image.Digest, aKnownID, aKnownHash, "")
	if _, err := service.CompleteDeployment(projectID, aJob.NodeID, aJob.ID, "result-a", aResult); err != nil {
		t.Fatal(err)
	}

	baseB := base
	bRequest := rolloutExposureRequest(t, baseB, "dep-exposure-b", "api.example.com", "/v2")
	bJob, _, err := service.StartExposureRollout(projectID, "user-1", "exposure-b", "req-b", bRequest)
	if err != nil || bJob.RolloutIntent.PreviousKnownGoodID != aKnownID || bJob.PreviousDigest != aJob.DesiredDigest {
		t.Fatalf("B job=%+v err=%v", bJob, err)
	}
	bLease := leaseRollout(t, service, projectID, bJob)
	reportRolloutProgress(t, service, projectID, bLease, deploymentv1.RolloutStateApplying, "4", "")
	reportRolloutProgress(t, service, projectID, bLease, deploymentv1.RolloutStateWaiting, "5", "")
	reportRolloutProgress(t, service, projectID, bLease, deploymentv1.RolloutStateFailed, "6", deploymentv1.RolloutCodeReadinessFailed)
	reportRolloutProgress(t, service, projectID, bLease, deploymentv1.RolloutStateRollingBack, "7", deploymentv1.RolloutCodeReadinessFailed)
	reportRolloutProgress(t, service, projectID, bLease, deploymentv1.RolloutStateRolledBack, "8", deploymentv1.RolloutCodeReadinessFailed)
	bResult := rolloutResult(bLease, deploymentv1.RolloutStateRolledBack, "8", aJob.DesiredDigest, aKnownID, aKnownHash, deploymentv1.RolloutCodeReadinessFailed)
	finished, err := service.CompleteDeployment(projectID, bJob.NodeID, bJob.ID, "result-b", bResult)
	if err != nil || finished.DesiredDigest != baseB.Snapshot.Image.Digest || finished.CurrentDigest != aJob.DesiredDigest || finished.PreviousDigest != aJob.DesiredDigest || finished.KnownGoodID != aKnownID {
		t.Fatalf("rolled back B=%+v err=%v", finished, err)
	}
	preview, err := service.PreviewExposure(projectID, "user-1", rolloutExposureRequest(t, base, "dep-exposure-c", "api.example.com", "/v3"))
	if err != nil || preview.Current == nil || preview.Current.DeploymentJobID != bJob.ID {
		t.Fatalf("Cloud desired exposure did not remain B: preview=%+v err=%v", preview, err)
	}
	cRequest := rolloutExposureRequest(t, baseB, "dep-exposure-c", "api.example.com", "/v3")
	cJob, _, err := service.StartExposureRollout(projectID, "user-1", "exposure-c", "req-c", cRequest)
	if err != nil {
		t.Fatal(err)
	}
	cLease := leaseRollout(t, service, projectID, cJob)
	reportRolloutProgress(t, service, projectID, cLease, deploymentv1.RolloutStateApplying, "b", "")
	reportRolloutProgress(t, service, projectID, cLease, deploymentv1.RolloutStateWaiting, "c", "")
	reportRolloutProgress(t, service, projectID, cLease, deploymentv1.RolloutStateFailed, "d", deploymentv1.RolloutCodeReadinessFailed)
	reportRolloutProgress(t, service, projectID, cLease, deploymentv1.RolloutStateRollingBack, "e", deploymentv1.RolloutCodeReadinessFailed)
	reportRolloutProgress(t, service, projectID, cLease, deploymentv1.RolloutStateRollbackFailed, "f", "ROLLBACK_APPLY_FAILED")
	rollbackFailed, err := service.CompleteDeployment(projectID, cJob.NodeID, cJob.ID, "rollback-failed", rolloutResult(cLease, deploymentv1.RolloutStateRollbackFailed, "f", "", "", "", "ROLLBACK_APPLY_FAILED"))
	if err != nil || rollbackFailed.Status != deploymentv1.RolloutStateRollbackFailed || rollbackFailed.FailureCode != "ROLLBACK_APPLY_FAILED" {
		t.Fatalf("rollback_failed=%+v err=%v", rollbackFailed, err)
	}
}

func TestBuildRecordImageRedeployPreservesAuthoritativeExposure(t *testing.T) {
	service, projectID, base := rolloutRegistryFixture(t, "image-exposure")
	exposureRequest := rolloutExposureRequest(t, base, "dep-image-exposure", "api.example.com", "/")
	exposureJob, _, err := service.StartExposureRollout(projectID, "user-1", "image-exposure-route", "route-request", exposureRequest)
	if err != nil {
		t.Fatal(err)
	}
	completeSuccessfulRollout(t, service, projectID, exposureJob, "known-exposure", strings.Repeat("b", 64), "4")
	if exposureJob.RolloutIntent == nil {
		t.Fatal("exposure rollout did not carry its canonical intent")
	}

	snapshot := *base.Snapshot
	snapshot.Image.Digest = "sha256:" + strings.Repeat("c", 64)
	snapshot.Image.Reference = snapshot.Image.Repository + "@" + snapshot.Image.Digest
	snapshot.Authority.BuildRecord.Build.OCIDigest = snapshot.Image.Digest
	snapshot.PayloadHash += "-redeploy"
	job, _, err := service.StartImmutableDeployment(snapshot, "user-1", "image-redeploy", "redeploy-request")
	if err != nil {
		t.Fatal(err)
	}
	if job.Mode != "rollout" || job.RolloutIntent == nil || job.ExposureSpec == nil {
		t.Fatalf("redeploy lost canonical rollout/exposure: %+v", job)
	}
	if job.RolloutIntent.Desired.ExposureSpecHash == "" || job.RolloutIntent.Desired.Exposure.Hostname != "api.example.com" || job.RolloutIntent.Desired.Exposure.Path != "/" {
		t.Fatalf("redeploy did not preserve authoritative exposure: %+v", job.RolloutIntent.Desired.Exposure)
	}
}

func TestExposureExplicitRollbackTargetsPreviousAndPinsExpectedCurrentKnownGood(t *testing.T) {
	service, projectID, baseA := rolloutRegistryFixture(t, "explicit")
	aRequest := rolloutExposureRequest(t, baseA, "dep-explicit-a", "api.example.com", "/a")
	aJob, _, err := service.StartExposureRollout(projectID, "user-1", "explicit-a", "req-a", aRequest)
	if err != nil {
		t.Fatal(err)
	}
	aKnownID, aKnownHash := "known-explicit-a", strings.Repeat("a", 64)
	aFinished := completeSuccessfulRollout(t, service, projectID, aJob, aKnownID, aKnownHash, "1")
	baseB := baseA
	bRequest := rolloutExposureRequest(t, baseB, "dep-explicit-b", "api.example.com", "/b")
	bJob, _, err := service.StartExposureRollout(projectID, "user-1", "explicit-b", "req-b", bRequest)
	if err != nil {
		t.Fatal(err)
	}
	bKnownID, bKnownHash := "known-explicit-b", strings.Repeat("b", 64)
	bFinished := completeSuccessfulRollout(t, service, projectID, bJob, bKnownID, bKnownHash, "4")
	if !bFinished.RollbackEligible {
		t.Fatalf("succeeded B was not rollback eligible: %+v", bFinished)
	}
	explicit, err := service.RollbackDeployment(projectID, bFinished.ID, "user-1", "rollback-b", "rollback-request")
	if err != nil || explicit.RolloutIntent == nil || explicit.RolloutIntent.Operation != deploymentv1.RolloutOperationRollback || explicit.RolloutIntent.PreviousKnownGoodID != aKnownID || explicit.RolloutIntent.ExpectedKnownGoodID != bKnownID || explicit.PreviousDigest != aFinished.CurrentDigest {
		t.Fatalf("explicit rollback=%+v err=%v", explicit, err)
	}
	lease := leaseRollout(t, service, projectID, explicit)
	if lease.Action != deploymentv1.RolloutOperationRollback {
		t.Fatalf("explicit rollback lease action=%q", lease.Action)
	}
	reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateRollingBack, "7", "")
	reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateRolledBack, "8", "")
	finished, err := service.CompleteDeployment(projectID, explicit.NodeID, explicit.ID, "explicit-result", rolloutResult(lease, deploymentv1.RolloutStateRolledBack, "8", aFinished.CurrentDigest, aKnownID, aKnownHash, ""))
	if err != nil || finished.CurrentDigest != aFinished.CurrentDigest || finished.KnownGoodID != aKnownID || finished.RollbackEligible {
		t.Fatalf("explicit rollback result=%+v err=%v", finished, err)
	}
	replay, err := service.RollbackDeployment(projectID, bFinished.ID, "user-1", "rollback-b", "rollback-replay")
	if err != nil || !replay.Reused || replay.ID != finished.ID || replay.RolloutState != deploymentv1.RolloutStateRolledBack {
		t.Fatalf("explicit rollback replay=%+v err=%v", replay, err)
	}
	if _, err := service.RollbackDeployment(projectID, bFinished.ID, "user-1", "invalid key", "rollback-invalid"); apiCode(err) != "IDEMPOTENCY_KEY_INVALID" {
		t.Fatalf("invalid rollback idempotency err=%v", err)
	}
}

func TestExposureNoKnownGoodStaleLeaseOutOfOrderAndConcurrentApply(t *testing.T) {
	service, projectID, base := rolloutRegistryFixture(t, "negative")
	request := rolloutExposureRequest(t, base, "dep-exposure-negative", "negative.example.com", "/")
	job, _, err := service.StartExposureRollout(projectID, "user-1", "negative-key", "req", request)
	if err != nil {
		t.Fatal(err)
	}
	lease := leaseRollout(t, service, projectID, job)
	stale := rolloutProgress(lease, deploymentv1.RolloutStateApplying, "1", "")
	stale.LeaseToken = "stale-token"
	if _, err := service.ProgressImmutableDeployment(projectID, job.NodeID, job.ID, "stale", stale); apiCode(err) != "DEPLOYMENT_STALE_LEASE" {
		t.Fatalf("stale progress err=%v", err)
	}
	outOfOrder := rolloutProgress(lease, deploymentv1.RolloutStateSucceeded, "2", "")
	if _, err := service.ProgressImmutableDeployment(projectID, job.NodeID, job.ID, "out-of-order", outOfOrder); apiCode(err) != "DEPLOYMENT_STATE_INVALID" {
		t.Fatalf("out-of-order progress err=%v", err)
	}
	reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateApplying, "3", "")
	reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateWaiting, "4", "")
	reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateFailed, "5", deploymentv1.RolloutCodeNoKnownGood)
	result := rolloutResult(lease, deploymentv1.RolloutStateFailed, "5", "", "", "", deploymentv1.RolloutCodeNoKnownGood)
	failed, err := service.CompleteDeployment(projectID, job.NodeID, job.ID, "no-known-good", result)
	if err != nil || failed.Status != deploymentv1.RolloutStateFailed || failed.FailureCode != deploymentv1.RolloutCodeNoKnownGood {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}

	other, otherProjectID, otherBase := rolloutRegistryFixture(t, "concurrent")
	requests := []deploymentv1.ExposureMutationRequest{
		rolloutExposureRequest(t, otherBase, "dep-concurrent-a", "a.example.com", "/"),
		rolloutExposureRequest(t, otherBase, "dep-concurrent-b", "b.example.com", "/"),
	}
	var wait sync.WaitGroup
	errorsFound := make(chan error, len(requests))
	for index, candidate := range requests {
		wait.Add(1)
		go func(index int, candidate deploymentv1.ExposureMutationRequest) {
			defer wait.Done()
			_, _, err := other.StartExposureRollout(otherProjectID, "user-1", "concurrent-"+string(rune('a'+index)), "req", candidate)
			errorsFound <- err
		}(index, candidate)
	}
	wait.Wait()
	close(errorsFound)
	winners, locked := 0, 0
	for err := range errorsFound {
		if err == nil {
			winners++
		} else if apiCode(err) == "DEPLOYMENT_LOCKED" {
			locked++
		}
	}
	if winners != 1 || locked != 1 {
		t.Fatalf("concurrent apply winners=%d locked=%d", winners, locked)
	}
}

func rolloutRegistryFixture(t *testing.T, suffix string) (*Service, string, DeploymentJob) {
	t.Helper()
	service, projectID := readyRegistry(t)
	record := createRegistryService(t, service, projectID, "api-"+suffix, "Dockerfile", "deploy/api", "svc-"+suffix)
	snapshot := immutableSnapshot(t, service, projectID, record.ID, suffix)
	snapshot.Authority.TopologyPlanID = "topology-" + suffix
	snapshot.Authority.TopologyRevision = 1
	snapshot.Authority.TopologyHash = strings.Repeat("1", 64)
	snapshot.Authority.DeploymentPolicyID = "policy-" + suffix
	snapshot.Authority.DeploymentPolicyRevision = 1
	snapshot.Authority.DeploymentPolicyHash = strings.Repeat("2", 64)
	snapshot.Authority.RoutingDecisionHash = strings.Repeat("3", 64)
	job, _, err := service.StartImmutableDeployment(snapshot, "user-1", "base-"+suffix, "base-request")
	if err != nil {
		t.Fatal(err)
	}
	lease, ok, err := service.LeaseDeployment(projectID, job.NodeID)
	if err != nil || !ok {
		t.Fatalf("base lease ok=%v err=%v", ok, err)
	}
	reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateApplying, "1", "")
	reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateWaiting, "2", "")
	reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateSucceeded, "3", "")
	result := rolloutResult(lease, deploymentv1.RolloutStateSucceeded, "3", job.DesiredDigest, job.RolloutIntent.RolloutID, strings.Repeat("a", 64), "")
	base, err := service.CompleteDeployment(projectID, job.NodeID, job.ID, "base-result", result)
	if err != nil {
		t.Fatal(err)
	}
	return service, projectID, base
}

func rolloutExposureRequest(t *testing.T, base DeploymentJob, jobID, hostname, path string) deploymentv1.ExposureMutationRequest {
	t.Helper()
	spec, err := (exposurev1.ExposureSpec{SchemaVersion: exposurev1.SchemaVersion, ProjectID: base.ProjectID, EnvironmentID: base.EnvironmentID, RuntimeID: base.RuntimeID, ServiceKey: base.Snapshot.Workload.ServiceKey, DeploymentJobID: jobID, Hostname: hostname, Path: path, ServicePort: base.Snapshot.Workload.ContainerPort, TLS: exposurev1.TLSConfig{Mode: exposurev1.TLSDisabled}}).Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	return deploymentv1.ExposureMutationRequest{SchemaVersion: deploymentv1.ExposureMutationVersion, BaseDeploymentJobID: base.ID, Exposure: spec}
}

func leaseRollout(t *testing.T, service *Service, projectID string, job DeploymentJob) DeploymentLease {
	t.Helper()
	lease, ok, err := service.LeaseDeployment(projectID, job.NodeID)
	if err != nil || !ok || lease.Command == nil || lease.Command.Rollout == nil {
		t.Fatalf("rollout lease=%+v ok=%v err=%v", lease, ok, err)
	}
	return lease
}

func completeSuccessfulRollout(t *testing.T, service *Service, projectID string, job DeploymentJob, knownGoodID, knownGoodHash, hashCharacter string) DeploymentJob {
	t.Helper()
	lease := leaseRollout(t, service, projectID, job)
	reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateApplying, hashCharacter, "")
	reportRolloutProgress(t, service, projectID, lease, deploymentv1.RolloutStateWaiting, hashCharacter, "")
	finished, err := service.CompleteDeployment(projectID, job.NodeID, job.ID, "success-"+job.ID, rolloutResult(lease, deploymentv1.RolloutStateSucceeded, hashCharacter, job.DesiredDigest, knownGoodID, knownGoodHash, ""))
	if err != nil {
		t.Fatal(err)
	}
	return finished
}

func reportRolloutProgress(t *testing.T, service *Service, projectID string, lease DeploymentLease, state, hashCharacter, failureCode string) DeploymentJob {
	t.Helper()
	job, err := service.ProgressImmutableDeployment(projectID, lease.Deployment.NodeID, lease.Deployment.ID, "progress-"+state, rolloutProgress(lease, state, hashCharacter, failureCode))
	if err != nil {
		t.Fatalf("progress %s: %v", state, err)
	}
	return job
}

func rolloutProgress(lease DeploymentLease, state, hashCharacter, failureCode string) deploymentv1.Progress {
	intent := lease.Command.Rollout
	currentDigest := ""
	if state == deploymentv1.RolloutStateSucceeded {
		currentDigest = intent.Desired.Image.Digest
	} else if state == deploymentv1.RolloutStateRolledBack {
		currentDigest = intent.PreviousDigest
	}
	return deploymentv1.Progress{SchemaVersion: deploymentv1.EventSchemaVersion, LeaseToken: lease.LeaseToken, State: state, MessageRedacted: "sanitized rollout progress", ProgressPercent: 50, RolloutID: intent.RolloutID, IntentHash: intent.IntentHash, StateHash: strings.Repeat(hashCharacter, 64), WorkloadSpecHash: intent.Desired.WorkloadSpecHash, ExposureSpecHash: intent.Desired.ExposureSpecHash, DesiredDigest: intent.Desired.Image.Digest, CurrentDigest: currentDigest, PreviousDigest: intent.PreviousDigest, FailureCode: failureCode, Attempt: intent.Attempt}
}

func rolloutResult(lease DeploymentLease, state, hashCharacter, currentDigest, knownGoodID, knownGoodHash, failureCode string) DeploymentResult {
	intent := lease.Command.Rollout
	readinessHash := ""
	if state == deploymentv1.RolloutStateSucceeded || state == deploymentv1.RolloutStateRolledBack {
		readinessHash = strings.Repeat("e", 64)
	}
	agentResult := &deploymentv1.AgentResult{SchemaVersion: deploymentv1.ResultSchemaVersion, Status: state, RolloutID: intent.RolloutID, RolloutState: state, IntentHash: intent.IntentHash, StateHash: strings.Repeat(hashCharacter, 64), SpecHash: intent.Desired.WorkloadSpecHash, WorkloadSpecHash: intent.Desired.WorkloadSpecHash, ExposureSpecHash: intent.Desired.ExposureSpecHash, DesiredDigest: intent.Desired.Image.Digest, CurrentDigest: currentDigest, PreviousDigest: intent.PreviousDigest, KnownGoodID: knownGoodID, KnownGoodHash: knownGoodHash, ReadinessEvidenceHash: readinessHash, FailureCode: failureCode, FailureMessageRedacted: "sanitized rollout failure", Attempt: intent.Attempt}
	if state == deploymentv1.RolloutStateSucceeded || state == deploymentv1.RolloutStateRolledBack {
		agentResult.Resources = []deploymentv1.ResourceIdentity{{Kind: "Deployment", Namespace: "opsi", Name: "api", UID: "uid-api", ResourceVersion: "1", FunctionalHash: strings.Repeat("f", 64)}}
	}
	return DeploymentResult{SchemaVersion: deploymentv1.ResultSchemaVersion, Status: state, LeaseToken: lease.LeaseToken, IntentHash: intent.IntentHash, FailureCode: failureCode, FailureMessageRedacted: agentResult.FailureMessageRedacted, RolloutResult: agentResult}
}

func cloneRolloutResult(result *deploymentv1.AgentResult) *deploymentv1.AgentResult {
	copy := *result
	copy.Resources = append([]deploymentv1.ResourceIdentity(nil), result.Resources...)
	return &copy
}
