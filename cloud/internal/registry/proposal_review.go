package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

const proposalReviewLifetime = 24 * time.Hour

type ProposalReviewKind string
type ProposalReviewStatus string

const (
	ProposalReviewServiceConfiguration ProposalReviewKind   = "service_configuration"
	ProposalReviewSourcePatch          ProposalReviewKind   = "source_patch"
	ReviewRequired                     ProposalReviewStatus = "review_required"
	ReviewApproved                     ProposalReviewStatus = "approved"
	ReviewRejected                     ProposalReviewStatus = "rejected"
	ReviewStale                        ProposalReviewStatus = "stale"
	ReviewExpired                      ProposalReviewStatus = "expired"
	ReviewApplied                      ProposalReviewStatus = "applied"
	ReviewApplyFailed                  ProposalReviewStatus = "apply_failed"
)

// ProposalReview is Cloud workflow authority. Its payload is deliberately a
// normalized review artifact, not a second configuration or source authority.
type ProposalReview struct {
	ID                             string               `json:"id"`
	ProjectID                      string               `json:"project_id"`
	EnvironmentID                  string               `json:"environment_id"`
	ApplicationID                  string               `json:"application_id"`
	Kind                           ProposalReviewKind   `json:"kind"`
	Status                         ProposalReviewStatus `json:"status"`
	ProposalHash                   string               `json:"proposal_hash"`
	AnalysisInputsHash             string               `json:"analysis_inputs_hash"`
	SourceCommit                   string               `json:"source_commit,omitempty"`
	ApplicationRoot                string               `json:"application_root,omitempty"`
	NormalizedPayload              json.RawMessage      `json:"normalized_payload"`
	ReviewedPayloadHash            string               `json:"reviewed_payload_hash"`
	ExpectedConfigurationRevision  uint64               `json:"expected_configuration_revision,omitempty"`
	ExpectedConfigurationStateHash string               `json:"expected_configuration_state_hash,omitempty"`
	CreatedBy                      string               `json:"created_by,omitempty"`
	CreatedAt                      time.Time            `json:"created_at"`
	ExpiresAt                      time.Time            `json:"expires_at"`
	ApprovedBy                     string               `json:"approved_by,omitempty"`
	ApprovedAt                     *time.Time           `json:"approved_at,omitempty"`
	RejectedBy                     string               `json:"rejected_by,omitempty"`
	RejectedAt                     *time.Time           `json:"rejected_at,omitempty"`
	AppliedAt                      *time.Time           `json:"applied_at,omitempty"`
	ResultingConfigurationRevision uint64               `json:"resulting_configuration_revision,omitempty"`
	ApplyIdempotencyKey            string               `json:"-"`
	FailureCode                    string               `json:"failure_code,omitempty"`
}

type ProposalReviewCreateRequest struct {
	EnvironmentID      string                     `json:"environment_id"`
	Kind               ProposalReviewKind         `json:"kind"`
	AnalysisInputsHash string                     `json:"analysis_inputs_hash"`
	SourceCommit       string                     `json:"source_commit,omitempty"`
	ApplicationRoot    string                     `json:"application_root,omitempty"`
	ConfigurationDraft *ServiceConfigurationDraft `json:"configuration_draft,omitempty"`
}

func reviewHash(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validReviewHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func proposalReviewStale(value ProposalReview, configuration ServiceConfiguration, now time.Time) ProposalReviewStatus {
	if value.Status != ReviewRequired && value.Status != ReviewApproved {
		return value.Status
	}
	if now.After(value.ExpiresAt) {
		return ReviewExpired
	}
	if isConfigurationReviewKind(value.Kind) && (configuration.Revision != value.ExpectedConfigurationRevision || configuration.StateHash != value.ExpectedConfigurationStateHash) {
		return ReviewStale
	}
	return value.Status
}

func decodeReviewedConfiguration(value ProposalReview) (ServiceConfigurationDraft, error) {
	var payload struct {
		Draft ServiceConfigurationDraft `json:"draft"`
	}
	if err := json.Unmarshal(value.NormalizedPayload, &payload); err != nil {
		return ServiceConfigurationDraft{}, APIError{Status: 409, Code: "TAMPERED_REVIEW", Message: "stored proposal review payload is invalid"}
	}
	return payload.Draft, nil
}

func isConfigurationReviewKind(kind ProposalReviewKind) bool {
	// Registry owns this persisted-value compatibility boundary. Remove the
	// dependency value after all pre-mcp-04 proposal rows have expired (24h)
	// or been migrated; new requests cannot create it.
	return kind == ProposalReviewServiceConfiguration || kind == ProposalReviewKind("dependency")
}

func sortProposalReviews(values []ProposalReview) {
	sort.Slice(values, func(i, j int) bool {
		leftActionable := values[i].Status == ReviewRequired || values[i].Status == ReviewApproved
		rightActionable := values[j].Status == ReviewRequired || values[j].Status == ReviewApproved
		if leftActionable != rightActionable {
			return leftActionable
		}
		return values[i].CreatedAt.After(values[j].CreatedAt)
	})
}

func boundedReviewText(value string) string { return strings.TrimSpace(value) }
