package deploymentv1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
)

const (
	RolloutSchemaVersion       = "opsi.rollout_intent/v1"
	RolloutRecordVersion       = "opsi.rollout_record/v1"
	RolloutEventVersion        = "opsi.rollout_event/v1"
	RuntimeSnapshotVersion     = "opsi.runtime_snapshot/v1"
	KnownGoodSchemaVersion     = "opsi.known_good/v1"
	ReadinessEvidenceVersion   = "opsi.readiness_evidence/v1"
	ExposureMutationVersion    = "opsi.exposure_mutation/v1"
	ExposurePreviewVersion     = "opsi.exposure_preview/v1"
	RolloutStatePrepared       = "prepared"
	RolloutStateApplying       = "applying"
	RolloutStateWaiting        = "waiting"
	RolloutStateSucceeded      = "succeeded"
	RolloutStateFailed         = "failed"
	RolloutStateRollingBack    = "rolling_back"
	RolloutStateRolledBack     = "rolled_back"
	RolloutStateRollbackFailed = "rollback_failed"
	RolloutOperationApply      = "apply"
	RolloutOperationRollback   = "rollback"
	MaxRolloutAttempts         = 3
	MaxRolloutErrorBytes       = 1024
	MaxRolloutResources        = 8
)

const (
	RolloutCodeInvalid             = "ROLLOUT_INVALID"
	RolloutCodeConflict            = "ROLLOUT_CONFLICT"
	RolloutCodeTargetBusy          = "ROLLOUT_TARGET_BUSY"
	RolloutCodeTerminalImmutable   = "ROLLOUT_TERMINAL_IMMUTABLE"
	RolloutCodeInvalidTransition   = "ROLLOUT_INVALID_TRANSITION"
	RolloutCodeNoKnownGood         = "NO_KNOWN_GOOD"
	RolloutCodeKnownGoodCorrupt    = "KNOWN_GOOD_CORRUPT"
	RolloutCodeAttemptsExhausted   = "ROLLOUT_ATTEMPTS_EXHAUSTED"
	RolloutCodeOwnershipConflict   = "K8S_OWNERSHIP_CONFLICT"
	RolloutCodeResourceChanged     = "K8S_RESOURCE_CHANGED"
	RolloutCodeReadinessFailed     = "RUNTIME_READINESS_FAILED"
	RolloutCodeExternalUnavailable = "EXTERNAL_VERIFICATION_UNAVAILABLE"
	RolloutCodePreflightFailed     = "ROLLOUT_PREFLIGHT_FAILED"
	RolloutCodeRuntimeFailed       = "ROLLOUT_RUNTIME_FAILED"
)

var rolloutHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type RolloutError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

