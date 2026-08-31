package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"
)

const proposalReviewLifetime = 24 * time.Hour

var sourceReviewSecrets = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:github_pat_|gh[pousr]_|glpat-|xox[baprs]-)[A-Za-z0-9_\-]{8,}`),
	regexp.MustCompile(`(?i)(?:postgres(?:ql)?|redis|rediss)://[^\s"\\]+`),
	regexp.MustCompile(`(?i)("(?:agent[_-]?token|postgres(?:ql)?[_-]?password|valkey[_-]?password|redis[_-]?password|registry[_-]?(?:credential|password|token)|password|token|pat|credential|secret)(?:[_-][^"]*)?"\s*:\s*)"(?:[^"\\]|\\.)*"`),
	regexp.MustCompile(`(?i)(?:agent[_-]?token|postgres(?:ql)?[_-]?password|valkey[_-]?password|redis[_-]?password|registry[_-]?(?:credential|password|token)|password|token|pat|credential|secret)\s*[:=]\s*["']?[^\s,"'}\\\]]+`),
}

type ProposalReviewKind string
type ProposalReviewStatus string

const (
	ProposalReviewDependency  ProposalReviewKind   = "dependency"
	ProposalReviewSourcePatch ProposalReviewKind   = "source_patch"
	ReviewRequired            ProposalReviewStatus = "review_required"
	ReviewApproved            ProposalReviewStatus = "approved"
	ReviewRejected            ProposalReviewStatus = "rejected"
	ReviewStale               ProposalReviewStatus = "stale"
	ReviewExpired             ProposalReviewStatus = "expired"
	ReviewApplied             ProposalReviewStatus = "applied"
	ReviewApplyFailed         ProposalReviewStatus = "apply_failed"
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
	DependencyDraft    *ServiceConfigurationDraft `json:"dependency_draft,omitempty"`
	SourcePatch        json.RawMessage            `json:"source_patch,omitempty"`
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

func normalizeSourcePatch(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > 256<<10 || !json.Valid(raw) {
		return nil, APIError{Status: 422, Code: "SOURCE_PATCH_INVALID", Message: "source patch review payload is invalid"}
	}
	// A source review is display-only. Scrub obvious credential-bearing text at
	// the Cloud boundary before persistence or Copy Patch can expose it.
	safe := string(raw)
	for _, pattern := range sourceReviewSecrets {
		if strings.Contains(pattern.String(), "\\s*:") {
			safe = pattern.ReplaceAllString(safe, `${1}"[REDACTED]"`)
		} else {
			safe = pattern.ReplaceAllString(safe, "[REDACTED]")
		}
	}
	return json.RawMessage(safe), nil
}

func proposalReviewStale(value ProposalReview, configuration ServiceConfiguration, now time.Time) ProposalReviewStatus {
	if value.Status != ReviewRequired && value.Status != ReviewApproved {
		return value.Status
	}
	if now.After(value.ExpiresAt) {
		return ReviewExpired
	}
	if value.Kind == ProposalReviewDependency && (configuration.Revision != value.ExpectedConfigurationRevision || configuration.StateHash != value.ExpectedConfigurationStateHash) {
		return ReviewStale
	}
	return value.Status
}

func decodeReviewedDependency(value ProposalReview) (ServiceConfigurationDraft, error) {
	var payload struct {
		Draft ServiceConfigurationDraft `json:"draft"`
	}
	if err := json.Unmarshal(value.NormalizedPayload, &payload); err != nil {
		return ServiceConfigurationDraft{}, APIError{Status: 409, Code: "TAMPERED_REVIEW", Message: "stored proposal review payload is invalid"}
	}
	return payload.Draft, nil
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
