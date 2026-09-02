package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

const proposalReviewSelectSQL = `SELECT id,project_id,environment_id,application_id,kind,status,proposal_hash,analysis_inputs_hash,source_commit,application_root,normalized_payload::text,reviewed_payload_hash,expected_configuration_revision,expected_configuration_state_hash,COALESCE(created_by,''),created_at,expires_at,COALESCE(approved_by,''),approved_at,COALESCE(rejected_by,''),rejected_at,applied_at,COALESCE(resulting_configuration_revision,0),COALESCE(apply_idempotency_key,''),COALESCE(failure_code,'') FROM proposal_reviews`

func (s PostgresService) CreateProposalReview(projectID, applicationID, actorUserID string, request ProposalReviewCreateRequest) (ProposalReview, error) {
	if request.Kind == ProposalReviewSourcePatch {
		return ProposalReview{}, APIError{Status: 422, Code: "SOURCE_PATCH_LOCAL_ONLY", Message: "source patches are confirmed and applied only in the local worktree"}
	}
	if request.Kind != ProposalReviewServiceConfiguration {
		return ProposalReview{}, APIError{Status: 422, Code: "PROPOSAL_REVIEW_KIND_INVALID", Message: "proposal review kind is invalid"}
	}
	if !validReviewHash(request.AnalysisInputsHash) {
		return ProposalReview{}, APIError{Status: 422, Code: "PROPOSAL_REVIEW_INPUTS_INVALID", Message: "analysis inputs hash is invalid"}
	}
	var payload json.RawMessage
	var reviewedHash, expectedHash string
	var expectedRevision uint64
	if request.Kind == ProposalReviewServiceConfiguration {
		if request.ConfigurationDraft == nil {
			return ProposalReview{}, APIError{Status: 422, Code: "PROPOSAL_REVIEW_DRAFT_REQUIRED", Message: "service configuration proposal requires a draft"}
		}
		preview, err := s.PreviewServiceConfiguration(projectID, applicationID, *request.ConfigurationDraft)
		if err != nil {
			return ProposalReview{}, err
		}
		payload, _ = json.Marshal(struct {
			Draft ServiceConfigurationDraft `json:"draft"`
		}{preview.Configuration})
		reviewedHash, expectedHash, expectedRevision = preview.DraftStateHash, preview.CurrentStateHash, preview.CurrentRevision
	}
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ProposalReview{}, err
	}
	defer tx.Rollback()
	service, err := scanService(tx.QueryRowContext(ctx, serviceSelectSQL+` WHERE id=$1 AND project_id=$2 FOR UPDATE`, applicationID, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return ProposalReview{}, ErrNotFound
	}
	if err != nil {
		return ProposalReview{}, err
	}
	if request.EnvironmentID == "" || service.EnvironmentID != request.EnvironmentID {
		return ProposalReview{}, ErrNotFound
	}
	current := normalizeStoredConfiguration(service.Configuration)
	if request.Kind == ProposalReviewServiceConfiguration && (current.Revision != expectedRevision || current.StateHash != expectedHash) {
		return ProposalReview{}, APIError{Status: 409, Code: "SERVICE_CONFIGURATION_STALE", Message: "configuration changed while creating the review"}
	}
	now := s.clock()
	value := ProposalReview{ID: newID("review"), ProjectID: projectID, EnvironmentID: request.EnvironmentID, ApplicationID: applicationID, Kind: request.Kind, Status: ReviewRequired, ProposalHash: reviewHash(struct {
		Kind    ProposalReviewKind `json:"kind"`
		Payload json.RawMessage    `json:"payload"`
		Inputs  string             `json:"inputs"`
	}{request.Kind, payload, request.AnalysisInputsHash}), AnalysisInputsHash: request.AnalysisInputsHash, SourceCommit: boundedReviewText(request.SourceCommit), ApplicationRoot: boundedReviewText(request.ApplicationRoot), NormalizedPayload: payload, ReviewedPayloadHash: reviewedHash, ExpectedConfigurationRevision: expectedRevision, ExpectedConfigurationStateHash: expectedHash, CreatedBy: actorUserID, CreatedAt: now, ExpiresAt: now.Add(proposalReviewLifetime)}
	_, err = tx.ExecContext(ctx, `INSERT INTO proposal_reviews(id,org_id,project_id,environment_id,application_id,kind,status,proposal_hash,analysis_inputs_hash,source_commit,application_root,normalized_payload,reviewed_payload_hash,expected_configuration_revision,expected_configuration_state_hash,created_by,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb,$13,$14,$15,NULLIF($16,''),$17,$18)`, value.ID, service.OrgID, projectID, value.EnvironmentID, applicationID, value.Kind, value.Status, value.ProposalHash, value.AnalysisInputsHash, value.SourceCommit, value.ApplicationRoot, string(value.NormalizedPayload), value.ReviewedPayloadHash, value.ExpectedConfigurationRevision, value.ExpectedConfigurationStateHash, actorUserID, value.CreatedAt, value.ExpiresAt)
	if err != nil {
		return ProposalReview{}, err
	}
	return value, tx.Commit()
}

