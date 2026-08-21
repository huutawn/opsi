package sourcescanner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanSource(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")

	report := Scan(context.Background(), dir, ".", nil, DefaultLimits())
	if report.AnalysisStatus != AnalysisStatusComplete {
		t.Fatalf("expected complete, got %s", report.AnalysisStatus)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("expected 0 findings, got %d: %+v", len(report.Findings), report.Findings)
	}
}

func TestLoopbackEndpoint(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.js", `
const api = "http://localhost:8080/api";
const db = "postgres://app:secret@127.0.0.1:5432/appdb";
const redis = "redis://127.0.0.1:6379";
`)

	report := Scan(context.Background(), dir, ".", nil, DefaultLimits())
	var loopbacks []Finding
	for _, f := range report.Findings {
		if f.RuleID == RuleLoopbackEndpoint {
			loopbacks = append(loopbacks, f)
			if f.Severity != SeverityWarn {
				t.Fatalf("expected WARN, got %s", f.Severity)
			}
			if f.Confidence != ConfidenceHigh {
				t.Fatalf("expected HIGH confidence, got %s", f.Confidence)
			}
		}
	}
	if len(loopbacks) < 3 {
		t.Fatalf("expected at least 3 loopback findings, got %d", len(loopbacks))
	}
}

func TestServerListenNotFlaggedAsLoopback(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "server.go", `
package main
import "net/http"

func main() {
    http.ListenAndServe("0.0.0.0:8080", nil)
}
`)

	report := Scan(context.Background(), dir, ".", nil, DefaultLimits())
	for _, f := range report.Findings {
		if f.RuleID == RuleLoopbackEndpoint || f.RuleID == RuleHardcodedIPEndpoint {
			t.Fatalf("unexpected finding on 0.0.0.0: %+v", f)
		}
	}
}

func TestHardcodedIPEndpoint(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config.py", `
API_HOST = "http://198.51.100.42:9000/v1"
`)

	report := Scan(context.Background(), dir, ".", nil, DefaultLimits())
	found := false
	for _, f := range report.Findings {
		if f.RuleID == RuleHardcodedIPEndpoint {
			found = true
			if f.Severity != SeverityWarn {
				t.Fatalf("expected WARN severity, got %s", f.Severity)
			}
		}
	}
	if !found {
		t.Fatal("expected SOURCE_HARDCODED_IP_ENDPOINT finding")
	}
}

func TestBrowserInternalDNS(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "frontend.ts", `
fetch("http://api.default.svc.cluster.local:8080/data");
`)

	deps := []Dependency{
		{
			LogicalName:   "api",
			AccessContext: "browser",
			Strategy:      "same_origin",
			Path:          "/api",
		},
	}

	report := Scan(context.Background(), dir, ".", deps, DefaultLimits())
	found := false
	for _, f := range report.Findings {
		if f.RuleID == RuleBrowserInternalDNS {
			found = true
			if f.Confidence != ConfidenceHigh {
				t.Fatalf("expected HIGH confidence, got %s", f.Confidence)
			}
		}
	}
	if !found {
		t.Fatal("expected SOURCE_BROWSER_INTERNAL_DNS finding")
	}
}

func TestSameOriginAbsoluteEndpoint(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "client.js", `
fetch("http://localhost:8080/api/users");
`)

	deps := []Dependency{
		{
			LogicalName:   "api",
			AccessContext: "browser",
			Strategy:      "same_origin",
			Path:          "/api",
		},
	}

	report := Scan(context.Background(), dir, ".", deps, DefaultLimits())
	found := false
	for _, f := range report.Findings {
		if f.RuleID == RuleSameOriginAbsEndpoint {
			found = true
		}
	}
	if !found {
		t.Fatal("expected SOURCE_SAME_ORIGIN_ABSOLUTE_ENDPOINT finding")
	}
}

func TestDeclaredEnvObserved(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.go", `
package main
import "os"

func getDB() string {
    return os.Getenv("APP_DATABASE_URL")
}
`)

	deps := []Dependency{
		{
			LogicalName:     "database",
			Protocol:        "postgres",
			DeclaredEnvKeys: []string{"APP_DATABASE_URL"},
		},
	}

	report := Scan(context.Background(), dir, ".", deps, DefaultLimits())
	for _, f := range report.Findings {
		if f.RuleID == RuleDeclaredEnvNotObserved {
			t.Fatalf("unexpected SOURCE_DECLARED_ENV_NOT_OBSERVED finding: %+v", f)
		}
	}
}

func TestDeclaredEnvNotObserved(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.go", `
package main
func main() {}
`)

	deps := []Dependency{
		{
			LogicalName:     "database",
			Protocol:        "postgres",
			DeclaredEnvKeys: []string{"APP_DATABASE_URL"},
		},
	}

	report := Scan(context.Background(), dir, ".", deps, DefaultLimits())
	found := false
	for _, f := range report.Findings {
		if f.RuleID == RuleDeclaredEnvNotObserved {
			found = true
			if f.Confidence != ConfidenceLow {
				t.Fatalf("expected LOW confidence, got %s", f.Confidence)
			}
			if !strings.Contains(f.SafeEvidence, "reference was not observed") {
				t.Fatalf("expected safe evidence to mention reference not observed, got: %s", f.SafeEvidence)
			}
		}
	}
	if !found {
		t.Fatal("expected SOURCE_DECLARED_ENV_NOT_OBSERVED finding")
	}
}

func TestAlternateEnvObserved(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "app.go", `
package main
import "os"

func main() {
    url := os.Getenv("DATABASE_URL")
    _ = url
}
`)

	deps := []Dependency{
		{
			LogicalName:     "database",
			Protocol:        "postgres",
			DeclaredEnvKeys: []string{"APP_DATABASE_URL"},
		},
	}

	report := Scan(context.Background(), dir, ".", deps, DefaultLimits())
	foundAlt := false
	foundNotObs := false
	for _, f := range report.Findings {
		if f.RuleID == RuleAlternateDepEnv {
			foundAlt = true
			if f.Confidence != ConfidenceMedium {
				t.Fatalf("expected MEDIUM confidence, got %s", f.Confidence)
			}
		}
		if f.RuleID == RuleDeclaredEnvNotObserved {
			foundNotObs = true
		}
	}
	if !foundAlt {
		t.Fatal("expected SOURCE_ALTERNATE_DEPENDENCY_ENV_OBSERVED finding")
	}
	if !foundNotObs {
		t.Fatal("expected SOURCE_DECLARED_ENV_NOT_OBSERVED finding")
	}
}

func TestEmbeddedCredentialRedaction(t *testing.T) {
	dir := t.TempDir()
	secretPass := "SuperSecretPassword123!"
	writeFile(t, dir, "db.js", `
const pg = "postgres://myuser:`+secretPass+`@dbhost.internal:5432/proddb";
const r = "redis://:`+secretPass+`@redishost:6379";
`)

	report := Scan(context.Background(), dir, ".", nil, DefaultLimits())
	credCount := 0
	for _, f := range report.Findings {
		if f.RuleID == RuleEmbeddedCredential {
			credCount++
			if strings.Contains(f.SafeEvidence, secretPass) {
				t.Fatalf("CRITICAL SECURITY VIOLATION: secret password leaked in SafeEvidence: %s", f.SafeEvidence)
			}
			if !strings.Contains(f.SafeEvidence, "[REDACTED]") {
				t.Fatalf("expected [REDACTED] in evidence, got: %s", f.SafeEvidence)
			}
		}
		if strings.Contains(f.SafeEvidence, secretPass) {
			t.Fatalf("CRITICAL SECURITY VIOLATION: secret leaked in finding %s: %s", f.RuleID, f.SafeEvidence)
		}
	}
	if credCount < 2 {
		t.Fatalf("expected 2 embedded credential findings, got %d", credCount)
	}
}

func TestIgnoredDirectoriesAndBinaryFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "node_modules/bad/index.js", `const bad = "http://localhost:8080";`)
	writeFile(t, dir, "vendor/bad/index.go", `package bad; var _ = "http://localhost:8080"`)
	writeFile(t, dir, ".git/hooks/pre-commit", `http://localhost:8080`)
	binaryContent := []byte("ELF\x00\x00http://localhost:8080\x00")
	if err := os.WriteFile(filepath.Join(dir, "binary.bin"), binaryContent, 0600); err != nil {
		t.Fatal(err)
	}

	report := Scan(context.Background(), dir, ".", nil, DefaultLimits())
	if len(report.Findings) != 0 {
		t.Fatalf("expected 0 findings from ignored dirs/binaries, got %d: %+v", len(report.Findings), report.Findings)
	}
}

