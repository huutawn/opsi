package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMCP02ProposalEvidenceIsBoundedAndRedacted(t *testing.T) {
	evidence, err := trimEvidence([]DependencyEvidence{{
		Type: "ENV_REFERENCE", File: "main.go", Line: 12, Reason: "database URL read",
		SafeExcerpt: `const value = "postgres://user:secret-password@db/app"`,
	}})
	if err != nil {
		t.Fatalf("trimEvidence failed: %v", err)
	}
	if strings.Contains(evidence[0].SafeExcerpt, "secret-password") || !strings.Contains(evidence[0].SafeExcerpt, "[REDACTED]") {
		t.Fatalf("evidence leaked a credential: %q", evidence[0].SafeExcerpt)
	}
}

func TestMCP02TargetResolutionDoesNotChooseAmbiguousTarget(t *testing.T) {
	targets := DependencyCompatibleTargets{ManagedResources: []DependencyResourceTarget{
		{ID: "resource-a", Protocol: "postgres"}, {ID: "resource-b", Protocol: "postgres"},
	}}
	resolution, ok := candidateTargetResolution(DependencyCandidate{DependencyKind: "managed_resource", Protocol: "postgres"}, targets)
	if ok || resolution != targetAmbiguous {
		t.Fatalf("expected explicit ambiguity, got resolution=%q ok=%v", resolution, ok)
	}
}

func TestMCP02ProposalRejectsOperationalFields(t *testing.T) {
	_, err := parseProposal(map[string]any{"proposal": map[string]any{
		"project_id": "project", "environment_id": "environment", "application_id": "application",
		"provenance": map[string]any{"source_commit": "abc1234", "application_root": "api", "analysis_inputs_hash": "hash"},
		"candidate":  map[string]any{"logical_name": "database", "dependency_kind": "managed_resource", "protocol": "postgres", "phase": "runtime", "required": true, "mappings": []any{}},
		"evidence":   []any{}, "confidence": "LOW", "apply": true,
	}})
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("expected unknown operational field rejection, got %v", err)
	}
}

func TestMCP02ContextIsExactSourceBoundAndStaleProposalsFailClosed(t *testing.T) {
	f := setupComprehensiveFixture(t)
	defer f.server.Close()
	s := f.createServer(t, "")
	ctx := context.Background()

	contextResult, err := callMCPTool(ctx, s, "dependency_analysis_context", map[string]any{
		"project_id": f.projectID, "environment_id": "production", "application_id": "api",
	})
	if err != nil || contextResult.IsError {
		t.Fatalf("dependency_analysis_context failed: %v %+v", err, contextResult)
	}
	var analysis DependencyAnalysisContext
	if err := json.Unmarshal([]byte(contextResult.Content[0].Text), &analysis); err != nil {
		t.Fatalf("decode analysis context: %v", err)
	}
	if analysis.Source.CommitSHA != f.commitSHA || analysis.Authority.AnalysisInputsHash == "" {
		t.Fatalf("context did not expose exact source provenance: %+v", analysis.Source)
	}
	if strings.Contains(contextResult.Content[0].Text, f.sourceSecret) || strings.Contains(contextResult.Content[0].Text, f.dbPassword) {
		t.Fatal("analysis context leaked a synthetic secret")
	}

	stale := DependencyProposal{
		ProjectID: f.projectID, EnvironmentID: "production", ApplicationID: "api",
		Provenance: DependencyProposalProvenance{SourceCommit: "deadbeef", ApplicationRoot: analysis.Source.ApplicationRoot, AnalysisInputsHash: analysis.Authority.AnalysisInputsHash},
		Candidate:  DependencyCandidate{LogicalName: "database", DependencyKind: "managed_resource", TargetID: "res-pg-100", Protocol: "postgres", Phase: "runtime", Required: true, Mappings: []DependencyInjectionMapping{{EnvName: "DATABASE_URL", SymbolicSource: "connection.url"}}},
		Evidence:   []DependencyEvidence{{Type: "ENV_REFERENCE", File: "main.go", Line: 11, Reason: "source reads DATABASE_URL"}}, Confidence: "HIGH",
	}
	validation, err := callMCPTool(ctx, s, "validate_dependency_proposal", map[string]any{"proposal": stale})
	if err != nil || validation.IsError {
		t.Fatalf("stale proposal call failed: %v %+v", err, validation)
	}
	if !strings.Contains(validation.Content[0].Text, `"status": "STALE"`) || !strings.Contains(validation.Content[0].Text, `"action": "NONE"`) {
		t.Fatalf("proposal did not fail closed: %s", validation.Content[0].Text)
	}
}
