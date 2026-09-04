package assistant

import (
	"context"
	"encoding/json"
	"time"
)

const (
	maxPromptBytes  = 16 << 10
	maxOutputBytes  = 2 << 20
	maxMessageBytes = 512 << 10
	maxStoredTurns  = 40
	turnTimeout     = 10 * time.Minute
)

// Typed Assistant error codes
const (
	ErrAssistantMCPStartFailed           = "ASSISTANT_MCP_START_FAILED"
	ErrAssistantMCPApprovalBlocked       = "ASSISTANT_MCP_APPROVAL_BLOCKED"
	ErrAssistantMCPToolFailed            = "ASSISTANT_MCP_TOOL_FAILED"
	ErrAssistantNotGrounded              = "ASSISTANT_NOT_GROUNDED"
	ErrAssistantProviderOutputInvalid    = "ASSISTANT_PROVIDER_OUTPUT_INVALID"
	ErrAssistantProposalUnvalidated      = "ASSISTANT_PROPOSAL_UNVALIDATED"
	ErrAssistantTimeout                  = "ASSISTANT_TIMEOUT"
	ErrAssistantCanceled                 = "ASSISTANT_CANCELED"
	ErrAssistantProviderInvocationFailed = "ASSISTANT_PROVIDER_INVOCATION_FAILED"
)

type AssistantError struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	DiagnosticCode string `json:"diagnostic_code,omitempty"`
	Retryable      bool   `json:"retryable,omitempty"`
	NextAction     string `json:"next_action,omitempty"`
}

func (e *AssistantError) Error() string {
	return e.Message
}

type ProviderStatus struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Available     bool     `json:"available"`
	Authenticated bool     `json:"authenticated"`
	Version       string   `json:"version,omitempty"`
	Capabilities  []string `json:"capabilities"`
	DataBoundary  string   `json:"data_boundary"`
	Message       string   `json:"message,omitempty"`
}

type GroundingMetadata struct {
	Status              string   `json:"status"` // "verified" | "failed" | "unverified"
	SuccessfulToolCalls int      `json:"successful_tool_calls"`
	Tools               []string `json:"tools"`
}

type Turn struct {
	ID                     string                  `json:"id"`
	ConversationID         string                  `json:"conversation_id"`
	ProviderID             string                  `json:"provider_id"`
	ProjectID              string                  `json:"project_id"`
	State                  string                  `json:"state"` // "running" | "succeeded" | "failed"
	Response               string                  `json:"response,omitempty"`
	ConfigurationProposals []ConfigurationProposal `json:"configuration_proposals,omitempty"`
	SourcePatchProposals   []SourcePatchProposal   `json:"source_patch_proposals,omitempty"`
	Grounding              GroundingMetadata       `json:"grounding"`
	ErrorCode              string                  `json:"error_code,omitempty"`
	DiagnosticCode         string                  `json:"diagnostic_code,omitempty"`
	Error                  string                  `json:"error,omitempty"`
	Retryable              bool                    `json:"retryable,omitempty"`
	NextAction             string                  `json:"next_action,omitempty"`
	Progress               []ProgressEvent         `json:"progress,omitempty"`
	Prompt                 string                  `json:"-"`
	PromptRedacted         bool                    `json:"-"`
	HistoryError           string                  `json:"history_error,omitempty"`
	StartedAt              time.Time               `json:"started_at"`
	FinishedAt             time.Time               `json:"finished_at,omitempty"`
}

type Provider interface {
	ID() string
	Status(context.Context) ProviderStatus
	Run(context.Context, RunRequest) (RunResult, error)
}

type RunRequest struct {
	ProjectID  string
	Prompt     string
	ThreadID   string
	Workspace  string
	TurnID     string
	OnProgress func(ProgressEvent)
}

type RunResult struct {
	ThreadID               string
	Text                   string
	ConfigurationProposals []ConfigurationProposal
	SourcePatchProposals   []SourcePatchProposal
	Grounding              GroundingMetadata
}

type ConfigurationProposal struct {
	ApplicationID      string `json:"application_id"`
	ApplicationName    string `json:"application_name"`
	EnvironmentID      string `json:"environment_id"`
	Rationale          string `json:"rationale"`
	ExpectedRevision   uint64 `json:"expected_revision"`
	ExpectedStateHash  string `json:"expected_state_hash"`
	AnalysisInputsHash string `json:"analysis_inputs_hash"`
	DraftJSON          string `json:"draft_json"`
}

// SourcePatchProposal is an already-attested, bounded patch candidate. It is
// retained only with the local assistant turn and is never sent to Cloud.
type SourcePatchProposal struct {
	ProjectID        string          `json:"project_id"`
	EnvironmentID    string          `json:"environment_id"`
	ApplicationID    string          `json:"application_id"`
	SourceCommit     string          `json:"source_commit"`
	ApplicationRoot  string          `json:"application_root"`
	ProposalHash     string          `json:"proposal_hash"`
	ValidationStatus string          `json:"validation_status"`
	Proposal         json.RawMessage `json:"proposal"`
}