func (e *RolloutError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

func NewRolloutError(code, message string, retryable bool) *RolloutError {
	message = strings.TrimSpace(message)
	if len(message) > MaxRolloutErrorBytes {
		message = message[:MaxRolloutErrorBytes]
	}
	return &RolloutError{Code: code, Message: message, Retryable: retryable}
}

type RuntimeTarget struct {
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	RuntimeID     string `json:"runtime_id"`
	ServiceKey    string `json:"service_key"`
	NodeID        string `json:"node_id"`
	AgentID       string `json:"agent_id"`
}

func (t RuntimeTarget) Key() string {
	return strings.Join([]string{t.ProjectID, t.EnvironmentID, t.RuntimeID, t.ServiceKey}, "\x00")
}

func (t RuntimeTarget) Validate() error {
	for field, value := range map[string]string{
		"project_id": t.ProjectID, "environment_id": t.EnvironmentID,
		"runtime_id": t.RuntimeID, "service_key": t.ServiceKey,
		"node_id": t.NodeID, "agent_id": t.AgentID,
	} {
		if !validOpaqueID(value) {
			return fmt.Errorf("%s is invalid", field)
		}
	}
	return nil
}

type RuntimeAuthority struct {
	TopologyPlanID           string `json:"topology_plan_id"`
	TopologyRevision         uint64 `json:"topology_revision"`
	TopologyHash             string `json:"topology_hash"`
	DeploymentPolicyID       string `json:"deployment_policy_id"`
	DeploymentPolicyRevision uint64 `json:"deployment_policy_revision"`
	DeploymentPolicyHash     string `json:"deployment_policy_hash"`
	RoutingDecisionHash      string `json:"routing_decision_hash"`
}

func (a RuntimeAuthority) Validate() error {
	if !validOpaqueID(a.TopologyPlanID) || a.TopologyRevision == 0 || !rolloutHashPattern.MatchString(a.TopologyHash) ||
		!validOpaqueID(a.DeploymentPolicyID) || a.DeploymentPolicyRevision == 0 || !rolloutHashPattern.MatchString(a.DeploymentPolicyHash) ||
		!rolloutHashPattern.MatchString(a.RoutingDecisionHash) {
		return errors.New("runtime authority identity is incomplete")
	}
	return nil
}

type RuntimeSnapshot struct {
	SchemaVersion    string                  `json:"schema_version"`
	Target           RuntimeTarget           `json:"target"`
	DeploymentJobID  string                  `json:"deployment_job_id"`
	Image            ImmutableImage          `json:"image"`
	Workload         WorkloadSpec            `json:"workload"`
	WorkloadSpecHash string                  `json:"workload_spec_hash"`
	Exposure         exposurev1.ExposureSpec `json:"exposure"`
	ExposureSpecHash string                  `json:"exposure_spec_hash"`
	Authority        RuntimeAuthority        `json:"authority"`
}

func (s RuntimeSnapshot) Validate() error {
	if s.SchemaVersion != RuntimeSnapshotVersion {
		return errors.New("unsupported runtime snapshot schema_version")
	}
	if err := s.Target.Validate(); err != nil {
		return err
	}
	if !validOpaqueID(s.DeploymentJobID) {
		return errors.New("deployment_job_id is invalid")
	}
	if err := s.Image.Validate(); err != nil {
		return err
	}
	workloadHash, err := s.Workload.Hash()
	if err != nil || workloadHash != s.WorkloadSpecHash || s.Workload.ServiceKey != s.Target.ServiceKey {
		return errors.New("workload snapshot hash or identity is invalid")
	}
	if s.ExposureSpecHash == "" {
		if !reflect.DeepEqual(s.Exposure, exposurev1.ExposureSpec{}) {
			return errors.New("no-external runtime snapshot must omit ExposureSpec")
		}
		return s.Authority.Validate()
	}
	exposure, err := s.Exposure.Canonicalize()
	if err != nil || exposure.SpecHash != s.ExposureSpecHash ||
		exposure.ProjectID != s.Target.ProjectID || exposure.EnvironmentID != s.Target.EnvironmentID ||
		exposure.RuntimeID != s.Target.RuntimeID || exposure.ServiceKey != s.Target.ServiceKey ||
		exposure.DeploymentJobID != s.DeploymentJobID || exposure.ServicePort != s.Workload.ContainerPort {
		return errors.New("exposure snapshot hash or identity is invalid")
	}
	return s.Authority.Validate()
}

func (s RuntimeSnapshot) HasExternalExposure() bool {
	return s.ExposureSpecHash != ""
}

// Hash includes every runtime-authority field. Exposure metadata is the only
// audit/display field and is excluded so it cannot reset known-good state.
func (s RuntimeSnapshot) Hash() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	payload := s
	payload.Exposure.Metadata = nil
	return canonicalHash(payload)
}

func (s RuntimeSnapshot) AgentCommand() AgentCommand {
	return AgentCommand{
		SchemaVersion: CommandSchemaVersion,
		JobID:         s.DeploymentJobID,
		ProjectID:     s.Target.ProjectID,
		EnvironmentID: s.Target.EnvironmentID,
		RuntimeID:     s.Target.RuntimeID,
		NodeID:        s.Target.NodeID,
		AgentID:       s.Target.AgentID,
		Attempt:       1,
		Image:         s.Image,
		Workload:      s.Workload,
		SpecHash:      s.WorkloadSpecHash,
	}
}

