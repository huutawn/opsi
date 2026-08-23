package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	maxPatchFiles        = 8
	maxPatchHunks        = 32
	maxPatchBytes        = 128 * 1024
	maxPatchChangedLines = 1000
	maxPatchEvidence     = 20
	maxPatchExcerpt      = 512
)

var (
	secretLiteralPattern = regexp.MustCompile(`(?i)(-----BEGIN (?:[A-Z ]+ )?PRIVATE KEY-----|github_pat_[A-Za-z0-9_]{20,}|gh[pousr]_[A-Za-z0-9]{20,}|(?:sk|pat|token)_[A-Za-z0-9_-]{16,}|(?:password|secret|token|api[_-]?key|pat)\s*[:=]\s*["'][^"']{4,}["'])`)
)

func parseSourcePatchProposal(args map[string]any) (SourcePatchProposal, error) {
	value, ok := args["proposal"]
	if !ok {
		return SourcePatchProposal{}, &DomainError{Code: ErrCodeInvalidArgument, Message: "proposal is required"}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return SourcePatchProposal{}, &DomainError{Code: ErrCodeInvalidArgument, Message: "proposal must be a JSON object"}
	}
	var proposal SourcePatchProposal
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&proposal); err != nil {
		return SourcePatchProposal{}, &DomainError{Code: ErrCodeInvalidArgument, Message: "proposal is malformed"}
	}
	proposal.ProjectID = strings.TrimSpace(proposal.ProjectID)
	proposal.EnvironmentID = strings.TrimSpace(proposal.EnvironmentID)
	proposal.ApplicationID = strings.TrimSpace(proposal.ApplicationID)
	proposal.Provenance.BuildRecordID = strings.TrimSpace(proposal.Provenance.BuildRecordID)
	proposal.Provenance.SourceCommit = strings.TrimSpace(proposal.Provenance.SourceCommit)
	proposal.Provenance.ApplicationRoot = CleanApplicationRoot(proposal.Provenance.ApplicationRoot)
	proposal.Provenance.AnalysisInputsHash = strings.TrimSpace(proposal.Provenance.AnalysisInputsHash)
	proposal.Provenance.DependencyProposalHash = strings.TrimSpace(proposal.Provenance.DependencyProposalHash)
	proposal.Provenance.DependencyProposalAnalysisInputsHash = strings.TrimSpace(proposal.Provenance.DependencyProposalAnalysisInputsHash)
	if proposal.ProjectID == "" || proposal.EnvironmentID == "" || proposal.ApplicationID == "" || proposal.Provenance.BuildRecordID == "" || proposal.Provenance.SourceCommit == "" || proposal.Provenance.AnalysisInputsHash == "" {
		return SourcePatchProposal{}, &DomainError{Code: ErrCodeInvalidArgument, Message: "proposal identity and exact provenance fields are required"}
	}
	if (proposal.Provenance.DependencyProposalHash == "") != (proposal.Provenance.DependencyProposalAnalysisInputsHash == "") {
		return SourcePatchProposal{}, &DomainError{Code: ErrCodeInvalidArgument, Message: "dependency proposal hash and analysis inputs hash must be supplied together"}
	}
	if strings.TrimSpace(proposal.Rationale.ObservedSource) == "" || strings.TrimSpace(proposal.Rationale.OpsiFacts) == "" || strings.TrimSpace(proposal.Rationale.Inference) == "" {
		return SourcePatchProposal{}, &DomainError{Code: ErrCodeInvalidArgument, Message: "rationale requires observed_source, opsi_facts, and inference"}
	}
	if len(proposal.Files) > maxPatchFiles || len(proposal.Evidence) > maxPatchEvidence {
		return SourcePatchProposal{}, &DomainError{Code: ErrCodeLimitExceeded, Message: "proposal exceeds canonical patch bounds"}
	}
	if hasSecretLiteral(proposal.Rationale.ObservedSource) || hasSecretLiteral(proposal.Rationale.OpsiFacts) || hasSecretLiteral(proposal.Rationale.Inference) {
		return SourcePatchProposal{}, &DomainError{Code: ErrCodeSecretLiteralIntroduced, Message: "proposal contains a credential-like literal"}
	}
	seen := map[string]bool{}
	for i := range proposal.Files {
		cleaned, err := CleanRelativePath(proposal.Files[i].Path)
		if err != nil {
			return SourcePatchProposal{}, &DomainError{Code: ErrCodeSourcePathInvalid, Message: "patch path is not allowed"}
		}
		if isForbiddenPatchPath(cleaned) {
			return SourcePatchProposal{}, &DomainError{Code: ErrCodePatchTargetGenerated, Message: "patch targets generated or vendor output"}
		}
		if seen[cleaned] {
			return SourcePatchProposal{}, &DomainError{Code: ErrCodePatchMalformed, Message: "a file may appear only once in a patch proposal"}
		}
		seen[cleaned] = true
		proposal.Files[i].Path = cleaned
		proposal.Files[i].BaseBlobSHA = strings.TrimSpace(proposal.Files[i].BaseBlobSHA)
		if proposal.Files[i].BaseBlobSHA == "" || !validGitObjectID(proposal.Files[i].BaseBlobSHA) {
			return SourcePatchProposal{}, &DomainError{Code: ErrCodeInvalidArgument, Message: "each patch file requires a canonical base_blob_sha"}
		}
		proposal.Files[i].UnifiedDiff = normalizeDiff(proposal.Files[i].UnifiedDiff)
	}
	for i := range proposal.Evidence {
		evidence := &proposal.Evidence[i]
		evidence.Type = strings.TrimSpace(evidence.Type)
		evidence.File = strings.TrimSpace(evidence.File)
		evidence.Symbol = strings.TrimSpace(evidence.Symbol)
		evidence.Reason = strings.TrimSpace(evidence.Reason)
		cleaned, err := CleanRelativePath(evidence.File)
		if err != nil || isForbiddenPatchPath(cleaned) || evidence.Type == "" || evidence.Line < 1 || evidence.Reason == "" {
			return SourcePatchProposal{}, &DomainError{Code: ErrCodeInvalidArgument, Message: "each evidence item requires a safe file, type, positive line, and reason"}
		}
		if !seen[cleaned] {
			return SourcePatchProposal{}, &DomainError{Code: ErrCodeInvalidArgument, Message: "evidence must refer to a proposed file"}
		}
		if hasSecretLiteral(evidence.SafeExcerpt) || hasSecretLiteral(evidence.Reason) {
			return SourcePatchProposal{}, &DomainError{Code: ErrCodeSecretLiteralIntroduced, Message: "proposal contains a credential-like literal"}
		}
		evidence.File = cleaned
		evidence.SafeExcerpt, _ = RedactSourceSecrets(evidence.SafeExcerpt)
		if len(evidence.SafeExcerpt) > maxPatchExcerpt {
			evidence.SafeExcerpt = evidence.SafeExcerpt[:maxPatchExcerpt]
		}
	}
	sort.Slice(proposal.Files, func(i, j int) bool { return proposal.Files[i].Path < proposal.Files[j].Path })
	sort.Slice(proposal.Evidence, func(i, j int) bool {
		return proposal.Evidence[i].File+fmt.Sprintf("%09d", proposal.Evidence[i].Line)+proposal.Evidence[i].Type < proposal.Evidence[j].File+fmt.Sprintf("%09d", proposal.Evidence[j].Line)+proposal.Evidence[j].Type
	})
	return proposal, nil
}

