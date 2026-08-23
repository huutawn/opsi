package registry

import (
	"encoding/json"
)

func (s *Service) CreateProposalReview(projectID, applicationID, actorUserID string, request ProposalReviewCreateRequest) (ProposalReview, error) {
	if request.Kind != ProposalReviewDependency && request.Kind != ProposalReviewSourcePatch {
		return ProposalReview{}, APIError{Status: 422, Code: "PROPOSAL_REVIEW_KIND_INVALID", Message: "proposal review kind is invalid"}
	}
	if !validReviewHash(request.AnalysisInputsHash) {
		return ProposalReview{}, APIError{Status: 422, Code: "PROPOSAL_REVIEW_INPUTS_INVALID", Message: "analysis inputs hash is invalid"}
	}
	var payload json.RawMessage
	var reviewedHash string
	var expectedRevision uint64
	var expectedStateHash string
	if request.Kind == ProposalReviewDependency {
		if request.DependencyDraft == nil {
			return ProposalReview{}, APIError{Status: 422, Code: "PROPOSAL_REVIEW_DRAFT_REQUIRED", Message: "dependency proposal requires a draft"}
		}
		preview, err := s.PreviewServiceConfiguration(projectID, applicationID, *request.DependencyDraft)
		if err != nil {
			return ProposalReview{}, err
		}
		payload, _ = json.Marshal(struct {
			Draft ServiceConfigurationDraft `json:"draft"`
		}{preview.Configuration})
		reviewedHash, expectedRevision, expectedStateHash = preview.DraftStateHash, preview.CurrentRevision, preview.CurrentStateHash
	} else {
		var err error
		payload, err = normalizeSourcePatch(request.SourcePatch)
		if err != nil {
			return ProposalReview{}, err
		}
		reviewedHash = reviewHash(payload)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	service, ok := s.services[applicationID]
	if !ok || service.ProjectID != projectID {
		return ProposalReview{}, ErrNotFound
	}
	if request.EnvironmentID == "" || service.EnvironmentID != request.EnvironmentID {
		return ProposalReview{}, ErrNotFound
	}
	current := normalizeStoredConfiguration(service.Configuration)
	if request.Kind == ProposalReviewDependency && (current.Revision != expectedRevision || current.StateHash != expectedStateHash) {
		return ProposalReview{}, APIError{Status: 409, Code: "SERVICE_CONFIGURATION_STALE", Message: "configuration changed while creating the review"}
	}
	now := s.clock()
	value := ProposalReview{ID: newID("review"), ProjectID: projectID, EnvironmentID: request.EnvironmentID, ApplicationID: applicationID, Kind: request.Kind, Status: ReviewRequired, ProposalHash: reviewHash(struct {
		Kind    ProposalReviewKind `json:"kind"`
		Payload json.RawMessage    `json:"payload"`
		Inputs  string             `json:"inputs"`
	}{request.Kind, payload, request.AnalysisInputsHash}), AnalysisInputsHash: request.AnalysisInputsHash, SourceCommit: boundedReviewText(request.SourceCommit), ApplicationRoot: boundedReviewText(request.ApplicationRoot), NormalizedPayload: payload, ReviewedPayloadHash: reviewedHash, ExpectedConfigurationRevision: expectedRevision, ExpectedConfigurationStateHash: expectedStateHash, CreatedBy: actorUserID, CreatedAt: now, ExpiresAt: now.Add(proposalReviewLifetime)}
	s.proposalReviews[value.ID] = value
	return value, nil
}

func (s *Service) GetProposalReview(projectID, reviewID string) (ProposalReview, error) {
	s.mu.Lock()
	value, ok := s.proposalReviews[reviewID]
	s.mu.Unlock()
	if !ok || value.ProjectID != projectID {
		return ProposalReview{}, ErrNotFound
	}
	if value.Kind == ProposalReviewDependency {
		configuration, err := s.GetServiceConfiguration(projectID, value.ApplicationID)
		if err != nil {
			return ProposalReview{}, err
		}
		next := proposalReviewStale(value, configuration, s.clock())
		if next != value.Status {
			s.mu.Lock()
			current := s.proposalReviews[reviewID]
			if current.Status == value.Status {
				current.Status = next
				s.proposalReviews[reviewID] = current
				value = current
			}
			s.mu.Unlock()
		}
	} else if s.clock().After(value.ExpiresAt) && value.Status == ReviewRequired {
		s.mu.Lock()
		current := s.proposalReviews[reviewID]
		current.Status = ReviewExpired
		s.proposalReviews[reviewID] = current
		value = current
		s.mu.Unlock()
	}
	return value, nil
}

func (s *Service) ListProposalReviews(projectID, applicationID string, limit int) ([]ProposalReview, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if _, err := s.GetServiceConfiguration(projectID, applicationID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	ids := make([]string, 0)
	for id, value := range s.proposalReviews {
		if value.ProjectID == projectID && value.ApplicationID == applicationID {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	out := make([]ProposalReview, 0, len(ids))
	for _, id := range ids {
		if value, err := s.GetProposalReview(projectID, id); err == nil {
			out = append(out, value)
		}
	}
	sortProposalReviews(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Service) ApproveProposalReview(projectID, reviewID, actorUserID string) (ProposalReview, error) {
	value, err := s.GetProposalReview(projectID, reviewID)
	if err != nil {
		return ProposalReview{}, err
	}
	if value.Status != ReviewRequired {
		return ProposalReview{}, APIError{Status: 409, Code: "PROPOSAL_REVIEW_NOT_ACTIONABLE", Message: "proposal review cannot be approved"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value = s.proposalReviews[reviewID]
	if value.Status != ReviewRequired {
		return ProposalReview{}, APIError{Status: 409, Code: "PROPOSAL_REVIEW_NOT_ACTIONABLE", Message: "proposal review cannot be approved"}
	}
	now := s.clock()
	value.Status, value.ApprovedBy, value.ApprovedAt = ReviewApproved, actorUserID, &now
	s.proposalReviews[reviewID] = value
	return value, nil
}

func (s *Service) RejectProposalReview(projectID, reviewID, actorUserID string) (ProposalReview, error) {
	value, err := s.GetProposalReview(projectID, reviewID)
	if err != nil {
		return ProposalReview{}, err
	}
	if value.Status != ReviewRequired && value.Status != ReviewApproved {
		return ProposalReview{}, APIError{Status: 409, Code: "PROPOSAL_REVIEW_NOT_ACTIONABLE", Message: "proposal review cannot be rejected"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value = s.proposalReviews[reviewID]
	if value.Status != ReviewRequired && value.Status != ReviewApproved {
		return ProposalReview{}, APIError{Status: 409, Code: "PROPOSAL_REVIEW_NOT_ACTIONABLE", Message: "proposal review cannot be rejected"}
	}
	now := s.clock()
	value.Status, value.RejectedBy, value.RejectedAt = ReviewRejected, actorUserID, &now
	s.proposalReviews[reviewID] = value
	return value, nil
}

func (s *Service) ApplyProposalReview(projectID, reviewID, actorUserID string) (ProposalReview, ServiceConfigurationApplyResult, error) {
	value, err := s.GetProposalReview(projectID, reviewID)
	if err != nil {
		return ProposalReview{}, ServiceConfigurationApplyResult{}, err
	}
	if value.Kind != ProposalReviewDependency {
		return ProposalReview{}, ServiceConfigurationApplyResult{}, APIError{Status: 409, Code: "SOURCE_WRITE_AUTHORITY_ABSENT", Message: "source patch reviews cannot be applied"}
	}
	if value.Status == ReviewApplied {
		return value, ServiceConfigurationApplyResult{Reused: true}, nil
	}
	if value.Status != ReviewApproved {
		return ProposalReview{}, ServiceConfigurationApplyResult{}, APIError{Status: 409, Code: "PROPOSAL_REVIEW_NOT_APPROVED", Message: "proposal review is not approved"}
	}
	draft, err := decodeReviewedDependency(value)
	if err != nil {
		return ProposalReview{}, ServiceConfigurationApplyResult{}, err
	}
	preview, err := s.PreviewServiceConfiguration(projectID, value.ApplicationID, draft)
	if err != nil {
		return ProposalReview{}, ServiceConfigurationApplyResult{}, err
	}
	if preview.DraftStateHash != value.ReviewedPayloadHash {
		return ProposalReview{}, ServiceConfigurationApplyResult{}, APIError{Status: 409, Code: "TAMPERED_REVIEW", Message: "reviewed payload hash does not match canonical draft"}
	}
	if preview.CurrentRevision != value.ExpectedConfigurationRevision || preview.CurrentStateHash != value.ExpectedConfigurationStateHash {
		s.markProposalReviewStale(reviewID)
		return ProposalReview{}, ServiceConfigurationApplyResult{}, APIError{Status: 409, Code: "SERVICE_CONFIGURATION_STALE", Message: "proposal review is stale"}
	}
	result, err := s.ApplyServiceConfiguration(projectID, value.ApplicationID, actorUserID, "proposal-review:"+value.ID, ServiceConfigurationApplyRequest{Draft: draft, ExpectedRevision: value.ExpectedConfigurationRevision, ExpectedStateHash: value.ExpectedConfigurationStateHash, ProposalReview: &ProposalReviewAudit{ProposalHash: value.ProposalHash, ReviewedPayloadHash: value.ReviewedPayloadHash, ProposerOrigin: "mcp_client"}})
	if err != nil {
		if api, ok := err.(APIError); ok && api.Code == "SERVICE_CONFIGURATION_STALE" {
			s.markProposalReviewStale(reviewID)
		}
		return ProposalReview{}, ServiceConfigurationApplyResult{}, err
	}
	s.mu.Lock()
	current := s.proposalReviews[reviewID]
	if current.Status == ReviewApproved {
		now := s.clock()
		current.Status, current.AppliedAt, current.ResultingConfigurationRevision, current.ApplyIdempotencyKey = ReviewApplied, &now, result.Configuration.Revision, "proposal-review:"+current.ID
		s.proposalReviews[reviewID] = current
	}
	s.mu.Unlock()
	return current, result, nil
}

func (s *Service) markProposalReviewStale(reviewID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.proposalReviews[reviewID]
	if ok && (value.Status == ReviewRequired || value.Status == ReviewApproved) {
		value.Status = ReviewStale
		s.proposalReviews[reviewID] = value
	}
}
