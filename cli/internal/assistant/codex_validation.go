package assistant

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

type validatedProposalRecord struct {
	ApplicationID      string
	EnvironmentID      string
	ExpectedRevision   uint64
	ExpectedStateHash  string
	AnalysisInputsHash string
	DraftHash          string
	Status             string
}

type validatedSourcePatchRecord struct {
	ProjectID       string
	EnvironmentID   string
	ApplicationID   string
	SourceCommit    string
	ApplicationRoot string
	ProposalHash    string
	Status          string
	ProposalJSON    json.RawMessage
}

type codexParsedEvents struct {
	ThreadID                string
	ApprovalBlocked         bool
	ToolFailed              bool
	EventInvalid            bool
	ToolFailureCode         string
	ToolFailureRetryable    bool
	ToolFailureNextAction   string
	ToolFailureMessage      string
	SuccessfulOpsiToolCalls int
	SuccessfulOpsiTools     []string
	ValidatedProposals      []validatedProposalRecord
	ValidatedSourcePatches  []validatedSourcePatchRecord
}

func parseCodexEventStream(events []byte, stderrOutput string) (codexParsedEvents, error) {
	return parseCodexEventStreamWithTurnID("", events, stderrOutput)
}

func parseCodexEventStreamWithTurnID(turnID string, events []byte, stderrOutput string) (codexParsedEvents, error) {
	collector := newCodexEventCollector(turnID, nil)
	collector.FeedStderr(stderrOutput)
	collector.FeedStdout(events)
	return collector.Finish()
}

func validateProposalAttestation(proposal ConfigurationProposal, records []validatedProposalRecord) error {
	appID := strings.TrimSpace(proposal.ApplicationID)
	if appID == "" || strings.TrimSpace(proposal.EnvironmentID) == "" || proposal.ExpectedRevision == 0 || proposal.ExpectedStateHash == "" || proposal.AnalysisInputsHash == "" {
		return errors.New("proposal identity, revision and authority hashes are required")
	}

	proposalDraftHash := canonicalDraftHash(proposal.DraftJSON)
	for _, record := range records {
		if record.ApplicationID != appID || record.EnvironmentID != proposal.EnvironmentID || record.ExpectedRevision != proposal.ExpectedRevision || record.ExpectedStateHash != proposal.ExpectedStateHash || record.AnalysisInputsHash != proposal.AnalysisInputsHash || record.DraftHash != proposalDraftHash {
			continue
		}
		if record.Status != "VALID" && record.Status != "VALID_WITH_WARNINGS" {
			return fmt.Errorf("proposal validation for %s failed with status %s", appID, record.Status)
		}
		return nil
	}
	return fmt.Errorf("proposal for %s has no matching validated execution in this turn", appID)
}

func validateSourcePatchAttestation(proposal SourcePatchProposal, records []validatedSourcePatchRecord) error {
	if strings.TrimSpace(proposal.ProjectID) == "" || strings.TrimSpace(proposal.EnvironmentID) == "" || strings.TrimSpace(proposal.ApplicationID) == "" || strings.TrimSpace(proposal.SourceCommit) == "" || strings.TrimSpace(proposal.ProposalHash) == "" || len(proposal.Proposal) == 0 {
		return errors.New("source patch identity, provenance and proposal are required")
	}
	if proposal.ValidationStatus != "VALID" && proposal.ValidationStatus != "VALID_WITH_WARNINGS" {
		return errors.New("source patch validation status is not actionable")
	}
	for _, record := range records {
		if record.ProjectID == proposal.ProjectID && record.EnvironmentID == proposal.EnvironmentID && record.ApplicationID == proposal.ApplicationID && record.SourceCommit == proposal.SourceCommit && record.ApplicationRoot == proposal.ApplicationRoot && record.ProposalHash == proposal.ProposalHash && record.Status == proposal.ValidationStatus && canonicalDraftHash(record.ProposalJSON) == canonicalDraftHash(proposal.Proposal) {
			return nil
		}
	}
	return errors.New("source patch has no matching validated execution in this turn")
}

func canonicalDraftHash(raw any) string {
	if raw == nil {
		return ""
	}
	if str, ok := raw.(string); ok {
		var object any
		if json.Unmarshal([]byte(str), &object) == nil {
			return hashCanonical(object)
		}
		return hashCanonical(str)
	}
	return hashCanonical(raw)
}

func hashCanonical(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func isMCPStartFailure(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "failed to start mcp server") ||
		strings.Contains(lower, "mcp server 'opsi' failed") ||
		strings.Contains(lower, "required mcp server") ||
		strings.Contains(lower, "mcp server error")
}

func isApprovalBlocked(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "approval_blocked") || strings.Contains(lower, "approval blocked") ||
		strings.Contains(lower, "approval rejected") || strings.Contains(lower, "rejected by user") ||
		strings.Contains(lower, "not approved") || strings.Contains(lower, "approval policy")
}

func disabledFeatureArgs(features []string) []string {
	args := make([]string, 0, len(features)*2)
	for _, feature := range features {
		args = append(args, "--disable", feature)
	}
	return args
}

func readFileLimited(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("assistant response exceeded the local message limit")
	}
	return body, nil
}

func codexThreadID(events []byte) string {
	collector := newCodexEventCollector("", nil)
	collector.FeedStdout(events)
	return collector.threadID
}

func tomlString(value string) string { return strconv.Quote(value) }

func tomlArray(values []string) string {
	encoded := make([]string, 0, len(values))
	for _, value := range values {
		encoded = append(encoded, tomlString(value))
	}
	return "[" + strings.Join(encoded, ",") + "]"
}
