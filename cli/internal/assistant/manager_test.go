package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/mcp"
)

type fakeProvider struct {
	result RunResult
	err    error
	block  chan struct{}
}

func (p *fakeProvider) ID() string { return "fake" }
func (p *fakeProvider) Status(context.Context) ProviderStatus {
	return ProviderStatus{ID: p.ID(), Name: "Fake", Available: true, Authenticated: true}
}
func (p *fakeProvider) Run(_ context.Context, request RunRequest) (RunResult, error) {
	if p.block != nil {
		<-p.block
	}
	if p.result.ThreadID == "" {
		p.result.ThreadID = request.ThreadID
	}
	return p.result, p.err
}

func TestManagerRunsTurnsAndScopesLookupToProject(t *testing.T) {
	manager := NewManager(&fakeProvider{result: RunResult{
		ThreadID:  "thread-1",
		Text:      "review",
		Grounding: GroundingMetadata{Status: "verified", SuccessfulToolCalls: 2, Tools: []string{"project_context", "topology"}},
	}})
	defer manager.Close()

	turn, err := manager.StartTurn("proj-1", "fake", "", "review this project")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for turn.State == "running" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		turn, _ = manager.Turn("proj-1", turn.ID)
	}
	if turn.State != "succeeded" || turn.Response != "review" || turn.ConversationID == "" {
		t.Fatalf("turn=%+v", turn)
	}
	if turn.Grounding.Status != "verified" || turn.Grounding.SuccessfulToolCalls != 2 {
		t.Fatalf("unexpected grounding: %+v", turn.Grounding)
	}
	if _, ok := manager.Turn("proj-2", turn.ID); ok {
		t.Fatal("turn escaped its project boundary")
	}
}

func TestManagerRejectsConcurrentConversationAndOversizedPrompt(t *testing.T) {
	blocked := make(chan struct{})
	manager := NewManager(&fakeProvider{block: blocked})
	defer manager.Close()

	first, err := manager.StartTurn("proj-1", "fake", "chat-1", "first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.StartTurn("proj-1", "fake", "chat-1", "second"); err == nil {
		t.Fatal("concurrent turn was accepted")
	}
	if _, err := manager.StartTurn("proj-1", "fake", "chat-2", strings.Repeat("x", maxPromptBytes+1)); err == nil {
		t.Fatal("oversized prompt was accepted")
	}
	close(blocked)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if turn, ok := manager.Turn("proj-1", first.ID); ok && turn.State != "running" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("blocked turn did not finish")
}

