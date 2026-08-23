package mcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func setupTestGitRepo(t *testing.T) (string, string) {
	t.Helper()
	tempDir := t.TempDir()

	runGit := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = tempDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %s", args, string(out))
		}
		return strings.TrimSpace(string(out))
	}

	runGit("init", "--quiet")
	runGit("config", "user.name", "Test")
	runGit("config", "user.email", "test@example.com")

	// Create application directory structure
	appDir := filepath.Join(tempDir, "src", "backend")
	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 1. Regular text file with embedded credential (for redaction testing)
	secretPass := "SUPER_SECRET_DB_PASS_12345"
	secretToken := "VALKEY_TOKEN_SECRET_98765"
	mainCode := fmt.Sprintf(`package main
import "fmt"
const dbURL = "postgres://appuser:%s@db.internal:5432/production"
const redisURL = "redis://:%s@cache.internal:6379"
func main() {
	fmt.Println("Hello world")
}
`, secretPass, secretToken)
	if err := os.WriteFile(filepath.Join(appDir, "main.go"), []byte(mainCode), 0644); err != nil {
		t.Fatal(err)
	}

	// 2. Large file to test size bounds & truncation
	largeContent := strings.Repeat("This is a line of repeated text for size bounds testing.\n", 2000)
	if err := os.WriteFile(filepath.Join(appDir, "large.txt"), []byte(largeContent), 0644); err != nil {
		t.Fatal(err)
	}

	// 3. Binary file
	binaryData := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x00}
	if err := os.WriteFile(filepath.Join(appDir, "logo.png"), binaryData, 0644); err != nil {
		t.Fatal(err)
	}

	// 4. Another text file
	if err := os.WriteFile(filepath.Join(appDir, "config.json"), []byte(`{"port": 8080}`), 0644); err != nil {
		t.Fatal(err)
	}

	runGit("add", ".")
	runGit("commit", "-m", "Initial commit")
	commitSHA := runGit("rev-parse", "HEAD")

	return tempDir, commitSHA
}

func TestSourceService_ListFiles(t *testing.T) {
	repoRoot, commitSHA := setupTestGitRepo(t)
	svc := NewSourceService(nil)
	ctx := context.Background()

	res, err := svc.ListFiles(ctx, repoRoot, commitSHA, "src/backend", "", 50, "")
	if err != nil {
		t.Fatalf("ListFiles failed: %v", err)
	}

	if res.CommitSHA != commitSHA {
		t.Errorf("expected commit %s, got %s", commitSHA, res.CommitSHA)
	}
	if res.ApplicationRoot != "src/backend" {
		t.Errorf("expected applicationRoot 'src/backend', got %s", res.ApplicationRoot)
	}
	if res.TotalFiles < 4 {
		t.Errorf("expected at least 4 files, got %d", res.TotalFiles)
	}

	foundMain := false
	foundLogo := false
	for _, f := range res.Files {
		if f.Path == "main.go" {
			foundMain = true
			if f.IsBinary {
				t.Errorf("main.go should not be marked binary")
			}
		}
		if f.Path == "logo.png" {
			foundLogo = true
			if !f.IsBinary {
				t.Errorf("logo.png should be marked binary")
			}
		}
	}
	if !foundMain {
		t.Errorf("main.go not found in file list")
	}
	if !foundLogo {
		t.Errorf("logo.png not found in file list")
	}
}

func TestSourceService_ReadFile_PathTraversalProtection(t *testing.T) {
	repoRoot, commitSHA := setupTestGitRepo(t)
	svc := NewSourceService(nil)
	ctx := context.Background()

	traversalPaths := []string{
		"../outside.txt",
		"../../etc/passwd",
		"/etc/passwd",
		`C:\\outside.txt`,
		`..\\outside.txt`,
		"src/../../outside.txt",
		"..",
		".",
	}

	for _, p := range traversalPaths {
		_, err := svc.ReadFile(ctx, repoRoot, commitSHA, "src/backend", p, 65536)
		if err == nil {
			t.Errorf("expected error for path traversal attempt %q, got nil", p)
		}
	}
}

