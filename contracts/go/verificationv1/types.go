// Package verificationv1 defines the wire contract for post-deploy dependency verification.
package verificationv1

import "time"

const SchemaVersion = "opsi.verification/v1"

// Layer statuses — used per-layer to describe outcome.
const (
	LayerStatusHealthy       = "HEALTHY"
	LayerStatusUnhealthy     = "UNHEALTHY"
	LayerStatusResolved      = "RESOLVED"
	LayerStatusInvalid       = "INVALID"
	LayerStatusVerified      = "VERIFIED"
	LayerStatusFailed        = "FAILED"
	LayerStatusNotConfigured = "NOT_CONFIGURED"
	LayerStatusNotSupported  = "NOT_SUPPORTED"
	LayerStatusPending       = "PENDING"
	LayerStatusSkipped       = "SKIPPED"
)

// Overall run statuses.
const (
	RunStatusVerified          = "VERIFIED"
	RunStatusPartiallyVerified = "PARTIALLY_VERIFIED"
	RunStatusFailed            = "FAILED"
	RunStatusStale             = "STALE"
	RunStatusNotRun            = "NOT_RUN"
)

// Failure codes — machine-readable reason for a non-successful outcome.
const (
	FailureProviderUnhealthy              = "PROVIDER_UNHEALTHY"
	FailureContractInvalid                = "CONTRACT_INVALID"
	FailureConnectionFailed               = "CONNECTION_FAILED"
	FailureConsumerUnhealthy              = "CONSUMER_UNHEALTHY"
	FailureConsumerAssertionFailed        = "CONSUMER_ASSERTION_FAILED"
	FailureConsumerAssertionNotConfigured = "CONSUMER_ASSERTION_NOT_CONFIGURED"
	FailureVerificationStale              = "VERIFICATION_STALE"
	FailureVerificationTimeout            = "VERIFICATION_TIMEOUT"
)

// ProviderHealthLayer captures the health of the upstream provider
// (e.g. postgres, valkey, or an application service).
type ProviderHealthLayer struct {
	Status       string            `json:"status"`                  // HEALTHY | UNHEALTHY | PENDING
	ProviderKind string            `json:"provider_kind"`           // "postgres" | "valkey" | "application"
	ProviderID   string            `json:"provider_id"`
	SafeEvidence map[string]string `json:"safe_evidence,omitempty"` // no credentials ever
	FailureCode  string            `json:"failure_code,omitempty"`
	Message      string            `json:"message,omitempty"`
}

// ContractResolutionLayer captures whether the dependency binding and
// credential injection were successfully resolved.
type ContractResolutionLayer struct {
	Status            string `json:"status"`               // RESOLVED | INVALID
	BindingID         string `json:"binding_id,omitempty"`
	Protocol          string `json:"protocol,omitempty"`
	InjectionComplete bool   `json:"injection_complete"`
	FailureCode       string `json:"failure_code,omitempty"`
	Message           string `json:"message,omitempty"`
}

// ConnectionLayer captures a low-level connectivity probe to the dependency.
// No credential fields must ever appear here.
type ConnectionLayer struct {
	Status      string `json:"status"`              // VERIFIED | FAILED | NOT_SUPPORTED | NOT_CONFIGURED
	Protocol    string `json:"protocol,omitempty"`
	LatencyMs   int64  `json:"latency_ms,omitempty"`
	FailureCode string `json:"failure_code,omitempty"`
	Message     string `json:"message,omitempty"`
}

// ConsumerHealthLayer captures the readiness state of the consuming workload.
type ConsumerHealthLayer struct {
	Status      string `json:"status"` // HEALTHY | UNHEALTHY
	ReadyPods   int    `json:"ready_pods"`
	TotalPods   int    `json:"total_pods"`
	FailureCode string `json:"failure_code,omitempty"`
	Message     string `json:"message,omitempty"`
}

// ConsumerAssertionLayer captures the result of an optional user-declared
// HTTP assertion against the consuming service. Without a passing assertion
// the overall run is PARTIALLY_VERIFIED, never VERIFIED.
// No response body must ever be stored here.
type ConsumerAssertionLayer struct {
	Status        string `json:"status"`                   // VERIFIED | FAILED | NOT_CONFIGURED | NOT_SUPPORTED
	AssertionPath string `json:"assertion_path,omitempty"`
	StatusCode    int    `json:"status_code,omitempty"`
	ExpectedCode  int    `json:"expected_code,omitempty"`
	FailureCode   string `json:"failure_code,omitempty"`
	Message       string `json:"message,omitempty"`
}

// ConsumerVerificationContract is optional user-declared verification intent
// expressed inline in a VerifyDependencyRequest. It mirrors
// serviceconfigurationv1.DependencyVerificationContract but lives here for
// use in the API request/response layer without a cross-module dependency.
type ConsumerVerificationContract struct {
	Type           string `json:"type"`            // "consumer_http"
	Path           string `json:"path"`            // relative path on the consumer service
	ExpectedStatus int    `json:"expected_status"` // e.g. 200
}

// VerificationRun is the complete record of a single post-deploy verification
// attempt across all five layers.
type VerificationRun struct {
	SchemaVersion string `json:"schema_version"`

	ID                    string `json:"id"`
	ProjectID             string `json:"project_id"`
	EnvironmentID         string `json:"environment_id"`
	ConsumerApplicationID string `json:"consumer_application_id"`
	DependencyLogicalName string `json:"dependency_logical_name"`
	DeploymentJobID       string `json:"deployment_job_id"`

	// Staleness fingerprints — used to detect whether a cached result is still current.
	ConfigRevision  uint64 `json:"config_revision"`
	TargetBindingID string `json:"target_binding_id,omitempty"`
	SourceCommitSHA string `json:"source_commit_sha,omitempty"`
	StalenessHash   string `json:"staleness_hash"`

	// Layers — evaluated in order; later layers may be SKIPPED if an earlier one fails.
	ProviderHealth     ProviderHealthLayer     `json:"provider_health"`
	ContractResolution ContractResolutionLayer `json:"contract_resolution"`
	Connection         ConnectionLayer         `json:"connection"`
	ConsumerHealth     ConsumerHealthLayer     `json:"consumer_health"`
	ConsumerAssertion  ConsumerAssertionLayer  `json:"consumer_assertion"`

	// Overall — synthesised from all layers.
	OverallStatus string `json:"overall_status"` // RunStatus* constants
	FailureCode   string `json:"failure_code,omitempty"`

	TriggeredBy string     `json:"triggered_by"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// DepProbeEvidence captures raw probe observation reported from agent or runtime probe.
type DepProbeEvidence struct {
	StatusCode int    `json:"status_code,omitempty"`
	LatencyMs  int64  `json:"latency_ms,omitempty"`
	Message    string `json:"message,omitempty"`
}

// VerifyDependencyRequest is the API trigger payload.
type VerifyDependencyRequest struct {
	DependencyLogicalName string                        `json:"dependency_logical_name"`
	DeploymentJobID       string                        `json:"deployment_job_id"`
	ConsumerContract      *ConsumerVerificationContract `json:"consumer_contract,omitempty"`
	ObservedStatusCode    int                           `json:"observed_status_code,omitempty"`
	ProbeResult           *DepProbeEvidence             `json:"probe_result,omitempty"`
}

// VerifyDependencyResponse is the API response after initiating or completing verification.
type VerifyDependencyResponse struct {
	Run VerificationRun `json:"run"`
}
