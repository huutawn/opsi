package deploy

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestExecAdaptersCallExpectedCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts are unix-only")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "commands.log")
	writeFakeCommand(t, dir, "git", logPath, `
if [ "$1" = "clone" ]; then last=""; for arg do last="$arg"; done; mkdir -p "$last"; fi
`)
	writeFakeCommand(t, dir, "docker", logPath, ``)
	writeFakeCommand(t, dir, "kubectl", logPath, ``)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx := context.Background()
	if err := (ExecGitClient{}).Clone(ctx, "https://example.test/repo.git", "main", "abc", filepath.Join(dir, "src")); err != nil {
		t.Fatal(err)
	}
	if err := (ExecBuilder{}).Build(ctx, dir, "Dockerfile", "api:abc"); err != nil {
		t.Fatal(err)
	}
	if err := (ExecBuilder{}).Push(ctx, "api:abc"); err != nil {
		t.Fatal(err)
	}
	if err := (KubectlAdapter{}).Apply(ctx, "k8s/deploy.yaml", "default", "api:abc"); err != nil {
		t.Fatal(err)
	}
	if err := (KubectlAdapter{}).WatchRollout(ctx, "api", "default", time.Minute, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := (KubectlAdapter{}).Rollback(ctx, "api", "default"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	for _, want := range []string{"git clone", "git checkout abc", "docker buildx build", "docker push api:abc", "kubectl apply", "kubectl rollout status", "kubectl rollout undo"} {
		if !strings.Contains(log, want) {
			t.Fatalf("missing %q in command log:\n%s", want, log)
		}
	}
}

func TestDryRunAdapters(t *testing.T) {
	ctx := context.Background()
	if err := (DryRunGitClient{}).Clone(ctx, "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := (DryRunBuilder{}).Build(ctx, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := (DryRunBuilder{}).Push(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := (DryRunK3sAdapter{}).Apply(ctx, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := (DryRunK3sAdapter{}).WatchRollout(ctx, "", "", time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := (DryRunK3sAdapter{}).Rollback(ctx, "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestRunInReportsCommandError(t *testing.T) {
	err := run(context.Background(), "sh", "-c", "echo boom; exit 7")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected command output in error, got %v", err)
	}
}

func writeFakeCommand(t *testing.T, dir, name, logPath, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\nprintf '%s %s\\n' \"" + name + "\" \"$*\" >> \"" + logPath + "\"\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}
