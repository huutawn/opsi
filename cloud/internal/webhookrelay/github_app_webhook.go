package webhookrelay

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	githubAppWebhookMaxBytes = 2 << 20
	githubDeliveryMaxBytes   = 128
	githubEventMaxBytes      = 64
	githubReplayTTL          = 24 * time.Hour
	githubReplayMaxEntries   = 10_000
)

var (
	ErrGitHubEventSinkUnavailable = errors.New("GitHub App event sink unavailable")
	ErrGitHubEventConflict        = errors.New("GitHub App event conflict")
)

type GitHubAppEventSink interface {
	HandleGitHubAppEvent(context.Context, GitHubAppEvent) error
}

type GitHubAppEvent struct {
	DeliveryID     string
	Event          string
	Action         string
	InstallationID int64
	AccountID      int64
	AccountLogin   string
	AccountType    string
	Repository     *GitHubRepository
	Added          []GitHubRepository
	Removed        []GitHubRepository
	ReceivedAt     time.Time
}

type GitHubRepository struct {
	ID            int64
	NodeID        string
	Name          string
	FullName      string
	Private       bool
	Archived      bool
	Disabled      bool
	DefaultBranch string
	OwnerID       int64
	OwnerLogin    string
}

type githubReplayState uint8

const (
	githubReplayInFlight githubReplayState = iota + 1
	githubReplayCompleted
)

type githubReplayEntry struct {
	state     githubReplayState
	expiresAt time.Time
}

type githubReplayStore struct {
	mu         sync.Mutex
	entries    map[string]githubReplayEntry
	ttl        time.Duration
	maxEntries int
	clock      func() time.Time
}

type githubReplayReservation int

const (
	githubReplayReserved githubReplayReservation = iota
	githubReplayDuplicateCompleted
	githubReplayDuplicateInFlight
	githubReplayFull
)

func newGitHubReplayStore(maxEntries int, ttl time.Duration, clock func() time.Time) *githubReplayStore {
	return &githubReplayStore{
		entries:    make(map[string]githubReplayEntry),
		ttl:        ttl,
		maxEntries: maxEntries,
		clock:      clock,
	}
}

func (s *githubReplayStore) now() time.Time {
	if s.clock != nil {
		return s.clock().UTC()
	}
	return time.Now().UTC()
}

func (s *githubReplayStore) reserve(deliveryID string) githubReplayReservation {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, entry := range s.entries {
		if !entry.expiresAt.After(now) {
			delete(s.entries, id)
		}
	}
	if entry, ok := s.entries[deliveryID]; ok {
		if entry.state == githubReplayCompleted {
			return githubReplayDuplicateCompleted
		}
		return githubReplayDuplicateInFlight
	}
	if len(s.entries) >= s.maxEntries {
		return githubReplayFull
	}
	s.entries[deliveryID] = githubReplayEntry{state: githubReplayInFlight, expiresAt: now.Add(s.ttl)}
	return githubReplayReserved
}

func (s *githubReplayStore) complete(deliveryID string) {
	s.mu.Lock()
	entry, ok := s.entries[deliveryID]
	if ok && entry.state == githubReplayInFlight {
		entry.state = githubReplayCompleted
		s.entries[deliveryID] = entry
	}
	s.mu.Unlock()
}

func (s *githubReplayStore) release(deliveryID string) {
	s.mu.Lock()
	if entry, ok := s.entries[deliveryID]; ok && entry.state == githubReplayInFlight {
		delete(s.entries, deliveryID)
	}
	s.mu.Unlock()
}

func (s *Server) SetGitHubAppEventSink(sink GitHubAppEventSink) {
	s.githubAppEventSink = sink
}

