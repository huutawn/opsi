package webhookrelay

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Server struct {
	Queue  *Queue
	Config Config
}

func NewServer(cfg Config) *Server {
	return &Server{Queue: NewQueue(), Config: cfg}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/v1/webhooks/github", s.handleGitHubWebhook)
	mux.HandleFunc("/v1/agents/", s.handleAgentWebhookNext)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "queued_webhooks": s.Queue.Len()})
}

func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var push githubPush
	if err := json.Unmarshal(body, &push); err != nil {
		writeError(w, http.StatusBadRequest, "invalid github payload")
		return
	}
	branch := branchFromRef(push.Ref)
	route, ok := s.matchRoute(push.Repository.CloneURL, push.Repository.FullName, branch)
	if !ok {
		writeError(w, http.StatusNotFound, "no relay route for repo/branch")
		return
	}
	now := time.Now().UTC()
	ttl := time.Duration(s.Config.TTL)
	if ttl <= 0 || ttl > 24*time.Hour {
		ttl = 24 * time.Hour
	}
	env := Envelope{
		ID:          newID(),
		ProjectID:   route.ProjectID,
		ServiceID:   route.ServiceID,
		ServiceName: route.ServiceName,
		ServiceType: route.ServiceType,
		RepoURL:     firstNonEmpty(route.RepoURL, push.Repository.CloneURL),
		Ref:         push.Ref,
		After:       push.After,
		Branch:      branch,
		TriggeredBy: push.Pusher.Name,
		Body:        string(body),
		Signature:   firstNonEmpty(r.Header.Get("X-Hub-Signature-256"), r.Header.Get("X-Hub-Signature")),
		ReceivedAt:  now,
		ExpiresAt:   now.Add(ttl),
	}
	if err := s.Queue.Enqueue(env); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": env.ID, "expires_at": env.ExpiresAt})
}

func (s *Server) handleAgentWebhookNext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/webhooks/next") {
		http.NotFound(w, r)
		return
	}
	wait := 30 * time.Second
	if raw := r.URL.Query().Get("wait"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid wait")
			return
		}
		wait = parsed
	}
	if wait > 30*time.Second {
		wait = 30 * time.Second
	}
	env, err := s.Queue.Next(r.Context(), r.URL.Query().Get("project_id"), wait)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if env == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, env)
}

func (s *Server) matchRoute(repoURL, fullName, branch string) (Route, bool) {
	for _, route := range s.Config.Routes {
		if route.Branch != "" && route.Branch != branch {
			continue
		}
		if route.RepoURL != "" && route.RepoURL == repoURL {
			return route, true
		}
		if route.RepoFullName != "" && route.RepoFullName == fullName {
			return route, true
		}
	}
	return Route{}, false
}

type githubPush struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Repository struct {
		CloneURL string `json:"clone_url"`
		FullName string `json:"full_name"`
	} `json:"repository"`
	Pusher struct {
		Name string `json:"name"`
	} `json:"pusher"`
}

func branchFromRef(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func newID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return fmt.Sprintf("webhook-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(data[:])
}
