package registry

import (
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
)

func (s *Service) PreviewExposure(projectID, actorUserID string, request deploymentv1.ExposureMutationRequest) (deploymentv1.ExposurePreview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.previewExposureLocked(projectID, request, s.clock())
}

func (s *Service) previewExposureLocked(projectID string, request deploymentv1.ExposureMutationRequest, now time.Time) (deploymentv1.ExposurePreview, error) {
	if request.SchemaVersion != deploymentv1.ExposureMutationVersion {
		return deploymentv1.ExposurePreview{}, APIError{Status: 400, Code: "EXPOSURE_REQUEST_INVALID", Message: "unsupported exposure mutation schema"}
	}
	base, ok := s.deployments[request.BaseDeploymentJobID]
	if !ok || base.ProjectID != projectID || base.Snapshot == nil {
		return deploymentv1.ExposurePreview{}, ErrNotFound
	}
	if base.Status != deploymentv1.StateSucceeded && base.Status != deploymentv1.RolloutStateRolledBack {
		return deploymentv1.ExposurePreview{}, APIError{Status: 409, Code: "EXPOSURE_BASE_NOT_READY", Message: "base deployment must have a terminal factual runtime result"}
	}
	desired, err := request.Exposure.Canonicalize()
	if err != nil {
		return deploymentv1.ExposurePreview{}, APIError{Status: 400, Code: "EXPOSURE_SPEC_INVALID", Message: err.Error()}
	}
	if desired.ProjectID != projectID || desired.EnvironmentID != base.EnvironmentID || desired.RuntimeID != base.RuntimeID || desired.ServiceKey != base.Snapshot.Workload.ServiceKey || desired.ServicePort != base.Snapshot.Workload.ContainerPort {
		return deploymentv1.ExposurePreview{}, APIError{Status: 409, Code: "EXPOSURE_TARGET_MISMATCH", Message: "ExposureSpec does not match the base deployment target"}
	}
	if existing, exists := s.deployments[desired.DeploymentJobID]; exists && (existing.ProjectID != projectID || existing.PayloadHash == "") {
		return deploymentv1.ExposurePreview{}, APIError{Status: 409, Code: "DEPLOYMENT_ID_CONFLICT", Message: "deployment_job_id is already owned by another record"}
	}
	current := s.latestExposureLocked(projectID, base.EnvironmentID, base.RuntimeID, base.ServiceID)
	changes := exposureChanges(current, desired)
	preview := deploymentv1.ExposurePreview{SchemaVersion: deploymentv1.ExposurePreviewVersion, BaseDeploymentJobID: base.ID, Current: current, Desired: desired, Changes: changes, Eligible: true, DecisionCode: "EXPOSURE_READY", Message: "exposure rollout is ready for Agent ownership preflight", ResolvedAt: now}
	preview.StateHash = hashJSON(map[string]any{"base_deployment_id": base.ID, "base_payload_hash": base.PayloadHash, "current": current, "desired": desired})
	if request.ExpectedStateHash != "" && request.ExpectedStateHash != preview.StateHash {
		return deploymentv1.ExposurePreview{}, APIError{Status: 409, Code: "EXPOSURE_STATE_CONFLICT", Message: "exposure state changed after preview"}
	}
	for _, other := range s.latestProjectExposuresLocked(projectID) {
		if other.ServiceKey == desired.ServiceKey && other.EnvironmentID == desired.EnvironmentID && other.RuntimeID == desired.RuntimeID {
			continue
		}
		if other.Hostname == desired.Hostname && exposurev1.PathsConflict(other.Path, desired.Path) {
			preview.Eligible = false
			preview.DecisionCode = "EXPOSURE_ROUTE_CONFLICT"
			preview.Message = "hostname and path overlap another Opsi desired exposure"
			break
		}
	}
	return preview, nil
}

