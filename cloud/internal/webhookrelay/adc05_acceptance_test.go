package webhookrelay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/sourcescanner"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
	verificationv1 "github.com/opsi-dev/opsi/contracts/go/verificationv1"
)

// Section 6, 7, 8, 9, 10, 12: Source Scanner Bounds, Redaction, & Semantics
func TestADC05SourceRiskScannerAcceptance(t *testing.T) {
	tempDir := t.TempDir()
	appDir := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 1. Synthetic source with sensitive credentials
	secretPassword := "SUPER_SECRET_PASSWORD_12345"
	secretRedisPass := "REDIS_SECRET_TOKEN_999"
	badSource := fmt.Sprintf(`
// Config file
const dbUrl = "postgres://appuser:%s@db.internal:5432/production";
const redisUrl = "redis://:%s@cache.internal:6379";
const loopback = "http://localhost:3000/api";
const analytics = "https://analytics.example.com/event"; // Should not trigger same_origin

function getDb() {
    return process.env.APP_DATABASE_URL;
}
`, secretPassword, secretRedisPass)

	if err := os.WriteFile(filepath.Join(appDir, "index.js"), []byte(badSource), 0644); err != nil {
		t.Fatal(err)
	}

	deps := []sourcescanner.Dependency{
		{
			LogicalName:     "database",
			Protocol:        "postgres",
			Strategy:        "same_origin",
			AccessContext:   "browser",
			Path:            "/api",
			DeclaredEnvKeys: []string{"APP_DATABASE_URL"},
		},
	}

	limits := sourcescanner.DefaultLimits()
	ctx := context.Background()
	report := sourcescanner.Scan(ctx, tempDir, "src", deps, limits)

	// Invariant: AnalysisStatus must be complete
	if report.AnalysisStatus != sourcescanner.AnalysisStatusComplete {
		t.Fatalf("expected complete, got %s", report.AnalysisStatus)
	}

	// Invariant: Credential Redaction (Section 8)
	// Check report JSON and findings - secret passwords must appear ZERO times
	for _, f := range report.Findings {
		if strings.Contains(f.SafeEvidence, secretPassword) || strings.Contains(f.SafeEvidence, secretRedisPass) {
			t.Fatalf("SECURITY VIOLATION: finding safe_evidence leaked plain secret: %s", f.SafeEvidence)
		}
		// All findings must be severity WARN or INFO (Section 12)
		if f.Severity != sourcescanner.SeverityWarn && f.Severity != sourcescanner.SeverityInfo {
			t.Fatalf("RULE VIOLATION: scanner finding produced non-warning severity: %s", f.Severity)
		}
	}

	// Invariant: Conservative Same-Origin Correlation (Section 9)
	for _, f := range report.Findings {
		if f.RuleID == sourcescanner.RuleSameOriginAbsEndpoint {
			if strings.Contains(f.SafeEvidence, "analytics.example.com") {
				t.Fatalf("RULE VIOLATION: unrelated 3rd party URL flagged as same-origin endpoint: %s", f.SafeEvidence)
			}
		}
	}

	// Invariant: Declared Env Observed (Section 10)
	for _, f := range report.Findings {
		if f.RuleID == sourcescanner.RuleDeclaredEnvNotObserved && f.DependencyLogicalName == "database" {
			t.Fatalf("RULE VIOLATION: declared env APP_DATABASE_URL was observed but flagged as unobserved")
		}
	}
}

