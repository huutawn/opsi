package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/assistant"
	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
)

type blockingAssistantProvider struct {
	started chan struct{}
	release chan struct{}
}

func (p *blockingAssistantProvider) ID() string { return "fake" }
func (p *blockingAssistantProvider) Status(context.Context) assistant.ProviderStatus {
	return assistant.ProviderStatus{ID: p.ID(), Available: true, Authenticated: true}
}
func (p *blockingAssistantProvider) Run(_ context.Context, request assistant.RunRequest) (assistant.RunResult, error) {
	request.OnProgress(assistant.ProgressEvent{Sequence: 2, Timestamp: time.Now().UTC(), Phase: assistant.PhaseToolRunning, Tool: "deployments_list", Summary: "Reading deployment history"})
	close(p.started)
	<-p.release
	return assistant.RunResult{Text: "done", Grounding: assistant.GroundingMetadata{Status: "verified", SuccessfulToolCalls: 1, Tools: []string{"deployments_list"}}}, nil
}

func setupTestAssistantAPI(t *testing.T, providers ...assistant.Provider) (*assistant.Manager, *assistant.HistoryStore, string) {
	t.Helper()
	tempDir := t.TempDir()
	historyDir := filepath.Join(tempDir, "history")
	cfgPath := filepath.Join(tempDir, "cli.yaml")

	store, err := assistant.NewHistoryStore(historyDir, cfgPath)
	if err != nil {
		t.Fatalf("failed to create history store: %v", err)
	}

	mgr := assistant.NewManager(providers...)
	mgr.SetHistoryStore(store)

	localSession := "valid-local-session-secret"
	return mgr, store, localSession
}

