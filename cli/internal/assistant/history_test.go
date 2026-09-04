package assistant

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHistoryStore_AtomicPersistenceAndPermissions(t *testing.T) {
	tempDir := t.TempDir()
	historyDir := filepath.Join(tempDir, "history")
	cfgPath := "/tmp/test-config.yaml"

	store, err := NewHistoryStore(historyDir, cfgPath)
	if err != nil {
		t.Fatalf("failed to create history store: %v", err)
	}

	turn := Turn{
		ID:             "turn-perm-1",
		ConversationID: "conv-perm-1",
		ProviderID:     "codex",
		ProjectID:      "proj-perm-1",
		State:          "succeeded",
		Response:       "response text",
		StartedAt:      time.Now().UTC(),
		FinishedAt:     time.Now().UTC(),
	}

	if err := store.SaveTurn(turn, "test prompt", "thread-perm-1"); err != nil {
		t.Fatalf("failed to save turn: %v", err)
	}

	filePath := store.FilePath()
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("history file not found: %v", err)
	}

	// File permission check: 0600
	filePerm := fileInfo.Mode().Perm()
	if filePerm != 0600 {
		t.Errorf("expected file mode 0600, got %o", filePerm)
	}

	// Dir permission check: 0700
	dirInfo, err := os.Stat(historyDir)
	if err != nil {
		t.Fatalf("history dir not found: %v", err)
	}
	dirPerm := dirInfo.Mode().Perm()
	if dirPerm != 0700 {
		t.Errorf("expected dir mode 0700, got %o", dirPerm)
	}

	// Verify valid JSON content on disk
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read history file: %v", err)
	}
	var file HistoryFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("invalid json on disk: %v", err)
	}
	if len(file.Conversations) != 1 || file.Conversations[0].ID != "conv-perm-1" {
		t.Fatalf("unexpected content on disk: %+v", file)
	}
}

func TestHistoryStore_RedactionBeforePersist(t *testing.T) {
	tempDir := t.TempDir()
	historyDir := filepath.Join(tempDir, "history")
	cfgPath := "/tmp/test-config.yaml"

	store, err := NewHistoryStore(historyDir, cfgPath)
	if err != nil {
		t.Fatalf("failed to create history store: %v", err)
	}

	secretPAT := "opsi_pat_super_secret_leak_12345"
	secretKey := "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA0\n-----END RSA PRIVATE KEY-----"
	secretBearer := "Bearer secret_bearer_token_abc123"
	secretEnv := `password="my_db_password_123"`

	promptWithSecrets := "Connect using " + secretPAT + " and " + secretKey + " and " + secretBearer + " and " + secretEnv

	turn := Turn{
		ID:             "turn-redact-1",
		ConversationID: "conv-redact-1",
		ProviderID:     "codex",
		ProjectID:      "proj-1",
		State:          "succeeded",
		Response:       "Analysis complete for " + secretPAT,
		StartedAt:      time.Now().UTC(),
		FinishedAt:     time.Now().UTC(),
	}

	if err := store.SaveTurn(turn, promptWithSecrets, "thread-1"); err != nil {
		t.Fatalf("save turn: %v", err)
	}

	// Read file directly from disk to ensure raw secrets do NOT appear on disk
	diskData, err := os.ReadFile(store.FilePath())
	if err != nil {
		t.Fatalf("read disk data: %v", err)
	}
	diskStr := string(diskData)

	if strings.Contains(diskStr, secretPAT) {
		t.Errorf("secret PAT leaked onto disk: %s", diskStr)
	}
	if strings.Contains(diskStr, "MIIEowIBAAKCAQEA0") {
		t.Errorf("private key leaked onto disk: %s", diskStr)
	}
	if strings.Contains(diskStr, "secret_bearer_token_abc123") {
		t.Errorf("bearer token leaked onto disk: %s", diskStr)
	}
	if strings.Contains(diskStr, "my_db_password_123") {
		t.Errorf("password leaked onto disk: %s", diskStr)
	}

	// Verify message in store is marked as Redacted = true
	conv, ok := store.GetConversation("proj-1", "conv-redact-1")
	if !ok || len(conv.Messages) == 0 {
		t.Fatalf("conversation not found: %+v", conv)
	}
	userMsg := conv.Messages[0]
	if !userMsg.Redacted {
		t.Errorf("expected user message to be marked Redacted: true")
	}
}

func TestHistoryStore_DoesNotPersistActionableProposals(t *testing.T) {
	store, err := NewHistoryStore(filepath.Join(t.TempDir(), "history"), "/tmp/test-config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	turn := Turn{
		ID: "turn-proposal", ConversationID: "conv-proposal", ProviderID: "codex", ProjectID: "proj-1",
		State: "succeeded", Response: "review", StartedAt: time.Now().UTC(),
		ConfigurationProposals: []ConfigurationProposal{{ApplicationID: "app-1", DraftJSON: `{"secret":"value"}`}},
		SourcePatchProposals:   []SourcePatchProposal{{ProposalHash: "patch-1", Proposal: json.RawMessage(`{"files":[{"unified_diff":"sensitive source"}]}`)}},
	}
	if err := store.SaveTurn(turn, "review", "thread-1"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.FilePath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sensitive source") || strings.Contains(string(data), "DraftJSON") || strings.Contains(string(data), "configuration_proposals") {
		t.Fatalf("actionable proposal leaked into durable history: %s", data)
	}
}

