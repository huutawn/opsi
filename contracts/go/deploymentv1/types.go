// Package deploymentv1 defines the immutable manual deployment contracts.
package deploymentv1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
)

const (
	WorkloadSchemaVersion = "opsi.workload_spec/v1"
	JobSchemaVersion      = "opsi.deployment_job/v1"
	EventSchemaVersion    = "opsi.deployment_event/v1"
	CommandSchemaVersion  = "opsi.agent_deployment_command/v1"
	ResultSchemaVersion   = "opsi.agent_deployment_result/v1"
	ApplicationContainer  = "app"
)

const (
	StateQueued       = "queued"
	StateLeased       = "leased"
	StatePulling      = "pulling"
	StateApplying     = "applying"
	StateWaitingReady = "waiting_ready"
	StateSucceeded    = "succeeded"
	StateFailed       = "failed"
	StateCancelled    = "cancelled"
)

var (
	serviceKeyPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	envNamePattern        = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
	digestPattern         = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	repositoryPattern     = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?(?::[0-9]{1,5})?(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)+$`)
	cpuQuantityPattern    = regexp.MustCompile(`^(?:[1-9][0-9]{0,6}m|[1-9][0-9]{0,3})$`)
	memoryQuantityPattern = regexp.MustCompile(`^[1-9][0-9]{0,9}(?:Ki|Mi|Gi|Ti)?$`)
	opaqueIDPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	sensitiveEnvPattern   = regexp.MustCompile(`(^|_)(TOKEN|PASSWORD|SECRET|PRIVATE_KEY|ACCESS_KEY|API_KEY|CREDENTIAL)(_|$)`)
)

type ImmutableImage struct {
	Repository string `json:"repository"`
	Digest     string `json:"digest"`
	Reference  string `json:"reference"`
}

func NewImmutableImage(repository, digest string) (ImmutableImage, error) {
	image := ImmutableImage{Repository: repository, Digest: digest, Reference: repository + "@" + digest}
	return image, image.Validate()
}

func (i ImmutableImage) Validate() error {
	if len(i.Repository) == 0 || len(i.Repository) > 255 || i.Repository != strings.ToLower(i.Repository) || strings.ContainsAny(i.Repository, "@ \t\r\n") || !repositoryPattern.MatchString(i.Repository) {
		return errors.New("OCI repository is not canonical")
	}
	if !digestPattern.MatchString(i.Digest) {
		return errors.New("OCI digest must be a lowercase sha256 digest")
	}
	if i.Reference != i.Repository+"@"+i.Digest {
		return errors.New("OCI reference must exactly match repository and digest")
	}
	return nil
}

func (i ImmutableImage) WithinPrefix(prefix string) bool {
	prefix = strings.TrimSuffix(prefix, "/")
	return i.Repository == prefix || strings.HasPrefix(i.Repository, prefix+"/")
}

type Probe struct {
	Path                string `json:"path"`
	Port                int32  `json:"port"`
	InitialDelaySeconds int32  `json:"initial_delay_seconds"`
	PeriodSeconds       int32  `json:"period_seconds"`
	TimeoutSeconds      int32  `json:"timeout_seconds"`
	FailureThreshold    int32  `json:"failure_threshold"`
}

type ResourceValues struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
}

type Resources struct {
	Requests ResourceValues `json:"requests"`
	Limits   ResourceValues `json:"limits"`
}

type EnvironmentVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type SecretReference struct {
	EnvName  string `json:"env_name"`
	SecretID string `json:"secret_id"`
}

type ExposureIntent struct {
	Mode string `json:"mode"`
}

type WorkloadSpec struct {
	SchemaVersion                string                `json:"schema_version"`
	ServiceKey                   string                `json:"service_key"`
	Replicas                     int32                 `json:"replicas"`
	ApplicationContainerName     string                `json:"application_container_name"`
	ContainerPort                int32                 `json:"container_port"`
	ReadinessProbe               *Probe                `json:"readiness_probe,omitempty"`
	LivenessProbe                *Probe                `json:"liveness_probe,omitempty"`
	Resources                    Resources             `json:"resources"`
	TerminationGracePeriodSecond int64                 `json:"termination_grace_period_seconds"`
	SecretReferences             []SecretReference     `json:"secret_references,omitempty"`
	Environment                  []EnvironmentVariable `json:"environment,omitempty"`
	Exposure                     ExposureIntent        `json:"exposure"`
}

func (s WorkloadSpec) Normalize() WorkloadSpec {
	out := s
	if out.SchemaVersion == "" {
		out.SchemaVersion = WorkloadSchemaVersion
	}
	if out.ApplicationContainerName == "" {
		out.ApplicationContainerName = ApplicationContainer
	}
	if out.TerminationGracePeriodSecond == 0 {
		out.TerminationGracePeriodSecond = 30
	}
	out.Environment = append([]EnvironmentVariable(nil), s.Environment...)
	out.SecretReferences = append([]SecretReference(nil), s.SecretReferences...)
	sort.Slice(out.Environment, func(i, j int) bool { return out.Environment[i].Name < out.Environment[j].Name })
	sort.Slice(out.SecretReferences, func(i, j int) bool { return out.SecretReferences[i].EnvName < out.SecretReferences[j].EnvName })
	return out
}

func (s WorkloadSpec) Validate() error {
	s = s.Normalize()
	if s.SchemaVersion != WorkloadSchemaVersion {
		return errors.New("unsupported WorkloadSpec schema_version")
	}
	if !serviceKeyPattern.MatchString(s.ServiceKey) {
		return errors.New("service_key is invalid")
	}
	if s.Replicas < 1 || s.Replicas > 20 {
		return errors.New("replicas must be between 1 and 20")
	}
	if s.ApplicationContainerName != ApplicationContainer {
		return fmt.Errorf("application_container_name must be %q", ApplicationContainer)
	}
	if s.ContainerPort < 1 || s.ContainerPort > 65535 {
		return errors.New("container_port must be between 1 and 65535")
	}
	if err := validateProbe(s.ReadinessProbe, s.ContainerPort); err != nil {
		return fmt.Errorf("readiness_probe: %w", err)
	}
	if err := validateProbe(s.LivenessProbe, s.ContainerPort); err != nil {
		return fmt.Errorf("liveness_probe: %w", err)
	}
	if err := validateResources(s.Resources); err != nil {
		return err
	}
	if s.TerminationGracePeriodSecond < 1 || s.TerminationGracePeriodSecond > 300 {
		return errors.New("termination_grace_period_seconds must be between 1 and 300")
	}
	if s.Exposure.Mode != "none" && s.Exposure.Mode != "internal" {
		return errors.New("R5-010 exposure mode must be none or internal")
	}
	if len(s.Environment) > 64 || len(s.SecretReferences) > 32 {
		return errors.New("environment or secret reference count exceeds the bound")
	}
	seen := map[string]struct{}{}
	for _, item := range s.Environment {
		if !envNamePattern.MatchString(item.Name) || len(item.Value) > 4096 || strings.ContainsRune(item.Value, '\x00') {
			return errors.New("environment entry is invalid")
		}
		if sensitiveEnvPattern.MatchString(item.Name) {
			return errors.New("secret-like environment names require a secret reference")
		}
		if _, exists := seen[item.Name]; exists {
			return errors.New("environment names must be unique")
		}
		seen[item.Name] = struct{}{}
	}
	for _, item := range s.SecretReferences {
		if !envNamePattern.MatchString(item.EnvName) || !validOpaqueID(item.SecretID) {
			return errors.New("secret reference is invalid")
		}
		if _, exists := seen[item.EnvName]; exists {
			return errors.New("environment and secret names must be unique")
		}
		seen[item.EnvName] = struct{}{}
	}
	return nil
}

func validateProbe(probe *Probe, containerPort int32) error {
	if probe == nil {
		return nil
	}
	if len(probe.Path) == 0 || len(probe.Path) > 256 || !strings.HasPrefix(probe.Path, "/") || strings.ContainsAny(probe.Path, "\r\n\x00") || probe.Port != containerPort {
		return errors.New("path or port is invalid")
	}
	if probe.InitialDelaySeconds < 0 || probe.InitialDelaySeconds > 300 || probe.PeriodSeconds < 1 || probe.PeriodSeconds > 60 || probe.TimeoutSeconds < 1 || probe.TimeoutSeconds > 30 || probe.FailureThreshold < 1 || probe.FailureThreshold > 10 {
		return errors.New("probe timing exceeds allowed bounds")
	}
	return nil
}

func validateResources(resources Resources) error {
	if !cpuQuantityPattern.MatchString(resources.Requests.CPU) || !cpuQuantityPattern.MatchString(resources.Limits.CPU) {
		return errors.New("CPU quantities must be bounded millicore or whole-core values")
	}
	if !memoryQuantityPattern.MatchString(resources.Requests.Memory) || !memoryQuantityPattern.MatchString(resources.Limits.Memory) {
		return errors.New("memory quantities must be bounded Ki/Mi/Gi/Ti values")
	}
	requestCPU, _ := cpuMillicores(resources.Requests.CPU)
	limitCPU, _ := cpuMillicores(resources.Limits.CPU)
	requestMemory, _ := memoryBytes(resources.Requests.Memory)
	limitMemory, _ := memoryBytes(resources.Limits.Memory)
	if requestCPU > limitCPU || requestMemory > limitMemory {
		return errors.New("resource limits must be greater than or equal to requests")
	}
	return nil
}

func cpuMillicores(value string) (int64, error) {
	if strings.HasSuffix(value, "m") {
		return strconv.ParseInt(strings.TrimSuffix(value, "m"), 10, 64)
	}
	cores, err := strconv.ParseInt(value, 10, 64)
	return cores * 1000, err
}

func memoryBytes(value string) (int64, error) {
	multiplier := int64(1)
	for suffix, factor := range map[string]int64{"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40} {
		if strings.HasSuffix(value, suffix) {
			multiplier = factor
			value = strings.TrimSuffix(value, suffix)
			break
		}
	}
	quantity, err := strconv.ParseInt(value, 10, 64)
	if err != nil || quantity > (1<<63-1)/multiplier {
		return 0, errors.New("memory quantity overflows")
	}
	return quantity * multiplier, nil
}

func (s WorkloadSpec) Hash() (string, error) {
	normalized := s.Normalize()
	if err := normalized.Validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

type AuthoritySnapshot struct {
	BuildRecord              buildrecordv1.Record `json:"build_record"`
	TopologyPlanID           string               `json:"topology_plan_id"`
	TopologyRevision         uint64               `json:"topology_revision"`
	TopologyHash             string               `json:"topology_hash"`
	DeploymentPolicyID       string               `json:"deployment_policy_id"`
	DeploymentPolicyRevision uint64               `json:"deployment_policy_revision"`
	DeploymentPolicyHash     string               `json:"deployment_policy_hash"`
	RoutingDecisionHash      string               `json:"routing_decision_hash"`
	EnvironmentID            string               `json:"environment_id"`
	RuntimeID                string               `json:"runtime_id"`
	NodeID                   string               `json:"node_id"`
	AgentID                  string               `json:"agent_id"`
}

type JobSnapshot struct {
	SchemaVersion  string            `json:"schema_version"`
	ProjectID      string            `json:"project_id"`
	Image          ImmutableImage    `json:"image"`
	Authority      AuthoritySnapshot `json:"authority"`
	Workload       WorkloadSpec      `json:"workload"`
	SpecHash       string            `json:"spec_hash"`
	ActorUserID    string            `json:"actor_user_id"`
	IdempotencyKey string            `json:"idempotency_key"`
	PayloadHash    string            `json:"payload_hash"`
	CreatedAt      time.Time         `json:"created_at"`
}

type CreateRequest struct {
	SchemaVersion  string       `json:"schema_version"`
	BuildRecordID  string       `json:"build_record_id"`
	EnvironmentID  string       `json:"environment_id"`
	Workload       WorkloadSpec `json:"workload"`
	IdempotencyKey string       `json:"-"`
}

type Preview struct {
	SchemaVersion string       `json:"schema_version"`
	Snapshot      JobSnapshot  `json:"snapshot"`
	Current       *JobSnapshot `json:"current,omitempty"`
	Changes       []string     `json:"changes"`
	Eligible      bool         `json:"eligible"`
	DecisionCode  string       `json:"decision_code"`
	Message       string       `json:"message"`
	ResolvedAt    time.Time    `json:"resolved_at"`
}

type Event struct {
	SchemaVersion   string    `json:"schema_version"`
	ID              string    `json:"id"`
	ProjectID       string    `json:"project_id"`
	DeploymentJobID string    `json:"deployment_job_id"`
	State           string    `json:"state"`
	MessageRedacted string    `json:"message_redacted"`
	ProgressPercent int32     `json:"progress_percent"`
	Attempt         int32     `json:"attempt"`
	CreatedAt       time.Time `json:"created_at"`
}

type AgentCommand struct {
	SchemaVersion string         `json:"schema_version"`
	JobID         string         `json:"job_id"`
	ProjectID     string         `json:"project_id"`
	EnvironmentID string         `json:"environment_id"`
	RuntimeID     string         `json:"runtime_id"`
	NodeID        string         `json:"node_id"`
	AgentID       string         `json:"agent_id"`
	LeaseToken    string         `json:"lease_token"`
	Attempt       int32          `json:"attempt"`
	Image         ImmutableImage `json:"image"`
	Workload      WorkloadSpec   `json:"workload"`
	SpecHash      string         `json:"spec_hash"`
	Rollout       *RolloutIntent `json:"rollout,omitempty"`
}

type Progress struct {
	SchemaVersion         string             `json:"schema_version"`
	LeaseToken            string             `json:"lease_token"`
	State                 string             `json:"state"`
	MessageRedacted       string             `json:"message_redacted,omitempty"`
	ProgressPercent       int32              `json:"progress_percent,omitempty"`
	RolloutID             string             `json:"rollout_id,omitempty"`
	IntentHash            string             `json:"intent_hash,omitempty"`
	StateHash             string             `json:"state_hash,omitempty"`
	WorkloadSpecHash      string             `json:"workload_spec_hash,omitempty"`
	ExposureSpecHash      string             `json:"exposure_spec_hash,omitempty"`
	DesiredDigest         string             `json:"desired_digest,omitempty"`
	CurrentDigest         string             `json:"current_digest,omitempty"`
	PreviousDigest        string             `json:"previous_digest,omitempty"`
	ReadinessEvidenceHash string             `json:"readiness_evidence_hash,omitempty"`
	Resources             []ResourceIdentity `json:"resources,omitempty"`
	FailureCode           string             `json:"failure_code,omitempty"`
	Attempt               int32              `json:"attempt,omitempty"`
}

type AgentResult struct {
	SchemaVersion          string             `json:"schema_version"`
	LeaseToken             string             `json:"-"`
	Status                 string             `json:"status"`
	SpecHash               string             `json:"spec_hash"`
	ApplicationImage       string             `json:"application_image"`
	ApplicationImageID     string             `json:"application_image_id"`
	Namespace              string             `json:"namespace"`
	DeploymentName         string             `json:"deployment_name"`
	ServiceName            string             `json:"service_name"`
	AvailableReplicas      int32              `json:"available_replicas"`
	FailureCode            string             `json:"failure_code,omitempty"`
	FailurePhase           string             `json:"failure_phase,omitempty"`
	FailureMessageRedacted string             `json:"failure_message_redacted,omitempty"`
	RolloutID              string             `json:"rollout_id,omitempty"`
	RolloutState           string             `json:"rollout_state,omitempty"`
	IntentHash             string             `json:"intent_hash,omitempty"`
	StateHash              string             `json:"state_hash,omitempty"`
	WorkloadSpecHash       string             `json:"workload_spec_hash,omitempty"`
	ExposureSpecHash       string             `json:"exposure_spec_hash,omitempty"`
	DesiredDigest          string             `json:"desired_digest,omitempty"`
	CurrentDigest          string             `json:"current_digest,omitempty"`
	PreviousDigest         string             `json:"previous_digest,omitempty"`
	KnownGoodID            string             `json:"known_good_id,omitempty"`
	KnownGoodHash          string             `json:"known_good_hash,omitempty"`
	ReadinessEvidenceHash  string             `json:"readiness_evidence_hash,omitempty"`
	Resources              []ResourceIdentity `json:"resources,omitempty"`
	Attempt                int32              `json:"attempt,omitempty"`
}

func validOpaqueID(value string) bool {
	return opaqueIDPattern.MatchString(value)
}