func (s *Server) handleValidateSourcePatchProposal(ctx context.Context, _ *Server, args map[string]any) (any, error) {
	proposal, err := parseSourcePatchProposal(args)
	if err != nil {
		return invalidPatchResult(SourcePatchValidation{Action: proposalActionNone, StructuralValidation: "NOT_RUN", ProvenanceValidation: "NOT_RUN", SecurityValidation: "NOT_RUN", DependencyAlignment: "NOT_APPLICABLE"}, err), nil
	}
	result := SourcePatchValidation{Action: proposalActionNone, StructuralValidation: "NOT_RUN", ProvenanceValidation: "NOT_RUN", SecurityValidation: "NOT_RUN", DependencyAlignment: "NOT_APPLICABLE"}
	state, err := s.dependencyAnalysisState(ctx, map[string]any{"project_id": proposal.ProjectID, "environment_id": proposal.EnvironmentID, "application_id": proposal.ApplicationID})
	if err != nil {
		return nil, err
	}
	result.PatchAnalysisInputsHash = sourcePatchAnalysisInputsHash(proposal, state)
	result.SourcePatchProposalHash = sourcePatchProposalHash(result.PatchAnalysisInputsHash, proposal)
	if proposal.Provenance.BuildRecordID != state.context.Source.BuildRecordID || proposal.Provenance.SourceCommit != state.context.Source.CommitSHA || proposal.Provenance.ApplicationRoot != CleanApplicationRoot(state.context.Source.ApplicationRoot) || proposal.Provenance.AnalysisInputsHash != state.context.Authority.AnalysisInputsHash {
		return stalePatchResult(result, "exact source or relevant authority provenance no longer matches current state"), nil
	}
	if len(proposal.Files) == 0 {
		result.Status = "NO_SOURCE_CHANGE_PROPOSED"
		result.StructuralValidation, result.ProvenanceValidation, result.SecurityValidation = "PASS", "PASS", "PASS"
		result.Impact = []string{"NO_SOURCE_CHANGE_PROPOSED", "PATCH_HAS_NOT_BEEN_COMPILED_OR_EXECUTED"}
		if proposal.Impact.AlternativeConfigurationOnlySolution {
			result.Impact = append(result.Impact, "CONFIGURATION_ONLY_SOLUTION_AVAILABLE")
		}
		return result, nil
	}
	if proposal.Provenance.DependencyProposalHash != "" {
		if proposal.Provenance.DependencyProposalAnalysisInputsHash != state.context.Authority.AnalysisInputsHash {
			return stalePatchResult(result, "referenced dependency proposal is no longer current"), nil
		}
		result.DependencyAlignment = "DEPENDS_ON_UNAPPLIED_DEPENDENCY_PROPOSAL"
		result.Impact = append(result.Impact, "REQUIRES_DEPENDENCY_PROPOSAL_FIRST")
	} else if proposal.Impact.DependsOnUnappliedDependencyProposal {
		result.Status = proposalStatusInvalid
		result.Issues = []SourcePatchIssue{{Code: ErrCodeInvalidArgument, Field: "impact.depends_on_unapplied_dependency_proposal", Message: "dependency proposal provenance is required when dependency alignment is declared"}}
		return result, nil
	}
	result.ProvenanceValidation = "PASS"

	totalHunks, totalBytes, totalChanges := 0, 0, 0
	for _, file := range proposal.Files {
		parsed, parseErr := parseUnifiedDiff(file.Path, file.UnifiedDiff)
		if parseErr != nil {
			return invalidPatchResult(result, parseErr), nil
		}
		totalHunks += len(parsed.hunks)
		totalBytes += len(parsed.normalized)
		totalChanges += parsed.changedLines
		if totalHunks > maxPatchHunks || totalBytes > maxPatchBytes || totalChanges > maxPatchChangedLines {
			return invalidPatchResult(result, &DomainError{Code: ErrCodeLimitExceeded, Message: "proposal exceeds canonical patch bounds"}), nil
		}
		blob, blobErr := s.SourceService.ReadBlob(ctx, s.RepoRoot, state.context.Source.CommitSHA, state.context.Source.ApplicationRoot, file.Path)
		if blobErr != nil {
			return invalidPatchResult(result, blobErr), nil
		}
		if blob.IsBinary {
			return invalidPatchResult(result, &DomainError{Code: ErrCodeSourceBinaryUnsupported, Message: "patch targets a binary source file"}), nil
		}
		if !strings.EqualFold(blob.ObjectID, file.BaseBlobSHA) {
			return stalePatchResult(result, "base blob identity no longer matches immutable source"), nil
		}
		if _, applyErr := virtualApply(blob.Content, parsed); applyErr != nil {
			return stalePatchResult(result, "patch preimage does not match exact immutable source"), nil
		}
		if patchAddsSecret(parsed) {
			return invalidPatchResult(result, &DomainError{Code: ErrCodeSecretLiteralIntroduced, Message: "patch adds a credential-like literal"}), nil
		}
		result.Preview = append(result.Preview, SourcePatchPreview{Path: file.Path, ChangedLines: parsed.changedLines, UnifiedDiff: safePatchPreview(parsed.normalized)})
		if !hasNearbyEvidence(proposal.Evidence, file.Path, parsed.hunks) {
			result.Warnings = append(result.Warnings, SourcePatchIssue{Code: "PATCH_SCOPE_EXCEEDS_EVIDENCE", Field: "files", Message: "one or more hunks are not near supporting evidence"})
		}
	}
	result.StructuralValidation, result.SecurityValidation = "PASS", "PASS"
	result.Status = proposalStatusValid
	if len(result.Warnings) > 0 {
		result.Status = proposalStatusValidWithWarnings
	}
	result.Impact = append(result.Impact, "SOURCE_CHANGE", "RUNTIME_BEHAVIOR_CHANGE", "NEW_BUILD_RECORD_REQUIRED_IF_APPLIED", "PATCH_HAS_NOT_BEEN_COMPILED_OR_EXECUTED")
	if proposal.Impact.AlternativeConfigurationOnlySolution {
		result.Impact = append(result.Impact, "CONFIGURATION_ONLY_SOLUTION_AVAILABLE")
	}
	return result, nil
}

