// Package assistant owns the local AI-agent bridge. Project facts remain owned
// by the read-only Opsi MCP server; providers own their conversation history.
package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)
type Manager struct {
	mu           sync.RWMutex
	providers    map[string]Provider
	turns        map[string]Turn
	threads      map[string]string
	workspaces   map[string]string // conversationKey -> workspace dir (mode 0700)
	busy         map[string]bool
	turnOrder    []string
	defaultID    string
	repoRoot     string
	patchMu      sync.Mutex
	turnWG       sync.WaitGroup
	historyStore *HistoryStore
	historyErr   error
	ctx          context.Context
	cancel       context.CancelFunc
	closed       bool
}

// SetRepositoryRoot defines the only local worktree eligible for explicitly
// confirmed source patch application.
func (m *Manager) SetRepositoryRoot(root string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.repoRoot = strings.TrimSpace(root)
}

func (m *Manager) SetHistoryStore(store *HistoryStore) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.historyStore = store
	m.historyErr = nil
	if store != nil {
		for k, threadID := range store.RestoreThreads() {
			if _, exists := m.threads[k]; !exists {
				m.threads[k] = threadID
			}
		}
	}
}

func (m *Manager) SetHistoryError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.historyStore = nil
	m.historyErr = err
}

func (m *Manager) HistoryAvailable() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.historyErr == nil
}

func (m *Manager) HistoryStore() *HistoryStore {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.historyStore
}

func (m *Manager) ListConversations(projectID, providerID string) []ConversationSummary {
	m.mu.RLock()
	store := m.historyStore
	m.mu.RUnlock()
	if store == nil {
		return []ConversationSummary{}
	}
	return store.ListConversations(projectID, providerID)
}

func (m *Manager) GetConversation(projectID, conversationID string) (*StoredConversation, bool) {
	m.mu.RLock()
	store := m.historyStore
	m.mu.RUnlock()
	if store == nil {
		return nil, false
	}
	conversation, ok := store.GetConversation(projectID, conversationID)
	if ok {
		conversation.ThreadID = ""
	}
	return conversation, ok
}

