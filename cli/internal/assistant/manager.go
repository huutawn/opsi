// Package assistant owns the local AI-agent bridge. Project facts remain owned
// by the read-only Opsi MCP server; providers own their conversation history.
package assistant

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/mcp"
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
	ErrAssistantMCPStartFailed        = "ASSISTANT_MCP_START_FAILED"
	ErrAssistantMCPApprovalBlocked    = "ASSISTANT_MCP_APPROVAL_BLOCKED"
	ErrAssistantMCPToolFailed         = "ASSISTANT_MCP_TOOL_FAILED"
	ErrAssistantNotGrounded           = "ASSISTANT_NOT_GROUNDED"
	ErrAssistantProviderOutputInvalid = "ASSISTANT_PROVIDER_OUTPUT_INVALID"
	ErrAssistantProposalUnvalidated   = "ASSISTANT_PROPOSAL_UNVALIDATED"
	ErrAssistantTimeout               = "ASSISTANT_TIMEOUT"
	ErrAssistantCanceled              = "ASSISTANT_CANCELED"
)

type AssistantError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *AssistantError) Error() string {
	return e.Message
}

var disabledCodexFeatures = []string{
	"shell_tool",
	"unified_exec",
	"browser_use",
	"browser_use_external",
	"in_app_browser",
	"standalone_web_search",
	"network_proxy",
	"computer_use",
	"apps",
	"enable_mcp_apps",
	"plugins",
	"recommended_plugins",
	"remote_plugin",
	"skill_search",
	"image_generation",
	"view_image",
	"tool_suggest",
	"multi_agent",
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
	Error                  string                  `json:"error,omitempty"`
	StartedAt              time.Time               `json:"started_at"`
	FinishedAt             time.Time               `json:"finished_at,omitempty"`
}

type Provider interface {
	ID() string
	Status(context.Context) ProviderStatus
	Run(context.Context, RunRequest) (RunResult, error)
}

type RunRequest struct {
	ProjectID string
	Prompt    string
	ThreadID  string
	Workspace string
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

type Manager struct {
	mu         sync.RWMutex
	providers  map[string]Provider
	turns      map[string]Turn
	threads    map[string]string
	workspaces map[string]string // conversationKey -> workspace dir (mode 0700)
	busy       map[string]bool
	turnOrder  []string
	nextID     uint64
	defaultID  string
	repoRoot   string
	patchMu    sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
}

// SetRepositoryRoot defines the only local worktree eligible for explicitly
// confirmed source patch application.
func (m *Manager) SetRepositoryRoot(root string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.repoRoot = strings.TrimSpace(root)
}

func NewManager(providers ...Provider) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		providers:  map[string]Provider{},
		turns:      map[string]Turn{},
		threads:    map[string]string{},
		workspaces: map[string]string{},
		busy:       map[string]bool{},
		ctx:        ctx,
		cancel:     cancel,
	}
	for _, provider := range providers {
		if provider == nil || strings.TrimSpace(provider.ID()) == "" {
			continue
		}
		m.providers[provider.ID()] = provider
		if m.defaultID == "" {
			m.defaultID = provider.ID()
		}
	}
	return m
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
	}
	for _, ws := range m.workspaces {
		_ = os.RemoveAll(ws)
	}
	m.workspaces = map[string]string{}
	return nil
}

func (m *Manager) ProviderStatuses(ctx context.Context) []ProviderStatus {
	m.mu.RLock()
	providers := make([]Provider, 0, len(m.providers))
	for _, provider := range m.providers {
		providers = append(providers, provider)
	}
	m.mu.RUnlock()
	statuses := make([]ProviderStatus, 0, len(providers))
	for _, provider := range providers {
		statuses = append(statuses, provider.Status(ctx))
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].ID < statuses[j].ID })
	return statuses
}