func TestHistoryStore_RejectsUnsafeOrUnsupportedHistory(t *testing.T) {
	t.Run("unsupported version", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewHistoryStore(filepath.Join(dir, "history"), "/tmp/config.yaml")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(store.BaseDir(), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.FilePath(), []byte(`{"version":2,"conversations":[]}`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewHistoryStore(store.BaseDir(), "/tmp/config.yaml"); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("expected unsupported-version error, got %v", err)
		}
	})

	t.Run("symlink file", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewHistoryStore(filepath.Join(dir, "history"), "/tmp/config.yaml")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(store.BaseDir(), 0700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dir, "target.json")
		if err := os.WriteFile(target, []byte(`{"version":1,"conversations":[]}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, store.FilePath()); err != nil {
			t.Fatal(err)
		}
		if _, err := NewHistoryStore(store.BaseDir(), "/tmp/config.yaml"); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("expected unsafe-path error, got %v", err)
		}
	})
}

func TestHistoryStore_Retention_OldestCompletedFirst(t *testing.T) {
	tempDir := t.TempDir()
	historyDir := filepath.Join(tempDir, "history")
	store, err := NewHistoryStore(historyDir, "/tmp/config.yaml")
	if err != nil {
		t.Fatal(err)
	}

	// Create 21 conversations (max is 20)
	// Conversation 1 is completed (succeeded)
	// Conversation 2 is running
	now := time.Now().UTC()

	for i := 1; i <= 21; i++ {
		state := "succeeded"
		if i == 2 {
			state = "running"
		}
		turn := Turn{
			ID:             "turn-" + strconv.Itoa(i),
			ConversationID: "conv-" + strconv.Itoa(i),
			ProviderID:     "codex",
			ProjectID:      "proj-1",
			State:          state,
			StartedAt:      now.Add(time.Duration(i) * time.Minute),
			FinishedAt:     now.Add(time.Duration(i) * time.Minute),
		}
		if err := store.SaveTurn(turn, "prompt "+strconv.Itoa(i), "thread-"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}

	list := store.ListConversations("proj-1", "")
	if len(list) > MaxConversations {
		t.Fatalf("expected at most %d conversations, got %d", MaxConversations, len(list))
	}

	// conv-1 was the oldest completed conversation, so it must have been pruned!
	if _, found := store.GetConversation("proj-1", "conv-1"); found {
		t.Errorf("expected oldest completed conversation conv-1 to be pruned")
	}

	// conv-2 was running, so it should not be pruned first even though it's older than conv-3..21
	if _, found := store.GetConversation("proj-1", "conv-2"); !found {
		t.Errorf("conv-2 (running) was prematurely pruned")
	}
}

func TestHistoryStore_ProjectIsolation(t *testing.T) {
	tempDir := t.TempDir()
	historyDir := filepath.Join(tempDir, "history")
	store, err := NewHistoryStore(historyDir, "/tmp/config.yaml")
	if err != nil {
		t.Fatal(err)
	}

	turn1 := Turn{
		ID:             "turn-p1",
		ConversationID: "conv-1",
		ProviderID:     "codex",
		ProjectID:      "proj-1",
		State:          "succeeded",
		StartedAt:      time.Now().UTC(),
	}
	turn2 := Turn{
		ID:             "turn-p2",
		ConversationID: "conv-2",
		ProviderID:     "codex",
		ProjectID:      "proj-2",
		State:          "succeeded",
		StartedAt:      time.Now().UTC(),
	}

	_ = store.SaveTurn(turn1, "prompt 1", "thread-1")
	_ = store.SaveTurn(turn2, "prompt 2", "thread-2")

	// Check ListConversations
	p1List := store.ListConversations("proj-1", "")
	if len(p1List) != 1 || p1List[0].ID != "conv-1" {
		t.Fatalf("unexpected p1 list: %+v", p1List)
	}

	p2List := store.ListConversations("proj-2", "")
	if len(p2List) != 1 || p2List[0].ID != "conv-2" {
		t.Fatalf("unexpected p2 list: %+v", p2List)
	}

	// Check GetConversation across project boundary
	if _, found := store.GetConversation("proj-2", "conv-1"); found {
		t.Errorf("conv-1 escaped project boundary to proj-2")
	}

	// Check DeleteConversation across project boundary
	deleted, err := store.DeleteConversation("proj-2", "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if deleted {
		t.Errorf("delete across project boundary succeeded")
	}
	if _, found := store.GetConversation("proj-1", "conv-1"); !found {
		t.Errorf("conv-1 was wrongly deleted by foreign project call")
	}
}

func TestManager_RestartResumeThread_And_Retry(t *testing.T) {
	tempDir := t.TempDir()
	historyDir := filepath.Join(tempDir, "history")
	cfgPath := filepath.Join(tempDir, "cli.yaml")
	_ = os.WriteFile(cfgPath, []byte("cloud_url: https://example.com\n"), 0600)

	store1, err := NewHistoryStore(historyDir, cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	provider1 := &fakeProvider{
		result: RunResult{
			ThreadID:  "thread-resume-xyz",
			Text:      "Initial review",
			Grounding: GroundingMetadata{Status: "verified", SuccessfulToolCalls: 1, Tools: []string{"project_context"}},
		},
	}
	mgr1 := NewManager(provider1)
	mgr1.SetHistoryStore(store1)

	turn, err := mgr1.StartTurn("proj-1", "fake", "conv-restart", "Initial prompt")
	if err != nil {
		t.Fatal(err)
	}

	waitForTurn(t, mgr1, turn.ID)

	// Simulate restart: create a new manager with a new history store pointing to same path
	store2, err := NewHistoryStore(historyDir, cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	var resumedThreadID string
	provider2 := &fakeProvider{
		result: RunResult{
			Text:      "Resumed response",
			Grounding: GroundingMetadata{Status: "verified", SuccessfulToolCalls: 1, Tools: []string{"project_context"}},
		},
	}
	mgr2 := NewManager(provider2)
	mgr2.SetHistoryStore(store2)

	// Verify thread was restored in memory on SetHistoryStore
	key := "proj-1\x00fake\x00conv-restart"
	mgr2.mu.RLock()
	resumedThreadID = mgr2.threads[key]
	mgr2.mu.RUnlock()

	if resumedThreadID != "thread-resume-xyz" {
		t.Fatalf("expected restored thread ID 'thread-resume-xyz', got %q", resumedThreadID)
	}

	// Verify starting another turn in same conversation passes the resumed thread ID
	turn2, err := mgr2.StartTurn("proj-1", "fake", "conv-restart", "Follow up prompt")
	if err != nil {
		t.Fatal(err)
	}
	waitForTurn(t, mgr2, turn2.ID)

	mgr2.Close()
	mgr1.Close()
}

func TestManager_RetryTurn_NormalAndRedacted(t *testing.T) {
	tempDir := t.TempDir()
	historyDir := filepath.Join(tempDir, "history")
	cfgPath := filepath.Join(tempDir, "cli.yaml")
	_ = os.WriteFile(cfgPath, []byte("cloud_url: https://example.com\n"), 0600)

	store, _ := NewHistoryStore(historyDir, cfgPath)

	failProvider := &fakeProvider{
		err: &AssistantError{
			Code:    ErrAssistantMCPToolFailed,
			Message: "Opsi MCP tool 'topology' failed: connection reset",
		},
	}
	mgr := NewManager(failProvider)
	mgr.SetHistoryStore(store)
	defer mgr.Close()

	// 1. Normal failed turn
	turn1, err := mgr.StartTurn("proj-1", "fake", "conv-retry", "Safe review prompt")
	if err != nil {
		t.Fatal(err)
	}
	waitForTurn(t, mgr, turn1.ID)

	// Retry turn1
	retriedTurn, err := mgr.RetryTurn("proj-1", turn1.ID)
	if err != nil {
		t.Fatalf("failed to retry normal turn: %v", err)
	}
	if retriedTurn.ID == turn1.ID {
		t.Errorf("retry must create a new turn, got same ID: %s", retriedTurn.ID)
	}
	if retriedTurn.ConversationID != "conv-retry" {
		t.Errorf("expected same conversation ID, got %s", retriedTurn.ConversationID)
	}
	if retriedTurn.Prompt != "Safe review prompt" {
		t.Errorf("expected prompt to be preserved, got %s", retriedTurn.Prompt)
	}

	// 2. Turn with redacted credentials
	secretPAT := "opsi_pat_secret_12345"
	turnSecret, err := mgr.StartTurn("proj-1", "fake", "conv-secret", "Review with "+secretPAT)
	if err != nil {
		t.Fatal(err)
	}
	waitForTurn(t, mgr, turnSecret.ID)

	// Attempt to retry redacted turn -> must be rejected!
	_, err = mgr.RetryTurn("proj-1", turnSecret.ID)
	if err == nil {
		t.Fatal("expected retry on redacted turn to be rejected, but it succeeded")
	}
	var aErr *AssistantError
	if !strings.Contains(err.Error(), "redacted") && (!errors.As(err, &aErr) || aErr.Code != "CANNOT_RETRY_REDACTED_PROMPT") {
		t.Fatalf("unexpected error: %v", err)
	}
}