func TestManagerReportsProviderFailureWithoutLeakingOtherState(t *testing.T) {
	manager := NewManager(&fakeProvider{err: &AssistantError{Code: ErrAssistantNotGrounded, Message: "assistant turn did not execute any Opsi MCP tool"}})
	defer manager.Close()

	turn, err := manager.StartTurn("proj-1", "fake", "chat-1", "review")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		current, _ := manager.Turn("proj-1", turn.ID)
		if current.State == "failed" {
			if current.ErrorCode != ErrAssistantNotGrounded || !strings.Contains(current.Error, "did not execute any Opsi MCP tool") {
				t.Fatalf("turn=%+v", current)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("failed turn did not finish")
}

func TestCodexThreadIDUsesThreadStartedEvent(t *testing.T) {
	events := []byte("{\"type\":\"item.completed\"}\n{\"type\":\"thread.started\",\"thread_id\":\"thread-123\"}\n")
	if got := codexThreadID(events); got != "thread-123" {
		t.Fatalf("thread id=%q", got)
	}
}

func TestManagerBoundsCompletedTurnsAndThreads(t *testing.T) {
	manager := NewManager(&fakeProvider{result: RunResult{ThreadID: "thread", Text: "review"}})
	defer manager.Close()

	turnIDs := make([]string, 0, maxStoredTurns+1)
	for index := 0; index <= maxStoredTurns; index++ {
		turn, err := manager.StartTurn("proj-1", "fake", "chat-"+strconv.Itoa(index), "review")
		if err != nil {
			t.Fatal(err)
		}
		turnIDs = append(turnIDs, turn.ID)
		waitForTurn(t, manager, turn.ID)
	}
	if _, ok := manager.Turn("proj-1", turnIDs[0]); ok {
		t.Fatal("oldest completed turn was not pruned")
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if len(manager.turns) != maxStoredTurns || len(manager.threads) != maxStoredTurns {
		t.Fatalf("turns=%d threads=%d", len(manager.turns), len(manager.threads))
	}
}

func TestCodexNativeDataToolsAreDisabled(t *testing.T) {
	for _, feature := range []string{"shell_tool", "unified_exec", "browser_use", "standalone_web_search", "plugins", "skill_search"} {
		if !slices.Contains(disabledCodexFeatures, feature) {
			t.Fatalf("feature %q was not disabled", feature)
		}
	}
}

func TestReadFileLimitedRejectsOversizedMessage(t *testing.T) {
	path := t.TempDir() + "/message"
	if err := os.WriteFile(path, []byte("oversized"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readFileLimited(path, 3); err == nil {
		t.Fatal("oversized message was accepted")
	}
}

func TestCodexMCPConfig_HasRequiredOptionsAndExact24Tools(t *testing.T) {
	provider := NewCodexProvider(CodexOptions{
		Binary:     "codex",
		OpsiBinary: "/bin/opsi",
		ConfigPath: "/etc/opsi/config.yaml",
		RepoRoot:   "/workspace",
	})
	configStr := provider.mcpConfig("proj-100")
	if !strings.Contains(configStr, "required=true") {
		t.Errorf("expected required=true in config: %s", configStr)
	}
	if !strings.Contains(configStr, `default_tools_approval_mode="writes"`) {
		t.Errorf("expected default_tools_approval_mode=\"writes\" in config: %s", configStr)
	}
	tools := mcp.AllTools()
	if len(tools) != 24 {
		t.Fatalf("expected 24 tools, got %d", len(tools))
	}
	for _, tool := range tools {
		if !strings.Contains(configStr, `"`+tool.Name+`"`) {
			t.Errorf("expected tool %s in enabled_tools: %s", tool.Name, configStr)
		}
	}
}

func TestParseCodexEventStream_FailClosedScenarios(t *testing.T) {
	// 1. MCP start failure in stderr
	_, err := parseCodexEventStream([]byte{}, "error: failed to start mcp server 'opsi'")
	var aErr *AssistantError
	if !errors.As(err, &aErr) || aErr.Code != ErrAssistantMCPStartFailed {
		t.Fatalf("expected ASSISTANT_MCP_START_FAILED, got: %v", err)
	}

	// 2. Approval blocked event
	blockedEvent := []byte(`{"type":"item.completed","item":{"type":"mcp_tool_call","server":"opsi","tool":"project_context","status":"approval_blocked"}}` + "\n")
	parsed, err := parseCodexEventStream(blockedEvent, "")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if !parsed.ApprovalBlocked {
		t.Fatal("expected ApprovalBlocked to be true")
	}

	// 3. Tool failure event
	failedToolEvent := []byte(`{"type":"item.completed","item":{"id":"call-1","type":"mcp_tool_call","server":"opsi","tool":"topology","status":"failed","error":"not found"}}` + "\n")
	parsed, err = parseCodexEventStream(failedToolEvent, "")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if !parsed.ToolFailed {
		t.Fatal("expected ToolFailed to be true")
	}

	// 4. Zero tools executed
	emptyEvents := []byte(`{"type":"thread.started","thread_id":"t-1"}` + "\n")
	parsed, err = parseCodexEventStream(emptyEvents, "")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if parsed.SuccessfulOpsiToolCalls != 0 {
		t.Fatalf("expected 0 successful tool calls, got %d", parsed.SuccessfulOpsiToolCalls)
	}
}

func TestParseCodexEventStreamCountsOnlyUniqueCompletedCalls(t *testing.T) {
	events := []byte("{\"type\":\"item.updated\",\"item\":{\"id\":\"call-1\",\"type\":\"mcp_tool_call\",\"server\":\"opsi\",\"tool\":\"project_context\",\"status\":\"in_progress\"}}\n" +
		"{\"type\":\"item.completed\",\"item\":{\"id\":\"call-1\",\"type\":\"mcp_tool_call\",\"server\":\"opsi\",\"tool\":\"project_context\",\"status\":\"completed\"}}\n")
	parsed, err := parseCodexEventStream(events, "")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.SuccessfulOpsiToolCalls != 1 {
		t.Fatalf("calls=%d, want 1", parsed.SuccessfulOpsiToolCalls)
	}
}

func TestProposalValidationAttestation(t *testing.T) {
	draft := map[string]any{
		"schema_version": "opsi.service_configuration/v1",
		"environment":    []map[string]any{{"name": "LOG_LEVEL", "value": "info"}},
	}
	draftBytes, _ := json.Marshal(draft)
	draftHash := canonicalDraftHash(string(draftBytes))

	records := []validatedProposalRecord{
		{
			ApplicationID:      "svc-web",
			EnvironmentID:      "env-1",
			ExpectedRevision:   5,
			ExpectedStateHash:  "hash-state-1",
			AnalysisInputsHash: "hash-inputs-1",
			DraftHash:          draftHash,
			Status:             "VALID",
		},
	}

	// 1. Matching proposal succeeds
	validProposal := ConfigurationProposal{
		ApplicationID:      "svc-web",
		ApplicationName:    "web",
		EnvironmentID:      "env-1",
		Rationale:          "safe log level",
		ExpectedRevision:   5,
		ExpectedStateHash:  "hash-state-1",
		AnalysisInputsHash: "hash-inputs-1",
		DraftJSON:          string(draftBytes),
	}
	if err := validateProposalAttestation(validProposal, records); err != nil {
		t.Fatalf("expected valid proposal to pass attestation, got error: %v", err)
	}

	// 2. Mismatched revision fails
	mismatchedRev := validProposal
	mismatchedRev.ExpectedRevision = 6
	if err := validateProposalAttestation(mismatchedRev, records); err == nil {
		t.Fatal("expected mismatched revision to fail attestation")
	}

	// 3. Mismatched draft content fails
	diffDraftBytes, _ := json.Marshal(map[string]any{"schema_version": "opsi.service_configuration/v1", "environment": []map[string]any{{"name": "LOG_LEVEL", "value": "debug"}}})
	mismatchedDraft := validProposal
	mismatchedDraft.DraftJSON = string(diffDraftBytes)
	if err := validateProposalAttestation(mismatchedDraft, records); err == nil {
		t.Fatal("expected mismatched draft hash to fail attestation")
	}

	// 4. Invalid status in validation record fails
	invalidRecord := records[0]
	invalidRecord.Status = "INVALID"
	if err := validateProposalAttestation(validProposal, []validatedProposalRecord{invalidRecord}); err == nil {
		t.Fatal("expected non-VALID record to fail attestation")
	}
}

func TestManagerClose_CancelsTurnsAndCleansWorkspaces(t *testing.T) {
	manager := NewManager(&fakeProvider{block: make(chan struct{})})
	turn, err := manager.StartTurn("proj-1", "fake", "conv-1", "prompt")
	if err != nil {
		t.Fatal(err)
	}

	manager.mu.RLock()
	ws := manager.workspaces["proj-1\x00fake\x00conv-1"]
	manager.mu.RUnlock()
	if ws == "" {
		t.Fatal("expected conversation workspace to be created")
	}
	if _, err := os.Stat(ws); err != nil {
		t.Fatalf("workspace dir does not exist: %v", err)
	}

	// Close manager
	if err := manager.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Workspace should be deleted
	if _, err := os.Stat(ws); !os.IsNotExist(err) {
		t.Fatalf("expected workspace to be deleted after manager.Close, err: %v", err)
	}

	// Turn should settle in failed / canceled state
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cur, ok := manager.Turn("proj-1", turn.ID); ok && cur.State == "failed" {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForTurn(t *testing.T, manager *Manager, turnID string) Turn {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if turn, ok := manager.Turn("proj-1", turnID); ok && turn.State != "running" {
			return turn
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("turn %s did not finish", turnID)
	return Turn{}
}