func (m *Manager) StartTurn(projectID, providerID, conversationID, prompt string) (Turn, error) {
	projectID, providerID, conversationID, prompt = strings.TrimSpace(projectID), strings.TrimSpace(providerID), strings.TrimSpace(conversationID), strings.TrimSpace(prompt)
	if projectID == "" || prompt == "" {
		return Turn{}, errors.New("project_id and prompt are required")
	}
	if len(prompt) > maxPromptBytes {
		return Turn{}, fmt.Errorf("prompt exceeds %d bytes", maxPromptBytes)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if providerID == "" {
		providerID = m.defaultID
	}
	provider := m.providers[providerID]
	if provider == nil {
		return Turn{}, errors.New("AI provider is not configured")
	}
	if conversationID == "" {
		m.nextID++
		conversationID = fmt.Sprintf("conversation-%d", m.nextID)
	}
	conversationKey := projectID + "\x00" + providerID + "\x00" + conversationID
	if m.busy[conversationKey] {
		return Turn{}, errors.New("conversation already has a running turn")
	}

	ws, ok := m.workspaces[conversationKey]
	if !ok || ws == "" {
		newWS, err := os.MkdirTemp("", "opsi-assistant-conv-")
		if err != nil {
			return Turn{}, fmt.Errorf("create conversation workspace: %w", err)
		}
		_ = os.Chmod(newWS, 0700)
		ws = newWS
		m.workspaces[conversationKey] = ws
	}

	m.nextID++
	turn := Turn{
		ID:             fmt.Sprintf("turn-%d", m.nextID),
		ConversationID: conversationID,
		ProviderID:     providerID,
		ProjectID:      projectID,
		State:          "running",
		Grounding:      GroundingMetadata{Status: "unverified", Tools: []string{}},
		StartedAt:      time.Now().UTC(),
	}
	m.turns[turn.ID] = turn
	m.turnOrder = append(m.turnOrder, turn.ID)
	m.busy[conversationKey] = true
	m.pruneTurnsLocked()
	threadID := m.threads[conversationKey]
	go m.runTurn(provider, conversationKey, threadID, ws, prompt, turn)
	return turn, nil
}

func (m *Manager) Turn(projectID, turnID string) (Turn, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	turn, ok := m.turns[turnID]
	return turn, ok && turn.ProjectID == projectID
}

func (m *Manager) runTurn(provider Provider, conversationKey, threadID, workspace, prompt string, turn Turn) {
	parentCtx := m.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(parentCtx, turnTimeout)
	defer cancel()

	result, err := provider.Run(ctx, RunRequest{
		ProjectID: turn.ProjectID,
		Prompt:    prompt,
		ThreadID:  threadID,
		Workspace: workspace,
	})

	m.mu.Lock()
	defer m.mu.Unlock()
	turn.FinishedAt = time.Now().UTC()
	if err != nil {
		turn.State = "failed"
		turn.Grounding.Status = "failed"
		var aErr *AssistantError
		if errors.As(err, &aErr) {
			turn.ErrorCode = aErr.Code
			turn.Error = aErr.Message
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			turn.ErrorCode = ErrAssistantTimeout
			turn.Error = "assistant turn timed out after 10 minutes"
		} else if errors.Is(ctx.Err(), context.Canceled) {
			turn.ErrorCode = ErrAssistantCanceled
			turn.Error = "assistant turn was canceled"
		} else {
			turn.ErrorCode = "ASSISTANT_TURN_FAILED"
			turn.Error = err.Error()
		}
	} else {
		turn.State = "succeeded"
		turn.Response = result.Text
		turn.ConfigurationProposals = result.ConfigurationProposals
		turn.SourcePatchProposals = result.SourcePatchProposals
		turn.Grounding = result.Grounding
		if result.ThreadID != "" {
			m.threads[conversationKey] = result.ThreadID
		}
	}
	m.turns[turn.ID] = turn
	delete(m.busy, conversationKey)
	m.pruneTurnsLocked()
}

func (m *Manager) pruneTurnsLocked() {
	for len(m.turnOrder) > maxStoredTurns {
		removeIndex := -1
		for index, turnID := range m.turnOrder {
			if turn, ok := m.turns[turnID]; ok && turn.State != "running" {
				removeIndex = index
				break
			}
		}
		if removeIndex < 0 {
			return
		}
		turnID := m.turnOrder[removeIndex]
		turn := m.turns[turnID]
		delete(m.turns, turnID)
		m.turnOrder = append(m.turnOrder[:removeIndex], m.turnOrder[removeIndex+1:]...)
		conversationKey := turn.ProjectID + "\x00" + turn.ProviderID + "\x00" + turn.ConversationID
		if !m.hasConversationLocked(conversationKey) {
			delete(m.threads, conversationKey)
			if ws, ok := m.workspaces[conversationKey]; ok {
				_ = os.RemoveAll(ws)
				delete(m.workspaces, conversationKey)
			}
		}
	}
}

func (m *Manager) hasConversationLocked(conversationKey string) bool {
	for _, turn := range m.turns {
		if turn.ProjectID+"\x00"+turn.ProviderID+"\x00"+turn.ConversationID == conversationKey {
			return true
		}
	}
	return false
}

type CodexOptions struct {
	Binary     string
	OpsiBinary string
	ConfigPath string
	RepoRoot   string
}

type CodexProvider struct{ options CodexOptions }

func NewCodexProvider(options CodexOptions) *CodexProvider {
	if strings.TrimSpace(options.Binary) == "" {
		options.Binary = "codex"
	}
	return &CodexProvider{options: options}
}

func (p *CodexProvider) ID() string { return "codex" }

func (p *CodexProvider) mcpConfig(projectID string) string {
	mcpArgs := []string{}
	if strings.TrimSpace(p.options.ConfigPath) != "" {
		mcpArgs = append(mcpArgs, "--config", p.options.ConfigPath)
	}
	mcpArgs = append(mcpArgs, "mcp", "--project-id", projectID)
	enabledTools := mcp.AllToolNames()
	envVars := []string{"DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR", "HOME", "PATH", "USER", "OPSI_CONFIG"}
	return fmt.Sprintf(
		"mcp_servers.opsi={command=%s,args=%s,cwd=%s,required=true,enabled_tools=%s,default_tools_approval_mode=\"writes\",env_vars=%s,startup_timeout_sec=10,tool_timeout_sec=45}",
		tomlString(p.options.OpsiBinary),
		tomlArray(mcpArgs),
		tomlString(p.options.RepoRoot),
		tomlArray(enabledTools),
		tomlArray(envVars),
	)
}

func (p *CodexProvider) Status(ctx context.Context) ProviderStatus {
	status := ProviderStatus{
		ID:           p.ID(),
		Name:         "OpenAI Codex",
		Capabilities: []string{"project_review", "configuration_advice", "proposal_drafting"},
		DataBoundary: "Project data is available to the agent only through the read-only Opsi MCP server.",
	}
	versionCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(versionCtx, p.options.Binary, "--version").CombinedOutput()
	if err != nil {
		status.Message = "Codex CLI is not installed or not executable."
		return status
	}
	status.Available = true
	status.Version = strings.TrimSpace(string(output))
	loginCtx, loginCancel := context.WithTimeout(ctx, 3*time.Second)
	defer loginCancel()
	loginOutput, err := exec.CommandContext(loginCtx, p.options.Binary, "login", "status").CombinedOutput()
	status.Authenticated = err == nil && strings.Contains(strings.ToLower(string(loginOutput)), "logged in")
	if !status.Authenticated {
		status.Message = "Run `codex login` on this machine before starting a chat."
		return status
	}

	// Capability probe: test approval options and server enablement
	probeConfig := p.mcpConfig("probe-project")
	probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Second)
	defer probeCancel()
	probeOutput, probeErr := exec.CommandContext(probeCtx, p.options.Binary, "mcp", "list", "--json", "-c", probeConfig).CombinedOutput()
	if probeErr != nil {
		status.Available = false
		status.Message = "Codex CLI MCP options are incompatible with required approval/tool configuration."
		return status
	}
	var probeServers []struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(probeOutput, &probeServers); err != nil {
		status.Available = false
		status.Message = "Codex CLI failed to report MCP server capabilities."
		return status
	}
	opsiFoundAndEnabled := false
	for _, s := range probeServers {
		if s.Name == "opsi" && s.Enabled {
			opsiFoundAndEnabled = true
			break
		}
	}
	if !opsiFoundAndEnabled {
		status.Available = false
		status.Message = "Opsi MCP server is not enabled in Codex configuration probe."
		return status
	}
	return status
}

