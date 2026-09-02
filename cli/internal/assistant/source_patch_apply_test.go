package assistant

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplySourcePatchWritesOnlyAttestedExactPreimage(t *testing.T) {
	root := t.TempDir()
	gitTest(t, root, "init")
	gitTest(t, root, "config", "user.email", "opsi@example.invalid")
	gitTest(t, root, "config", "user.name", "Opsi Test")
	if err := os.Mkdir(filepath.Join(root, "app"), 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "app", "main.go")
	if err := os.WriteFile(path, []byte("package main\nold\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, root, "add", "app/main.go")
	gitTest(t, root, "commit", "-m", "initial")
	head := strings.TrimSpace(gitTest(t, root, "rev-parse", "HEAD"))
	blob := strings.TrimSpace(gitTest(t, root, "rev-parse", "HEAD:app/main.go"))
	raw, _ := json.Marshal(map[string]any{
		"project_id": "project-1", "environment_id": "env-1", "application_id": "app-1",
		"provenance": map[string]any{"build_record_id": "build-1", "source_commit": head, "application_root": "app", "analysis_inputs_hash": strings.Repeat("a", 64)},
		"rationale":  map[string]any{"observed_source": "old value", "opsi_facts": "reviewed", "inference": "replace old"},
		"files":      []map[string]any{{"path": "main.go", "base_blob_sha": blob, "unified_diff": "--- a/main.go\n+++ b/main.go\n@@ -1,2 +1,2 @@\n package main\n-old\n+new\n"}},
		"evidence":   []map[string]any{{"type": "SOURCE", "file": "main.go", "line": 2, "reason": "old"}},
	})
	manager := NewManager()
	manager.SetRepositoryRoot(root)
	manager.turns["turn-1"] = Turn{ID: "turn-1", ProjectID: "project-1", State: "succeeded", SourcePatchProposals: []SourcePatchProposal{{ProjectID: "project-1", EnvironmentID: "env-1", ApplicationID: "app-1", SourceCommit: head, ApplicationRoot: "app", ProposalHash: "proposal-1", ValidationStatus: "VALID", Proposal: raw}}}
	receipt, err := manager.ApplySourcePatch(context.Background(), "project-1", "turn-1", "proposal-1", head)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "applied" || receipt.JournalID == "" {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "package main\nnew\n" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	replay, err := manager.ApplySourcePatch(context.Background(), "project-1", "turn-1", "proposal-1", head)
	if err != nil || !replay.Reused {
		t.Fatalf("expected idempotent replay, receipt=%+v err=%v", replay, err)
	}
	if _, err := manager.ApplySourcePatch(context.Background(), "project-1", "turn-1", "proposal-1", strings.Repeat("b", 40)); err == nil {
		t.Fatal("expected source commit mismatch")
	}
}

func gitTest(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}
