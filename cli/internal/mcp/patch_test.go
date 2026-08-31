package mcp

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestMCP03ExactSourcePatchValidationIsVirtualAndStaleSafe(t *testing.T) {
	f := setupComprehensiveFixture(t)
	defer f.server.Close()
	s := f.createServer(t, "")
	ctx := context.Background()

	contextResult, err := callMCPTool(ctx, s, "dependency_analysis_context", map[string]any{
		"project_id": f.projectID, "environment_id": "production", "application_id": "api",
	})
	if err != nil || contextResult.IsError {
		t.Fatalf("dependency context failed: %v %+v", err, contextResult)
	}
	var analysis DependencyAnalysisContext
	if err := json.Unmarshal([]byte(contextResult.Content[0].Text), &analysis); err != nil {
		t.Fatalf("decode dependency context: %v", err)
	}
	blob := gitOutput(t, f.repoRoot, "rev-parse", f.commitSHA+":services/api/main.go")
	proposal := validMCP03Proposal(f, analysis, blob)

	beforeStatus := gitOutput(t, f.repoRoot, "status", "--porcelain")
	beforeSource := gitOutput(t, f.repoRoot, "show", f.commitSHA+":services/api/main.go")
	result, err := callMCPTool(ctx, s, "validate_source_patch_proposal", map[string]any{"proposal": proposal})
	if err != nil || result.IsError {
		t.Fatalf("validate_source_patch_proposal failed: %v %+v", err, result)
	}
	var validation SourcePatchValidation
	if err := json.Unmarshal([]byte(result.Content[0].Text), &validation); err != nil {
		t.Fatalf("decode patch validation: %v", err)
	}
	if validation.Status != proposalStatusValid || validation.Action != proposalActionNone || validation.SourcePatchProposalHash == "" {
		t.Fatalf("unexpected patch validation: %+v", validation)
	}
	if !strings.Contains(strings.Join(validation.Impact, ","), "NEW_BUILD_RECORD_REQUIRED_IF_APPLIED") || !strings.Contains(strings.Join(validation.Impact, ","), "PATCH_HAS_NOT_BEEN_COMPILED_OR_EXECUTED") {
		t.Fatalf("missing required source-patch impact: %+v", validation.Impact)
	}
	if afterStatus := gitOutput(t, f.repoRoot, "status", "--porcelain"); afterStatus != beforeStatus {
		t.Fatalf("MCP-03 mutated source worktree: before=%q after=%q", beforeStatus, afterStatus)
	}
	if afterSource := gitOutput(t, f.repoRoot, "show", f.commitSHA+":services/api/main.go"); afterSource != beforeSource {
		t.Fatal("MCP-03 mutated the immutable source object")
	}

	noChange := proposal
	noChange.Files, noChange.Evidence = nil, nil
	noChange.Impact.AlternativeConfigurationOnlySolution = true
	noChangeResult, err := callMCPTool(ctx, s, "validate_source_patch_proposal", map[string]any{"proposal": noChange})
	if err != nil || noChangeResult.IsError || !strings.Contains(noChangeResult.Content[0].Text, `"status": "NO_SOURCE_CHANGE_PROPOSED"`) || !strings.Contains(noChangeResult.Content[0].Text, "CONFIGURATION_ONLY_SOLUTION_AVAILABLE") {
		t.Fatalf("no-source-change proposal was not recognized: %v %+v", err, noChangeResult)
	}

	stale := proposal
	stale.Files[0].BaseBlobSHA = strings.Repeat("0", 40)
	staleResult, err := callMCPTool(ctx, s, "validate_source_patch_proposal", map[string]any{"proposal": stale})
	if err != nil || staleResult.IsError || !strings.Contains(staleResult.Content[0].Text, `"status": "STALE"`) {
		t.Fatalf("base blob mismatch did not fail closed as stale: %v %+v", err, staleResult)
	}
}