func (s *Service) StartExposureRollout(projectID, actorUserID, key, requestID string, request deploymentv1.ExposureMutationRequest) (DeploymentJob, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validDeploymentIdempotencyKey(key) {
		return DeploymentJob{}, false, APIError{Status: 400, Code: "IDEMPOTENCY_KEY_INVALID", Message: "deployment idempotency key is invalid", RequestID: requestID}
	}
	payloadHash := hashJSON(request)
	scope := "exposure-rollout:v1:" + projectID + ":" + key
	if existing, ok := s.idempotency[scope].(DeploymentJob); ok {
		if existing.PayloadHash != payloadHash {
			return DeploymentJob{}, false, APIError{Status: 409, Code: "IDEMPOTENCY_CONFLICT", Message: "idempotency key was used with a different exposure payload", RequestID: requestID}
		}
		if current, exists := s.deployments[existing.ID]; exists {
			existing = current
		}
		existing.Reused = true
		return existing, true, nil
	}
	now := s.clock()
	preview, err := s.previewExposureLocked(projectID, request, now)
	if err != nil {
		return DeploymentJob{}, false, err
	}
	if !preview.Eligible {
		return DeploymentJob{}, false, APIError{Status: 409, Code: preview.DecisionCode, Message: preview.Message, RequestID: requestID}
	}
	base := s.deployments[request.BaseDeploymentJobID]
	if _, exists := s.deployments[preview.Desired.DeploymentJobID]; exists {
		return DeploymentJob{}, false, APIError{Status: 409, Code: "DEPLOYMENT_ID_CONFLICT", Message: "deployment_job_id already exists", RequestID: requestID}
	}
	previousID, previousHash, previousDigest := s.latestKnownGoodLocked(projectID, base.EnvironmentID, base.RuntimeID, base.ServiceID)
	intent, err := buildRolloutIntent(base, preview.Desired, previousID, previousHash, previousDigest, "", "", deploymentv1.RolloutOperationApply, now)
	if err != nil {
		return DeploymentJob{}, false, APIError{Status: 400, Code: "ROLLOUT_INTENT_INVALID", Message: err.Error(), RequestID: requestID}
	}
	job := rolloutDeploymentJob(base, intent, preview.Desired, actorUserID, key, payloadHash, now)
	if err := s.acquireDeploymentLockLocked(job.ServiceID, job.ID, now, requestID); err != nil {
		return DeploymentJob{}, false, err
	}
	s.deployments[job.ID] = job
	s.deployEvents[job.ID] = []DeploymentEvent{rolloutEvent(job, deploymentv1.RolloutStatePrepared, "durable exposure rollout prepared", 0, requestID, now, "")}
	s.idempotency[scope] = job
	return job, false, nil
}

func buildRolloutIntent(base DeploymentJob, exposure exposurev1.ExposureSpec, previousID, previousHash, previousDigest, expectedID, expectedHash, operation string, now time.Time) (deploymentv1.RolloutIntent, error) {
	target := deploymentv1.RuntimeTarget{ProjectID: base.ProjectID, EnvironmentID: base.EnvironmentID, RuntimeID: base.RuntimeID, ServiceKey: base.Snapshot.Workload.ServiceKey, NodeID: base.NodeID, AgentID: base.AgentID}
	runtime := deploymentv1.RuntimeSnapshot{SchemaVersion: deploymentv1.RuntimeSnapshotVersion, Target: target, DeploymentJobID: exposure.DeploymentJobID, Image: base.Snapshot.Image, Workload: base.Snapshot.Workload, WorkloadSpecHash: base.Snapshot.SpecHash, Exposure: exposure, ExposureSpecHash: exposure.SpecHash, Authority: deploymentv1.RuntimeAuthority{TopologyPlanID: base.Snapshot.Authority.TopologyPlanID, TopologyRevision: base.Snapshot.Authority.TopologyRevision, TopologyHash: base.Snapshot.Authority.TopologyHash, DeploymentPolicyID: base.Snapshot.Authority.DeploymentPolicyID, DeploymentPolicyRevision: base.Snapshot.Authority.DeploymentPolicyRevision, DeploymentPolicyHash: base.Snapshot.Authority.DeploymentPolicyHash, RoutingDecisionHash: base.Snapshot.Authority.RoutingDecisionHash}}
	rolloutHash := hashJSON(exposure.DeploymentJobID)
	intent := deploymentv1.RolloutIntent{SchemaVersion: deploymentv1.RolloutSchemaVersion, RolloutID: "rol-" + rolloutHash[:32], Operation: operation, Target: target, Desired: runtime, PreviousKnownGoodID: previousID, PreviousKnownGoodHash: previousHash, PreviousDigest: previousDigest, ExpectedKnownGoodID: expectedID, ExpectedKnownGoodHash: expectedHash, Attempt: 1, CreatedAt: now}
	return intent.Canonicalize()
}