// Section 13, 14, 15, 16, 17: Bad Consumer Verification Acceptance
func TestADC05BadConsumerLayeredVerification(t *testing.T) {
	f := setupVerificationFixture(t)
	ctx := context.Background()

	// Bad Consumer: Has healthy provider, resolved contract, healthy consumer workload,
	// but fails consumer assertion
	badReq := verificationv1.VerifyDependencyRequest{
		DependencyLogicalName: f.depName,
		ConsumerContract: &verificationv1.ConsumerVerificationContract{
			Type:           "consumer_http",
			Path:           "invalid-no-slash-path",
			ExpectedStatus: 200,
		},
	}

	run, err := f.server.ExecuteDependencyVerification(ctx, f.projectID, f.envID, f.appID, badReq, "test-user")
	if err != nil {
		t.Fatal(err)
	}

	// Central ADC-05 Acceptance Result:
	// Provider = HEALTHY
	// Contract = RESOLVED
	// Connection = VERIFIED
	// Consumer = HEALTHY
	// Assertion = FAILED
	// Overall = FAILED
	if run.ProviderHealth.Status != verificationv1.LayerStatusHealthy {
		t.Fatalf("expected Provider HEALTHY, got %s", run.ProviderHealth.Status)
	}
	if run.ContractResolution.Status != verificationv1.LayerStatusResolved {
		t.Fatalf("expected Contract RESOLVED, got %s", run.ContractResolution.Status)
	}
	if run.Connection.Status != verificationv1.LayerStatusVerified {
		t.Fatalf("expected Connection VERIFIED, got %s", run.Connection.Status)
	}
	if run.ConsumerHealth.Status != verificationv1.LayerStatusHealthy {
		t.Fatalf("expected Consumer HEALTHY, got %s", run.ConsumerHealth.Status)
	}
	if run.ConsumerAssertion.Status != verificationv1.LayerStatusFailed {
		t.Fatalf("expected Assertion FAILED, got %s", run.ConsumerAssertion.Status)
	}
	if run.OverallStatus != verificationv1.RunStatusFailed {
		t.Fatalf("expected Overall FAILED, got %s", run.OverallStatus)
	}
}

// Section 18, 19: Fixed Consumer Verification Acceptance
func TestADC05FixedConsumerLayeredVerification(t *testing.T) {
	f := setupVerificationFixture(t)
	ctx := context.Background()

	// Fixed Consumer: All 5 layers pass
	req := verificationv1.VerifyDependencyRequest{
		DependencyLogicalName: f.depName,
		ConsumerContract: &verificationv1.ConsumerVerificationContract{
			Type:           "consumer_http",
			Path:           "/health/dependencies/database",
			ExpectedStatus: 200,
		},
	}

	run, err := f.server.ExecuteDependencyVerification(ctx, f.projectID, f.envID, f.appID, req, "test-user")
	if err != nil {
		t.Fatal(err)
	}

	if run.ProviderHealth.Status != verificationv1.LayerStatusHealthy {
		t.Fatalf("expected Provider HEALTHY, got %s", run.ProviderHealth.Status)
	}
	if run.ContractResolution.Status != verificationv1.LayerStatusResolved {
		t.Fatalf("expected Contract RESOLVED, got %s", run.ContractResolution.Status)
	}
	if run.Connection.Status != verificationv1.LayerStatusVerified {
		t.Fatalf("expected Connection VERIFIED, got %s", run.Connection.Status)
	}
	if run.ConsumerHealth.Status != verificationv1.LayerStatusHealthy {
		t.Fatalf("expected Consumer HEALTHY, got %s", run.ConsumerHealth.Status)
	}
	if run.ConsumerAssertion.Status != verificationv1.LayerStatusVerified {
		t.Fatalf("expected Assertion VERIFIED, got %s", run.ConsumerAssertion.Status)
	}
	if run.OverallStatus != verificationv1.RunStatusVerified {
		t.Fatalf("expected Overall VERIFIED, got %s", run.OverallStatus)
	}
}

