package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cloud/internal/adminbootstrap"
)

func TestBootstrapOwnerHelpListsIdentityFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"admin", "bootstrap-owner", "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("help exit=%d stderr=%s", code, stderr.String())
	}
	for _, flag := range []string{"--config", "--email", "--org-name", "--project-name", "--oauth-provider", "--oauth-subject", "--pat-output-file", "--json"} {
		if !strings.Contains(stderr.String(), flag) {
			t.Fatalf("help missing %s: %s", flag, stderr.String())
		}
	}
}

func TestBootstrapOwnerValidationFailsBeforeDatabaseAccess(t *testing.T) {
	tests := [][]string{
		{"admin", "bootstrap-owner"},
		{"admin", "bootstrap-owner", "--config", "missing.json", "--email", "owner@example.com", "--org-name", "Org", "--project-name", "Project"},
	}
	for _, args := range tests {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code == 0 {
			t.Fatalf("expected failure for %v", args)
		}
	}
}

func TestVersionAndCheckRemainAvailable(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--version"}, &stdout, &stderr); code != 0 || strings.TrimSpace(stdout.String()) == "" {
		t.Fatalf("version exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--check"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "configuration valid") {
		t.Fatalf("check exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestPATOutputIsMode0600AndNeverOverwrites(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "opsi-pat-output-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	target := filepath.Join(dir, "initial-owner.pat")
	output, err := preparePATOutput(target)
	if err != nil {
		t.Fatal(err)
	}
	raw := "opsi_pat_sensitive_test_value"
	if err := output.write(raw); err != nil {
		t.Fatal(err)
	}
	if err := output.finalize(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("PAT mode=%o", info.Mode().Perm())
	}
	existing, err := preparePATOutput(target)
	if err != nil || !existing.existed {
		t.Fatalf("existing output was not preserved: output=%+v err=%v", existing, err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != raw {
		t.Fatalf("existing PAT changed: %q err=%v", data, err)
	}
}

func TestPATOutputCleanupRemovesTemporarySecret(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "opsi-pat-cleanup-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	target := filepath.Join(dir, "initial-owner.pat")
	output, err := preparePATOutput(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := output.write("opsi_pat_sensitive_test_value"); err != nil {
		t.Fatal(err)
	}
	temporary := output.temporary
	output.cleanup()
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatalf("temporary PAT survived cleanup: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target PAT unexpectedly exists: %v", err)
	}
}

func TestBootstrapOutputNeverContainsPATMaterial(t *testing.T) {
	var output bytes.Buffer
	writeBootstrapOwnerResult(&output, adminbootstrap.Result{UserID: "u", OrganizationID: "o", ProjectID: "p", MembershipRole: "Owner", PATCreated: true}, "/tmp/initial-owner.pat")
	if strings.Contains(output.String(), "opsi_pat_") {
		t.Fatalf("human output leaked PAT: %s", output.String())
	}
}