func TestApplicationRootIsolation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "root_bad.js", `const x = "http://localhost:8080";`)
	writeFile(t, dir, "app/good.js", `console.log("clean");`)

	report := Scan(context.Background(), dir, "app", nil, DefaultLimits())
	if len(report.Findings) != 0 {
		t.Fatalf("expected 0 findings when scanning app subdir, got %d: %+v", len(report.Findings), report.Findings)
	}
}

func TestLanguagePatterns(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "node.js", `const x = process.env.NODE_KEY; const y = process.env["NODE_KEY_BRACKET"]; const z = process.env['NODE_KEY_SINGLE'];`)
	writeFile(t, dir, "golang.go", `package main; import "os"; var _ = os.Getenv("GO_KEY"); var _, _ = os.LookupEnv("GO_LOOKUP_KEY")`)
	writeFile(t, dir, "java.java", `class J { String k = System.getenv("JAVA_KEY"); }`)
	writeFile(t, dir, "csharp.cs", `class C { string k = Environment.GetEnvironmentVariable("CSHARP_KEY"); }`)

	deps := []Dependency{
		{LogicalName: "node", Protocol: "http", DeclaredEnvKeys: []string{"NODE_KEY", "NODE_KEY_BRACKET", "NODE_KEY_SINGLE"}},
		{LogicalName: "go", Protocol: "http", DeclaredEnvKeys: []string{"GO_KEY", "GO_LOOKUP_KEY"}},
		{LogicalName: "java", Protocol: "http", DeclaredEnvKeys: []string{"JAVA_KEY"}},
		{LogicalName: "csharp", Protocol: "http", DeclaredEnvKeys: []string{"CSHARP_KEY"}},
	}

	report := Scan(context.Background(), dir, ".", deps, DefaultLimits())
	for _, f := range report.Findings {
		if f.RuleID == RuleDeclaredEnvNotObserved {
			t.Fatalf("expected all declared env keys to be observed, but got: %+v", f)
		}
	}
}