func (s *Server) handleGitHubAppWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	signature := r.Header.Get("X-Hub-Signature-256")
	if signature == "" {
		writeError(w, http.StatusUnauthorized, "GitHub App webhook signature is required")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, githubAppWebhookMaxBytes))
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "GitHub App webhook body exceeds limit")
			return
		}
		writeError(w, http.StatusBadRequest, "GitHub App webhook body is invalid")
		return
	}
	secret := s.Config.GitHubApp.WebhookSecret
	if secret == "" {
		writeError(w, http.StatusServiceUnavailable, "GitHub App webhook integration is unavailable")
		return
	}
	if !validGitHubAppWebhookSignature(secret, body, signature) {
		writeError(w, http.StatusUnauthorized, "GitHub App webhook signature is invalid")
		return
	}

	deliveryID := r.Header.Get("X-GitHub-Delivery")
	if err := validateGitHubDeliveryID(deliveryID); err != nil {
		writeError(w, http.StatusBadRequest, "GitHub App delivery ID is invalid")
		return
	}
	eventName := r.Header.Get("X-GitHub-Event")
	if err := validateGitHubEventName(eventName); err != nil {
		writeError(w, http.StatusBadRequest, "GitHub App event name is invalid")
		return
	}

	if eventName == "ping" {
		if err := parseGitHubPing(body); err != nil {
			writeError(w, http.StatusBadRequest, "GitHub App ping payload is invalid")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
		return
	}
	if !supportedGitHubAppEvent(eventName) {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "ignored"})
		return
	}

	event, supportedAction, err := parseGitHubAppEvent(eventName, deliveryID, body, s.clock())
	if err != nil {
		writeError(w, http.StatusBadRequest, "GitHub App webhook payload is invalid")
		return
	}
	if !supportedAction {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "ignored"})
		return
	}

	switch s.githubReplay.reserve(deliveryID) {
	case githubReplayDuplicateCompleted:
		writeJSON(w, http.StatusOK, map[string]any{"status": "duplicate", "duplicate": true})
		return
	case githubReplayDuplicateInFlight:
		writeError(w, http.StatusConflict, "GitHub App delivery is already being processed")
		return
	case githubReplayFull:
		writeError(w, http.StatusServiceUnavailable, "GitHub App replay protection is at capacity")
		return
	}

	if s.githubAppEventSink == nil {
		s.githubReplay.release(deliveryID)
		writeError(w, http.StatusServiceUnavailable, "GitHub App event sink is unavailable")
		return
	}
	if err := s.githubAppEventSink.HandleGitHubAppEvent(r.Context(), event); err != nil {
		s.githubReplay.release(deliveryID)
		switch {
		case errors.Is(err, ErrGitHubEventSinkUnavailable):
			writeError(w, http.StatusServiceUnavailable, "GitHub App webhook processing is unavailable")
		case errors.Is(err, ErrGitHubEventConflict):
			writeError(w, http.StatusConflict, "GitHub App webhook state conflict")
		default:
			writeError(w, http.StatusInternalServerError, "GitHub App event processing failed")
		}
		return
	}
	s.githubReplay.complete(deliveryID)
	writeJSON(w, http.StatusOK, map[string]any{"status": "processed"})
}

func validGitHubAppWebhookSignature(secret string, body []byte, signature string) bool {
	if len(signature) != len("sha256=")+sha256.Size*2 || !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	provided, err := hex.DecodeString(signature[len("sha256="):])
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(mac.Sum(nil), provided)
}

func validateGitHubDeliveryID(value string) error {
	if value == "" || len(value) > githubDeliveryMaxBytes {
		return fmt.Errorf("invalid delivery ID length")
	}
	for _, character := range []byte(value) {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' && character != '.' && character != ':' {
			return fmt.Errorf("delivery ID contains an unsafe character")
		}
	}
	return nil
}

func validateGitHubEventName(value string) error {
	if value == "" || len(value) > githubEventMaxBytes {
		return fmt.Errorf("invalid event name length")
	}
	for _, character := range []byte(value) {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' {
			return fmt.Errorf("invalid event name character")
		}
	}
	return nil
}

func supportedGitHubAppEvent(event string) bool {
	return event == "installation" || event == "installation_repositories" || event == "repository"
}