func TestMCP03RejectsUnsafePatchData(t *testing.T) {
	base := SourcePatchProposal{
		ProjectID: "project", EnvironmentID: "environment", ApplicationID: "application",
		Provenance: SourcePatchProvenance{BuildRecordID: "build", SourceCommit: "abcdef1", ApplicationRoot: "api", AnalysisInputsHash: "hash"},
		Rationale:  SourcePatchRationale{ObservedSource: "observed", OpsiFacts: "fact", Inference: "inference"},
		Evidence:   []SourcePatchEvidence{},
		Impact:     SourcePatchProposedImpact{},
	}
	for name, file := range map[string]SourcePatchFile{
		"traversal": {Path: "../.ssh/id_rsa", BaseBlobSHA: strings.Repeat("a", 40), UnifiedDiff: "--- a/../.ssh/id_rsa\n+++ b/../.ssh/id_rsa\n@@ -1 +1 @@\n-a\n+b\n"},
		"generated": {Path: "dist/app.js", BaseBlobSHA: strings.Repeat("a", 40), UnifiedDiff: "--- a/dist/app.js\n+++ b/dist/app.js\n@@ -1 +1 @@\n-a\n+b\n"},
		"secret":    {Path: "main.go", BaseBlobSHA: strings.Repeat("a", 40), UnifiedDiff: "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-a\n+postgres://user:password@example/db\n"},
		"malformed": {Path: "main.go", BaseBlobSHA: strings.Repeat("a", 40), UnifiedDiff: "not a unified diff"},
	} {
		t.Run(name, func(t *testing.T) {
			proposal := base
			proposal.Files = []SourcePatchFile{file}
			parsed, err := parseSourcePatchProposal(map[string]any{"proposal": proposal})
			if err == nil && (name == "malformed" || name == "secret") {
				patch, parseErr := parseUnifiedDiff(parsed.Files[0].Path, parsed.Files[0].UnifiedDiff)
				err = parseErr
				if err == nil && name == "secret" && !patchAddsSecret(patch) {
					t.Fatal("credential-like added line was not detected")
				}
				if err == nil && name == "secret" {
					err = &DomainError{Code: ErrCodeSecretLiteralIntroduced}
				}
			}
			if err == nil {
				t.Fatal("unsafe patch data was accepted")
			}
		})
	}
}

