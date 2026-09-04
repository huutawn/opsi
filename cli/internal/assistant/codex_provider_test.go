package assistant

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)
func TestBuildCodexArgs_FirstTurn(t *testing.T) {
	params := CodexCommandParams{
		MCPConfig:       `mcp_servers.opsi={command="opsi"}`,
		OutputSchema:    "/tmp/schema.json",
		LastMessagePath: "/tmp/last-message.txt",
		ThreadID:        "",
	}

	args := BuildCodexArgs(params)
	if len(args) == 0 || args[0] != "exec" {
		t.Fatalf("expected args to start with 'exec', got: %v", args)
	}

	// First turn must NOT contain resume or -C
	for _, arg := range args {
		if arg == "resume" {
			t.Fatalf("first turn command must not contain 'resume': %v", args)
		}
		if arg == "-C" {
			t.Fatalf("command must never contain '-C': %v", args)
		}
	}

	// Must have sandbox and MCP flags
	sandboxFound := false
	for i, arg := range args {
		if arg == "--sandbox" && i+1 < len(args) && args[i+1] == "read-only" {
			sandboxFound = true
			break
		}
	}
	if !sandboxFound {
		t.Fatalf("expected '--sandbox read-only' in args: %v", args)
	}

	mcpFound := false
	for i, arg := range args {
		if arg == "-c" && i+1 < len(args) && args[i+1] == params.MCPConfig {
			mcpFound = true
			break
		}
	}
	if !mcpFound {
		t.Fatalf("expected '-c <mcpConfig>' in args: %v", args)
	}

	// Must end with stdin prompt '-'
	if args[len(args)-1] != "-" {
		t.Fatalf("first turn command must end with '-', got: %v", args[len(args)-1])
	}
}

func TestBuildCodexArgs_ResumeTurn(t *testing.T) {
	threadID := "01a0669b-89fd-7390-88a2-a767fd8fdfb6"
	params := CodexCommandParams{
		MCPConfig:       `mcp_servers.opsi={command="opsi"}`,
		OutputSchema:    "/tmp/schema.json",
		LastMessagePath: "/tmp/last-message.txt",
		ThreadID:        threadID,
	}

	args := BuildCodexArgs(params)
	if len(args) == 0 || args[0] != "exec" {
		t.Fatalf("expected args to start with 'exec', got: %v", args)
	}

	// Never contain -C
	for _, arg := range args {
		if arg == "-C" {
			t.Fatalf("resume command must never contain '-C': %v", args)
		}
	}

	// Exactly one "resume"
	resumeIndex := -1
	resumeCount := 0
	for i, arg := range args {
		if arg == "resume" {
			resumeCount++
			resumeIndex = i
		}
	}
	if resumeCount != 1 {
		t.Fatalf("expected exactly 1 'resume', found %d in: %v", resumeCount, args)
	}

	// Verify all common options are before "resume"
	beforeResume := args[:resumeIndex]
	commonRequired := []string{"--json", "--ignore-user-config", "--skip-git-repo-check", "--sandbox", "-c", "--output-schema", "-o"}
	for _, opt := range commonRequired {
		found := false
		for _, arg := range beforeResume {
			if arg == opt {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected option %q before 'resume', but beforeResume was: %v", opt, beforeResume)
		}
	}

	// After "resume", must be exactly: ["--all", threadID, "-"]
	afterResume := args[resumeIndex+1:]
	expectedAfter := []string{"--all", threadID, "-"}
	if len(afterResume) != len(expectedAfter) {
		t.Fatalf("expected after resume: %v, got: %v", expectedAfter, afterResume)
	}
	for i := range expectedAfter {
		if afterResume[i] != expectedAfter[i] {
			t.Fatalf("at position %d after resume: want %q, got %q", i, expectedAfter[i], afterResume[i])
		}
	}
}

func createFakeCodexScript(t *testing.T, dir string, failWithStderr string) string {
	t.Helper()
	scriptPath := filepath.Join(dir, "fake-codex.sh")
	logPath := filepath.Join(dir, "codex-args.log")

	script := fmt.Sprintf(`#!/bin/bash
if [ "$1" = "--version" ]; then
  echo "codex-cli 0.153.0"
  exit 0
fi
if [ "$1" = "login" ] && [ "$2" = "status" ]; then
  echo "Logged in"
  exit 0
fi
if [ "$1" = "mcp" ] && [ "$2" = "list" ]; then
  echo '[{"name":"opsi","enabled":true}]'
  exit 0
fi

# Log arguments
echo "$*" >> "%s"

if [ "%s" != "" ]; then
  echo "%s" >&2
  exit 2
fi

# Extract -o argument for last message path
last_msg=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    last_msg="$2"
    shift 2
  else
    shift
  fi
done

if [ -n "$last_msg" ]; then
  cat << 'EOF' > "$last_msg"
{
  "message": "Fake Codex turn succeeded",
  "configuration_proposals": [],
  "source_patch_proposals": []
}
EOF
fi

echo '{"type":"thread.started","thread_id":"01a0669b-89fd-7390-88a2-a767fd8fdfb6"}'
echo '{"type":"item.completed","item":{"id":"call-1","type":"mcp_tool_call","server":"opsi","tool":"project_context","status":"completed"}}'
`, logPath, failWithStderr, failWithStderr)

	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write fake codex script: %v", err)
	}
	return scriptPath
}