func TestSourceService_ReadFile_RedactionAndBounds(t *testing.T) {
	repoRoot, commitSHA := setupTestGitRepo(t)
	svc := NewSourceService(nil)
	ctx := context.Background()

	// 1. Test credential redaction in main.go
	res, err := svc.ReadFile(ctx, repoRoot, commitSHA, "src/backend", "main.go", 65536)
	if err != nil {
		t.Fatalf("ReadFile main.go failed: %v", err)
	}
	if !res.Redacted {
		t.Errorf("expected Redacted=true for main.go")
	}
	if strings.Contains(res.Content, "SUPER_SECRET_DB_PASS_12345") {
		t.Fatalf("SECURITY VIOLATION: secret leaked in ReadFile content: %s", res.Content)
	}
	if strings.Contains(res.Content, "VALKEY_TOKEN_SECRET_98765") {
		t.Fatalf("SECURITY VIOLATION: secret token leaked in ReadFile content: %s", res.Content)
	}
	if !strings.Contains(res.Content, "postgres://appuser:[REDACTED]@db.internal:5432/production") {
		t.Errorf("expected redacted postgres URI in content, got: %s", res.Content)
	}

	// 2. Test large file truncation
	largeRes, err := svc.ReadFile(ctx, repoRoot, commitSHA, "src/backend", "large.txt", 1024)
	if err != nil {
		t.Fatalf("ReadFile large.txt failed: %v", err)
	}
	if !largeRes.Truncated {
		t.Errorf("expected Truncated=true for large.txt with maxBytes=1024")
	}
	if len(largeRes.Content) > 1024 {
		t.Errorf("expected content length <= 1024, got %d", len(largeRes.Content))
	}

	// 3. Test binary file detection
	binRes, err := svc.ReadFile(ctx, repoRoot, commitSHA, "src/backend", "logo.png", 65536)
	if err != nil {
		t.Fatalf("ReadFile logo.png failed: %v", err)
	}
	if !binRes.IsBinary {
		t.Errorf("expected IsBinary=true for logo.png")
	}
	if binRes.Content != "" {
		t.Errorf("expected empty content for binary file, got length %d", len(binRes.Content))
	}

	// 4. Test non-existent commit
	_, err = svc.ReadFile(ctx, repoRoot, "0000000000000000000000000000000000000000", "src/backend", "main.go", 65536)
	if err == nil {
		t.Errorf("expected error for non-existent commit SHA, got nil")
	}
}

func TestSourceService_Search(t *testing.T) {
	repoRoot, commitSHA := setupTestGitRepo(t)
	svc := NewSourceService(nil)
	ctx := context.Background()

	// 1. Search for literal text
	res, err := svc.Search(ctx, repoRoot, commitSHA, "src/backend", "Hello world", "", 10)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(res.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(res.Matches))
	}
	if res.Matches[0].File != "main.go" {
		t.Errorf("expected match in main.go, got %s", res.Matches[0].File)
	}

	// 2. Search for credential snippet (must be redacted in match snippet)
	res, err = svc.Search(ctx, repoRoot, commitSHA, "src/backend", "appuser", "", 10)
	if err != nil {
		t.Fatalf("Search appuser failed: %v", err)
	}
	if len(res.Matches) == 0 {
		t.Fatalf("expected match for appuser")
	}
	if strings.Contains(res.Matches[0].MatchSnippet, "SUPER_SECRET_DB_PASS_12345") {
		t.Fatalf("SECURITY VIOLATION: secret password leaked in search snippet: %s", res.Matches[0].MatchSnippet)
	}
	if !strings.Contains(res.Matches[0].MatchSnippet, "[REDACTED]") {
		t.Errorf("expected [REDACTED] in search snippet, got: %s", res.Matches[0].MatchSnippet)
	}
}