func rolloutDeploymentJob(base DeploymentJob, intent deploymentv1.RolloutIntent, exposure exposurev1.ExposureSpec, actor, key, payloadHash string, now time.Time) DeploymentJob {
	return DeploymentJob{SchemaVersion: deploymentv1.JobSchemaVersion, Mode: "rollout", ID: exposure.DeploymentJobID, OrgID: base.OrgID, ProjectID: base.ProjectID, EnvironmentID: base.EnvironmentID, RuntimeID: base.RuntimeID, ServiceID: base.ServiceID, Status: deploymentv1.StateQueued, Action: intent.Operation, IdempotencyKey: key, RequestedBy: actor, AgentID: base.AgentID, NodeID: base.NodeID, MaxAttempts: defaultDeploymentMaxAttempts, Snapshot: base.Snapshot, SpecHash: base.SpecHash, PayloadHash: payloadHash, IntentHash: intent.IntentHash, BaseDeploymentID: base.ID, RolloutIntent: &intent, RolloutState: deploymentv1.RolloutStatePrepared, DesiredDigest: intent.Desired.Image.Digest, PreviousDigest: intent.PreviousDigest, ExposureSpec: &exposure, KnownGoodID: intent.PreviousKnownGoodID, KnownGoodHash: intent.PreviousKnownGoodHash, CreatedAt: now, UpdatedAt: now}
}

func (s *Service) latestExposureLocked(projectID, environmentID, runtimeID, serviceID string) *exposurev1.ExposureSpec {
	var selected *DeploymentJob
	for id := range s.deployments {
		job := s.deployments[id]
		if job.ProjectID == projectID && job.EnvironmentID == environmentID && job.RuntimeID == runtimeID && job.ServiceID == serviceID && job.ExposureSpec != nil && (selected == nil || job.CreatedAt.After(selected.CreatedAt)) {
			copy := job
			selected = &copy
		}
	}
	if selected == nil {
		return nil
	}
	copy := *selected.ExposureSpec
	return &copy
}

func (s *Service) latestProjectExposuresLocked(projectID string) []exposurev1.ExposureSpec {
	latest := map[string]DeploymentJob{}
	for _, job := range s.deployments {
		if job.ProjectID != projectID || job.ExposureSpec == nil || job.Status == deploymentv1.StateCancelled {
			continue
		}
		key := job.EnvironmentID + "\x00" + job.RuntimeID + "\x00" + job.ServiceID
		if current, ok := latest[key]; !ok || job.CreatedAt.After(current.CreatedAt) {
			latest[key] = job
		}
	}
	result := make([]exposurev1.ExposureSpec, 0, len(latest))
	for _, job := range latest {
		result = append(result, *job.ExposureSpec)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Hostname+result[i].Path < result[j].Hostname+result[j].Path })
	return result
}

func (s *Service) latestKnownGoodLocked(projectID, environmentID, runtimeID, serviceID string) (string, string, string) {
	var selected *DeploymentJob
	for id := range s.deployments {
		job := s.deployments[id]
		if job.ProjectID == projectID && job.EnvironmentID == environmentID && job.RuntimeID == runtimeID && job.ServiceID == serviceID && job.TerminalResult != nil && job.TerminalResult.KnownGoodID != "" && (selected == nil || job.UpdatedAt.After(selected.UpdatedAt)) {
			copy := job
			selected = &copy
		}
	}
	if selected == nil {
		return "", "", ""
	}
	return selected.TerminalResult.KnownGoodID, selected.TerminalResult.KnownGoodHash, selected.TerminalResult.CurrentDigest
}