func TestCodexSubprocess_ConsecutiveTurns(t *testing.T) {
	tempDir := t.TempDir()
	fakeCodex := createFakeCodexScript(t, tempDir, "")
	logPath := filepath.Join(tempDir, "codex-args.log")

	provider := NewCodexProvider(CodexOptions{
		Binary:     fakeCodex,
		OpsiBinary: "/bin/echo",
		ConfigPath: "/tmp/config.yaml",
		RepoRoot:   tempDir,
	})

	manager := NewManager(provider)
	defer manager.Close()

	// Turn 1: Initial turn
	turn1, err := manager.StartTurn("proj-1", "codex", "conv-consecutive", "First question")
	if err != nil {
		t.Fatalf("turn 1 StartTurn failed: %v", err)
	}
	waitForTurn(t, manager, turn1.ID)

	turn1Result, ok := manager.Turn("proj-1", turn1.ID)
	if !ok || turn1Result.State != "succeeded" {
		t.Fatalf("turn 1 did not succeed: %+v", turn1Result)
	}

	manager.mu.RLock()
	threadID := manager.threads["proj-1\x00codex\x00conv-consecutive"]
	manager.mu.RUnlock()

	expectedThreadID := "01a0669b-89fd-7390-88a2-a767fd8fdfb6"
	if threadID != expectedThreadID {
		t.Fatalf("expected thread ID %q after turn 1, got %q", expectedThreadID, threadID)
	}

	// Turn 2: Resume in same conversation
	turn2, err := manager.StartTurn("proj-1", "codex", "conv-consecutive", "Second question")
	if err != nil {
		t.Fatalf("turn 2 StartTurn failed: %v", err)
	}
	waitForTurn(t, manager, turn2.ID)

	turn2Result, ok := manager.Turn("proj-1", turn2.ID)
	if !ok || turn2Result.State != "succeeded" {
		t.Fatalf("turn 2 did not succeed: %+v", turn2Result)
	}

	manager.mu.RLock()
	threadIDAfter2 := manager.threads["proj-1\x00codex\x00conv-consecutive"]
	manager.mu.RUnlock()
	if threadIDAfter2 != expectedThreadID {
		t.Fatalf("expected thread ID %q after turn 2, got %q", expectedThreadID, threadIDAfter2)
	}

	// Verify logged args: turn 1 has no resume, turn 2 has resume --all <threadID>
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read arg log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 invocations in log, got: %v", lines)
	}

	line1 := lines[0]
	line2 := lines[1]

	if strings.Contains(line1, "resume") {
		t.Fatalf("turn 1 should not contain resume: %s", line1)
	}
	if strings.Contains(line1, "-C") {
		t.Fatalf("turn 1 should not contain -C: %s", line1)
	}

	if !strings.Contains(line2, fmt.Sprintf("resume --all %s -", expectedThreadID)) {
		t.Fatalf("turn 2 should contain 'resume --all %s -', got: %s", expectedThreadID, line2)
	}
	if strings.Contains(line2, "-C") {
		t.Fatalf("turn 2 should not contain -C: %s", line2)
	}
}