func (s PostgresService) GetProposalReview(projectID, reviewID string) (ProposalReview, error) {
	value, err := scanProposalReview(s.DB.QueryRowContext(context.Background(), proposalReviewSelectSQL+` WHERE project_id=$1 AND id=$2`, projectID, reviewID))
	if errors.Is(err, sql.ErrNoRows) {
		return ProposalReview{}, ErrNotFound
	}
	if err != nil {
		return ProposalReview{}, err
	}
	if isConfigurationReviewKind(value.Kind) {
		configuration, err := s.GetServiceConfiguration(projectID, value.ApplicationID)
		if err != nil {
			return ProposalReview{}, err
		}
		next := proposalReviewStale(value, configuration, s.clock())
		if next != value.Status {
			if _, err := s.DB.ExecContext(context.Background(), `UPDATE proposal_reviews SET status=$1 WHERE project_id=$2 AND id=$3 AND status=$4`, next, projectID, reviewID, value.Status); err != nil {
				return ProposalReview{}, err
			}
			value.Status = next
		}
	} else if value.Kind == ProposalReviewSourcePatch && s.clock().After(value.ExpiresAt) {
		if _, err := s.DB.ExecContext(context.Background(), `UPDATE proposal_reviews SET status=CASE WHEN status IN ('review_required','approved') THEN $1 ELSE status END, normalized_payload=$2::jsonb, source_commit='', application_root='' WHERE project_id=$3 AND id=$4`, ReviewExpired, `{"redacted":"source_patch_moved_to_local"}`, projectID, reviewID); err != nil {
			return ProposalReview{}, err
		}
		if value.Status == ReviewRequired || value.Status == ReviewApproved {
			value.Status = ReviewExpired
		}
		value.NormalizedPayload = json.RawMessage(`{"redacted":"source_patch_moved_to_local"}`)
		value.SourceCommit, value.ApplicationRoot = "", ""
	}
	return value, nil
}