// Section 21: PARTIALLY_VERIFIED Acceptance (No Assertion Configured)
func TestADC05PartiallyVerifiedAcceptance(t *testing.T) {
	f := setupVerificationFixture(t)
	ctx := context.Background()

	// Clear verification contract from service configuration draft
	curCfg, err := f.server.Registry.GetServiceConfiguration(f.projectID, f.appID)
	if err != nil {
		t.Fatal(err)
	}
	draft := registry.ServiceConfigurationDraft{
		Dependencies: []serviceconfigurationv1.ApplicationDependency{
			{
				LogicalName:    f.depName,
				TargetKind:     "managed_resource",
				TargetIdentity: f.resourceID,
				Protocol:       "postgres",
				Required:       true,
				InjectionPhase: "runtime",
			},
		},
	}
	if _, err := f.server.Registry.ApplyServiceConfiguration(f.projectID, f.appID, "user-1", "cfg-no-assertion", registry.ServiceConfigurationApplyRequest{
		Draft:             draft,
		ExpectedRevision:  curCfg.Revision,
		ExpectedStateHash: curCfg.StateHash,
	}); err != nil {
		t.Fatal(err)
	}

	req := verificationv1.VerifyDependencyRequest{
		DependencyLogicalName: f.depName,
	}

	run, err := f.server.ExecuteDependencyVerification(ctx, f.projectID, f.envID, f.appID, req, "test-user")
	if err != nil {
		t.Fatal(err)
	}

	if run.ProviderHealth.Status != verificationv1.LayerStatusHealthy {
		t.Fatalf("expected Provider HEALTHY, got %s", run.ProviderHealth.Status)
	}
	if run.ContractResolution.Status != verificationv1.LayerStatusResolved {
		t.Fatalf("expected Contract RESOLVED, got %s", run.ContractResolution.Status)
	}
	if run.Connection.Status != verificationv1.LayerStatusVerified {
		t.Fatalf("expected Connection VERIFIED, got %s", run.Connection.Status)
	}
	if run.ConsumerHealth.Status != verificationv1.LayerStatusHealthy {
		t.Fatalf("expected Consumer HEALTHY, got %s", run.ConsumerHealth.Status)
	}
	if run.ConsumerAssertion.Status != verificationv1.LayerStatusNotConfigured {
		t.Fatalf("expected Assertion NOT_CONFIGURED, got %s", run.ConsumerAssertion.Status)
	}
	// Invariant: Without consumer assertion -> PARTIALLY_VERIFIED, NEVER VERIFIED
	if run.OverallStatus != verificationv1.RunStatusPartiallyVerified {
		t.Fatalf("expected Overall PARTIALLY_VERIFIED, got %s", run.OverallStatus)
	}
}

// Section 29, 37: SSRF & IDOR Boundary Acceptance
func TestADC05SecuritySSRFAndIDOR(t *testing.T) {
	f := setupVerificationFixture(t)
	ctx := context.Background()

	// SSRF attempts on consumer assertion path
	ssrfPayloads := []string{
		"http://169.254.169.254/latest/meta-data",
		"http://127.0.0.1:8080/admin",
		"//foreign-host/api",
		"https://example.com/steal-creds",
	}

	for _, payload := range ssrfPayloads {
		req := verificationv1.VerifyDependencyRequest{
			DependencyLogicalName: f.depName,
			ConsumerContract: &verificationv1.ConsumerVerificationContract{
				Type:           "consumer_http",
				Path:           payload,
				ExpectedStatus: 200,
			},
		}
		run, err := f.server.ExecuteDependencyVerification(ctx, f.projectID, f.envID, f.appID, req, "test-user")
		if err == nil && run.OverallStatus == verificationv1.RunStatusVerified {
			t.Fatalf("SECURITY FLAW: SSRF payload accepted and verified: %s", payload)
		}
	}

	// IDOR: Attempting to verify dependency for non-existent or foreign project
	foreignProjectID := "proj-foreign-999"
	_, err := f.server.ExecuteDependencyVerification(ctx, foreignProjectID, f.envID, f.appID, verificationv1.VerifyDependencyRequest{
		DependencyLogicalName: f.depName,
	}, "test-user")
	if err == nil {
		t.Fatal("SECURITY FLAW: IDOR succeeded across foreign project boundary")
	}
}

// Section 20, 44: Source Immutability & Zero Mutation Proof
func TestADC05SourceZeroMutation(t *testing.T) {
	tempDir := t.TempDir()
	appDir := filepath.Join(tempDir, "app")
	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(appDir, "server.go")
	srcContent := []byte("package main\nimport \"os\"\nfunc main() { _ = os.Getenv(\"APP_DATABASE_URL\") }\n")
	if err := os.WriteFile(srcFile, srcContent, 0644); err != nil {
		t.Fatal(err)
	}

	hashBefore := sha256.Sum256(srcContent)

	deps := []sourcescanner.Dependency{
		{
			LogicalName:     "db",
			Protocol:        "postgres",
			DeclaredEnvKeys: []string{"APP_DATABASE_URL"},
		},
	}
	_ = sourcescanner.Scan(context.Background(), tempDir, "app", deps, sourcescanner.DefaultLimits())

	readAfter, err := os.ReadFile(srcFile)
	if err != nil {
		t.Fatal(err)
	}
	hashAfter := sha256.Sum256(readAfter)

	if hex.EncodeToString(hashBefore[:]) != hex.EncodeToString(hashAfter[:]) {
		t.Fatal("MUTATION DETECTED: source scanner modified application source files")
	}
}