func (p *CodexProvider) Run(ctx context.Context, request RunRequest) (RunResult, error) {
	if strings.TrimSpace(p.options.OpsiBinary) == "" || strings.TrimSpace(p.options.RepoRoot) == "" {
		return RunResult{}, errors.New("Opsi MCP executable or repository root is unavailable")
	}

	workspace := request.Workspace
	cleanupWS := false
	if strings.TrimSpace(workspace) == "" {
		ws, err := os.MkdirTemp("", "opsi-assistant-")
		if err != nil {
			return RunResult{}, fmt.Errorf("create isolated assistant workspace: %w", err)
		}
		workspace = ws
		cleanupWS = true
	}
	if cleanupWS {
		defer os.RemoveAll(workspace)
	}
	_ = os.Chmod(workspace, 0700)

	lastMessage := filepath.Join(workspace, "last-message.txt")
	_ = os.Remove(lastMessage)

	outputSchema := filepath.Join(workspace, "assistant-output-schema.json")
	if err := os.WriteFile(outputSchema, []byte(codexOutputSchema), 0600); err != nil {
		return RunResult{}, fmt.Errorf("create assistant response schema: %w", err)
	}

	mcpConfig := p.mcpConfig(request.ProjectID)
	instructions := "You are the Opsi AI Assistant. Use only the opsi MCP tools for project facts. Do not use shell, filesystem, web, connectors, or any non-Opsi tool. Never claim a change is applied. Return a concise review. If configuration changes are useful, call validate_service_configuration_proposal and include only its matching VALID proposal. If a source change is useful, call validate_source_patch_proposal and include only its matching VALID patch candidate. Configuration requires Cloud review; source patches require explicit local-worktree confirmation and are never built, tested, committed, or pushed by Opsi.\n\nUser request:\n" + request.Prompt
	args := []string{"exec"}
	disabledNativeTools := disabledFeatureArgs(disabledCodexFeatures)
	if request.ThreadID != "" {
		args = append(args, "resume")
		args = append(args, disabledNativeTools...)
		args = append(args, "--all", "--json", "--ignore-user-config", "-c", mcpConfig, "-C", workspace, "--output-schema", outputSchema, "-o", lastMessage, request.ThreadID, "-")
	} else {
		args = append(args, disabledNativeTools...)
		args = append(args, "--json", "--ignore-user-config", "--skip-git-repo-check", "--sandbox", "read-only", "-C", workspace, "-c", mcpConfig, "--output-schema", outputSchema, "-o", lastMessage, "-")
	}

	command := exec.CommandContext(ctx, p.options.Binary, args...)
	command.Dir = workspace
	command.Stdin = strings.NewReader(instructions)
	var stdout limitedBuffer
	stdout.limit = maxOutputBytes
	var stderr limitedBuffer
	stderr.limit = 64 << 10
	command.Stdout, command.Stderr = &stdout, &stderr

	runErr := command.Run()
	stderrStr := strings.TrimSpace(stderr.String())

	// Check if MCP server failed to start
	if isMCPStartFailure(stderrStr) {
		return RunResult{}, &AssistantError{Code: ErrAssistantMCPStartFailed, Message: "failed to start required Opsi MCP server"}
	}

	if runErr != nil {
		message := stderrStr
		if message == "" {
			message = runErr.Error()
		}
		if isApprovalBlocked(message) {
			return RunResult{}, &AssistantError{Code: ErrAssistantMCPApprovalBlocked, Message: "MCP tool call was blocked by approval policy"}
		}
		return RunResult{}, fmt.Errorf("Codex turn failed: %s", message)
	}
	if stdout.overflow {
		return RunResult{}, errors.New("Codex event stream exceeded the local response limit")
	}

	parsedEvents, parseErr := parseCodexEventStream(stdout.Bytes(), stderrStr)
	if parseErr != nil {
		return RunResult{}, parseErr
	}

	if parsedEvents.ApprovalBlocked {
		return RunResult{}, &AssistantError{Code: ErrAssistantMCPApprovalBlocked, Message: "MCP tool call was blocked by approval policy"}
	}
	if parsedEvents.ToolFailed {
		msg := parsedEvents.ToolFailureMessage
		if msg == "" {
			msg = "one or more Opsi MCP tool calls failed"
		}
		return RunResult{}, &AssistantError{Code: ErrAssistantMCPToolFailed, Message: msg}
	}
	if parsedEvents.SuccessfulOpsiToolCalls == 0 {
		return RunResult{}, &AssistantError{Code: ErrAssistantNotGrounded, Message: "assistant turn did not execute any Opsi MCP tool"}
	}

	body, readErr := readFileLimited(lastMessage, maxMessageBytes)
	if readErr != nil {
		return RunResult{}, &AssistantError{Code: ErrAssistantProviderOutputInvalid, Message: fmt.Sprintf("read Codex assistant message: %v", readErr)}
	}

	var response struct {
		Message                string                  `json:"message"`
		ConfigurationProposals []ConfigurationProposal `json:"configuration_proposals"`
		SourcePatchProposals   []SourcePatchProposal   `json:"source_patch_proposals"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return RunResult{}, &AssistantError{Code: ErrAssistantProviderOutputInvalid, Message: "assistant response is not valid JSON against output schema"}
	}
	response.Message = strings.TrimSpace(response.Message)
	if response.Message == "" {
		return RunResult{}, &AssistantError{Code: ErrAssistantProviderOutputInvalid, Message: "assistant returned an empty message"}
	}

	// Validate that all proposals were validated during this turn
	for _, proposal := range response.ConfigurationProposals {
		if err := validateProposalAttestation(proposal, parsedEvents.ValidatedProposals); err != nil {
			return RunResult{}, &AssistantError{Code: ErrAssistantProposalUnvalidated, Message: err.Error()}
		}
	}
	for _, proposal := range response.SourcePatchProposals {
		if err := validateSourcePatchAttestation(proposal, parsedEvents.ValidatedSourcePatches); err != nil {
			return RunResult{}, &AssistantError{Code: ErrAssistantProposalUnvalidated, Message: err.Error()}
		}
	}

	grounding := GroundingMetadata{
		Status:              "verified",
		SuccessfulToolCalls: parsedEvents.SuccessfulOpsiToolCalls,
		Tools:               parsedEvents.SuccessfulOpsiTools,
	}

	return RunResult{
		ThreadID:               parsedEvents.ThreadID,
		Text:                   response.Message,
		ConfigurationProposals: response.ConfigurationProposals,
		SourcePatchProposals:   response.SourcePatchProposals,
		Grounding:              grounding,
	}, nil
}

type validatedProposalRecord struct {
	ApplicationID      string
	EnvironmentID      string
	ExpectedRevision   uint64
	ExpectedStateHash  string
	AnalysisInputsHash string
	DraftHash          string
	Status             string // VALID or VALID_WITH_WARNINGS
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
	ToolFailureMessage      string
	SuccessfulOpsiToolCalls int
	SuccessfulOpsiTools     []string
	ValidatedProposals      []validatedProposalRecord
	ValidatedSourcePatches  []validatedSourcePatchRecord
}

func parseCodexEventStream(events []byte, stderrOutput string) (codexParsedEvents, error) {
	var result codexParsedEvents
	toolSet := map[string]bool{}
	terminalCalls := map[string]map[string]any{}

	if isMCPStartFailure(stderrOutput) {
		return result, &AssistantError{Code: ErrAssistantMCPStartFailed, Message: "failed to start required Opsi MCP server"}
	}

	scanner := bufio.NewScanner(bytes.NewReader(events))
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var generic map[string]any
		if err := json.Unmarshal(line, &generic); err != nil {
			continue
		}

		eventType, _ := generic["type"].(string)
		if eventType == "thread.started" {
			if tid, ok := generic["thread_id"].(string); ok && tid != "" {
				result.ThreadID = strings.TrimSpace(tid)
			}
		}

		if eventType == "error" {
			if errMsg, ok := generic["message"].(string); ok {
				if isApprovalBlocked(errMsg) {
					result.ApprovalBlocked = true
				}
			}
		}

		// Look for tool calls in item or top-level
		item, _ := generic["item"].(map[string]any)
		if item == nil {
			item = generic
		}

		itemType, _ := item["type"].(string)
		if itemType == "mcp_tool_call" || itemType == "tool_call" || itemType == "mcp_call" || itemType == "mcp_tool_result" {
			server, _ := item["server"].(string)
			toolName, _ := item["tool"].(string)
			if toolName == "" {
				toolName, _ = item["name"].(string)
			}
			// If toolName has prefix opsi__ or opsi., clean it
			if strings.HasPrefix(toolName, "opsi__") {
				server = "opsi"
				toolName = strings.TrimPrefix(toolName, "opsi__")
			} else if strings.HasPrefix(toolName, "opsi.") {
				server = "opsi"
				toolName = strings.TrimPrefix(toolName, "opsi.")
			}

			status, _ := item["status"].(string)
			isError := mcpItemIsError(item)
			errVal := item["error"]
			resultIsError, _ := mcpResultFailure(item)

			if server == "opsi" || server == "" {
				if status == "approval_blocked" || isApprovalBlocked(fmt.Sprint(errVal)) {
					result.ApprovalBlocked = true
					continue
				}
				callID := eventCallID(item)
				if callID == "" {
					continue // no stable identity means this event cannot ground a turn
				}
				if status == "completed" || status == "success" || status == "failed" || status == "error" || isError || resultIsError {
					item["tool"] = toolName
					terminalCalls[callID] = item
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	for _, item := range terminalCalls {
		toolName, _ := item["tool"].(string)
		status, _ := item["status"].(string)
		isError := mcpItemIsError(item)
		errVal := item["error"]
		resultIsError, resultFailure := mcpResultFailure(item)
		if status == "failed" || status == "error" || isError || resultIsError || (errVal != nil && fmt.Sprint(errVal) != "" && fmt.Sprint(errVal) != "<nil>") {
			result.ToolFailed = true
			failureMessage := mcpFailureMessage(errVal, resultFailure)
			result.ToolFailureMessage = fmt.Sprintf("Opsi MCP tool %q failed: %s", toolName, failureMessage)
			continue
		}
		if status != "completed" && status != "success" {
			continue
		}
		result.SuccessfulOpsiToolCalls++
		if toolName != "" && !toolSet[toolName] {
			toolSet[toolName] = true
			result.SuccessfulOpsiTools = append(result.SuccessfulOpsiTools, toolName)
		}
		if toolName == "validate_service_configuration_proposal" {
			if record := extractValidationRecord(item); record != nil {
				result.ValidatedProposals = append(result.ValidatedProposals, *record)
			}
		}
		if toolName == "validate_source_patch_proposal" {
			if record := extractSourcePatchValidationRecord(item); record != nil {
				result.ValidatedSourcePatches = append(result.ValidatedSourcePatches, *record)
			}
		}
	}

	sort.Strings(result.SuccessfulOpsiTools)
	return result, nil
}

func mcpItemIsError(item map[string]any) bool {
	if isError, ok := item["is_error"].(bool); ok && isError {
		return true
	}
	isError, _ := item["isError"].(bool)
	return isError
}

func mcpResultFailure(item map[string]any) (bool, string) {
	result, ok := item["result"].(map[string]any)
	if !ok {
		return false, ""
	}
	isError := mcpItemIsError(result)
	if !isError {
		return false, ""
	}
	if detail := mcpContentText(result["content"]); detail != "" {
		return true, detail
	}
	if detail := mcpContentText(result["structured_content"]); detail != "" {
		return true, detail
	}
	return true, ""
}

func mcpContentText(value any) string {
	content, ok := value.([]any)
	if !ok {
		return ""
	}
	for _, entry := range content {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		text, _ := item["text"].(string)
		if strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func mcpFailureMessage(errorValue any, resultFailure string) string {
	if errorValue != nil {
		if message := strings.TrimSpace(fmt.Sprint(errorValue)); message != "" && message != "<nil>" {
			return message
		}
	}
	if strings.TrimSpace(resultFailure) != "" {
		var payload struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(resultFailure), &payload) == nil && strings.TrimSpace(payload.Code) != "" {
			if strings.TrimSpace(payload.Message) != "" {
				return payload.Code + ": " + payload.Message
			}
			return payload.Code
		}
		return resultFailure
	}
	return "MCP server reported a failed tool call without diagnostic details"
}

func eventCallID(item map[string]any) string {
	for _, key := range []string{"call_id", "id", "item_id"} {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func extractValidationRecord(item map[string]any) *validatedProposalRecord {
	// Extract args and result
	var args map[string]any
	if a, ok := item["arguments"].(map[string]any); ok {
		args = a
	} else if a, ok := item["input"].(map[string]any); ok {
		args = a
	} else if a, ok := item["params"].(map[string]any); ok {
		args = a
	}

	var proposalObj map[string]any
	if args != nil {
		if p, ok := args["proposal"].(map[string]any); ok {
			proposalObj = p
		} else {
			proposalObj = args
		}
	}

	if proposalObj == nil {
		return nil
	}

	appID, _ := proposalObj["application_id"].(string)
	envID, _ := proposalObj["environment_id"].(string)
	expRevNum, _ := proposalObj["expected_revision"].(float64)
	expStateHash, _ := proposalObj["expected_state_hash"].(string)
	analysisHash, _ := proposalObj["analysis_inputs_hash"].(string)
	draftRaw := proposalObj["draft"]

	draftHash := canonicalDraftHash(draftRaw)

	// Extract result status
	status := ""
	if res, ok := item["result"].(map[string]any); ok {
		if st, ok := res["status"].(string); ok && st != "" {
			status = st
		}
	} else if res, ok := item["output"].(map[string]any); ok {
		if st, ok := res["status"].(string); ok && st != "" {
			status = st
		}
	} else if content, ok := item["content"].([]any); ok && len(content) > 0 {
		if c0, ok := content[0].(map[string]any); ok {
			if text, ok := c0["text"].(string); ok {
				var parsedRes struct {
					Status string `json:"status"`
				}
				if json.Unmarshal([]byte(text), &parsedRes) == nil && parsedRes.Status != "" {
					status = parsedRes.Status
				}
			}
		}
	}

	if strings.TrimSpace(appID) == "" || strings.TrimSpace(envID) == "" || expRevNum < 1 || strings.TrimSpace(expStateHash) == "" || strings.TrimSpace(analysisHash) == "" || draftHash == "" || status == "" {
		return nil
	}
	return &validatedProposalRecord{
		ApplicationID:      strings.TrimSpace(appID),
		EnvironmentID:      strings.TrimSpace(envID),
		ExpectedRevision:   uint64(expRevNum),
		ExpectedStateHash:  strings.TrimSpace(expStateHash),
		AnalysisInputsHash: strings.TrimSpace(analysisHash),
		DraftHash:          draftHash,
		Status:             status,
	}
}

func validateProposalAttestation(proposal ConfigurationProposal, records []validatedProposalRecord) error {
	appID := strings.TrimSpace(proposal.ApplicationID)
	if appID == "" || strings.TrimSpace(proposal.EnvironmentID) == "" || proposal.ExpectedRevision == 0 || proposal.ExpectedStateHash == "" || proposal.AnalysisInputsHash == "" {
		return errors.New("proposal identity, revision and authority hashes are required")
	}

	proposalDraftHash := canonicalDraftHash(proposal.DraftJSON)

	matched := false
	for _, rec := range records {
		if rec.ApplicationID != appID || rec.EnvironmentID != proposal.EnvironmentID || rec.ExpectedRevision != proposal.ExpectedRevision || rec.ExpectedStateHash != proposal.ExpectedStateHash || rec.AnalysisInputsHash != proposal.AnalysisInputsHash || rec.DraftHash != proposalDraftHash {
			continue
		}
		if rec.Status != "VALID" && rec.Status != "VALID_WITH_WARNINGS" {
			return fmt.Errorf("proposal validation for %s failed with status %s", appID, rec.Status)
		}
		matched = true
		break
	}

	if !matched {
		return fmt.Errorf("proposal for %s has no matching validated execution in this turn", appID)
	}
	return nil
}

func extractSourcePatchValidationRecord(item map[string]any) *validatedSourcePatchRecord {
	args := toolArguments(item)
	proposal, _ := args["proposal"].(map[string]any)
	if proposal == nil {
		return nil
	}
	provenance, _ := proposal["provenance"].(map[string]any)
	result := toolResult(item)
	status, _ := result["status"].(string)
	proposalHash, _ := result["source_patch_proposal_hash"].(string)
	projectID, _ := proposal["project_id"].(string)
	environmentID, _ := proposal["environment_id"].(string)
	applicationID, _ := proposal["application_id"].(string)
	sourceCommit, _ := provenance["source_commit"].(string)
	applicationRoot, _ := provenance["application_root"].(string)
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(environmentID) == "" || strings.TrimSpace(applicationID) == "" || strings.TrimSpace(sourceCommit) == "" || strings.TrimSpace(proposalHash) == "" || status == "" {
		return nil
	}
	raw, err := json.Marshal(proposal)
	if err != nil {
		return nil
	}
	return &validatedSourcePatchRecord{ProjectID: strings.TrimSpace(projectID), EnvironmentID: strings.TrimSpace(environmentID), ApplicationID: strings.TrimSpace(applicationID), SourceCommit: strings.TrimSpace(sourceCommit), ApplicationRoot: strings.TrimSpace(applicationRoot), ProposalHash: strings.TrimSpace(proposalHash), Status: status, ProposalJSON: raw}
}

func validateSourcePatchAttestation(proposal SourcePatchProposal, records []validatedSourcePatchRecord) error {
	if strings.TrimSpace(proposal.ProjectID) == "" || strings.TrimSpace(proposal.EnvironmentID) == "" || strings.TrimSpace(proposal.ApplicationID) == "" || strings.TrimSpace(proposal.SourceCommit) == "" || strings.TrimSpace(proposal.ProposalHash) == "" || len(proposal.Proposal) == 0 {
		return errors.New("source patch identity, provenance and proposal are required")
	}
	if proposal.ValidationStatus != "VALID" && proposal.ValidationStatus != "VALID_WITH_WARNINGS" {
		return errors.New("source patch validation status is not actionable")
	}
	for _, record := range records {
		if record.ProjectID != proposal.ProjectID || record.EnvironmentID != proposal.EnvironmentID || record.ApplicationID != proposal.ApplicationID || record.SourceCommit != proposal.SourceCommit || record.ApplicationRoot != proposal.ApplicationRoot || record.ProposalHash != proposal.ProposalHash || record.Status != proposal.ValidationStatus || canonicalDraftHash(record.ProposalJSON) != canonicalDraftHash(proposal.Proposal) {
			continue
		}
		if record.Status == "VALID" || record.Status == "VALID_WITH_WARNINGS" {
			return nil
		}
	}
	return errors.New("source patch has no matching validated execution in this turn")
}

func toolArguments(item map[string]any) map[string]any {
	for _, key := range []string{"arguments", "input", "params"} {
		if value, ok := item[key].(map[string]any); ok {
			return value
		}
	}
	return nil
}

func toolResult(item map[string]any) map[string]any {
	for _, key := range []string{"result", "output"} {
		if value, ok := item[key].(map[string]any); ok {
			return value
		}
	}
	if content, ok := item["content"].([]any); ok && len(content) > 0 {
		if first, ok := content[0].(map[string]any); ok {
			if text, ok := first["text"].(string); ok {
				var result map[string]any
				if json.Unmarshal([]byte(text), &result) == nil {
					return result
				}
			}
		}
	}
	return nil
}

func canonicalDraftHash(raw any) string {
	if raw == nil {
		return ""
	}
	if str, ok := raw.(string); ok {
		var obj any
		if json.Unmarshal([]byte(str), &obj) == nil {
			return hashCanonical(obj)
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
	return strings.Contains(lower, "approval_blocked") ||
		strings.Contains(lower, "approval blocked") ||
		strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "approval rejected") ||
		strings.Contains(lower, "rejected by user") ||
		strings.Contains(lower, "not approved")
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

const codexOutputSchema = `{
  "type":"object",
  "additionalProperties":false,
  "properties":{
    "message":{"type":"string"},
    "configuration_proposals":{"type":"array","items":{
      "type":"object","additionalProperties":false,
      "properties":{
        "application_id":{"type":"string"},
        "application_name":{"type":"string"},
        "environment_id":{"type":"string"},
        "rationale":{"type":"string"},
        "expected_revision":{"type":"integer","minimum":0},
        "expected_state_hash":{"type":"string"},
        "analysis_inputs_hash":{"type":"string"},
        "draft_json":{"type":"string"}
      },
      "required":["application_id","application_name","environment_id","rationale","expected_revision","expected_state_hash","analysis_inputs_hash","draft_json"]
    }},
    "source_patch_proposals":{"type":"array","items":{
      "type":"object","additionalProperties":false,
      "properties":{
        "project_id":{"type":"string"},
        "environment_id":{"type":"string"},
        "application_id":{"type":"string"},
        "source_commit":{"type":"string"},
        "application_root":{"type":"string"},
        "proposal_hash":{"type":"string"},
        "validation_status":{"type":"string","enum":["VALID","VALID_WITH_WARNINGS"]},
        "proposal":{
          "type":"object",
          "additionalProperties":false,
          "properties":{
            "project_id":{"type":"string"},
            "environment_id":{"type":"string"},
            "application_id":{"type":"string"},
            "provenance":{
              "type":"object",
              "additionalProperties":false,
              "properties":{
                "build_record_id":{"type":"string"},
                "source_commit":{"type":"string"},
                "application_root":{"type":"string"}
              },
              "required":["build_record_id","source_commit","application_root"]
            },
            "rationale":{
              "type":"object",
              "additionalProperties":false,
              "properties":{
                "observed_source":{"type":"string"},
                "opsi_facts":{"type":"string"},
                "inference":{"type":"string"}
              },
              "required":["observed_source","opsi_facts","inference"]
            },
            "files":{
              "type":"array",
              "items":{
                "type":"object",
                "additionalProperties":false,
                "properties":{
                  "path":{"type":"string"},
                  "base_blob_sha":{"type":"string"},
                  "unified_diff":{"type":"string"}
                },
                "required":["path","base_blob_sha","unified_diff"]
              }
            },
            "evidence":{
              "type":"array",
              "items":{
                "type":"object",
                "additionalProperties":false,
                "properties":{
                  "type":{"type":"string"},
                  "file":{"type":"string"},
                  "line":{"type":"integer"},
                  "reason":{"type":"string"},
                  "symbol":{"type":"string"}
                },
                "required":["type","file","line","reason","symbol"]
              }
            },
            "impact":{
              "type":"object",
              "additionalProperties":false,
              "properties":{
                "alternative_configuration_only_solution":{"type":"boolean"},
                "depends_on_unapplied_dependency_proposal":{"type":"boolean"}
              },
              "required":["alternative_configuration_only_solution","depends_on_unapplied_dependency_proposal"]
            }
          },
          "required":["project_id","environment_id","application_id","provenance","rationale","files","evidence","impact"]
        }
      },
      "required":["project_id","environment_id","application_id","source_commit","application_root","proposal_hash","validation_status","proposal"]
    }}
  },
  "required":["message","configuration_proposals","source_patch_proposals"]
}`

func codexThreadID(events []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(events))
	for scanner.Scan() {
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Type == "thread.started" {
			return strings.TrimSpace(event.ThreadID)
		}
	}
	return ""
}

func tomlString(value string) string { return strconv.Quote(value) }
func tomlArray(values []string) string {
	encoded := make([]string, 0, len(values))
	for _, value := range values {
		encoded = append(encoded, tomlString(value))
	}
	return "[" + strings.Join(encoded, ",") + "]"
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	if b.Len()+len(value) > b.limit {
		remaining := b.limit - b.Len()
		if remaining > 0 {
			_, _ = b.Buffer.Write(value[:remaining])
		}
		b.overflow = true
		return len(value), nil
	}
	return b.Buffer.Write(value)
}