type RolloutIntent struct {
	SchemaVersion         string          `json:"schema_version"`
	RolloutID             string          `json:"rollout_id"`
	Operation             string          `json:"operation"`
	Target                RuntimeTarget   `json:"target"`
	Desired               RuntimeSnapshot `json:"desired"`
	PreviousKnownGoodID   string          `json:"previous_known_good_id,omitempty"`
	PreviousKnownGoodHash string          `json:"previous_known_good_hash,omitempty"`
	PreviousDigest        string          `json:"previous_digest,omitempty"`
	ExpectedKnownGoodID   string          `json:"expected_known_good_id,omitempty"`
	ExpectedKnownGoodHash string          `json:"expected_known_good_hash,omitempty"`
	Attempt               int32           `json:"attempt"`
	CreatedAt             time.Time       `json:"created_at"`
	IntentHash            string          `json:"intent_hash"`
}

type RolloutRecord struct {
	SchemaVersion string             `json:"schema_version"`
	Intent        RolloutIntent      `json:"intent"`
	State         string             `json:"state"`
	Version       uint64             `json:"version"`
	StateHash     string             `json:"state_hash"`
	Error         *RolloutError      `json:"error,omitempty"`
	Resources     []ResourceIdentity `json:"resources,omitempty"`
	Evidence      *ReadinessEvidence `json:"evidence,omitempty"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
	TerminalAt    *time.Time         `json:"terminal_at,omitempty"`
}

func (r RolloutRecord) CalculateStateHash() (string, error) {
	payload := r
	payload.StateHash = ""
	return canonicalHash(payload)
}

type RolloutEvent struct {
	SchemaVersion string        `json:"schema_version"`
	RolloutID     string        `json:"rollout_id"`
	Version       uint64        `json:"version"`
	State         string        `json:"state"`
	StateHash     string        `json:"state_hash"`
	Error         *RolloutError `json:"error,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
}

func (i RolloutIntent) Canonicalize() (RolloutIntent, error) {
	out := i
	if out.SchemaVersion != RolloutSchemaVersion || !validOpaqueID(out.RolloutID) {
		return RolloutIntent{}, errors.New("rollout schema or id is invalid")
	}
	if out.Operation == "" {
		out.Operation = RolloutOperationApply
	}
	if out.Operation != RolloutOperationApply && out.Operation != RolloutOperationRollback {
		return RolloutIntent{}, errors.New("rollout operation is invalid")
	}
	if err := out.Target.Validate(); err != nil {
		return RolloutIntent{}, err
	}
	if err := out.Desired.Validate(); err != nil || out.Target != out.Desired.Target {
		return RolloutIntent{}, errors.New("desired runtime snapshot does not match rollout target")
	}
	if out.Attempt < 1 || out.Attempt > MaxRolloutAttempts || out.CreatedAt.IsZero() {
		return RolloutIntent{}, errors.New("rollout attempt or timestamp is invalid")
	}
	if (out.PreviousKnownGoodID == "") != (out.PreviousKnownGoodHash == "") {
		return RolloutIntent{}, errors.New("previous known-good reference is incomplete")
	}
	if out.PreviousKnownGoodID != "" && (!validOpaqueID(out.PreviousKnownGoodID) || !rolloutHashPattern.MatchString(out.PreviousKnownGoodHash)) {
		return RolloutIntent{}, errors.New("previous known-good reference is invalid")
	}
	if out.PreviousDigest != "" && !digestPattern.MatchString(out.PreviousDigest) {
		return RolloutIntent{}, errors.New("previous digest is invalid")
	}
	if out.PreviousKnownGoodID == "" && out.PreviousDigest != "" {
		return RolloutIntent{}, errors.New("previous digest requires a known-good reference")
	}
	if (out.ExpectedKnownGoodID == "") != (out.ExpectedKnownGoodHash == "") {
		return RolloutIntent{}, errors.New("expected known-good reference is incomplete")
	}
	if out.ExpectedKnownGoodID != "" && (!validOpaqueID(out.ExpectedKnownGoodID) || !rolloutHashPattern.MatchString(out.ExpectedKnownGoodHash)) {
		return RolloutIntent{}, errors.New("expected known-good reference is invalid")
	}
	if out.Operation == RolloutOperationApply && out.ExpectedKnownGoodID != "" {
		return RolloutIntent{}, errors.New("apply rollout must use the previous known-good reference as its expectation")
	}
	if out.Operation == RolloutOperationRollback && (out.PreviousKnownGoodID == "" || out.ExpectedKnownGoodID == "") {
		return RolloutIntent{}, errors.New("explicit rollback requires target and expected current known-good references")
	}
	payload := out
	payload.IntentHash = ""
	payload.Desired.Exposure.Metadata = nil
	hash, err := canonicalHash(payload)
	if err != nil {
		return RolloutIntent{}, err
	}
	if out.IntentHash != "" && out.IntentHash != hash {
		return RolloutIntent{}, errors.New("rollout intent hash mismatch")
	}
	out.IntentHash = hash
	return out, nil
}

