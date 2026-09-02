package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/assistant"
	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
)

func handleLocalAssistantRoutes(w http.ResponseWriter, r *http.Request, manager *assistant.Manager, cfg config.Config, factory func() (keychain.Store, error), localSession string) bool {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 4 && parts[0] == "api" && parts[1] == "local" && parts[2] == "ai" && parts[3] == "providers" {
		if r.Method != http.MethodGet {
			writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
			return true
		}
		ctx, cancel := contextWithTimeout(r, 5*time.Second)
		defer cancel()
		w.Header().Set("Cache-Control", "no-store")
		writeLocalJSON(w, http.StatusOK, map[string]any{"providers": manager.ProviderStatuses(ctx), "mcp_surface": "mcp-04"})
		return true
	}
	if len(parts) < 6 || parts[0] != "api" || parts[1] != "local" || parts[2] != "projects" || parts[4] != "assistant" || parts[5] != "turns" {
		return false
	}
	projectID, err := url.PathUnescape(parts[3])
	if err != nil || strings.TrimSpace(projectID) == "" {
		writeLocalError(w, r, http.StatusBadRequest, "PROJECT_ID_REQUIRED", "project_id is required")
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	if len(parts) == 10 && parts[6] != "" && parts[7] == "source-patches" && parts[8] != "" && parts[9] == "apply" {
		if r.Method != http.MethodPost {
			writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
			return true
		}
		if !requireLocalSession(w, r, localSession) || !requireLocalIdempotencyKey(w, r) {
			return true
		}
		valid, _, _, identity := localPATStatus(r.Context(), cfg, factory, projectID)
		if !valid {
			writeLocalError(w, r, http.StatusUnauthorized, "CLOUD_PAT_REQUIRED", "a valid Cloud session is required to apply a local source patch")
			return true
		}
		if identity.Role == "viewer" {
			writeLocalError(w, r, http.StatusForbidden, "FORBIDDEN", "viewer role cannot apply a local source patch")
			return true
		}
		turnID, turnErr := url.PathUnescape(parts[6])
		proposalHash, hashErr := url.PathUnescape(parts[8])
		if turnErr != nil || hashErr != nil || strings.TrimSpace(turnID) == "" || strings.TrimSpace(proposalHash) == "" {
			writeLocalError(w, r, http.StatusBadRequest, "INVALID_SOURCE_PATCH_REQUEST", "turn and proposal hash are required")
			return true
		}
		var request struct {
			ConfirmedProposalHash string `json:"confirmed_proposal_hash"`
			ExpectedSourceCommit  string `json:"expected_source_commit"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, localRequestBodyLimit)).Decode(&request); err != nil || request.ConfirmedProposalHash != proposalHash {
			writeLocalError(w, r, http.StatusBadRequest, "INVALID_SOURCE_PATCH_REQUEST", "confirmed proposal hash is required")
			return true
		}
		receipt, err := manager.ApplySourcePatch(r.Context(), projectID, turnID, proposalHash, request.ExpectedSourceCommit)
		if err != nil {
			code := "SOURCE_PATCH_APPLY_FAILED"
			if assistantErr, ok := err.(*assistant.AssistantError); ok && assistantErr.Code != "" {
				code = assistantErr.Code
			}
			writeLocalError(w, r, http.StatusConflict, code, err.Error())
			return true
		}
		writeLocalJSON(w, http.StatusOK, receipt)
		return true
	}
	if len(parts) == 6 {
		if r.Method != http.MethodPost {
			writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
			return true
		}
		if !requireLocalSession(w, r, localSession) || !requireLocalIdempotencyKey(w, r) {
			return true
		}
		var request struct {
			ProviderID     string `json:"provider_id"`
			ConversationID string `json:"conversation_id"`
			Prompt         string `json:"prompt"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, localRequestBodyLimit)).Decode(&request); err != nil {
			writeLocalError(w, r, http.StatusBadRequest, "INVALID_ASSISTANT_REQUEST", "invalid assistant request")
			return true
		}
		turn, err := manager.StartTurn(projectID, request.ProviderID, request.ConversationID, request.Prompt)
		if err != nil {
			writeLocalError(w, r, http.StatusConflict, "ASSISTANT_TURN_REJECTED", err.Error())
			return true
		}
		writeLocalJSON(w, http.StatusAccepted, turn)
		return true
	}
	if len(parts) == 7 {
		if r.Method != http.MethodGet {
			writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
			return true
		}
		turnID, err := url.PathUnescape(parts[6])
		if err != nil || strings.TrimSpace(turnID) == "" {
			writeLocalError(w, r, http.StatusBadRequest, "TURN_ID_REQUIRED", "turn_id is required")
			return true
		}
		turn, ok := manager.Turn(projectID, turnID)
		if !ok {
			writeLocalError(w, r, http.StatusNotFound, "ASSISTANT_TURN_NOT_FOUND", "assistant turn was not found")
			return true
		}
		writeLocalJSON(w, http.StatusOK, turn)
		return true
	}
	writeLocalError(w, r, http.StatusNotFound, "LOCAL_ROUTE_NOT_FOUND", "local assistant route is not implemented")
	return true
}

func contextWithTimeout(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), timeout)
}