func parseGitHubPing(body []byte) error {
	var payload struct {
		Zen          string `json:"zen"`
		HookID       int64  `json:"hook_id"`
		Installation *struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	if payload.Installation != nil && payload.Installation.ID <= 0 {
		return fmt.Errorf("invalid installation ID")
	}
	return nil
}

type githubRepositoryPayload struct {
	ID            int64  `json:"id"`
	NodeID        string `json:"node_id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	Disabled      bool   `json:"disabled"`
	DefaultBranch string `json:"default_branch"`
	Owner         *struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	} `json:"owner"`
}

func parseGitHubAppEvent(eventName, deliveryID string, body []byte, receivedAt time.Time) (GitHubAppEvent, bool, error) {
	var actionEnvelope struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(body, &actionEnvelope); err != nil {
		return GitHubAppEvent{}, false, err
	}
	if !supportedGitHubAppAction(eventName, actionEnvelope.Action) {
		return GitHubAppEvent{}, false, nil
	}
	event := GitHubAppEvent{
		DeliveryID: deliveryID,
		Event:      eventName,
		Action:     actionEnvelope.Action,
		ReceivedAt: receivedAt.UTC(),
	}

	switch eventName {
	case "installation":
		var payload struct {
			Installation *struct {
				ID      int64 `json:"id"`
				Account *struct {
					ID    int64  `json:"id"`
					Login string `json:"login"`
					Type  string `json:"type"`
				} `json:"account"`
			} `json:"installation"`
		}
		if err := json.Unmarshal(body, &payload); err != nil || payload.Installation == nil || payload.Installation.Account == nil {
			return GitHubAppEvent{}, false, fmt.Errorf("missing installation identity")
		}
		account := payload.Installation.Account
		if payload.Installation.ID <= 0 || account.ID <= 0 || !validMetadata(account.Login, 255, true) || !validMetadata(account.Type, 64, true) {
			return GitHubAppEvent{}, false, fmt.Errorf("invalid installation identity")
		}
		event.InstallationID = payload.Installation.ID
		event.AccountID = account.ID
		event.AccountLogin = account.Login
		event.AccountType = account.Type
	case "installation_repositories":
		var payload struct {
			Installation *struct {
				ID int64 `json:"id"`
			} `json:"installation"`
			Added   []githubRepositoryPayload `json:"repositories_added"`
			Removed []githubRepositoryPayload `json:"repositories_removed"`
		}
		if err := json.Unmarshal(body, &payload); err != nil || payload.Installation == nil || payload.Installation.ID <= 0 || payload.Added == nil || payload.Removed == nil {
			return GitHubAppEvent{}, false, fmt.Errorf("invalid installation repositories payload")
		}
		added, err := parseGitHubRepositories(payload.Added)
		if err != nil {
			return GitHubAppEvent{}, false, err
		}
		removed, err := parseGitHubRepositories(payload.Removed)
		if err != nil {
			return GitHubAppEvent{}, false, err
		}
		event.InstallationID = payload.Installation.ID
		event.Added = added
		event.Removed = removed
	case "repository":
		var payload struct {
			Installation *struct {
				ID int64 `json:"id"`
			} `json:"installation"`
			Repository *githubRepositoryPayload `json:"repository"`
		}
		if err := json.Unmarshal(body, &payload); err != nil || payload.Installation == nil || payload.Installation.ID <= 0 || payload.Repository == nil {
			return GitHubAppEvent{}, false, fmt.Errorf("invalid repository payload")
		}
		repository, err := parseGitHubRepository(*payload.Repository)
		if err != nil {
			return GitHubAppEvent{}, false, err
		}
		event.InstallationID = payload.Installation.ID
		event.Repository = &repository
	}
	return event, true, nil
}

func supportedGitHubAppAction(eventName, action string) bool {
	switch eventName {
	case "installation":
		switch action {
		case "created", "deleted", "suspend", "unsuspend", "new_permissions_accepted":
			return true
		}
	case "installation_repositories":
		return action == "added" || action == "removed"
	case "repository":
		switch action {
		case "created", "renamed", "transferred", "archived", "unarchived", "deleted", "edited":
			return true
		}
	}
	return false
}

func parseGitHubRepositories(payloads []githubRepositoryPayload) ([]GitHubRepository, error) {
	repositories := make([]GitHubRepository, 0, len(payloads))
	seen := make(map[int64]struct{}, len(payloads))
	for _, payload := range payloads {
		repository, err := parseGitHubRepository(payload)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[repository.ID]; ok {
			return nil, fmt.Errorf("duplicate repository ID")
		}
		seen[repository.ID] = struct{}{}
		repositories = append(repositories, repository)
	}
	return repositories, nil
}

func parseGitHubRepository(payload githubRepositoryPayload) (GitHubRepository, error) {
	if payload.ID <= 0 || !validMetadata(payload.FullName, 512, false) || !validMetadata(payload.Name, 255, false) ||
		!validMetadata(payload.NodeID, 255, false) || !validMetadata(payload.DefaultBranch, 255, false) {
		return GitHubRepository{}, fmt.Errorf("invalid repository metadata")
	}
	repository := GitHubRepository{
		ID:            payload.ID,
		NodeID:        payload.NodeID,
		Name:          payload.Name,
		FullName:      payload.FullName,
		Private:       payload.Private,
		Archived:      payload.Archived,
		Disabled:      payload.Disabled,
		DefaultBranch: payload.DefaultBranch,
	}
	if payload.Owner != nil {
		if payload.Owner.ID <= 0 || !validMetadata(payload.Owner.Login, 255, false) {
			return GitHubRepository{}, fmt.Errorf("invalid repository owner")
		}
		repository.OwnerID = payload.Owner.ID
		repository.OwnerLogin = payload.Owner.Login
	}
	return repository, nil
}

func validMetadata(value string, maxBytes int, required bool) bool {
	if required && value == "" {
		return false
	}
	if len(value) > maxBytes || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return false
	}
	return true
}