func TestAssistantAPI_Conversations_ProjectScopingAndList(t *testing.T) {
	mgr, store, session := setupTestAssistantAPI(t)
	defer mgr.Close()

	// Seed conversations for proj-1 and proj-2
	now := time.Now().UTC()
	turn1 := assistant.Turn{
		ID:             "turn-1",
		ConversationID: "conv-1",
		ProviderID:     "codex",
		ProjectID:      "proj-1",
		State:          "succeeded",
		StartedAt:      now,
		Progress: []assistant.ProgressEvent{
			{Sequence: 1, Phase: "tool_running", Tool: "deployments_list", Summary: "Đang lấy lịch sử deployment"},
			{Sequence: 2, Phase: "tool_succeeded", Tool: "deployments_list", Summary: "Đã lấy lịch sử deployment"},
		},
	}
	turn2 := assistant.Turn{
		ID:             "turn-2",
		ConversationID: "conv-2",
		ProviderID:     "codex",
		ProjectID:      "proj-2",
		State:          "succeeded",
		StartedAt:      now,
	}

	_ = store.SaveTurn(turn1, "prompt 1 for proj 1", "thread-1")
	_ = store.SaveTurn(turn2, "prompt 2 for proj 2", "thread-2")

	// 1. List conversations for proj-1 -> should only contain conv-1
	req := httptest.NewRequest(http.MethodGet, "/api/local/projects/proj-1/assistant/conversations", nil)
	rec := httptest.NewRecorder()

	handled := handleLocalAssistantRoutes(rec, req, mgr, config.Config{}, nil, session)
	if !handled {
		t.Fatal("expected route to be handled")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var res struct {
		Conversations []assistant.ConversationSummary `json:"conversations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Conversations) != 1 || res.Conversations[0].ID != "conv-1" {
		t.Fatalf("expected only conv-1 for proj-1, got: %+v", res.Conversations)
	}

	// 2. Get conversation for proj-1 -> returns conv-1 with messages and progress
	reqConv := httptest.NewRequest(http.MethodGet, "/api/local/projects/proj-1/assistant/conversations/conv-1", nil)
	recConv := httptest.NewRecorder()
	if !handleLocalAssistantRoutes(recConv, reqConv, mgr, config.Config{}, nil, session) {
		t.Fatal("expected route to be handled")
	}
	if recConv.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recConv.Code, recConv.Body.String())
	}
	var convDetail assistant.StoredConversation
	if err := json.Unmarshal(recConv.Body.Bytes(), &convDetail); err != nil {
		t.Fatal(err)
	}
	if convDetail.ID != "conv-1" || len(convDetail.Messages) < 2 {
		t.Fatalf("unexpected conversation detail: %+v", convDetail)
	}

	// 3. Project isolation: accessing conv-2 through proj-1 returns 404
	reqCross := httptest.NewRequest(http.MethodGet, "/api/local/projects/proj-1/assistant/conversations/conv-2", nil)
	recCross := httptest.NewRecorder()
	if !handleLocalAssistantRoutes(recCross, reqCross, mgr, config.Config{}, nil, session) {
		t.Fatal("expected route to be handled")
	}
	if recCross.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-project conversation, got %d", recCross.Code)
	}
}

func TestAssistantAPI_Delete_RequiresSessionAndDeletes(t *testing.T) {
	mgr, store, validSession := setupTestAssistantAPI(t)
	defer mgr.Close()

	turn := assistant.Turn{
		ID:             "turn-del",
		ConversationID: "conv-del",
		ProviderID:     "codex",
		ProjectID:      "proj-1",
		State:          "succeeded",
		StartedAt:      time.Now().UTC(),
	}
	_ = store.SaveTurn(turn, "prompt to delete", "thread-del")

	// 1. Delete without session -> 401
	reqUnauth := httptest.NewRequest(http.MethodDelete, "/api/local/projects/proj-1/assistant/conversations/conv-del", nil)
	recUnauth := httptest.NewRecorder()
	handleLocalAssistantRoutes(recUnauth, reqUnauth, mgr, config.Config{}, nil, validSession)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without session header, got %d", recUnauth.Code)
	}

	// 2. Delete with valid session -> 200
	reqAuth := httptest.NewRequest(http.MethodDelete, "/api/local/projects/proj-1/assistant/conversations/conv-del", nil)
	reqAuth.Header.Set("X-Local-Session", validSession)
	reqAuth.Header.Set("Idempotency-Key", "idem-del")
	recAuth := httptest.NewRecorder()
	handleLocalAssistantRoutes(recAuth, reqAuth, mgr, config.Config{}, nil, validSession)
	if recAuth.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recAuth.Code, recAuth.Body.String())
	}

	// 3. Subsequent GET returns 404
	reqGet := httptest.NewRequest(http.MethodGet, "/api/local/projects/proj-1/assistant/conversations/conv-del", nil)
	recGet := httptest.NewRecorder()
	handleLocalAssistantRoutes(recGet, reqGet, mgr, config.Config{}, nil, validSession)
	if recGet.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after deletion, got %d", recGet.Code)
	}
}

func TestAssistantAPI_Turn_NotFoundAndProgressPolling(t *testing.T) {
	provider := &blockingAssistantProvider{started: make(chan struct{}), release: make(chan struct{})}
	mgr, _, session := setupTestAssistantAPI(t, provider)
	defer mgr.Close()

	// 1. Non-existent turn -> 404
	reqNotFound := httptest.NewRequest(http.MethodGet, "/api/local/projects/proj-1/assistant/turns/turn-missing", nil)
	recNotFound := httptest.NewRecorder()
	handleLocalAssistantRoutes(recNotFound, reqNotFound, mgr, config.Config{}, nil, session)
	if recNotFound.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing turn, got %d", recNotFound.Code)
	}

	// 2. Poll a real running turn after its provider emitted progress.
	turn, err := mgr.StartTurn("proj-1", "fake", "conv-poll", "Prompt for polling")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("provider did not emit progress")
	}
	req := httptest.NewRequest(http.MethodGet, "/api/local/projects/proj-1/assistant/turns/"+turn.ID, nil)
	rec := httptest.NewRecorder()
	handleLocalAssistantRoutes(rec, req, mgr, config.Config{}, nil, session)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var polled assistant.Turn
	if err := json.Unmarshal(rec.Body.Bytes(), &polled); err != nil {
		t.Fatal(err)
	}
	if polled.State != "running" || len(polled.Progress) < 2 || polled.Progress[len(polled.Progress)-1].Tool != "deployments_list" {
		t.Fatalf("poll did not expose running progress: %+v", polled)
	}
	close(provider.release)
}

func TestAssistantAPI_RetryTurn_SessionAndRedaction(t *testing.T) {
	mgr, store, session := setupTestAssistantAPI(t)
	defer mgr.Close()

	// 1. Retry without session -> 401
	reqUnauth := httptest.NewRequest(http.MethodPost, "/api/local/projects/proj-1/assistant/turns/turn-1/retry", nil)
	recUnauth := httptest.NewRecorder()
	handleLocalAssistantRoutes(recUnauth, reqUnauth, mgr, config.Config{}, nil, session)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without session, got %d", recUnauth.Code)
	}

	// 2. Retry without Idempotency-Key -> 400
	reqNoIdem := httptest.NewRequest(http.MethodPost, "/api/local/projects/proj-1/assistant/turns/turn-1/retry", nil)
	reqNoIdem.Header.Set("X-Local-Session", session)
	recNoIdem := httptest.NewRecorder()
	handleLocalAssistantRoutes(recNoIdem, reqNoIdem, mgr, config.Config{}, nil, session)
	if recNoIdem.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without idempotency key, got %d", recNoIdem.Code)
	}

	// 3. Retry on non-existent turn -> 404
	reqMissing := httptest.NewRequest(http.MethodPost, "/api/local/projects/proj-1/assistant/turns/turn-ghost/retry", nil)
	reqMissing.Header.Set("X-Local-Session", session)
	reqMissing.Header.Set("Idempotency-Key", "idem-missing")
	recMissing := httptest.NewRecorder()
	handleLocalAssistantRoutes(recMissing, reqMissing, mgr, config.Config{}, nil, session)
	if recMissing.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing turn, got %d: %s", recMissing.Code, recMissing.Body.String())
	}

	// 4. Retry on turn whose prompt was redacted -> 409 CANNOT_RETRY_REDACTED_PROMPT
	secretTurn := assistant.Turn{
		ID:             "turn-secret",
		ConversationID: "conv-secret",
		ProviderID:     "codex",
		ProjectID:      "proj-1",
		State:          "failed",
		StartedAt:      time.Now().UTC(),
	}
	_ = store.SaveTurn(secretTurn, "Analyze with opsi_pat_leak_12345", "thread-secret")

	reqRedacted := httptest.NewRequest(http.MethodPost, "/api/local/projects/proj-1/assistant/turns/turn-secret/retry", nil)
	reqRedacted.Header.Set("X-Local-Session", session)
	reqRedacted.Header.Set("Idempotency-Key", "idem-secret")
	recRedacted := httptest.NewRecorder()
	handleLocalAssistantRoutes(recRedacted, reqRedacted, mgr, config.Config{}, func() (keychain.Store, error) { return nil, nil }, session)
	if recRedacted.Code != http.StatusConflict {
		t.Fatalf("expected 409 conflict for redacted turn, got %d: %s", recRedacted.Code, recRedacted.Body.String())
	}
	if !strings.Contains(recRedacted.Body.String(), "CANNOT_RETRY_REDACTED_PROMPT") {
		t.Fatalf("expected CANNOT_RETRY_REDACTED_PROMPT code, got: %s", recRedacted.Body.String())
	}
}
