package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEngineDeploySuccessRecordsAndCleansBuildDir(t *testing.T) {
	store := openTestStore(t)
	buildRoot := t.TempDir()
	fakes := &fakeAdapters{}
	engine := NewEngine(store, EngineConfig{Git: fakes, Builder: fakes, K3s: fakes, BuildRoot: buildRoot, RolloutTimeout: time.Second, PollInterval: time.Millisecond})
	var phases []string

	record, err := engine.Deploy(context.Background(), testRequest(), func(event *ProgressEvent) error {
		phases = append(phases, event.Phase)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != StatusSuccess || record.ImageTag == "" {
		t.Fatalf("unexpected record: %+v", record)
	}
	if _, err := os.Stat(filepath.Join(buildRoot, record.ProjectID, record.DeployID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected build dir cleanup, stat err=%v", err)
	}
	if phases[len(phases)-1] != PhaseSuccess {
		t.Fatalf("expected success phase, got %v", phases)
	}

	replay, err := engine.Deploy(context.Background(), testRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if replay.DeployID != record.DeployID {
		t.Fatalf("expected idempotent replay to reuse success record")
	}
}

func TestEngineDeployRollbackOnRolloutFailure(t *testing.T) {
	store := openTestStore(t)
	fakes := &fakeAdapters{watchErr: errors.New("rollout timeout")}
	engine := NewEngine(store, EngineConfig{Git: fakes, Builder: fakes, K3s: fakes, BuildRoot: t.TempDir(), RolloutTimeout: time.Millisecond, PollInterval: time.Millisecond})

	record, err := engine.Deploy(context.Background(), testRequest(), nil)
	if err == nil {
		t.Fatal("expected rollout error")
	}
	if record.Status != StatusRolledBack || !record.RollbackSafe || record.RollbackReason == "" || !fakes.rollbackCalled {
		t.Fatalf("expected rollback, record=%+v rollback=%v", record, fakes.rollbackCalled)
	}
	if fakes.watchCalls != 2 {
		t.Fatalf("expected rollout watch plus rollback verification, got %d", fakes.watchCalls)
	}
}

func TestEngineDeployBuildFailure(t *testing.T) {
	store := openTestStore(t)
	fakes := &fakeAdapters{buildErr: errors.New("build failed")}
	engine := NewEngine(store, EngineConfig{Git: fakes, Builder: fakes, K3s: fakes, BuildRoot: t.TempDir()})

	record, err := engine.Deploy(context.Background(), testRequest(), nil)
	if err == nil {
		t.Fatal("expected build error")
	}
	if record.Status != StatusFailed || record.Error != "build failed" {
		t.Fatalf("unexpected record: %+v", record)
	}
	if record.RollbackSafe || fakes.rollbackCalled {
		t.Fatalf("build failure must not rollback: %+v", record)
	}
}

func TestEngineDeployRollbackFailure(t *testing.T) {
	store := openTestStore(t)
	fakes := &fakeAdapters{watchErr: errors.New("rollout timeout"), rollbackErr: errors.New("undo failed")}
	engine := NewEngine(store, EngineConfig{Git: fakes, Builder: fakes, K3s: fakes, BuildRoot: t.TempDir()})

	record, err := engine.Deploy(context.Background(), testRequest(), nil)
	if err == nil {
		t.Fatal("expected rollback error")
	}
	if record.Status != StatusFailedAfterRollback {
		t.Fatalf("unexpected record: %+v", record)
	}
}

func TestEngineDeployRollbackVerificationFailure(t *testing.T) {
	store := openTestStore(t)
	fakes := &fakeAdapters{watchErrs: []error{errors.New("rollout timeout"), errors.New("old revision not ready")}}
	engine := NewEngine(store, EngineConfig{Git: fakes, Builder: fakes, K3s: fakes, BuildRoot: t.TempDir()})

	record, err := engine.Deploy(context.Background(), testRequest(), nil)
	if err == nil {
		t.Fatal("expected rollback verification error")
	}
	if record.Status != StatusFailedAfterRollback || record.Error != "rollout: rollout timeout; rollback verification: old revision not ready" {
		t.Fatalf("unexpected record: %+v", record)
	}
}

func TestEngineFailureRedactsSecretsInRecordAndProgress(t *testing.T) {
	store := openTestStore(t)
	secret := "secret-token-123"
	fakes := &fakeAdapters{buildErr: errors.New("push failed token=" + secret + " url=https://user:" + secret + "@example.test/repo.git")}
	engine := NewEngine(store, EngineConfig{Git: fakes, Builder: fakes, K3s: fakes, BuildRoot: t.TempDir()})
	var messages []string

	record, err := engine.Deploy(context.Background(), testRequest(), func(event *ProgressEvent) error {
		messages = append(messages, event.Message)
		return nil
	})
	if err == nil {
		t.Fatal("expected build error")
	}
	if contains(record.Error, secret) {
		t.Fatalf("record leaked secret: %q", record.Error)
	}
	for _, msg := range messages {
		if contains(msg, secret) {
			t.Fatalf("progress leaked secret: %q", msg)
		}
	}
}

func openTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "agent.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testRequest() Request {
	return Request{
		ProjectID:    "proj-dev",
		ServiceID:    "svc-api",
		ServiceName:  "api",
		ServiceType:  "backend",
		Service:      "api",
		RepoURL:      "https://example.test/repo.git",
		Branch:       "main",
		GitSHA:       "abcdef1234567890",
		Namespace:    "default",
		BuildContext: ".",
		Dockerfile:   "Dockerfile",
		ManifestPath: "k8s/deploy.yaml",
		ImageTag:     "api:abcdef123456",
		TriggeredBy:  "test",
	}
}

type fakeAdapters struct {
	watchErr       error
	watchErrs      []error
	watchCalls     int
	buildErr       error
	rollbackErr    error
	rollbackCalled bool
}

func (f *fakeAdapters) Clone(_ context.Context, _, _, _, dest string) error {
	path := filepath.Join(dest, "k8s")
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(path, "deploy.yaml"), []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  template:
    spec:
      containers:
        - name: api
          image: api:old
`), 0o600)
}
func (f *fakeAdapters) Build(context.Context, string, string, string) error         { return f.buildErr }
func (f *fakeAdapters) Push(context.Context, string) error                          { return nil }
func (f *fakeAdapters) Apply(context.Context, string, string, string, string) error { return nil }
func (f *fakeAdapters) WatchRollout(context.Context, string, string, time.Duration, time.Duration) error {
	f.watchCalls++
	if len(f.watchErrs) > 0 {
		err := f.watchErrs[0]
		f.watchErrs = f.watchErrs[1:]
		return err
	}
	if f.watchCalls > 1 {
		return nil
	}
	return f.watchErr
}
func (f *fakeAdapters) Rollback(context.Context, string, string) error {
	f.rollbackCalled = true
	return f.rollbackErr
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
