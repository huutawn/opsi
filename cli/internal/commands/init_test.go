package commands

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
)

type initGitRunner struct {
	root string
}

func (r initGitRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	joined := strings.Join(append([]string{name}, args...), " ")
	if strings.Contains(joined, "rev-parse --show-toplevel") {
		return []byte(r.root + "\n"), nil
	}
	if strings.Contains(joined, "remote get-url origin") {
		return []byte("git@github.com:owner/repo.git\n"), nil
	}
	return nil, fmt.Errorf("unexpected command %s", joined)
}

type fakeCloudState struct {
	mu              sync.Mutex
	repositoryReady bool
	binding         *cloudclient.GitHubBinding
	posts           []string
	callback        string
	localState      string
	grant           string
	installation    bool
	serviceMissing  bool
	denyAccess      bool
}

func (s *fakeCloudState) handler(response http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Authorization") != "Bearer test-pat" {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	s.mu.Lock()
	defer s.mu.Unlock()
	path := request.URL.Path
	switch {
	case request.Method == http.MethodGet && strings.HasSuffix(path, "/api/projects/proj-1/services"):
		if s.serviceMissing {
			_, _ = io.WriteString(response, `{"services":[]}`)
		} else {
			_, _ = io.WriteString(response, `{"services":[{"id":"svc-1","project_id":"proj-1","status":"active"}]}`)
		}
	case request.Method == http.MethodGet && strings.HasSuffix(path, "/github/repositories"):
		if s.repositoryReady {
			_, _ = io.WriteString(response, `{"repositories":[{"repository_id":123456,"installation_id":77,"full_name":"Owner/Repo","default_branch":"main","status":"active"}]}`)
		} else {
			_, _ = io.WriteString(response, `{"repositories":[]}`)
		}
	case request.Method == http.MethodGet && strings.HasSuffix(path, "/github/installations"):
		if s.installation {
			_, _ = io.WriteString(response, `{"installations":[{"installation_id":77,"status":"active","suspended":false}]}`)
		} else {
			_, _ = io.WriteString(response, `{"installations":[]}`)
		}
	case request.Method == http.MethodGet && strings.HasSuffix(path, "/github/bindings"):
		if s.binding == nil {
			_, _ = io.WriteString(response, `{"bindings":[]}`)
		} else {
			_ = json.NewEncoder(response).Encode(map[string]any{"bindings": []cloudclient.GitHubBinding{*s.binding}})
		}
	case request.Method == http.MethodPost && strings.HasSuffix(path, "/claim/start"):
		s.posts = append(s.posts, "installation-start")
		var body struct {
			Callback string `json:"local_callback"`
			State    string `json:"local_state"`
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		s.callback, s.localState, s.grant = body.Callback, body.State, "grant-value"
		callback, state, grant := s.callback, s.localState, s.grant
		go func() {
			target := callback + "?state=" + url.QueryEscape(state) + "&grant=" + url.QueryEscape(grant)
			result, err := http.Get(target)
			if err == nil {
				result.Body.Close()
			}
		}()
		_, _ = io.WriteString(response, `{"authorization_url":"https://github.com/login/oauth/authorize?opaque=1"}`)
	case request.Method == http.MethodPost && path == "/v1/github/installations/claim/redeem":
		s.posts = append(s.posts, "installation-redeem")
		var body struct {
			Grant string `json:"grant"`
			State string `json:"state"`
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		if body.Grant != s.grant || body.State != s.localState {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		s.installation = true
		if !s.denyAccess {
			s.repositoryReady = true
		}
		_, _ = io.WriteString(response, `{"installation":{"installation_id":77,"status":"active"},"repositories_synced":1}`)
	case request.Method == http.MethodPost && strings.HasSuffix(path, "/repositories/123456/claim"):
		s.posts = append(s.posts, "repository-claim")
		_, _ = io.WriteString(response, `{"repository_id":123456,"project_id":"proj-1","status":"active"}`)
	case request.Method == http.MethodPost && strings.HasSuffix(path, "/github/bindings"):
		s.posts = append(s.posts, "binding-create")
		var draft cloudclient.GitHubBinding
		_ = json.NewDecoder(request.Body).Decode(&draft)
		draft.ID, draft.ProjectID, draft.Status = "ghbind-1", "proj-1", "active"
		s.binding = &draft
		response.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(response).Encode(draft)
	default:
		http.Error(response, "not found", http.StatusNotFound)
	}
}

func newInitRoot(t *testing.T, root, cloudURL string, state *fakeCloudState) (*cobraHarness, *bytes.Buffer) {
	t.Helper()
	store := keychain.NewFakeStore()
	if err := store.SetPAT("test-pat"); err != nil {
		t.Fatal(err)
	}
	command := NewRootCommand(Options{
		Version: "test",
		KeychainFactory: func() (keychain.Store, error) {
			return store, nil
		},
		GitRunner:     initGitRunner{root: root},
		BrowserOpener: func(string) error { return errors.New("browser unavailable") },
	})
	buffer := bytes.NewBuffer(nil)
	command.SetOut(buffer)
	command.SetErr(buffer)
	args := []string{"init", "--project-id", "proj-1", "--service-id", "svc-1", "--service-key", "api", "--cloud-url", cloudURL, "--repo-dir", root, "--timeout", "5s"}
	command.SetArgs(args)
	return &cobraHarness{execute: command.Execute, setArgs: command.SetArgs, baseArgs: args, state: state}, buffer
}

type cobraHarness struct {
	execute  func() error
	setArgs  func([]string)
	baseArgs []string
	state    *fakeCloudState
}

func createRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestInitIntegrationAndIdempotentRerun(t *testing.T) {
	root := createRepository(t)
	state := &fakeCloudState{repositoryReady: true}
	server := httptest.NewServer(http.HandlerFunc(state.handler))
	defer server.Close()
	harness, output := newInitRoot(t, root, server.URL, state)
	if err := harness.execute(); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{defaultConfigPath, defaultWorkflowPath} {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil || len(content) == 0 {
			t.Fatalf("generated %s: %q err=%v", relative, content, err)
		}
	}
	state.mu.Lock()
	posts := append([]string(nil), state.posts...)
	state.mu.Unlock()
	if strings.Join(posts, ",") != "repository-claim,binding-create" {
		t.Fatalf("mutation order=%v", posts)
	}
	if strings.Contains(output.String(), "test-pat") || !strings.Contains(output.String(), "Repository: owner/repo (123456)") {
		t.Fatalf("unsafe or incomplete output: %s", output.String())
	}
	harness, _ = newInitRoot(t, root, server.URL, state)
	if err := harness.execute(); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.posts) != 2 {
		t.Fatalf("rerun created duplicate mutations: %v", state.posts)
	}
}

func TestInitDryRunAndValidationBeforeMutation(t *testing.T) {
	t.Run("dry-run", func(t *testing.T) {
		root := createRepository(t)
		state := &fakeCloudState{repositoryReady: true}
		server := httptest.NewServer(http.HandlerFunc(state.handler))
		defer server.Close()
		harness, output := newInitRoot(t, root, server.URL, state)
		harness.setArgs(append(harness.baseArgs, "--dry-run"))
		if err := harness.execute(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, defaultConfigPath)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("dry-run wrote file: %v", err)
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		if len(state.posts) != 0 || !json.Valid(output.Bytes()) {
			t.Fatalf("dry-run posts=%v output=%s", state.posts, output.String())
		}
	})
	t.Run("missing-dockerfile", func(t *testing.T) {
		root := t.TempDir()
		state := &fakeCloudState{repositoryReady: true}
		server := httptest.NewServer(http.HandlerFunc(state.handler))
		defer server.Close()
		harness, _ := newInitRoot(t, root, server.URL, state)
		if err := harness.execute(); err == nil {
			t.Fatal("missing Dockerfile accepted")
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		if len(state.posts) != 0 {
			t.Fatalf("mutation happened before file validation: %v", state.posts)
		}
	})
}

func TestInitBindingConflictAndOverwriteSafety(t *testing.T) {
	root := createRepository(t)
	if err := os.MkdirAll(filepath.Join(root, ".opsi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, defaultConfigPath), []byte("user content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := &fakeCloudState{repositoryReady: true}
	server := httptest.NewServer(http.HandlerFunc(state.handler))
	defer server.Close()
	harness, _ := newInitRoot(t, root, server.URL, state)
	if err := harness.execute(); err == nil || !strings.Contains(err.Error(), "--force --yes") {
		t.Fatalf("default overwrite error=%v", err)
	}
	state.mu.Lock()
	if len(state.posts) != 0 {
		t.Fatalf("overwrite failure mutated Cloud: %v", state.posts)
	}
	state.binding = &cloudclient.GitHubBinding{ID: "other", ProjectID: "proj-1", ServiceID: "svc-1", RepositoryID: 999, ServiceKey: "api", ConfigPath: defaultConfigPath, Status: "active"}
	state.mu.Unlock()
	harness, _ = newInitRoot(t, root, server.URL, state)
	harness.setArgs(append(harness.baseArgs, "--force", "--yes"))
	if err := harness.execute(); err == nil || !strings.Contains(err.Error(), "different repository binding") {
		t.Fatalf("binding conflict error=%v", err)
	}
	content, _ := os.ReadFile(filepath.Join(root, defaultConfigPath))
	if string(content) != "user content\n" {
		t.Fatalf("conflict wrote file: %q", content)
	}
}

func TestInitMissingPATFailsBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("network called")
	})
	command := NewRootCommand(Options{
		Version: "test",
		KeychainFactory: func() (keychain.Store, error) {
			return keychain.NewFakeStore(), nil
		},
		HTTPClient: &http.Client{Transport: transport},
	})
	command.SetArgs([]string{"init", "--project-id", "proj-1", "--service-id", "svc-1", "--service-key", "api"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "opsi login --pat-file") || calls.Load() != 0 {
		t.Fatalf("error=%v calls=%d", err, calls.Load())
	}
}

func TestInitMissingServiceFailsBeforeMutation(t *testing.T) {
	root := createRepository(t)
	state := &fakeCloudState{repositoryReady: true, serviceMissing: true}
	server := httptest.NewServer(http.HandlerFunc(state.handler))
	defer server.Close()
	harness, _ := newInitRoot(t, root, server.URL, state)
	err := harness.execute()
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("error=%v", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.posts) != 0 {
		t.Fatalf("missing service caused mutation: %v", state.posts)
	}
}

func TestInitForceYesOverwritesAtomically(t *testing.T) {
	root := createRepository(t)
	if err := os.MkdirAll(filepath.Join(root, ".opsi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, defaultConfigPath), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := &fakeCloudState{repositoryReady: true}
	server := httptest.NewServer(http.HandlerFunc(state.handler))
	defer server.Close()
	harness, output := newInitRoot(t, root, server.URL, state)
	harness.setArgs(append(harness.baseArgs, "--force", "--yes"))
	if err := harness.execute(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, defaultConfigPath))
	if err != nil || !strings.HasPrefix(string(content), "# Generated by opsi init") {
		t.Fatalf("content=%q err=%v", content, err)
	}
	info, err := os.Stat(filepath.Join(root, defaultConfigPath))
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("mode=%v err=%v", info.Mode(), err)
	}
	if !strings.Contains(output.String(), "sha256") {
		t.Fatalf("overwrite hashes not reported: %s", output.String())
	}
}

func TestInstallationClaimRefreshesInventory(t *testing.T) {
	root := createRepository(t)
	state := &fakeCloudState{}
	server := httptest.NewServer(http.HandlerFunc(state.handler))
	defer server.Close()
	harness, output := newInitRoot(t, root, server.URL, state)
	harness.setArgs(append(harness.baseArgs, "--installation-id", "77"))
	if err := harness.execute(); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	posts := append([]string(nil), state.posts...)
	grant := state.grant
	state.mu.Unlock()
	if strings.Join(posts, ",") != "installation-start,installation-redeem,repository-claim,binding-create" {
		t.Fatalf("claim flow order=%v", posts)
	}
	if strings.Contains(output.String(), grant) || strings.Contains(output.String(), "test-pat") {
		t.Fatalf("claim secret leaked: %s", output.String())
	}
}

func TestInstallationWithoutRepositoryAccessFailsClearly(t *testing.T) {
	root := createRepository(t)
	state := &fakeCloudState{denyAccess: true}
	server := httptest.NewServer(http.HandlerFunc(state.handler))
	defer server.Close()
	harness, _ := newInitRoot(t, root, server.URL, state)
	harness.setArgs(append(harness.baseArgs, "--installation-id", "77"))
	err := harness.execute()
	if err == nil || !strings.Contains(err.Error(), "does not have access to owner/repo") {
		t.Fatalf("error=%v", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if strings.Contains(strings.Join(state.posts, ","), "repository-claim") || strings.Contains(strings.Join(state.posts, ","), "binding-create") {
		t.Fatalf("repository mutation happened without access: %v", state.posts)
	}
}

func TestClaimCallbackValidationAndOneTimeUse(t *testing.T) {
	callback := newClaimCallback("127.0.0.1:1234", "expected-state")
	for _, invalid := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "http://127.0.0.1:1234"+claimCallbackPath+"?state=expected-state&grant=value", nil),
		httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9999"+claimCallbackPath+"?state=expected-state&grant=value", nil),
		httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1234/wrong?state=expected-state&grant=value", nil),
	} {
		response := httptest.NewRecorder()
		callback.ServeHTTP(response, invalid)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid callback status=%d", response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1234"+claimCallbackPath+"?state=wrong&grant=value", nil)
	response := httptest.NewRecorder()
	callback.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("state mismatch status=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1234"+claimCallbackPath+"?state=expected-state", nil)
	response = httptest.NewRecorder()
	callback.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("missing grant status=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:1234"+claimCallbackPath+"?state=expected-state&grant=value", nil)
	response = httptest.NewRecorder()
	callback.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "value") || strings.Contains(response.Body.String(), "expected-state") {
		t.Fatalf("valid callback status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	callback.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("second callback status=%d", response.Code)
	}
}

func TestRandomStateIsStrongAndUnique(t *testing.T) {
	first, err := randomState(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomState(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) < 43 || strings.Contains(first, "=") {
		t.Fatalf("states first=%q second=%q", first, second)
	}
}

func TestInstallationClaimTimeoutAndNoBrowser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/claim/start") {
			_, _ = io.WriteString(response, `{"authorization_url":"https://github.com/authorize"}`)
			return
		}
		http.NotFound(response, request)
	}))
	defer server.Close()
	client, err := cloudclient.New(server.URL, "test-pat", "test", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	var opened atomic.Int32
	err = runInstallationClaim(ctx, io.Discard, client, Options{BrowserOpener: func(string) error { opened.Add(1); return nil }}, initOptions{ProjectID: "proj-1", InstallationID: 77, NoBrowser: true})
	if err == nil || !strings.Contains(err.Error(), "timed out") || opened.Load() != 0 {
		t.Fatalf("error=%v opened=%d", err, opened.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