func sourcePatchAnalysisInputsHash(proposal SourcePatchProposal, state dependencyAnalysisState) string {
	blobs := make([]struct{ Path, BaseBlobSHA string }, 0, len(proposal.Files))
	for _, file := range proposal.Files {
		blobs = append(blobs, struct{ Path, BaseBlobSHA string }{file.Path, file.BaseBlobSHA})
	}
	return hashCanonical(struct {
		SourceCommit, ApplicationRoot, AuthorityHash, DependencyProposalHash, DependencyInputsHash string
		Blobs                                                                                      []struct{ Path, BaseBlobSHA string }
	}{state.context.Source.CommitSHA, CleanApplicationRoot(state.context.Source.ApplicationRoot), state.context.Authority.AnalysisInputsHash, proposal.Provenance.DependencyProposalHash, proposal.Provenance.DependencyProposalAnalysisInputsHash, blobs})
}

func sourcePatchProposalHash(analysisHash string, proposal SourcePatchProposal) string {
	return hashCanonical(struct {
		AnalysisHash string                `json:"analysis_hash"`
		Files        []SourcePatchFile     `json:"files"`
		Evidence     []SourcePatchEvidence `json:"evidence"`
	}{analysisHash, proposal.Files, proposal.Evidence})
}

func stalePatchResult(result SourcePatchValidation, message string) SourcePatchValidation {
	result.Status, result.ProvenanceValidation = proposalStatusStale, "STALE"
	result.Issues = []SourcePatchIssue{{Code: ErrCodePatchPreimageMismatch, Message: message}}
	return result
}

func invalidPatchResult(result SourcePatchValidation, err error) SourcePatchValidation {
	result.Status = proposalStatusInvalid
	result.StructuralValidation = "FAIL"
	result.SecurityValidation = "PASS"
	var domainErr *DomainError
	if errorsAsDomain(err, &domainErr) {
		if domainErr.Code == ErrCodeSecretLiteralIntroduced {
			result.SecurityValidation = "FAIL"
		}
		result.Issues = []SourcePatchIssue{{Code: domainErr.Code, Message: domainErr.Message}}
	} else {
		result.Issues = []SourcePatchIssue{{Code: ErrCodePatchMalformed, Message: "patch is malformed"}}
	}
	return result
}

func errorsAsDomain(err error, target **DomainError) bool {
	if value, ok := err.(*DomainError); ok {
		*target = value
		return true
	}
	return false
}

func hasSecretLiteral(value string) bool {
	if secretLiteralPattern.MatchString(value) {
		return true
	}
	_, redacted := RedactSourceSecrets(value)
	return redacted
}
