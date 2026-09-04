package assistant

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	MaxConversations    = 20
	MaxTotalMessages    = 200
	MaxHistorySizeBytes = 4 * 1024 * 1024 // 4 MiB
)

var (
	historyPrivKeyRegex = regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z ]+ )?PRIVATE KEY-----.*?-----END (?:[A-Z ]+ )?PRIVATE KEY-----`)
	historyTokenRegex   = regexp.MustCompile(`(?i)\b(?:opsi_(?:pat|agent_token)|ghp_[a-zA-Z0-9]+|github_pat_[a-zA-Z0-9_]+)[a-zA-Z0-9_\-\.]*`)
	historyBearerRegex  = regexp.MustCompile(`(?i)Bearer\s+[a-zA-Z0-9_.\-]+`)
	historyAuthHdrRegex = regexp.MustCompile(`(?i)(?:authorization|proxy-authorization):\s*[^\r\n]+`)
	historyCredURIRegex = regexp.MustCompile(`([a-zA-Z0-9+.-]+://)([^/\s:@]*):([^/\s:@]+)@([^\s"'\` + "`" + `]+)`)
	historyCredEnvRegex = regexp.MustCompile(`(?i)(password|secret|token|api_key|pat)\s*[:=]\s*["']?([^\s"',;]+)["']?`)
)

func redactSensitive(input string) (string, bool) {
	if input == "" {
		return "", false
	}
	redacted := false
	out := historyPrivKeyRegex.ReplaceAllStringFunc(input, func(string) string {
		redacted = true
		return "[REDACTED_PRIVATE_KEY]"
	})
	out = historyAuthHdrRegex.ReplaceAllStringFunc(out, func(string) string {
		redacted = true
		return "Authorization: [REDACTED]"
	})
	out = historyBearerRegex.ReplaceAllStringFunc(out, func(string) string {
		redacted = true
		return "Bearer [REDACTED]"
	})
	out = historyTokenRegex.ReplaceAllStringFunc(out, func(string) string {
		redacted = true
		return "[REDACTED_TOKEN]"
	})
	out = historyCredURIRegex.ReplaceAllStringFunc(out, func(m string) string {
		redacted = true
		return historyCredURIRegex.ReplaceAllString(m, "$1$2:[REDACTED]@$4")
	})
	out = historyCredEnvRegex.ReplaceAllStringFunc(out, func(m string) string {
		redacted = true
		return historyCredEnvRegex.ReplaceAllString(m, "$1=[REDACTED]")
	})
	return strings.TrimSpace(out), redacted
}

type StoredMessage struct {
	ID             string             `json:"id"`
	TurnID         string             `json:"turn_id"`
	Role           string             `json:"role"` // "user" | "assistant"
	Text           string             `json:"text"`
	Redacted       bool               `json:"redacted,omitempty"`
	Grounding      *GroundingMetadata `json:"grounding,omitempty"`
	Progress       []ProgressEvent    `json:"progress,omitempty"`
	ErrorCode      string             `json:"error_code,omitempty"`
	DiagnosticCode string             `json:"diagnostic_code,omitempty"`
	Error          string             `json:"error,omitempty"`
	NextAction     string             `json:"next_action,omitempty"`
	State          string             `json:"state,omitempty"` // "running" | "succeeded" | "failed"
	CreatedAt      time.Time          `json:"created_at"`
}

type StoredConversation struct {
	ID            string          `json:"id"`
	ProjectID     string          `json:"project_id"`
	ProviderID    string          `json:"provider_id"`
	Title         string          `json:"title"`
	ThreadID      string          `json:"thread_id,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	LastTurnState string          `json:"last_turn_state,omitempty"`
	Messages      []StoredMessage `json:"messages"`
}

type ConversationSummary struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	ProviderID    string    `json:"provider_id"`
	Title         string    `json:"title"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	MessageCount  int       `json:"message_count"`
	LastTurnState string    `json:"last_turn_state,omitempty"`
}

type HistoryFile struct {
	Version       int                  `json:"version"`
	Conversations []StoredConversation `json:"conversations"`
}

type HistoryStore struct {
	mu       sync.RWMutex
	baseDir  string
	filePath string
	file     HistoryFile
}