func (i RolloutIntent) Validate() error {
	if i.IntentHash == "" {
		return errors.New("rollout intent_hash is required")
	}
	_, err := i.Canonicalize()
	return err
}

type ResourceIdentity struct {
	Kind            string `json:"kind"`
	Namespace       string `json:"namespace,omitempty"`
	Name            string `json:"name"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resource_version"`
	FunctionalHash  string `json:"functional_hash"`
}

type ReadinessEvidence struct {
	SchemaVersion          string    `json:"schema_version"`
	RuntimeReady           bool      `json:"runtime_ready"`
	LocalRoutingReady      bool      `json:"local_routing_ready"`
	ExternalReady          bool      `json:"external_ready"`
	WorkloadEvidenceHash   string    `json:"workload_evidence_hash"`
	ServiceEvidenceHash    string    `json:"service_evidence_hash"`
	ExposureEvidenceHash   string    `json:"exposure_evidence_hash"`
	ApplicationImageIDHash string    `json:"application_image_id_hash"`
	LocalProbeEvidenceHash string    `json:"local_probe_evidence_hash,omitempty"`
	ExternalEvidenceHash   string    `json:"external_evidence_hash,omitempty"`
	ObservedAt             time.Time `json:"observed_at"`
}

func (e ReadinessEvidence) Validate(requireLocal, requireExternal bool) error {
	if e.SchemaVersion != ReadinessEvidenceVersion || e.ObservedAt.IsZero() || !e.RuntimeReady {
		return errors.New("runtime readiness evidence is incomplete")
	}
	for _, hash := range []string{e.WorkloadEvidenceHash, e.ServiceEvidenceHash, e.ExposureEvidenceHash, e.ApplicationImageIDHash} {
		if !rolloutHashPattern.MatchString(hash) {
			return errors.New("runtime readiness evidence hash is invalid")
		}
	}
	if requireLocal && (!e.LocalRoutingReady || !rolloutHashPattern.MatchString(e.LocalProbeEvidenceHash)) {
		return errors.New("local routing readiness evidence is incomplete")
	}
	if requireExternal && (!e.ExternalReady || !rolloutHashPattern.MatchString(e.ExternalEvidenceHash)) {
		return errors.New("external readiness evidence is incomplete")
	}
	return nil
}

func (e ReadinessEvidence) Hash() (string, error) {
	return canonicalHash(e)
}

type KnownGoodSnapshot struct {
	SchemaVersion string             `json:"schema_version"`
	ID            string             `json:"id"`
	Target        RuntimeTarget      `json:"target"`
	Runtime       RuntimeSnapshot    `json:"runtime"`
	Resources     []ResourceIdentity `json:"resources"`
	EvidenceHash  string             `json:"evidence_hash"`
	VerifiedAt    time.Time          `json:"verified_at"`
	SnapshotHash  string             `json:"snapshot_hash"`
}