func (s PostgresService) ListProposalReviews(projectID, applicationID string, limit int) ([]ProposalReview, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if _, err := s.GetServiceConfiguration(projectID, applicationID); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(context.Background(), proposalReviewSelectSQL+` WHERE project_id=$1 AND application_id=$2 ORDER BY created_at DESC LIMIT $3`, projectID, applicationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ProposalReview{}
	for rows.Next() {
		value, err := scanProposalReview(rows)
		if err != nil {
			return nil, err
		}
		refreshed, err := s.GetProposalReview(projectID, value.ID)
		if err != nil {
			return nil, err
		}
		values = append(values, refreshed)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortProposalReviews(values)
	return values, nil
}

func (s PostgresService) ApproveProposalReview(projectID, reviewID, actorUserID string) (ProposalReview, error) {
	value, err := s.GetProposalReview(projectID, reviewID)
	if err != nil {
		return ProposalReview{}, err
	}
	if value.Status != ReviewRequired {
		return ProposalReview{}, APIError{Status: 409, Code: "PROPOSAL_REVIEW_NOT_ACTIONABLE", Message: "proposal review cannot be approved"}
	}
	now := s.clock()
	row := s.DB.QueryRowContext(context.Background(), `UPDATE proposal_reviews SET status=$1,approved_by=$2,approved_at=$3 WHERE project_id=$4 AND id=$5 AND status=$6 RETURNING `+proposalReviewColumns(), ReviewApproved, actorUserID, now, projectID, reviewID, ReviewRequired)
	value, err = scanProposalReview(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ProposalReview{}, APIError{Status: 409, Code: "PROPOSAL_REVIEW_NOT_ACTIONABLE", Message: "proposal review cannot be approved"}
	}
	return value, err
}

func (s PostgresService) RejectProposalReview(projectID, reviewID, actorUserID string) (ProposalReview, error) {
	value, err := s.GetProposalReview(projectID, reviewID)
	if err != nil {
		return ProposalReview{}, err
	}
	if value.Status != ReviewRequired && value.Status != ReviewApproved {
		return ProposalReview{}, APIError{Status: 409, Code: "PROPOSAL_REVIEW_NOT_ACTIONABLE", Message: "proposal review cannot be rejected"}
	}
	now := s.clock()
	row := s.DB.QueryRowContext(context.Background(), `UPDATE proposal_reviews SET status=$1,rejected_by=$2,rejected_at=$3 WHERE project_id=$4 AND id=$5 AND status IN ($6,$7) RETURNING `+proposalReviewColumns(), ReviewRejected, actorUserID, now, projectID, reviewID, ReviewRequired, ReviewApproved)
	value, err = scanProposalReview(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ProposalReview{}, APIError{Status: 409, Code: "PROPOSAL_REVIEW_NOT_ACTIONABLE", Message: "proposal review cannot be rejected"}
	}
	return value, err
}

func (s PostgresService) ApplyProposalReview(projectID, reviewID, actorUserID string) (ProposalReview, ServiceConfigurationApplyResult, error) {
	value, err := s.GetProposalReview(projectID, reviewID)
	if err != nil {
		return ProposalReview{}, ServiceConfigurationApplyResult{}, err
	}
	if !isConfigurationReviewKind(value.Kind) {
		return ProposalReview{}, ServiceConfigurationApplyResult{}, APIError{Status: 409, Code: "SOURCE_WRITE_AUTHORITY_ABSENT", Message: "source patch reviews cannot be applied"}
	}
	if value.Status == ReviewApplied {
		return value, ServiceConfigurationApplyResult{Reused: true}, nil
	}
	if value.Status != ReviewApproved {
		return ProposalReview{}, ServiceConfigurationApplyResult{}, APIError{Status: 409, Code: "PROPOSAL_REVIEW_NOT_APPROVED", Message: "proposal review is not approved"}
	}
	draft, err := decodeReviewedConfiguration(value)
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
		s.markPostgresProposalReviewStale(projectID, reviewID)
		return ProposalReview{}, ServiceConfigurationApplyResult{}, APIError{Status: 409, Code: "SERVICE_CONFIGURATION_STALE", Message: "proposal review is stale"}
	}
	result, err := s.ApplyServiceConfiguration(projectID, value.ApplicationID, actorUserID, "proposal-review:"+value.ID, ServiceConfigurationApplyRequest{Draft: draft, ExpectedRevision: value.ExpectedConfigurationRevision, ExpectedStateHash: value.ExpectedConfigurationStateHash, ProposalReview: &ProposalReviewAudit{ProposalHash: value.ProposalHash, ReviewedPayloadHash: value.ReviewedPayloadHash, ProposerOrigin: "mcp_client"}})
	if err != nil {
		s.markPostgresProposalReviewStale(projectID, reviewID)
		return ProposalReview{}, ServiceConfigurationApplyResult{}, err
	}
	now := s.clock()
	_, err = s.DB.ExecContext(context.Background(), `UPDATE proposal_reviews SET status=$1,applied_at=$2,resulting_configuration_revision=$3,apply_idempotency_key=$4 WHERE project_id=$5 AND id=$6 AND status=$7`, ReviewApplied, now, result.Configuration.Revision, "proposal-review:"+value.ID, projectID, reviewID, ReviewApproved)
	if err != nil {
		return ProposalReview{}, ServiceConfigurationApplyResult{}, err
	}
	persisted, err := s.GetProposalReview(projectID, reviewID)
	return persisted, result, err
}

func (s PostgresService) markPostgresProposalReviewStale(projectID, reviewID string) {
	_, _ = s.DB.ExecContext(context.Background(), `UPDATE proposal_reviews SET status=$1 WHERE project_id=$2 AND id=$3 AND status IN ($4,$5)`, ReviewStale, projectID, reviewID, ReviewRequired, ReviewApproved)
}

func proposalReviewColumns() string {
	return `id,project_id,environment_id,application_id,kind,status,proposal_hash,analysis_inputs_hash,source_commit,application_root,normalized_payload::text,reviewed_payload_hash,expected_configuration_revision,expected_configuration_state_hash,COALESCE(created_by,''),created_at,expires_at,COALESCE(approved_by,''),approved_at,COALESCE(rejected_by,''),rejected_at,applied_at,COALESCE(resulting_configuration_revision,0),COALESCE(apply_idempotency_key,''),COALESCE(failure_code,'')`
}

type proposalReviewScanner interface{ Scan(...any) error }

func scanProposalReview(row proposalReviewScanner) (ProposalReview, error) {
	var value ProposalReview
	var kind, status, normalizedPayload string
	var approvedAt, rejectedAt, appliedAt sql.NullTime
	var revision uint64
	err := row.Scan(&value.ID, &value.ProjectID, &value.EnvironmentID, &value.ApplicationID, &kind, &status, &value.ProposalHash, &value.AnalysisInputsHash, &value.SourceCommit, &value.ApplicationRoot, &normalizedPayload, &value.ReviewedPayloadHash, &value.ExpectedConfigurationRevision, &value.ExpectedConfigurationStateHash, &value.CreatedBy, &value.CreatedAt, &value.ExpiresAt, &value.ApprovedBy, &approvedAt, &value.RejectedBy, &rejectedAt, &appliedAt, &revision, &value.ApplyIdempotencyKey, &value.FailureCode)
	if err != nil {
		return ProposalReview{}, err
	}
	value.Kind, value.Status, value.NormalizedPayload, value.ResultingConfigurationRevision = ProposalReviewKind(kind), ProposalReviewStatus(status), json.RawMessage(normalizedPayload), revision
	if approvedAt.Valid {
		value.ApprovedAt = &approvedAt.Time
	}
	if rejectedAt.Valid {
		value.RejectedAt = &rejectedAt.Time
	}
	if appliedAt.Valid {
		value.AppliedAt = &appliedAt.Time
	}
	return value, nil
}