func TestMCP03UnifiedDiffFormatMatrix(t *testing.T) {
	valid := "--- a/main.go\n+++ b/main.go\n@@ -1,2 +1,2 @@\n package main\n-old\n+new\n"
	validPatch, err := parseUnifiedDiff("main.go", valid)
	if err != nil {
		t.Fatalf("valid patch rejected: %v", err)
	}
	if _, err := virtualApply([]byte("package main\nold\n"), validPatch); err != nil {
		t.Fatalf("valid virtual apply failed: %v", err)
	}
	for name, diff := range map[string]string{
		"new file":       "--- /dev/null\n+++ b/main.go\n@@ -0,0 +1 @@\n+new\n",
		"delete file":    "--- a/main.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n",
		"rename":         "diff --git a/main.go b/renamed.go\n--- a/main.go\n+++ b/renamed.go\n@@ -1 +1 @@\n-old\n+new\n",
		"mode change":    "old mode 100644\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n",
		"binary":         "--- a/main.go\n+++ b/main.go\nGIT binary patch\n",
		"bad counts":     "--- a/main.go\n+++ b/main.go\n@@ -1,2 +1 @@\n-old\n+new\n",
		"malformed hunk": "--- a/main.go\n+++ b/main.go\n@@ -zero +1 @@\n-old\n+new\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseUnifiedDiff("main.go", diff); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
	if _, err := parseUnifiedDiff("main.go", "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-a\n+b\n@@ -1 +1 @@\n-a\n+c\n"); err == nil {
		t.Fatal("overlapping hunks were accepted")
	}
}

func TestMCP03PatchBoundsAndSecretMatrix(t *testing.T) {
	privateKey := "-----BEGIN " + "PRIVATE" + " KEY-----\\nprivate\\n-----END " + "PRIVATE" + " KEY-----"
	for name, literal := range map[string]string{
		"PAT":                 "github_pat_abcdefghijklmnopqrstuvwxyz123456",
		"agent token":         "opsi_agent_token_abcdefghijklmnopqrstuvwxyz",
		"PostgreSQL password": "DATABASE_PASSWORD=\"postgres-synthetic-password\"",
		"Valkey password":     "REDIS_PASSWORD=\"valkey-synthetic-password\"",
		"registry credential": "registry_auth_basicabcdefghijklmnopqrstuvwxyz",
		"private key":         privateKey,
		"credential URI":      "postgres://user:synthetic-password@db.example/opsi",
	} {
		t.Run(name, func(t *testing.T) {
			patch, err := parseUnifiedDiff("main.go", "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+"+literal+"\n")
			if err != nil {
				t.Fatalf("secret test patch malformed: %v", err)
			}
			if !patchAddsSecret(patch) {
				t.Fatalf("%s was not rejected", name)
			}
			preview := safePatchPreview(patch.normalized)
			if strings.Contains(preview, literal) {
				t.Fatalf("%s leaked into preview: %q", name, preview)
			}
		})
	}
	files := make([]SourcePatchFile, maxPatchFiles+1)
	for index := range files {
		files[index] = SourcePatchFile{Path: "main.go", BaseBlobSHA: strings.Repeat("a", 40), UnifiedDiff: "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n"}
	}
	proposal := SourcePatchProposal{
		ProjectID: "project", EnvironmentID: "environment", ApplicationID: "application",
		Provenance: SourcePatchProvenance{BuildRecordID: "build", SourceCommit: "abcdef1", ApplicationRoot: "api", AnalysisInputsHash: "hash"},
		Rationale:  SourcePatchRationale{ObservedSource: "observed", OpsiFacts: "facts", Inference: "inference"},
		Files:      files,
	}
	if _, err := parseSourcePatchProposal(map[string]any{"proposal": proposal}); err == nil {
		t.Fatal("file bound was not enforced")
	}
}

func validMCP03Proposal(f *comprehensiveFixture, analysis DependencyAnalysisContext, blob string) SourcePatchProposal {
	return SourcePatchProposal{
		ProjectID: f.projectID, EnvironmentID: "production", ApplicationID: "api",
		Provenance: SourcePatchProvenance{
			BuildRecordID: "br-api-accepted", SourceCommit: analysis.Source.CommitSHA, ApplicationRoot: analysis.Source.ApplicationRoot,
			AnalysisInputsHash: analysis.Authority.AnalysisInputsHash, DependencyProposalHash: "dependency-proposal-hash",
			DependencyProposalAnalysisInputsHash: analysis.Authority.AnalysisInputsHash,
		},
		Rationale: SourcePatchRationale{ObservedSource: "main.go reads APP_DATABASE_URL", OpsiFacts: "the advisory dependency proposal maps DATABASE_URL", Inference: "the application should read DATABASE_URL"},
		Files: []SourcePatchFile{{
			Path: "main.go", BaseBlobSHA: blob,
			UnifiedDiff: "--- a/main.go\n+++ b/main.go\n@@ -10,5 +10,5 @@\n func main() {\n \tdb := os.Getenv(\"DATABASE_URL\")\n-\tappDb := os.Getenv(\"APP_DATABASE_URL\")\n+\tappDb := os.Getenv(\"DATABASE_URL\")\n \tfmt.Println(\"Localhost endpoint:\", \"http://localhost:8080\")\n \tfmt.Printf(\"DB: %s, APP_DB: %s, cached: %s, %s\\n\", db, appDb, dbURL, cacheURL)\n",
		}},
		Evidence: []SourcePatchEvidence{{Type: "ENV_REFERENCE", File: "main.go", Line: 12, Symbol: "appDb", Reason: "current environment read"}},
		Impact:   SourcePatchProposedImpact{DependsOnUnappliedDependencyProposal: true},
	}
}

func gitOutput(t *testing.T, repoRoot string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repoRoot}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %s", args, output)
	}
	return strings.TrimSpace(string(output))
}
