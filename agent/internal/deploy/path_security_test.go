package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeRelPathAllowsCleanRelativePath(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "apps", "api"), 0o700); err != nil {
		t.Fatal(err)
	}
	target, err := safeRelPath(base, "./apps/../apps/api")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(target, base) || filepath.Base(target) != "api" {
		t.Fatalf("unexpected target: %s", target)
	}
}

func TestSafeRelPathRejectsUnsafeInputs(t *testing.T) {
	base := t.TempDir()
	for _, input := range []string{
		"../../etc/passwd",
		"/etc/passwd",
		`..\..\windows`,
		"./subdir/../../../secret",
		"C:/Windows/System32",
	} {
		if _, err := safeRelPath(base, input); err == nil {
			t.Fatalf("expected %q to be rejected", input)
		}
	}
}

func TestSafeRelPathRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "symlink-to-outside")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := safeRelPath(base, "symlink-to-outside/secret"); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}