func TestDeterminismAndStaleness(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.js", `const x = "http://localhost:3000";`)
	writeFile(t, dir, "b.js", `const y = "http://localhost:4000";`)

	opts1 := ScanOptions{ApplicationID: "app1", ProjectID: "proj1", RepositoryID: 100, CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	opts2 := ScanOptions{ApplicationID: "app1", ProjectID: "proj1", RepositoryID: 100, CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	opts3 := ScanOptions{ApplicationID: "app1", ProjectID: "proj1", RepositoryID: 100, CommitSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}

	rep1 := ScanWithOptions(context.Background(), dir, ".", nil, DefaultLimits(), opts1)
	rep2 := ScanWithOptions(context.Background(), dir, ".", nil, DefaultLimits(), opts2)
	rep3 := ScanWithOptions(context.Background(), dir, ".", nil, DefaultLimits(), opts3)

	if rep1.ReportHash != rep2.ReportHash {
		t.Fatalf("expected identical report hash for identical inputs: %s vs %s", rep1.ReportHash, rep2.ReportHash)
	}
	if rep1.ReportHash == rep3.ReportHash {
		t.Fatalf("expected different report hash for different commit SHA")
	}
}

func TestLimitsEnforced(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 20; i++ {
		writeFile(t, dir, filepath.Join("sub", string(rune('a'+i))+".js"), `const x = "http://localhost:8080";`)
	}

	limits := DefaultLimits()
	limits.MaxFindings = 5
	report := Scan(context.Background(), dir, ".", nil, limits)
	if len(report.Findings) > 5 {
		t.Fatalf("expected max 5 findings, got %d", len(report.Findings))
	}
	if !report.Truncated {
		t.Fatal("expected Truncated=true when hitting MaxFindings")
	}
}

func writeFile(t *testing.T, base, rel, content string) {
	t.Helper()
	p := filepath.Join(base, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
