package commands

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/opsi-dev/opsi/cli/internal/repository"
)

type localMutationRequest struct {
	ConfigPath   string               `json:"config_path"`
	WorkflowPath string               `json:"workflow_path"`
	Service      repository.ServiceV2 `json:"service"`
	Confirm      bool                 `json:"confirm"`
}

type localPlanRequest struct {
	ConfigPath string               `json:"config_path"`
	Event      repository.EventType `json:"event"`
	Base       string               `json:"base"`
	Head       string               `json:"head"`
}

func registerRepositoryCDRoutes(mux *http.ServeMux, localSession string) {
	root, rootErr := repository.Root(context.Background(), nil, ".")
	registerRepositoryCDRoutesAt(mux, localSession, root, rootErr, repository.CDService{})
}

func registerRepositoryCDRoutesAt(mux *http.ServeMux, localSession, root string, rootErr error, service repository.CDService) {
	requireRoot := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		if rootErr != nil || root == "" {
			writeLocalError(w, r, http.StatusUnprocessableEntity, "REPOSITORY_UNAVAILABLE", "Start opsi from inside the Git repository to manage repository CD intent")
			return "", false
		}
		return root, true
	}
	mux.HandleFunc("/api/local/repository/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
			return
		}
		repoRoot, ok := requireRoot(w, r)
		if !ok {
			return
		}
		configPath := strings.TrimSpace(r.URL.Query().Get("config_path"))
		if configPath == "" {
			configPath = defaultConfigPath
		}
		if configPath != defaultConfigPath {
			writeLocalError(w, r, http.StatusBadRequest, "CANONICAL_PATH_REQUIRED", "local repository API uses the canonical Opsi config path only")
			return
		}
		cfg, migrated, rendered, err := repository.LoadConfig(repoRoot, configPath)
		if err != nil {
			writeLocalError(w, r, http.StatusUnprocessableEntity, "CONFIG_INVALID", safeRepositoryError(repoRoot, err))
			return
		}
		writeLocalJSON(w, http.StatusOK, map[string]any{"config": cfg, "migrated_v1": migrated, "config_hash": repository.ConfigHash(rendered)})
	})
	mux.HandleFunc("/api/local/repository/config/preview", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
			return
		}
		repoRoot, ok := requireRoot(w, r)
		if !ok {
			return
		}
		var body localMutationRequest
		if !decodeLocalRepositoryJSON(w, r, &body) {
			return
		}
		defaultMutationPaths(&body)
		if !requireCanonicalMutationPaths(w, r, body) {
			return
		}
		preview, err := service.PreviewMutation(repository.MutationRequest{Repository: repoRoot, ConfigPath: body.ConfigPath, WorkflowPath: body.WorkflowPath, Service: body.Service, Force: true, Confirmed: true})
		if err != nil {
			writeLocalError(w, r, http.StatusUnprocessableEntity, "CONFIG_PREVIEW_FAILED", safeRepositoryError(repoRoot, err))
			return
		}
		writeLocalJSON(w, http.StatusOK, preview)
	})
	mux.HandleFunc("/api/local/repository/plan/preview", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
			return
		}
		repoRoot, ok := requireRoot(w, r)
		if !ok {
			return
		}
		var body localPlanRequest
		if !decodeLocalRepositoryJSON(w, r, &body) {
			return
		}
		if body.ConfigPath == "" {
			body.ConfigPath = defaultConfigPath
		}
		if body.ConfigPath != defaultConfigPath {
			writeLocalError(w, r, http.StatusBadRequest, "CANONICAL_PATH_REQUIRED", "local repository API uses the canonical Opsi config path only")
			return
		}
		cfg, _, _, err := repository.LoadConfig(repoRoot, body.ConfigPath)
		if err != nil {
			writeLocalError(w, r, http.StatusUnprocessableEntity, "CONFIG_INVALID", safeRepositoryError(repoRoot, err))
			return
		}
		plan, err := service.Plan(r.Context(), repository.PlanRequest{Repository: repoRoot, Config: cfg, Event: body.Event, Base: strings.TrimSpace(body.Base), Head: strings.TrimSpace(body.Head)})
		if err != nil {
			writeLocalError(w, r, http.StatusUnprocessableEntity, "PLAN_FAILED", safeRepositoryError(repoRoot, err))
			return
		}
		writeLocalJSON(w, http.StatusOK, plan)
	})
	mux.HandleFunc("/api/local/repository/workflow/preview", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
			return
		}
		repoRoot, ok := requireRoot(w, r)
		if !ok {
			return
		}
		configPath := strings.TrimSpace(r.URL.Query().Get("config_path"))
		if configPath == "" {
			configPath = defaultConfigPath
		}
		if configPath != defaultConfigPath {
			writeLocalError(w, r, http.StatusBadRequest, "CANONICAL_PATH_REQUIRED", "local repository API uses the canonical Opsi config path only")
			return
		}
		cfg, _, rendered, err := repository.LoadConfig(repoRoot, configPath)
		if err != nil {
			writeLocalError(w, r, http.StatusUnprocessableEntity, "CONFIG_INVALID", safeRepositoryError(repoRoot, err))
			return
		}
		writeLocalJSON(w, http.StatusOK, map[string]any{"workflow_yaml": string(repository.RenderWorkflow(cfg)), "config_hash": repository.ConfigHash(rendered)})
	})
	mux.HandleFunc("/api/local/repository/apply", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
			return
		}
		if !requireLocalSession(w, r, localSession) {
			return
		}
		repoRoot, ok := requireRoot(w, r)
		if !ok {
			return
		}
		var body localMutationRequest
		if !decodeLocalRepositoryJSON(w, r, &body) {
			return
		}
		defaultMutationPaths(&body)
		if !requireCanonicalMutationPaths(w, r, body) {
			return
		}
		if !body.Confirm {
			writeLocalError(w, r, http.StatusBadRequest, "CONFIRMATION_REQUIRED", "apply requires confirm=true after preview")
			return
		}
		preview, err := service.ApplyMutation(repository.MutationRequest{Repository: repoRoot, ConfigPath: body.ConfigPath, WorkflowPath: body.WorkflowPath, Service: body.Service, Force: true, Confirmed: true})
		if err != nil {
			writeLocalError(w, r, http.StatusUnprocessableEntity, "APPLY_FAILED", safeRepositoryError(repoRoot, err))
			return
		}
		writeLocalJSON(w, http.StatusOK, preview)
	})
}

func decodeLocalRepositoryJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeLocalError(w, r, http.StatusBadRequest, "INVALID_JSON", "request body is invalid")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeLocalError(w, r, http.StatusBadRequest, "INVALID_JSON", "request body must contain one JSON value")
		return false
	}
	return true
}

func defaultMutationPaths(body *localMutationRequest) {
	if body.ConfigPath == "" {
		body.ConfigPath = defaultConfigPath
	}
	if body.WorkflowPath == "" {
		body.WorkflowPath = defaultWorkflowPath
	}
}

func requireCanonicalMutationPaths(w http.ResponseWriter, r *http.Request, body localMutationRequest) bool {
	if body.ConfigPath != defaultConfigPath || body.WorkflowPath != defaultWorkflowPath {
		writeLocalError(w, r, http.StatusBadRequest, "CANONICAL_PATH_REQUIRED", "local repository mutations use canonical config and workflow paths only")
		return false
	}
	return true
}

func safeRepositoryError(root string, err error) string {
	text := strings.ReplaceAll(strings.TrimSpace(err.Error()), root, ".")
	text = strings.Map(func(value rune) rune {
		if value < 0x20 || value == 0x7f {
			return -1
		}
		return value
	}, text)
	if len(text) > 512 {
		text = text[:512]
	}
	return text
}