func TestManagerRestart_ResumeFromHistory(t *testing.T) {
	tempDir := t.TempDir()
	fakeCodex := createFakeCodexScript(t, tempDir, "")
	logPath := filepath.Join(tempDir, "codex-args.log")
	historyDir := filepath.Join(tempDir, "history")
	cfgPath := filepath.Join(tempDir, "opsi-config.yaml")

	store1, err := NewHistoryStore(historyDir, cfgPath)
	if err != nil {
		t.Fatalf("create history store 1 failed: %v", err)
	}

	provider1 := NewCodexProvider(CodexOptions{
		Binary:     fakeCodex,
		OpsiBinary: "/bin/echo",
		ConfigPath: cfgPath,
		RepoRoot:   tempDir,
	})

	manager1 := NewManager(provider1)
	manager1.SetHistoryStore(store1)

	// Turn 1 on manager 1
	turn1, err := manager1.StartTurn("proj-1", "codex", "conv-persist", "First turn before restart")
	if err != nil {
		t.Fatalf("turn 1 failed to start: %v", err)
	}
	waitForTurn(t, manager1, turn1.ID)

	turn1Result, ok := manager1.Turn("proj-1", turn1.ID)
	if !ok || turn1Result.State != "succeeded" {
		t.Fatalf("turn 1 did not succeed: %+v", turn1Result)
	}

	// Close manager 1 (simulating Opsi shutdown)
	if err := manager1.Close(); err != nil {
		t.Fatalf("close manager 1 failed: %v", err)
	}

	// Restart manager with new instance, loading same history store
	store2, err := NewHistoryStore(historyDir, cfgPath)
	if err != nil {
		t.Fatalf("create history store 2 failed: %v", err)
	}

	provider2 := NewCodexProvider(CodexOptions{
		Binary:     fakeCodex,
		OpsiBinary: "/bin/echo",
		ConfigPath: cfgPath,
		RepoRoot:   tempDir,
	})

	manager2 := NewManager(provider2)
	manager2.SetHistoryStore(store2)
	defer manager2.Close()

	// Turn 2 on manager 2 in same conversation
	turn2, err := manager2.StartTurn("proj-1", "codex", "conv-persist", "Second turn after restart")
	if err != nil {
		t.Fatalf("turn 2 failed to start: %v", err)
	}
	waitForTurn(t, manager2, turn2.ID)

	turn2Result, ok := manager2.Turn("proj-1", turn2.ID)
	if !ok || turn2Result.State != "succeeded" {
		t.Fatalf("turn 2 did not succeed: %+v", turn2Result)
	}

	// Verify logged args for turn 2 used the persisted thread ID
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read arg log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 invocations in log, got: %v", lines)
	}
	line2 := lines[len(lines)-1]
	expectedThreadID := "01a0669b-89fd-7390-88a2-a767fd8fdfb6"
	if !strings.Contains(line2, fmt.Sprintf("resume --all %s -", expectedThreadID)) {
		t.Fatalf("turn 2 after restart should resume with thread ID, got line: %s", line2)
	}
}

func TestCodexSubprocess_TypedInvocationError(t *testing.T) {
	tempDir := t.TempDir()
	// Fake script that fails with unexpected argument -C and a secret token in stderr
	fakeCodex := createFakeCodexScript(t, tempDir, "error: unexpected argument '-C' found Bearer opsi_pat_1234567890abcdef")

	provider := NewCodexProvider(CodexOptions{
		Binary:     fakeCodex,
		OpsiBinary: "/bin/echo",
		ConfigPath: "/tmp/config.yaml",
		RepoRoot:   tempDir,
	})

	manager := NewManager(provider)
	defer manager.Close()

	turn, err := manager.StartTurn("proj-1", "codex", "conv-err", "Test failing turn")
	if err != nil {
		t.Fatalf("StartTurn failed: %v", err)
	}
	waitForTurn(t, manager, turn.ID)

	failedTurn, ok := manager.Turn("proj-1", turn.ID)
	if !ok {
		t.Fatal("turn not found")
	}

	if failedTurn.State != "failed" {
		t.Fatalf("expected turn state 'failed', got: %s", failedTurn.State)
	}

	// Must be typed ASSISTANT_PROVIDER_INVOCATION_FAILED, not generic ASSISTANT_TURN_FAILED
	if failedTurn.ErrorCode != ErrAssistantProviderInvocationFailed {
		t.Fatalf("expected error code %q, got: %q", ErrAssistantProviderInvocationFailed, failedTurn.ErrorCode)
	}

	// Stderr must be sanitized: no raw token
	if strings.Contains(failedTurn.Error, "opsi_pat_1234567890abcdef") {
		t.Fatalf("error message contained unsanitized token: %s", failedTurn.Error)
	}
	if !strings.Contains(failedTurn.Error, "unexpected argument") {
		t.Fatalf("expected error to contain sanitized stderr, got: %s", failedTurn.Error)
	}

	// Must contain next_action checking Codex CLI compatibility
	if !strings.Contains(failedTurn.NextAction, "Codex CLI") {
		t.Fatalf("expected next_action to mention Codex CLI, got: %q", failedTurn.NextAction)
	}
}
