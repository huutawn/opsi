package assistant

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/mcp"
)

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

type CodexOptions struct {
	Binary     string
	OpsiBinary string
	ConfigPath string
	RepoRoot   string
}

type CodexProvider struct {
	options CodexOptions
}

func NewCodexProvider(options CodexOptions) *CodexProvider {
	if strings.TrimSpace(options.Binary) == "" {
		options.Binary = "codex"
	}
	return &CodexProvider{options: options}
}

func (p *CodexProvider) ID() string {
	return "codex"
}

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

type CodexCommandParams struct {
	MCPConfig       string
	OutputSchema    string
	LastMessagePath string
	ThreadID        string
}

// BuildCodexArgs constructs the exact CLI arguments for a new turn or resume turn.
// Common options are placed before any subcommand, working directory is managed
// strictly by exec.Cmd.Dir, and -C is never emitted.
func BuildCodexArgs(params CodexCommandParams) []string {
	args := []string{"exec"}
	args = append(args, disabledFeatureArgs(disabledCodexFeatures)...)
	args = append(args,
		"--json",
		"--ignore-user-config",
		"--skip-git-repo-check",
		"--sandbox", "read-only",
		"-c", params.MCPConfig,
		"--output-schema", params.OutputSchema,
		"-o", params.LastMessagePath,
	)
	if strings.TrimSpace(params.ThreadID) != "" {
		args = append(args, "resume", "--all", strings.TrimSpace(params.ThreadID), "-")
	} else {
		args = append(args, "-")
	}
	return args
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

	args := BuildCodexArgs(CodexCommandParams{
		MCPConfig:       mcpConfig,
		OutputSchema:    outputSchema,
		LastMessagePath: lastMessage,
		ThreadID:        request.ThreadID,
	})

	collector := newCodexEventCollector(request.TurnID, request.OnProgress)
	collector.emit(PhaseStartingProvider, "", "", "Đang khởi động tiến trình AI")

	command := exec.CommandContext(ctx, p.options.Binary, args...)
	command.Dir = workspace
	command.Stdin = strings.NewReader(instructions)

	stdoutPipe, err := command.StdoutPipe()
	if err != nil {
		return RunResult{}, fmt.Errorf("open stdout pipe: %w", err)
	}
	stderrPipe, err := command.StderrPipe()
	if err != nil {
		return RunResult{}, fmt.Errorf("open stderr pipe: %w", err)
	}

	if err := command.Start(); err != nil {
		return RunResult{}, fmt.Errorf("start Codex turn: %w", err)
	}

	var wg sync.WaitGroup
	var stderrBuf strings.Builder
	var stdoutOverflow bool
	var stdoutReadErr error
	var stderrReadErr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdoutPipe)
		buf := make([]byte, 64*1024)
		scanner.Buffer(buf, maxOutputBytes)
		var total int64
		for scanner.Scan() {
			line := scanner.Bytes()
			total += int64(len(line))
			if total > maxOutputBytes {
				stdoutOverflow = true
				continue
			}
			collector.FeedStdoutLine(line)
		}
		stdoutReadErr = scanner.Err()
		_, _ = io.Copy(io.Discard, stdoutPipe)
	}()

	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderrPipe)
		scanner.Buffer(make([]byte, 64*1024), maxOutputBytes)
		for scanner.Scan() {
			line := scanner.Text()
			collector.FeedStderrLine(line)
			if stderrBuf.Len() < 64*1024 {
				stderrBuf.WriteString(line)
				stderrBuf.WriteString("\n")
			}
		}
		stderrReadErr = scanner.Err()
		_, _ = io.Copy(io.Discard, stderrPipe)
	}()

	wg.Wait()
	runErr := command.Wait()
	stderrStr := strings.TrimSpace(stderrBuf.String())

	if isMCPStartFailure(stderrStr) || collector.mcpStartFailed {
		return RunResult{}, &AssistantError{Code: ErrAssistantMCPStartFailed, Message: "failed to start required Opsi MCP server"}
	}

	if runErr != nil {
		message := stderrStr
		if message == "" {
			message = runErr.Error()
		}
		if isApprovalBlocked(message) || collector.approvalBlocked {
			return RunResult{}, &AssistantError{Code: ErrAssistantMCPApprovalBlocked, Message: "MCP tool call was blocked by approval policy"}
		}
		return RunResult{}, &AssistantError{
			Code:       ErrAssistantProviderInvocationFailed,
			Message:    redactDiagnostic(message),
			NextAction: "Verify Codex CLI version and compatibility.",
			Retryable:  false,
		}
	}
	if stdoutOverflow {
		return RunResult{}, errors.New("Codex event stream exceeded the local response limit")
	}
	if stdoutReadErr != nil {
		return RunResult{}, fmt.Errorf("read Codex event stream: %w", stdoutReadErr)
	}
	if stderrReadErr != nil {
		return RunResult{}, fmt.Errorf("read Codex diagnostic stream: %w", stderrReadErr)
	}

	parsedEvents, parseErr := collector.Finish()
	if parseErr != nil {
		return RunResult{}, parseErr
	}

	if parsedEvents.ApprovalBlocked {
		return RunResult{}, &AssistantError{Code: ErrAssistantMCPApprovalBlocked, Message: "MCP tool call was blocked by approval policy"}
	}
	if parsedEvents.EventInvalid {
		return RunResult{}, &AssistantError{
			Code:           ErrAssistantMCPEventInvalid,
			Message:        parsedEvents.ToolFailureMessage,
			DiagnosticCode: ErrAssistantMCPEventInvalid,
			Retryable:      true,
			NextAction:     "Retry the turn; if it repeats, report the turn ID and Codex CLI version.",
		}
	}
	if parsedEvents.ToolFailed {
		msg := parsedEvents.ToolFailureMessage
		if msg == "" {
			msg = "one or more Opsi MCP tool calls failed"
		}
		return RunResult{}, &AssistantError{
			Code:           ErrAssistantMCPToolFailed,
			Message:        msg,
			DiagnosticCode: parsedEvents.ToolFailureCode,
			Retryable:      parsedEvents.ToolFailureRetryable,
			NextAction:     parsedEvents.ToolFailureNextAction,
		}
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
