package assistant

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/config"
)

func TestLiveCodexCLI_ConsecutiveTurnsAndResume(t *testing.T) {
	if os.Getenv("OPSI_LIVE_CODEX") != "1" {
		t.Skip("skipping live Codex CLI test; set OPSI_LIVE_CODEX=1 to run")
	}

	codexBin := "/home/tawn/.local/bin/codex"
	if _, err := os.Stat(codexBin); err != nil {
		t.Skipf("codex binary not found at %s: %v", codexBin, err)
	}

	opsiBin := "/home/tawn/.local/bin/opsi"
	if _, err := os.Stat(opsiBin); err != nil {
		t.Skipf("opsi binary not found at %s: %v", opsiBin, err)
	}

	// 1. Mock Cloud server for Opsi MCP
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case p == "/v1/auth/pat/verify":
			_ = json.NewEncoder(w).Encode(map[string]any{"valid": true, "org_id": "org-live", "user_id": "u-live", "role": "owner"})
		case strings.HasSuffix(p, "/readiness"):
			_ = json.NewEncoder(w).Encode(map[string]any{"project_id": "proj-live", "status": "ready", "can_deploy": true})
		case strings.HasSuffix(p, "/topology/facts"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"environments": []map[string]any{{"id": "env-prod", "name": "production", "status": "active"}},
				"runtimes":     []any{},
				"nodes":        []any{},
			})
		case strings.HasSuffix(p, "/topology"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schema_version": "opsi.topology_plan/v1",
				"id":             "topo-1",
				"project_id":     "proj-live",
				"revision":       1,
				"state_hash":     strings.Repeat("a", 64),
				"plan_hash":      strings.Repeat("b", 64),
				"assignments":    []any{},
			})
		case strings.HasSuffix(p, "/services"):
			_ = json.NewEncoder(w).Encode(map[string]any{"services": []any{}})
		case strings.HasSuffix(p, "/deployments"):
			_ = json.NewEncoder(w).Encode(map[string]any{"deployments": []any{}})
		case strings.HasSuffix(p, "/nodes"):
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []any{}})
		case strings.Contains(p, "/projects/proj-live"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "proj-live", "org_id": "org-live", "name": "Live Project", "slug": "live-project"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	defer cloud.Close()

	// 2. Setup temporary CLI config and Secret Service PAT
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "cli.yaml")
	if err := config.Save(configPath, config.Config{
		CloudURL: cloud.URL,
	}); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	// Store test PAT in OS keychain
	patCmd := exec.Command("secret-tool", "store", "--label=Opsi PAT", "service", "opsi", "key", "pat")
	patCmd.Stdin = strings.NewReader("opsi_pat_live_acceptance_test")
	if out, err := patCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to store test PAT in secret-tool: %v (%s)", err, string(out))
	}
	defer func() {
		_ = exec.Command("secret-tool", "clear", "service", "opsi", "key", "pat").Run()
	}()

	historyDir := filepath.Join(tempDir, "history")
	store1, err := NewHistoryStore(historyDir, configPath)
	if err != nil {
		t.Fatalf("failed to create history store 1: %v", err)
	}

	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("failed to resolve repoRoot: %v", err)
	}

	providerOptions := CodexOptions{
		Binary:     codexBin,
		OpsiBinary: opsiBin,
		ConfigPath: configPath,
		RepoRoot:   repoRoot,
	}

	provider1 := NewCodexProvider(providerOptions)
	manager1 := NewManager(provider1)
	manager1.SetHistoryStore(store1)

	convID := "live-acceptance-conv"
	projectID := "proj-live"

	runAndVerifyTurn := func(m *Manager, turnNum int, prompt string) (string, Turn) {
		t.Helper()
		t.Logf("Starting turn %d: %q", turnNum, prompt)
		turn, err := m.StartTurn(projectID, "codex", convID, prompt)
		if err != nil {
			t.Fatalf("turn %d StartTurn failed: %v", turnNum, err)
		}

		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			cur, ok := m.Turn(projectID, turn.ID)
			if ok && cur.State != "running" {
				turn = cur
				break
			}
			time.Sleep(200 * time.Millisecond)
		}

		if turn.State != "succeeded" {
			t.Fatalf("turn %d failed with error_code=%q error=%q next_action=%q progress=%+v",
				turnNum, turn.ErrorCode, turn.Error, turn.NextAction, turn.Progress)
		}

		// Must not contain unexpected argument '-C'
		if strings.Contains(turn.Error, "unexpected argument '-C'") {
			t.Fatalf("turn %d failed with unexpected argument '-C'", turnNum)
		}

		// Grounding must be verified
		if turn.Grounding.Status != "verified" {
			t.Fatalf("turn %d grounding was not verified: %+v", turnNum, turn.Grounding)
		}
		if turn.Grounding.SuccessfulToolCalls == 0 {
			t.Fatalf("turn %d executed 0 tools", turnNum)
		}

		m.mu.RLock()
		threadID := m.threads[projectID+"\x00codex\x00"+convID]
		m.mu.RUnlock()

		if threadID == "" {
			t.Fatalf("turn %d did not track a thread ID", turnNum)
		}

		t.Logf("Turn %d succeeded! ThreadID=%s GroundingTools=%v Response=%s",
			turnNum, threadID, turn.Grounding.Tools, strings.TrimSpace(turn.Response))
		return threadID, turn
	}

	// Turn 1
	thread1, _ := runAndVerifyTurn(manager1, 1,
		"Use the opsi project_context tool to check facts for project proj-live.")

	// Turn 2
	thread2, _ := runAndVerifyTurn(manager1, 2,
		"Use the opsi topology tool to check the topology for project proj-live.")
	if thread2 != thread1 {
		t.Fatalf("thread ID changed on turn 2: want %q, got %q", thread1, thread2)
	}

	// Turn 3
	thread3, _ := runAndVerifyTurn(manager1, 3,
		"Use the opsi deployment_readiness_context tool to check deployment readiness.")
	if thread3 != thread1 {
		t.Fatalf("thread ID changed on turn 3: want %q, got %q", thread1, thread3)
	}

	// Close Manager 1 (simulating restarting CLI)
	if err := manager1.Close(); err != nil {
		t.Fatalf("failed to close manager 1: %v", err)
	}

	// Restart Manager from same persisted history and same config
	store2, err := NewHistoryStore(historyDir, configPath)
	if err != nil {
		t.Fatalf("failed to load history store 2: %v", err)
	}

	provider2 := NewCodexProvider(providerOptions)
	manager2 := NewManager(provider2)
	manager2.SetHistoryStore(store2)
	defer manager2.Close()

	// Turn 4 after restart
	thread4, _ := runAndVerifyTurn(manager2, 4,
		"Use the opsi project_context tool again to review project proj-live.")
	if thread4 != thread1 {
		t.Fatalf("thread ID changed after restart on turn 4: want %q, got %q", thread1, thread4)
	}
}