func (m *Manager) DeleteConversation(projectID, conversationID string) (bool, error) {
	m.mu.Lock()
	store := m.historyStore
	conversationKey := ""
	if store != nil {
		if conversation, ok := store.GetConversation(projectID, conversationID); ok {
			conversationKey = projectID + "\x00" + conversation.ProviderID + "\x00" + conversationID
		}
	}
	for key, ws := range m.workspaces {
		parts := strings.Split(key, "\x00")
		if len(parts) == 3 && parts[0] == projectID && parts[2] == conversationID {
			_ = ws
			conversationKey = key
			if m.busy[key] {
				m.mu.Unlock()
				return false, errors.New("cannot delete a conversation while its turn is running")
			}
		}
	}
	m.mu.Unlock()

	if store == nil {
		return false, nil
	}
	deleted, err := store.DeleteConversation(projectID, conversationID)
	if err != nil || !deleted {
		return deleted, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if conversationKey != "" {
		if ws := m.workspaces[conversationKey]; ws != "" {
			if err := os.RemoveAll(ws); err != nil {
				return true, fmt.Errorf("remove conversation workspace: %w", err)
			}
		}
		delete(m.workspaces, conversationKey)
		delete(m.threads, conversationKey)
		delete(m.busy, conversationKey)
	}
	var remaining []string
	for _, tid := range m.turnOrder {
		if turn, ok := m.turns[tid]; ok && turn.ProjectID == projectID && turn.ConversationID == conversationID {
			delete(m.turns, tid)
		} else {
			remaining = append(remaining, tid)
		}
	}
	m.turnOrder = remaining
	return true, nil
}

func (m *Manager) RetryTurn(projectID, turnID string) (Turn, error) {
	projectID, turnID = strings.TrimSpace(projectID), strings.TrimSpace(turnID)
	m.mu.RLock()
	turn, exists := m.turns[turnID]
	store := m.historyStore
	m.mu.RUnlock()

	var prompt string
	var redacted bool
	var convID string
	var provID string

	if exists && turn.ProjectID == projectID {
		prompt = turn.Prompt
		redacted = turn.PromptRedacted
		convID = turn.ConversationID
		provID = turn.ProviderID
	} else if store != nil {
		p, r, found := store.GetTurnPrompt(projectID, turnID)
		if !found {
			return Turn{}, errors.New("assistant turn was not found")
		}
		prompt = p
		redacted = r
		for _, c := range store.ListConversations(projectID, "") {
			conv, foundConv := store.GetConversation(projectID, c.ID)
			if foundConv {
				for _, msg := range conv.Messages {
					if msg.TurnID == turnID {
						convID = conv.ID
						provID = conv.ProviderID
						break
					}
				}
			}
			if convID != "" {
				break
			}
		}
	} else {
		return Turn{}, errors.New("assistant turn was not found")
	}

	if redacted {
		return Turn{}, &AssistantError{
			Code:    "CANNOT_RETRY_REDACTED_PROMPT",
			Message: "cannot retry a turn whose prompt contained redacted credentials",
		}
	}
	if strings.TrimSpace(prompt) == "" {
		return Turn{}, errors.New("prompt for turn is empty")
	}

	_, wouldRedact := redactSensitive(prompt)
	if wouldRedact {
		return Turn{}, &AssistantError{
			Code:    "CANNOT_RETRY_REDACTED_PROMPT",
			Message: "cannot retry a turn whose prompt contained redacted credentials",
		}
	}

	return m.StartTurn(projectID, provID, convID, prompt)
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
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()

	m.turnWG.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()
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
	if m.closed {
		return Turn{}, errors.New("assistant manager is closed")
	}
	if m.historyErr != nil {
		return Turn{}, &AssistantError{
			Code:       "ASSISTANT_HISTORY_UNAVAILABLE",
			Message:    "local assistant history is unavailable",
			Retryable:  false,
			NextAction: "Repair or remove the invalid local assistant history file, then restart Opsi.",
		}
	}
	if providerID == "" {
		providerID = m.defaultID
	}
	provider := m.providers[providerID]
	if provider == nil {
		return Turn{}, errors.New("AI provider is not configured")
	}
	if conversationID == "" {
		var err error
		conversationID, err = randomAssistantID("conversation")
		if err != nil {
			return Turn{}, fmt.Errorf("create conversation id: %w", err)
		}
	}
	conversationKey := projectID + "\x00" + providerID + "\x00" + conversationID
	if m.busy[conversationKey] {
		return Turn{}, errors.New("conversation already has a running turn")
	}

	ws, ok := m.workspaces[conversationKey]
	createdWorkspace := false
	if !ok || ws == "" {
		newWS, err := os.MkdirTemp("", "opsi-assistant-conv-")
		if err != nil {
			return Turn{}, fmt.Errorf("create conversation workspace: %w", err)
		}
		_ = os.Chmod(newWS, 0700)
		ws = newWS
		m.workspaces[conversationKey] = ws
		createdWorkspace = true
	}

	_, wasRedacted := redactSensitive(prompt)
	turnID, err := randomAssistantID("turn")
	if err != nil {
		return Turn{}, fmt.Errorf("create turn id: %w", err)
	}
	turn := Turn{
		ID:             turnID,
		ConversationID: conversationID,
		ProviderID:     providerID,
		ProjectID:      projectID,
		State:          "running",
		Grounding:      GroundingMetadata{Status: "unverified", Tools: []string{}},
		Progress: []ProgressEvent{
			{
				Sequence:  1,
				Timestamp: time.Now().UTC(),
				Phase:     PhaseQueued,
				Summary:   "Queued for execution",
			},
		},
		Prompt:         prompt,
		PromptRedacted: wasRedacted,
		StartedAt:      time.Now().UTC(),
	}
	m.turns[turn.ID] = turn
	m.turnOrder = append(m.turnOrder, turn.ID)
	m.busy[conversationKey] = true
	m.pruneTurnsLocked()
	threadID := m.threads[conversationKey]
	if m.historyStore != nil {
		if err := m.historyStore.SaveTurn(turn, prompt, threadID); err != nil {
			delete(m.turns, turn.ID)
			m.turnOrder = m.turnOrder[:len(m.turnOrder)-1]
			delete(m.busy, conversationKey)
			if createdWorkspace {
				_ = os.RemoveAll(ws)
				delete(m.workspaces, conversationKey)
			}
			return Turn{}, fmt.Errorf("persist assistant turn: %w", err)
		}
	}
	m.turnWG.Add(1)
	go func() {
		defer m.turnWG.Done()
		m.runTurn(provider, conversationKey, threadID, ws, prompt, turn)
	}()
	return turn, nil
}

func randomAssistantID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(value[:]), nil
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

	onProgress := func(pe ProgressEvent) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if current, ok := m.turns[turn.ID]; ok {
			current.Progress = append(current.Progress, pe)
			if m.historyStore != nil {
				if err := m.historyStore.SaveTurn(current, prompt, m.threads[conversationKey]); err != nil {
					current.HistoryError = "Local chat history could not be saved."
				}
			}
			m.turns[turn.ID] = current
		}
	}

	result, err := provider.Run(ctx, RunRequest{
		ProjectID:  turn.ProjectID,
		Prompt:     prompt,
		ThreadID:   threadID,
		Workspace:  workspace,
		TurnID:     turn.ID,
		OnProgress: onProgress,
	})

	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.turns[turn.ID]; ok {
		turn = current
	}
	turn.FinishedAt = time.Now().UTC()
	if err != nil {
		turn.State = "failed"
		turn.Grounding.Status = "failed"
		var aErr *AssistantError
		if errors.As(err, &aErr) {
			turn.ErrorCode = aErr.Code
			turn.DiagnosticCode = aErr.DiagnosticCode
			turn.Error = redactDiagnostic(aErr.Message)
			turn.Retryable = aErr.Retryable
			turn.NextAction = redactDiagnostic(aErr.NextAction)
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			turn.ErrorCode = ErrAssistantTimeout
			turn.Error = "assistant turn timed out after 10 minutes"
		} else if errors.Is(ctx.Err(), context.Canceled) {
			turn.ErrorCode = ErrAssistantCanceled
			turn.Error = "assistant turn was canceled"
		} else {
			turn.ErrorCode = "ASSISTANT_TURN_FAILED"
			turn.Error = redactDiagnostic(err.Error())
		}

		if turn.NextAction != "" {
			// The provider supplied an authoritative safe recovery action.
		} else if turn.DiagnosticCode == "AUTH_REQUIRED" || strings.Contains(turn.Error, "AUTH_REQUIRED") {
			turn.NextAction = "Run 'opsi login' outside MCP to authenticate."
		} else if turn.ErrorCode == ErrAssistantMCPStartFailed {
			turn.NextAction = "Verify Opsi binary path and MCP server permissions."
		} else if turn.ErrorCode == ErrAssistantMCPApprovalBlocked {
			turn.NextAction = "Review Codex tool approval settings in your configuration."
		} else if turn.ErrorCode == ErrAssistantMCPEventInvalid {
			turn.NextAction = "Report this invalid Codex event and retry."
		} else if turn.ErrorCode == ErrAssistantProviderInvocationFailed {
			turn.NextAction = "Verify Codex CLI version and compatibility."
		}

		seq := len(turn.Progress) + 1
		turn.Progress = append(turn.Progress, ProgressEvent{
			Sequence:  seq,
			Timestamp: time.Now().UTC(),
			Phase:     PhaseFailed,
			Code:      turn.ErrorCode,
			Summary:   fmt.Sprintf("Turn failed: %s", turn.ErrorCode),
		})
	} else {
		turn.State = "succeeded"
		turn.Response = result.Text
		turn.ConfigurationProposals = result.ConfigurationProposals
		turn.SourcePatchProposals = result.SourcePatchProposals
		turn.Grounding = result.Grounding
		if result.ThreadID != "" {
			m.threads[conversationKey] = result.ThreadID
		}

		seq := len(turn.Progress) + 1
		turn.Progress = append(turn.Progress, ProgressEvent{
			Sequence:  seq,
			Timestamp: time.Now().UTC(),
			Phase:     PhaseSucceeded,
			Summary:   "Conversation turn completed",
		})
	}
	m.turns[turn.ID] = turn
	delete(m.busy, conversationKey)
	m.pruneTurnsLocked()

	if m.historyStore != nil {
		if err := m.historyStore.SaveTurn(turn, prompt, m.threads[conversationKey]); err != nil {
			turn.HistoryError = "Local chat history could not be saved."
			m.turns[turn.ID] = turn
		}
	}
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