func exposureChanges(current *exposurev1.ExposureSpec, desired exposurev1.ExposureSpec) []string {
	if current == nil {
		return []string{"create exposure " + desired.Hostname + desired.Path}
	}
	changes := make([]string, 0, 4)
	if current.Hostname != desired.Hostname {
		changes = append(changes, "hostname")
	}
	if current.Path != desired.Path {
		changes = append(changes, "path")
	}
	if current.ServicePort != desired.ServicePort {
		changes = append(changes, "service_port")
	}
	if current.TLS != desired.TLS {
		changes = append(changes, "tls")
	}
	if len(changes) == 0 {
		changes = append(changes, "unchanged")
	}
	return changes
}

func rolloutEvent(job DeploymentJob, state, message string, percent int, requestID string, now time.Time, stateHash string) DeploymentEvent {
	rolloutID, intentHash := "", ""
	if job.RolloutIntent != nil {
		rolloutID, intentHash = job.RolloutIntent.RolloutID, job.RolloutIntent.IntentHash
	}
	return DeploymentEvent{SchemaVersion: deploymentv1.RolloutEventVersion, ID: newID("depevt"), OrgID: job.OrgID, ProjectID: job.ProjectID, DeploymentID: job.ID, ServiceID: job.ServiceID, Level: "info", Step: state, MessageRedacted: RedactString(message), ProgressPercent: percent, Attempt: job.AttemptCount, RequestID: requestID, CreatedAt: now, RolloutID: rolloutID, IntentHash: intentHash, StateHash: stateHash}
}

func validateRolloutProgress(job DeploymentJob, progress deploymentv1.Progress) error {
	if progress.SchemaVersion != deploymentv1.EventSchemaVersion || job.RolloutIntent == nil || progress.RolloutID != job.RolloutIntent.RolloutID || progress.IntentHash != job.RolloutIntent.IntentHash {
		return fmt.Errorf("rollout progress identity does not match the leased intent")
	}
	if progress.WorkloadSpecHash != job.RolloutIntent.Desired.WorkloadSpecHash || progress.ExposureSpecHash != job.RolloutIntent.Desired.ExposureSpecHash || progress.DesiredDigest != job.RolloutIntent.Desired.Image.Digest || progress.PreviousDigest != job.RolloutIntent.PreviousDigest {
		return fmt.Errorf("rollout progress hashes or digests do not match the leased intent")
	}
	if !validRolloutHash(progress.StateHash) || !validOptionalRolloutHash(progress.ReadinessEvidenceHash) || progress.Attempt != job.RolloutIntent.Attempt || !validSanitizedResources(progress.Resources) || len(progress.FailureCode) > 128 || progress.ProgressPercent < 0 || progress.ProgressPercent > 100 {
		return fmt.Errorf("rollout progress metadata exceeds its bound")
	}
	if progress.State == deploymentv1.RolloutStateSucceeded && progress.CurrentDigest != progress.DesiredDigest || progress.State == deploymentv1.RolloutStateRolledBack && progress.CurrentDigest != progress.PreviousDigest || progress.State != deploymentv1.RolloutStateSucceeded && progress.State != deploymentv1.RolloutStateRolledBack && progress.CurrentDigest != "" {
		return fmt.Errorf("rollout progress current digest is inconsistent with its state")
	}
	if (progress.State == deploymentv1.RolloutStateFailed || progress.State == deploymentv1.RolloutStateRollbackFailed) && progress.FailureCode == "" {
		return fmt.Errorf("failed rollout progress requires a typed failure")
	}
	if progress.FailureCode != "" && progress.State != deploymentv1.RolloutStateFailed && progress.State != deploymentv1.RolloutStateRollingBack && progress.State != deploymentv1.RolloutStateRolledBack && progress.State != deploymentv1.RolloutStateRollbackFailed {
		return fmt.Errorf("rollout progress failure code is inconsistent with its state")
	}
	if progress.State != job.RolloutState && !deploymentv1.CanTransitionRollout(job.RolloutState, progress.State) {
		return fmt.Errorf("rollout state transition is not monotonic")
	}
	return nil
}