func (s KnownGoodSnapshot) Canonicalize() (KnownGoodSnapshot, error) {
	out := s
	if out.SchemaVersion != KnownGoodSchemaVersion || !validOpaqueID(out.ID) || out.Target != out.Runtime.Target || out.VerifiedAt.IsZero() || !rolloutHashPattern.MatchString(out.EvidenceHash) {
		return KnownGoodSnapshot{}, errors.New("known-good identity or evidence is invalid")
	}
	if err := out.Runtime.Validate(); err != nil || len(out.Resources) == 0 || len(out.Resources) > MaxRolloutResources {
		return KnownGoodSnapshot{}, errors.New("known-good runtime or resources are invalid")
	}
	sort.Slice(out.Resources, func(i, j int) bool {
		return out.Resources[i].Kind+"\x00"+out.Resources[i].Namespace+"\x00"+out.Resources[i].Name < out.Resources[j].Kind+"\x00"+out.Resources[j].Namespace+"\x00"+out.Resources[j].Name
	})
	for _, resource := range out.Resources {
		if resource.Kind == "" || resource.Name == "" || resource.UID == "" || resource.ResourceVersion == "" || !rolloutHashPattern.MatchString(resource.FunctionalHash) {
			return KnownGoodSnapshot{}, errors.New("known-good resource identity is invalid")
		}
	}
	payload := out
	payload.SnapshotHash = ""
	payload.Runtime.Exposure.Metadata = nil
	hash, err := canonicalHash(payload)
	if err != nil {
		return KnownGoodSnapshot{}, err
	}
	if out.SnapshotHash != "" && out.SnapshotHash != hash {
		return KnownGoodSnapshot{}, errors.New("known-good snapshot hash mismatch")
	}
	out.SnapshotHash = hash
	return out, nil
}

func (s KnownGoodSnapshot) Validate() error {
	if s.SnapshotHash == "" {
		return errors.New("known-good snapshot_hash is required")
	}
	_, err := s.Canonicalize()
	return err
}

func IsTerminalRolloutState(state string) bool {
	switch state {
	case RolloutStateSucceeded, RolloutStateRolledBack, RolloutStateRollbackFailed:
		return true
	default:
		return false
	}
}

func CanTransitionRollout(from, to string) bool {
	allowed := map[string]map[string]bool{
		RolloutStatePrepared:    {RolloutStateApplying: true, RolloutStateRollingBack: true, RolloutStateFailed: true},
		RolloutStateApplying:    {RolloutStateWaiting: true, RolloutStateFailed: true},
		RolloutStateWaiting:     {RolloutStateSucceeded: true, RolloutStateFailed: true},
		RolloutStateFailed:      {RolloutStateRollingBack: true},
		RolloutStateRollingBack: {RolloutStateRolledBack: true, RolloutStateRollbackFailed: true},
	}
	return allowed[from][to]
}

type ExposureMutationRequest struct {
	SchemaVersion       string                  `json:"schema_version"`
	BaseDeploymentJobID string                  `json:"base_deployment_job_id"`
	ExpectedStateHash   string                  `json:"expected_state_hash,omitempty"`
	Exposure            exposurev1.ExposureSpec `json:"exposure"`
}

type ExposurePreview struct {
	SchemaVersion       string                   `json:"schema_version"`
	BaseDeploymentJobID string                   `json:"base_deployment_job_id"`
	Current             *exposurev1.ExposureSpec `json:"current,omitempty"`
	Desired             exposurev1.ExposureSpec  `json:"desired"`
	Changes             []string                 `json:"changes"`
	StateHash           string                   `json:"state_hash"`
	Eligible            bool                     `json:"eligible"`
	DecisionCode        string                   `json:"decision_code"`
	Message             string                   `json:"message"`
	ResolvedAt          time.Time                `json:"resolved_at"`
}

func canonicalHash(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
