package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/agent/internal/config"
)

func TestVersionDoesNotRequireConfigAndIncludesInjectedMetadata(t *testing.T) {
	originalVersion, originalCommit := version, commit
	version, commit = "1.2.3-test", "0123456789abcdef0123456789abcdef01234567"
	defer func() {
		version, commit = originalVersion, originalCommit
	}()

	var stdout, stderr bytes.Buffer
	called := 0
	code := run(context.Background(), []string{"--version"}, &stdout, &stderr, func(context.Context, config.Config, string, *slog.Logger) error {
		called++
		return nil
	})

	if code != 0 {
		t.Fatalf("run returned %d, stderr=%q", code, stderr.String())
	}
	if got, want := stdout.String(), "opsi-agent version=1.2.3-test commit=0123456789abcdef0123456789abcdef01234567\n"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
	if called != 0 {
		t.Fatalf("server called %d times", called)
	}
}

func TestCheckValidConfigDoesNotRunServer(t *testing.T) {
	path := writeConfig(t, "node_id: check-node\ntelemetry:\n  enabled: false\n")
	sqlitePath := filepath.Join(t.TempDir(), "must-not-exist.sqlite")
	appendConfig(t, path, "sqlite_path: "+sqlitePath+"\n")

	var stdout, stderr bytes.Buffer
	called := 0
	code := run(context.Background(), []string{"--config", path, "--check"}, &stdout, &stderr, func(context.Context, config.Config, string, *slog.Logger) error {
		called++
		return nil
	})

	if code != 0 {
		t.Fatalf("run returned %d, stderr=%q", code, stderr.String())
	}
	if got, want := stdout.String(), "configuration valid\n"; got != want {
		t.Fatalf("check output = %q, want %q", got, want)
	}
	if called != 0 {
		t.Fatalf("server called %d times", called)
	}
	if _, err := os.Stat(sqlitePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("check mode created SQLite state: %v", err)
	}
}

func TestRuntimeRequiresConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), nil, &stdout, &stderr, unusedServer)
	if code != 2 {
		t.Fatalf("run returned %d, stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--config is required") {
		t.Fatalf("missing usage error: %q", stderr.String())
	}
}

func TestMissingConfigReturnsFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	path := filepath.Join(t.TempDir(), "missing.yaml")
	code := run(context.Background(), []string{"--config", path}, &stdout, &stderr, unusedServer)
	if code != 1 {
		t.Fatalf("run returned %d, stderr=%q", code, stderr.String())
	}
}

func TestInvalidConfigReturnsFailure(t *testing.T) {
	path := writeConfig(t, "node_id: \"\"\n")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--config", path}, &stdout, &stderr, unusedServer)
	if code != 1 {
		t.Fatalf("run returned %d, stderr=%q", code, stderr.String())
	}
}

func TestPositionalArgumentIsRejected(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"unexpected"}, &stdout, &stderr, unusedServer)
	if code != 2 {
		t.Fatalf("run returned %d, stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "unexpected positional argument") {
		t.Fatalf("missing positional argument error: %q", stderr.String())
	}
}

func TestValidRuntimeConfigCallsServerOnceWithLoadedConfigAndVersion(t *testing.T) {
	path := writeConfig(t, "node_id: runtime-node\ntelemetry:\n  enabled: false\n")
	wantConfig, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	originalVersion := version
	version = "2.0.0"
	defer func() { version = originalVersion }()

	var stdout, stderr bytes.Buffer
	called := 0
	code := run(context.Background(), []string{"--config", path}, &stdout, &stderr, func(_ context.Context, gotConfig config.Config, gotVersion string, logger *slog.Logger) error {
		called++
		if !reflect.DeepEqual(gotConfig, wantConfig) {
			t.Errorf("server config = %#v, want %#v", gotConfig, wantConfig)
		}
		if gotVersion != "2.0.0" {
			t.Errorf("server version = %q, want pure version", gotVersion)
		}
		if logger == nil {
			t.Error("server logger is nil")
		}
		return nil
	})

	if code != 0 {
		t.Fatalf("run returned %d, stderr=%q", code, stderr.String())
	}
	if called != 1 {
		t.Fatalf("server called %d times", called)
	}
}

func TestServerErrorReturnsFailure(t *testing.T) {
	path := writeConfig(t, "node_id: runtime-node\ntelemetry:\n  enabled: false\n")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--config", path}, &stdout, &stderr, func(context.Context, config.Config, string, *slog.Logger) error {
		return errors.New("runtime failed")
	})
	if code != 1 {
		t.Fatalf("run returned %d, stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "runtime failed") {
		t.Fatalf("missing runtime error: %q", stderr.String())
	}
}

func TestHelpReturnsSuccess(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--help"}, &stdout, &stderr, unusedServer)
	if code != 0 {
		t.Fatalf("run returned %d, stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "usage: opsi-agent") {
		t.Fatalf("missing help output: %q", stderr.String())
	}
}

func TestConfigErrorDoesNotPrintConfigContents(t *testing.T) {
	const secret = "do-not-print-this-agent-token"
	path := writeConfig(t, "node_id: \"\"\ncloud_relay:\n  agent_token: "+secret+"\n")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--config", path}, &stdout, &stderr, unusedServer)
	if code != 1 {
		t.Fatalf("run returned %d, stderr=%q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), secret) {
		t.Fatalf("config contents leaked in error: %q", stderr.String())
	}
}

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func appendConfig(t *testing.T, path, contents string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(contents); err != nil {
		t.Fatal(err)
	}
}

func unusedServer(context.Context, config.Config, string, *slog.Logger) error {
	return errors.New("server must not be called")
}