func NewHistoryStore(baseDir, configPath string) (*HistoryStore, error) {
	if strings.TrimSpace(baseDir) == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("user config directory: %w", err)
		}
		baseDir = filepath.Join(configDir, "opsi", "assistant-history")
	}

	canonicalConfigPath, err := filepath.Abs(filepath.Clean(configPath))
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	h := sha256.Sum256([]byte(canonicalConfigPath))
	configHash := hex.EncodeToString(h[:16])
	filePath := filepath.Join(baseDir, configHash+".json")

	store := &HistoryStore{
		baseDir:  baseDir,
		filePath: filePath,
		file:     HistoryFile{Version: 1, Conversations: []StoredConversation{}},
	}

	if err := store.load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("load assistant history: %w", err)
	}

	return store, nil
}

func (s *HistoryStore) BaseDir() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.baseDir
}

func (s *HistoryStore) FilePath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.filePath
}

func (s *HistoryStore) load() error {
	if info, err := os.Lstat(s.baseDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("assistant history directory is not a regular directory")
		}
		if info.Mode().Perm()&0o077 != 0 {
			return errors.New("assistant history directory permissions must be 0700 or stricter")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	info, err := os.Lstat(s.filePath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("assistant history path is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("assistant history file permissions must be 0600 or stricter")
	}
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}
	var file HistoryFile
	if err := json.Unmarshal(data, &file); err != nil {
		return err
	}
	if file.Version != 1 {
		return fmt.Errorf("unsupported assistant history version %d", file.Version)
	}
	if file.Conversations == nil {
		file.Conversations = []StoredConversation{}
	}
	s.file = file
	return nil
}

func (s *HistoryStore) SaveTurn(turn Turn, rawPrompt string, threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	original, err := cloneHistoryFile(s.file)
	if err != nil {
		return fmt.Errorf("snapshot assistant history: %w", err)
	}

	redactedPrompt, promptWasRedacted := redactSensitive(rawPrompt)
	redactedResponse, _ := redactSensitive(turn.Response)

	now := time.Now().UTC()
	var conv *StoredConversation
	for i := range s.file.Conversations {
		if s.file.Conversations[i].ID == turn.ConversationID && s.file.Conversations[i].ProjectID == turn.ProjectID {
			conv = &s.file.Conversations[i]
			break
		}
	}

	if conv == nil {
		title := redactedPrompt
		titleRunes := []rune(title)
		if len(titleRunes) > 60 {
			title = string(titleRunes[:60]) + "…"
		}
		newConv := StoredConversation{
			ID:         turn.ConversationID,
			ProjectID:  turn.ProjectID,
			ProviderID: turn.ProviderID,
			Title:      title,
			ThreadID:   threadID,
			CreatedAt:  now,
			UpdatedAt:  now,
			Messages:   []StoredMessage{},
		}
		s.file.Conversations = append(s.file.Conversations, newConv)
		conv = &s.file.Conversations[len(s.file.Conversations)-1]
	}

	if threadID != "" {
		conv.ThreadID = threadID
	}
	conv.UpdatedAt = now
	conv.LastTurnState = turn.State

	// Upsert user message for this turn
	userMsgIdx := -1
	for i, m := range conv.Messages {
		if m.TurnID == turn.ID && m.Role == "user" {
			userMsgIdx = i
			break
		}
	}
	if userMsgIdx >= 0 {
		conv.Messages[userMsgIdx].Text = redactedPrompt
		conv.Messages[userMsgIdx].Redacted = promptWasRedacted
	} else {
		conv.Messages = append(conv.Messages, StoredMessage{
			ID:        fmt.Sprintf("msg-%s-user", turn.ID),
			TurnID:    turn.ID,
			Role:      "user",
			Text:      redactedPrompt,
			Redacted:  promptWasRedacted,
			CreatedAt: turn.StartedAt,
		})
	}

	// Upsert assistant message for this turn
	assistantMsgIdx := -1
	for i, m := range conv.Messages {
		if m.TurnID == turn.ID && m.Role == "assistant" {
			assistantMsgIdx = i
			break
		}
	}

	var grounding *GroundingMetadata
	if turn.Grounding.Status != "" {
		g := turn.Grounding
		g.Status = redactDiagnostic(g.Status)
		safeTools := make([]string, 0, len(g.Tools))
		for _, tool := range g.Tools {
			if isAllowedOpsiTool(tool) {
				safeTools = append(safeTools, tool)
			}
		}
		g.Tools = safeTools
		grounding = &g
	}

	createdAt := now
	if assistantMsgIdx >= 0 {
		createdAt = conv.Messages[assistantMsgIdx].CreatedAt
	}
	safeProgress := make([]ProgressEvent, 0, len(turn.Progress))
	for _, progress := range turn.Progress {
		progress.Summary = redactDiagnostic(progress.Summary)
		progress.Code = redactDiagnostic(progress.Code)
		if !isAllowedOpsiTool(progress.Tool) {
			progress.Tool = ""
		}
		safeProgress = append(safeProgress, progress)
	}
	safeError, _ := redactSensitive(turn.Error)
	safeNextAction, _ := redactSensitive(turn.NextAction)
	assistantMsg := StoredMessage{
		ID:             fmt.Sprintf("msg-%s-assistant", turn.ID),
		TurnID:         turn.ID,
		Role:           "assistant",
		Text:           redactedResponse,
		Grounding:      grounding,
		Progress:       safeProgress,
		ErrorCode:      redactDiagnostic(turn.ErrorCode),
		DiagnosticCode: redactDiagnostic(turn.DiagnosticCode),
		Redacted:       promptWasRedacted,
		Error:          safeError,
		NextAction:     safeNextAction,
		State:          turn.State,
		CreatedAt:      createdAt,
	}

	if assistantMsgIdx >= 0 {
		conv.Messages[assistantMsgIdx] = assistantMsg
	} else {
		conv.Messages = append(conv.Messages, assistantMsg)
	}

	s.enforceRetentionLocked()
	if err := s.validateBoundsLocked(); err != nil {
		s.file = original
		return err
	}
	if err := s.persistLocked(); err != nil {
		s.file = original
		return err
	}
	return nil
}

func (s *HistoryStore) ListConversations(projectID, providerID string) []ConversationSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []ConversationSummary
	for _, conv := range s.file.Conversations {
		if conv.ProjectID != projectID {
			continue
		}
		if providerID != "" && conv.ProviderID != providerID {
			continue
		}
		result = append(result, ConversationSummary{
			ID:            conv.ID,
			ProjectID:     conv.ProjectID,
			ProviderID:    conv.ProviderID,
			Title:         conv.Title,
			CreatedAt:     conv.CreatedAt,
			UpdatedAt:     conv.UpdatedAt,
			MessageCount:  len(conv.Messages),
			LastTurnState: conv.LastTurnState,
		})
	}

	// Sort newest first by UpdatedAt
	sort.Slice(result, func(i, j int) bool {
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	return result
}

func (s *HistoryStore) GetConversation(projectID, conversationID string) (*StoredConversation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, conv := range s.file.Conversations {
		if conv.ProjectID == projectID && conv.ID == conversationID {
			cCopy := conv
			return &cCopy, true
		}
	}
	return nil, false
}

func (s *HistoryStore) DeleteConversation(projectID, conversationID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, conv := range s.file.Conversations {
		if conv.ProjectID == projectID && conv.ID == conversationID {
			original, err := cloneHistoryFile(s.file)
			if err != nil {
				return false, fmt.Errorf("snapshot assistant history: %w", err)
			}
			s.file.Conversations = append(s.file.Conversations[:i], s.file.Conversations[i+1:]...)
			if err := s.persistLocked(); err != nil {
				s.file = original
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}

func (s *HistoryStore) RestoreThreads() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]string)
	for _, conv := range s.file.Conversations {
		if conv.ThreadID != "" {
			key := conv.ProjectID + "\x00" + conv.ProviderID + "\x00" + conv.ID
			result[key] = conv.ThreadID
		}
	}
	return result
}

func (s *HistoryStore) GetTurnPrompt(projectID, turnID string) (prompt string, redacted bool, found bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, conv := range s.file.Conversations {
		if conv.ProjectID != projectID {
			continue
		}
		for _, msg := range conv.Messages {
			if msg.TurnID == turnID && msg.Role == "user" {
				return msg.Text, msg.Redacted, true
			}
		}
	}
	return "", false, false
}

func (s *HistoryStore) enforceRetentionLocked() {
	// Prune completed conversations first if over MaxConversations
	for len(s.file.Conversations) > MaxConversations {
		idx := s.findOldestCompletedConversationIndex()
		if idx < 0 {
			break
		}
		s.file.Conversations = append(s.file.Conversations[:idx], s.file.Conversations[idx+1:]...)
	}

	// Prune completed conversations first if total messages > MaxTotalMessages
	for s.totalMessagesLocked() > MaxTotalMessages {
		idx := s.findOldestCompletedConversationIndex()
		if idx < 0 {
			break
		}
		s.file.Conversations = append(s.file.Conversations[:idx], s.file.Conversations[idx+1:]...)
	}

	// Prune if total size exceeds MaxHistorySizeBytes
	for {
		data, _ := json.Marshal(s.file)
		if len(data) <= MaxHistorySizeBytes || len(s.file.Conversations) == 0 {
			break
		}
		idx := s.findOldestCompletedConversationIndex()
		if idx < 0 {
			break
		}
		s.file.Conversations = append(s.file.Conversations[:idx], s.file.Conversations[idx+1:]...)
	}
}

func (s *HistoryStore) totalMessagesLocked() int {
	count := 0
	for _, conv := range s.file.Conversations {
		count += len(conv.Messages)
	}
	return count
}

func (s *HistoryStore) findOldestCompletedConversationIndex() int {
	oldestIdx := -1
	var oldestTime time.Time

	for i, conv := range s.file.Conversations {
		if isConversationCompleted(conv) {
			if oldestIdx == -1 || conv.UpdatedAt.Before(oldestTime) {
				oldestIdx = i
				oldestTime = conv.UpdatedAt
			}
		}
	}
	return oldestIdx
}

func (s *HistoryStore) validateBoundsLocked() error {
	if len(s.file.Conversations) > MaxConversations || s.totalMessagesLocked() > MaxTotalMessages {
		return errors.New("assistant history retention limit is occupied by running conversations")
	}
	data, err := json.Marshal(s.file)
	if err != nil {
		return fmt.Errorf("marshal assistant history bounds: %w", err)
	}
	if len(data) > MaxHistorySizeBytes {
		return errors.New("assistant history size limit is occupied by running conversations")
	}
	return nil
}

func isConversationCompleted(conv StoredConversation) bool {
	if len(conv.Messages) == 0 {
		return true
	}
	last := conv.Messages[len(conv.Messages)-1]
	return last.State != "running"
}

func redactDiagnostic(value string) string {
	redacted, _ := redactSensitive(value)
	const maxDiagnosticBytes = 2048
	if len(redacted) <= maxDiagnosticBytes {
		return redacted
	}
	for len(redacted) > maxDiagnosticBytes {
		_, size := utf8.DecodeLastRuneInString(redacted)
		redacted = redacted[:len(redacted)-size]
	}
	return redacted + "…"
}

func cloneHistoryFile(source HistoryFile) (HistoryFile, error) {
	data, err := json.Marshal(source)
	if err != nil {
		return HistoryFile{}, err
	}
	var cloned HistoryFile
	if err := json.Unmarshal(data, &cloned); err != nil {
		return HistoryFile{}, err
	}
	return cloned, nil
}

func (s *HistoryStore) persistLocked() error {
	if info, err := os.Lstat(s.baseDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("assistant history directory is not a regular directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect history dir: %w", err)
	}
	if err := os.MkdirAll(s.baseDir, 0700); err != nil {
		return fmt.Errorf("create history dir: %w", err)
	}
	if err := os.Chmod(s.baseDir, 0700); err != nil {
		return fmt.Errorf("chmod history dir: %w", err)
	}

	tmp, err := os.CreateTemp(s.baseDir, ".history-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp history file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp history file: %w", err)
	}

	data, err := json.MarshalIndent(s.file, "", "  ")
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("marshal history data: %w", err)
	}

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp history file: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp history file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp history file: %w", err)
	}

	if err := os.Rename(tmpName, s.filePath); err != nil {
		return fmt.Errorf("atomic rename history file: %w", err)
	}

	_ = os.Chmod(s.filePath, 0600)
	return nil
}