func validateRolloutResult(job DeploymentJob, result *deploymentv1.AgentResult) error {
	if result == nil || job.RolloutIntent == nil || result.SchemaVersion != deploymentv1.ResultSchemaVersion || result.RolloutID != job.RolloutIntent.RolloutID || result.IntentHash != job.RolloutIntent.IntentHash {
		return fmt.Errorf("rollout result identity is invalid")
	}
	if result.WorkloadSpecHash != job.RolloutIntent.Desired.WorkloadSpecHash || result.ExposureSpecHash != job.RolloutIntent.Desired.ExposureSpecHash || result.DesiredDigest != job.RolloutIntent.Desired.Image.Digest || result.PreviousDigest != job.RolloutIntent.PreviousDigest || !validRolloutHash(result.StateHash) || !validOptionalRolloutHash(result.ReadinessEvidenceHash) || result.Attempt != job.RolloutIntent.Attempt || !validSanitizedResources(result.Resources) || len(result.FailureCode) > 128 || len(result.FailureMessageRedacted) > deploymentv1.MaxRolloutErrorBytes {
		return fmt.Errorf("rollout result hashes or digests do not match the leased intent")
	}
	if result.RolloutState != job.RolloutState && !deploymentv1.CanTransitionRollout(job.RolloutState, result.RolloutState) {
		return fmt.Errorf("rollout terminal result is out of order")
	}
	switch result.RolloutState {
	case deploymentv1.RolloutStateSucceeded:
		if result.CurrentDigest != result.DesiredDigest || result.KnownGoodID == "" || !validRolloutHash(result.KnownGoodHash) || !validRolloutHash(result.ReadinessEvidenceHash) || len(result.Resources) == 0 {
			return fmt.Errorf("successful rollout result lacks factual known-good metadata")
		}
	case deploymentv1.RolloutStateRolledBack:
		if result.CurrentDigest == "" || result.CurrentDigest != result.PreviousDigest || result.KnownGoodID != job.RolloutIntent.PreviousKnownGoodID || result.KnownGoodHash != job.RolloutIntent.PreviousKnownGoodHash || !validRolloutHash(result.ReadinessEvidenceHash) || len(result.Resources) == 0 {
			return fmt.Errorf("rolled-back result does not match the expected previous known-good")
		}
	case deploymentv1.RolloutStateRollbackFailed:
		if result.FailureCode == "" {
			return fmt.Errorf("rollback_failed requires a typed failure")
		}
	case deploymentv1.RolloutStateFailed:
		if result.FailureCode != deploymentv1.RolloutCodeNoKnownGood {
			return fmt.Errorf("terminal failed rollout requires NO_KNOWN_GOOD")
		}
	default:
		return fmt.Errorf("rollout result state is not terminal")
	}
	return nil
}

func exactTerminalReplay(job DeploymentJob, result DeploymentResult) bool {
	if job.Mode == "rollout" {
		if job.TerminalResult == nil || result.RolloutResult == nil || result.FailureCode != job.FailureCode || RedactString(result.FailureMessageRedacted) != job.FailureMessageRedacted {
			return false
		}
		candidate := *result.RolloutResult
		candidate.LeaseToken = ""
		candidate.FailureMessageRedacted = RedactString(candidate.FailureMessageRedacted)
		return reflect.DeepEqual(*job.TerminalResult, candidate)
	}
	return job.TerminalResult != nil && result.SpecHash == job.TerminalResult.SpecHash && normalizedDeploymentResultStatus(result.Status) == job.Status
}

func validRolloutHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validOptionalRolloutHash(value string) bool {
	return value == "" || validRolloutHash(value)
}

func validSanitizedResources(resources []deploymentv1.ResourceIdentity) bool {
	if len(resources) > deploymentv1.MaxRolloutResources {
		return false
	}
	for _, resource := range resources {
		if resource.Kind == "" || len(resource.Kind) > 64 || resource.Name == "" || len(resource.Name) > 253 || len(resource.Namespace) > 253 || resource.UID == "" || len(resource.UID) > 256 || resource.ResourceVersion == "" || len(resource.ResourceVersion) > 256 || !validRolloutHash(resource.FunctionalHash) {
			return false
		}
	}
	return true
}
