// Package sourcescanner implements the ADC-05 source risk analysis scanner.
// It scans a materialized source directory for dependency risk patterns using
// purely deterministic, bounded heuristics. No network calls, no user code
// execution, no external dependencies.
//
// Key invariants:
//   - All rules produce WARN severity only — never BLOCK.
//   - Credentials in findings MUST be redacted; the actual secret never appears in output.
//   - Scanner failure sets analysis_status=failed, it does NOT fail the build.
//   - Report hash excludes timestamps so the same inputs always produce the same hash.
package sourcescanner

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const ScannerVersion = "opsi.source-scanner/v1"

// Severity values — source heuristic rules NEVER produce BLOCK, only WARN.
const (
	SeverityInfo = "INFO"
	SeverityWarn = "WARN"
)

// Confidence values.
const (
	ConfidenceHigh   = "HIGH"
	ConfidenceMedium = "MEDIUM"
	ConfidenceLow    = "LOW"
)

// AnalysisStatus for the report itself.
const (
	AnalysisStatusComplete    = "complete"
	AnalysisStatusUnavailable = "unavailable"
	AnalysisStatusFailed      = "failed"
)

// Rule IDs.
const (
	RuleLoopbackEndpoint       = "SOURCE_LOOPBACK_ENDPOINT"
	RuleHardcodedIPEndpoint    = "SOURCE_HARDCODED_IP_ENDPOINT"
	RuleBrowserInternalDNS     = "SOURCE_BROWSER_INTERNAL_DNS"
	RuleSameOriginAbsEndpoint  = "SOURCE_SAME_ORIGIN_ABSOLUTE_ENDPOINT"
	RuleDeclaredEnvNotObserved = "SOURCE_DECLARED_ENV_NOT_OBSERVED"
	RuleAlternateDepEnv        = "SOURCE_ALTERNATE_DEPENDENCY_ENV_OBSERVED"
	RuleEmbeddedCredential     = "SOURCE_EMBEDDED_CREDENTIAL_SUSPECTED"
)

// Dependency is the scanner's view of an ApplicationDependency.
type Dependency struct {
	LogicalName     string
	Protocol        string   // "postgres", "redis", "nats", "http"
	Strategy        string   // "same_origin", "internal_http", ""
	AccessContext   string   // "browser", "server"
	Path            string   // for same_origin
	DeclaredEnvKeys []string // env keys declared in InjectionMappings
}

// Finding is a single risk observation produced by a rule.
type Finding struct {
	FindingID             string `json:"finding_id"`             // deterministic: rule:file:line
	RuleID                string `json:"rule_id"`
	Severity              string `json:"severity"`
	Confidence            string `json:"confidence"`
	Category              string `json:"category"`
	DependencyLogicalName string `json:"dependency_logical_name,omitempty"`
	File                  string `json:"file"`   // relative to ApplicationRoot
	Line                  int    `json:"line"`
	Column                int    `json:"column,omitempty"`
	SafeEvidence          string `json:"safe_evidence"` // never contains actual credentials
	RemediationCode       string `json:"remediation_code,omitempty"`
}

// EnvReference records an observed env key reference in the source.
type EnvReference struct {
	EnvKey string `json:"env_key"`
	File   string `json:"file"`
	Line   int    `json:"line"`
}

// Report is the result of a Scan call.
type Report struct {
	ScannerVersion  string         `json:"scanner_version"`
	ApplicationID   string         `json:"application_id"`
	ProjectID       string         `json:"project_id"`
	RepositoryID    int64          `json:"repository_id"`
	CommitSHA       string         `json:"commit_sha"`
	ApplicationRoot string         `json:"application_root"`
	BuildJobID      string         `json:"build_job_id,omitempty"`
	AnalysisStatus  string         `json:"analysis_status"` // "complete" | "failed" | "unavailable"
	Findings        []Finding      `json:"findings"`
	EnvReferences   []EnvReference `json:"env_references"`
	FilesScanned    int            `json:"files_scanned"`
	BytesScanned    int64          `json:"bytes_scanned"`
	Truncated       bool           `json:"truncated"`   // hit a limit
	ReportHash      string         `json:"report_hash"` // SHA-256 of deterministic content
}

// Limits constrains the scanner's resource consumption.
type Limits struct {
	MaxFiles        int
	MaxBytesPerFile int64
	MaxTotalBytes   int64
	MaxFindings     int
	MaxLineLenBytes int
	MaxDuration     time.Duration
}

// DefaultLimits returns safe production defaults.
func DefaultLimits() Limits {
	return Limits{
		MaxFiles:        5000,
		MaxBytesPerFile: 1 << 20,  // 1 MiB
		MaxTotalBytes:   50 << 20, // 50 MiB
		MaxFindings:     500,
		MaxLineLenBytes: 4096,
		MaxDuration:     30 * time.Second,
	}
}

// skippedDirs are directories that are never scanned.
var skippedDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	".next":        true,
	"target":       true,
	"bin":          true,
	"coverage":     true,
	"tmp":          true,
	"__pycache__":  true,
	".turbo":       true,
	".cache":       true,
}

// --- Compiled rules (platform-owned, never derived from user input) ---

// reLoopback matches http/https/postgres/redis URLs with localhost or 127.0.0.1.
// Server-listen patterns on 0.0.0.0 are NOT matched.
var reLoopback = regexp.MustCompile(
	`(?i)(postgres|postgresql|redis|http|https)://[^@\s]*@?(localhost|127\.0\.0\.1)(:\d+)?[/\s"` + "`" + `']?`)

// reHardcodedIP matches URLs using a raw IPv4 (not loopback, not 0.0.0.0).
var reHardcodedIP = regexp.MustCompile(
	`(?i)(postgres|postgresql|redis|http|https)://[^@\s]*@?` +
		`((?:(?:1\d\d|2[0-4]\d|25[0-5]|[1-9]\d|\d)\.){3}(?:1\d\d|2[0-4]\d|25[0-5]|[1-9]\d|\d))` +
		`(:\d+)?[/\s"` + "`" + `']?`)

// reInternalDNS matches Kubernetes cluster-internal hostnames.
var reInternalDNS = regexp.MustCompile(`\b[\w.-]+\.svc(?:\.cluster\.local)?\b`)

// reCredentialURL matches URLs that embed a non-empty password.
// Groups: 1=scheme, 2=user, 3=password, 4=host, 5=path
var reCredentialURL = regexp.MustCompile(
	`(?i)(postgres|postgresql|redis|http|https)://([^:@\s]*):([^@\s]+)@([^/\s"` + "`" + `']+)(/[^\s"` + "`" + `']*)? `)

// reCredentialURLFull is the same pattern without trailing space — used for matching within lines.
var reCredentialURLFull = regexp.MustCompile(
	`(?i)(postgres|postgresql|redis|http|https)://([^:@\s]*):([^@\s]+)@([^/\s"` + "`" + `']+)(/[^\s"` + "`" + `']*)?`)

// reAbsoluteURL matches absolute http/https URLs.
var reAbsoluteURL = regexp.MustCompile(`(?i)https?://[^\s"` + "`" + `']+`)

// envKeyPatterns extracts env key names from various language patterns.
// The captured group (index 1) is the env key name.
var envKeyPatterns = []*regexp.Regexp{
	// Node/TypeScript: process.env.KEY or process.env["KEY"] or process.env['KEY']
	regexp.MustCompile(`process\.env\.([A-Z][A-Z0-9_]*)`),
	regexp.MustCompile(`process\.env\[["']([A-Z][A-Z0-9_]*)["']\]`),
	// Go: os.Getenv("KEY") or os.LookupEnv("KEY")
	regexp.MustCompile(`os\.(?:Getenv|LookupEnv)\(["'` + "`" + `]([A-Z][A-Z0-9_]*)["'` + "`" + `]\)`),
	// Java: System.getenv("KEY")
	regexp.MustCompile(`System\.getenv\(["']([A-Z][A-Z0-9_]*)["']\)`),
	// C#: Environment.GetEnvironmentVariable("KEY")
	regexp.MustCompile(`Environment\.GetEnvironmentVariable\(["']([A-Z][A-Z0-9_]*)["']\)`),
}

// knownAlternates maps a canonical protocol to its known-alternate env key names.
var postgresAlternates = []string{"DATABASE_URL", "POSTGRES_URL", "POSTGRESQL_URL", "PG_URL", "DB_URL"}
var redisAlternates = []string{"REDIS_URL", "CACHE_URL", "REDISCLOUD_URL"}

// ScanOptions carries identifying metadata used to populate the report header.
type ScanOptions struct {
	ApplicationID string
	ProjectID     string
	RepositoryID  int64
	CommitSHA     string
}

// ScanWithOptions is the full-featured entry point that also populates the report header.
func ScanWithOptions(ctx context.Context, sourceDir, applicationRoot string, deps []Dependency, limits Limits, opts ScanOptions) Report {
	r := Scan(ctx, sourceDir, applicationRoot, deps, limits)
	r.ApplicationID = opts.ApplicationID
	r.ProjectID = opts.ProjectID
	r.RepositoryID = opts.RepositoryID
	r.CommitSHA = opts.CommitSHA
	r.ReportHash = computeHash(r)
	return r
}

// Scan performs a bounded, deterministic source risk scan of the given directory.
// sourceDir is the materialized checkout root; applicationRoot is the sub-path
// within sourceDir that forms the application boundary. deps are the declared
// ApplicationDependencies translated into the scanner's Dependency type.
//
// On any internal error the returned Report has AnalysisStatus=failed. The
// caller must NOT treat scanner failure as a build failure.
func Scan(ctx context.Context, sourceDir, applicationRoot string, deps []Dependency, limits Limits) Report {
	deadline := time.Now().Add(limits.MaxDuration)
	scanCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	report := Report{
		ScannerVersion:  ScannerVersion,
		ApplicationRoot: applicationRoot,
		AnalysisStatus:  AnalysisStatusComplete,
	}

	// Resolve and validate the scan root.
	scanRoot := filepath.Join(sourceDir, applicationRoot)
	resolvedRoot, err := filepath.EvalSymlinks(scanRoot)
	if err != nil {
		report.AnalysisStatus = AnalysisStatusFailed
		return finalize(report)
	}
	resolvedSourceDir, err := filepath.EvalSymlinks(sourceDir)
	if err != nil {
		report.AnalysisStatus = AnalysisStatusFailed
		return finalize(report)
	}
	// Ensure applicationRoot is within sourceDir.
	if !strings.HasPrefix(resolvedRoot+string(filepath.Separator), resolvedSourceDir+string(filepath.Separator)) &&
		resolvedRoot != resolvedSourceDir {
		report.AnalysisStatus = AnalysisStatusFailed
		return finalize(report)
	}

	// Per-dependency state tracked across all files.
	type depState struct {
		dep             Dependency
		observedEnvKeys map[string]bool   // env keys actually found in source
		observedAlts    map[string]string // alternate key → "file:line"
	}
	states := make([]depState, len(deps))
	for i, d := range deps {
		states[i] = depState{
			dep:             d,
			observedEnvKeys: make(map[string]bool),
			observedAlts:    make(map[string]string),
		}
	}

	var findings []Finding
	var envRefs []EnvReference
	truncated := false

	// Walk the application root.
	walkErr := filepath.WalkDir(resolvedRoot, func(path string, entry fs.DirEntry, err error) error {
		// Respect context deadline.
		select {
		case <-scanCtx.Done():
			truncated = true
			return fs.SkipAll
		default:
		}

		if err != nil {
			// Skip unreadable entries without failing the whole scan.
			return nil
		}

		// Skip symlinks that escape the source dir.
		if entry.Type()&os.ModeSymlink != 0 {
			target, lerr := os.Readlink(path)
			if lerr != nil {
				return nil
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(path), target)
			}
			resolved, lerr := filepath.EvalSymlinks(target)
			if lerr != nil {
				return nil
			}
			if !strings.HasPrefix(resolved+string(filepath.Separator), resolvedSourceDir+string(filepath.Separator)) &&
				resolved != resolvedSourceDir {
				return nil // skip escaping symlink
			}
		}

		if entry.IsDir() {
			if skippedDirs[entry.Name()] {
				return fs.SkipDir
			}
			return nil
		}

		// File limit.
		if report.FilesScanned >= limits.MaxFiles {
			truncated = true
			return fs.SkipAll
		}

		// Total bytes limit.
		if report.BytesScanned >= limits.MaxTotalBytes {
			truncated = true
			return fs.SkipAll
		}

		// Finding limit — stop walking if already at max.
		if len(findings) >= limits.MaxFindings {
			truncated = true
			return fs.SkipAll
		}

		info, serr := entry.Info()
		if serr != nil {
			return nil
		}
		fileSize := info.Size()

		// Relative path from application root for reporting.
		relPath, rerr := filepath.Rel(resolvedRoot, path)
		if rerr != nil {
			return nil
		}
		relPath = filepath.ToSlash(relPath)

		// Read up to MaxBytesPerFile.
		readLimit := limits.MaxBytesPerFile
		if fileSize > readLimit {
			truncated = true
		}

		f, ferr := os.Open(path)
		if ferr != nil {
			return nil
		}
		data, ferr := io.ReadAll(io.LimitReader(f, readLimit))
		f.Close()
		if ferr != nil {
			return nil
		}

		// Skip binary files (null byte in first 8 KiB).
		checkLen := len(data)
		if checkLen > 8192 {
			checkLen = 8192
		}
		if bytes.ContainsRune(data[:checkLen], 0) {
			return nil
		}

		report.FilesScanned++
		report.BytesScanned += int64(len(data))

		// Scan line-by-line.
		lineNum := 0
		sc := bufio.NewScanner(bytes.NewReader(data))
		buf := make([]byte, limits.MaxLineLenBytes+1)
		sc.Buffer(buf, limits.MaxLineLenBytes+1)
		for sc.Scan() {
			lineNum++
			line := sc.Text()

			if len(findings) >= limits.MaxFindings {
				truncated = true
				break
			}

			// --- Rule: SOURCE_LOOPBACK_ENDPOINT ---
			applyLoopback(line, relPath, lineNum, &findings, limits.MaxFindings, &truncated)

			// --- Rule: SOURCE_HARDCODED_IP_ENDPOINT ---
			applyHardcodedIP(line, relPath, lineNum, &findings, limits.MaxFindings, &truncated)

			// --- Rule: SOURCE_EMBEDDED_CREDENTIAL_SUSPECTED ---
			applyEmbeddedCredential(line, relPath, lineNum, &findings, limits.MaxFindings, &truncated)

			// --- Per-dependency rules ---
			for si := range states {
				st := &states[si]
				dep := st.dep

				// --- Rule: SOURCE_BROWSER_INTERNAL_DNS ---
				if dep.AccessContext == "browser" {
					applyBrowserInternalDNS(line, relPath, lineNum, dep, &findings, limits.MaxFindings, &truncated)
				}

				// --- Rule: SOURCE_SAME_ORIGIN_ABSOLUTE_ENDPOINT ---
				if dep.Strategy == "same_origin" && dep.AccessContext == "browser" && dep.Path != "" {
					applySameOriginAbsEndpoint(line, relPath, lineNum, dep, &findings, limits.MaxFindings, &truncated)
				}

				// --- Env key observation (for declared-env rules, evaluated after walk) ---
				for _, key := range dep.DeclaredEnvKeys {
					observeEnvKey(line, relPath, lineNum, key, st.observedEnvKeys, &envRefs)
					// Also check language-specific patterns.
					for _, pat := range envKeyPatterns {
						for _, sm := range pat.FindAllStringSubmatch(line, -1) {
							if sm[1] == key && !st.observedEnvKeys[key] {
								st.observedEnvKeys[key] = true
							}
						}
					}

					// Alternate key observation.
					for _, alt := range alternatesForProtocol(dep.Protocol) {
						if alt != key && containsEnvKey(line, alt) {
							if st.observedAlts[alt] == "" {
								st.observedAlts[alt] = fmt.Sprintf("%s:%d", relPath, lineNum)
							}
							envRefs = append(envRefs, EnvReference{EnvKey: alt, File: relPath, Line: lineNum})
						}
					}
				}
			}
		}

		return nil
	})

	if walkErr != nil {
		report.AnalysisStatus = AnalysisStatusFailed
		return finalize(report)
	}

	// --- Post-walk rules ---
	for _, st := range states {
		dep := st.dep
		for _, key := range dep.DeclaredEnvKeys {
			observed := st.observedEnvKeys[key]

			if !observed {
				// SOURCE_DECLARED_ENV_NOT_OBSERVED
				if len(findings) < limits.MaxFindings {
					findings = append(findings, Finding{
						RuleID:                RuleDeclaredEnvNotObserved,
						Severity:              SeverityWarn,
						Confidence:            ConfidenceLow,
						Category:              "dependency_wiring",
						DependencyLogicalName: dep.LogicalName,
						File:                  ".",
						Line:                  0,
						SafeEvidence:          fmt.Sprintf("env key %q: reference was not observed in source", key),
					})
				} else {
					truncated = true
				}

				// SOURCE_ALTERNATE_DEPENDENCY_ENV_OBSERVED
				for _, alt := range alternatesForProtocol(dep.Protocol) {
					if alt != key && st.observedAlts[alt] != "" {
						if len(findings) < limits.MaxFindings {
							findings = append(findings, Finding{
								RuleID:                RuleAlternateDepEnv,
								Severity:              SeverityWarn,
								Confidence:            ConfidenceMedium,
								Category:              "dependency_wiring",
								DependencyLogicalName: dep.LogicalName,
								File:                  ".",
								Line:                  0,
								SafeEvidence: fmt.Sprintf(
									"declared key %q not observed; alternate %q observed at %s",
									key, alt, st.observedAlts[alt],
								),
							})
						} else {
							truncated = true
						}
					}
				}
			}
		}
	}

	// Sort and deduplicate.
	findings = sortAndDedup(findings)
	envRefs = deduplicateEnvRefs(envRefs)

	report.Findings = findings
	report.EnvReferences = envRefs
	report.Truncated = truncated

	return finalize(report)
}

// --- Rule application helpers ---

func applyLoopback(line, relPath string, lineNum int, findings *[]Finding, maxFindings int, truncated *bool) {
	if len(*findings) >= maxFindings {
		*truncated = true
		return
	}
	m := reLoopback.FindStringIndex(line)
	if m == nil {
		return
	}
	matched := line[m[0]:m[1]]
	*findings = append(*findings, Finding{
		RuleID:       RuleLoopbackEndpoint,
		Severity:     SeverityWarn,
		Confidence:   ConfidenceHigh,
		Category:     "endpoint_configuration",
		File:         relPath,
		Line:         lineNum,
		Column:       utf8.RuneCountInString(line[:m[0]]) + 1,
		SafeEvidence: redactCredentialURL(matched),
	})
}

func applyHardcodedIP(line, relPath string, lineNum int, findings *[]Finding, maxFindings int, truncated *bool) {
	if len(*findings) >= maxFindings {
		*truncated = true
		return
	}
	sm := reHardcodedIP.FindStringSubmatch(line)
	if sm == nil {
		return
	}
	ip := sm[2]
	if ip == "127.0.0.1" || ip == "0.0.0.0" {
		return // Loopback covered by other rule; 0.0.0.0 is a server listen, never flagged.
	}
	idx := reHardcodedIP.FindStringIndex(line)
	if idx == nil {
		return
	}
	matched := line[idx[0]:idx[1]]
	*findings = append(*findings, Finding{
		RuleID:       RuleHardcodedIPEndpoint,
		Severity:     SeverityWarn,
		Confidence:   ConfidenceMedium,
		Category:     "endpoint_configuration",
		File:         relPath,
		Line:         lineNum,
		Column:       utf8.RuneCountInString(line[:idx[0]]) + 1,
		SafeEvidence: redactCredentialURL(matched),
	})
}

func applyEmbeddedCredential(line, relPath string, lineNum int, findings *[]Finding, maxFindings int, truncated *bool) {
	for _, sm := range reCredentialURLFull.FindAllStringSubmatch(line, -1) {
		if len(*findings) >= maxFindings {
			*truncated = true
			return
		}
		if sm[3] == "" {
			continue // No password — not a credential finding.
		}
		scheme := strings.ToLower(sm[1])
		host := sm[4]
		dbPath := sm[5]
		safeEvidence := fmt.Sprintf("%s://[REDACTED]@%s%s", scheme, host, dbPath)
		idx := strings.Index(line, sm[0])
		col := 0
		if idx >= 0 {
			col = utf8.RuneCountInString(line[:idx]) + 1
		}
		*findings = append(*findings, Finding{
			RuleID:       RuleEmbeddedCredential,
			Severity:     SeverityWarn,
			Confidence:   ConfidenceHigh,
			Category:     "credential_exposure",
			File:         relPath,
			Line:         lineNum,
			Column:       col,
			SafeEvidence: safeEvidence,
		})
	}
}

func applyBrowserInternalDNS(line, relPath string, lineNum int, dep Dependency, findings *[]Finding, maxFindings int, truncated *bool) {
	for _, m := range reInternalDNS.FindAllStringIndex(line, -1) {
		if len(*findings) >= maxFindings {
			*truncated = true
			return
		}
		matched := line[m[0]:m[1]]
		*findings = append(*findings, Finding{
			RuleID:                RuleBrowserInternalDNS,
			Severity:              SeverityWarn,
			Confidence:            ConfidenceHigh,
			Category:              "access_context",
			DependencyLogicalName: dep.LogicalName,
			File:                  relPath,
			Line:                  lineNum,
			Column:                utf8.RuneCountInString(line[:m[0]]) + 1,
			SafeEvidence:          matched,
		})
	}
}

func applySameOriginAbsEndpoint(line, relPath string, lineNum int, dep Dependency, findings *[]Finding, maxFindings int, truncated *bool) {
	for _, absURL := range reAbsoluteURL.FindAllString(line, -1) {
		if len(*findings) >= maxFindings {
			*truncated = true
			return
		}
		u, err := url.Parse(absURL)
		if err != nil {
			continue
		}
		cleanPath := strings.TrimRight(dep.Path, "/")
		if cleanPath != "" && (u.Path == cleanPath || strings.HasPrefix(u.Path, cleanPath+"/")) {
			idx := strings.Index(line, absURL)
			col := 0
			if idx >= 0 {
				col = utf8.RuneCountInString(line[:idx]) + 1
			}
			*findings = append(*findings, Finding{
				RuleID:                RuleSameOriginAbsEndpoint,
				Severity:              SeverityWarn,
				Confidence:            ConfidenceMedium,
				Category:              "endpoint_configuration",
				DependencyLogicalName: dep.LogicalName,
				File:                  relPath,
				Line:                  lineNum,
				Column:                col,
				SafeEvidence:          redactCredentialURL(absURL),
			})
		}
	}
}

func observeEnvKey(line, relPath string, lineNum int, key string, observed map[string]bool, envRefs *[]EnvReference) {
	if containsEnvKey(line, key) {
		if !observed[key] {
			observed[key] = true
		}
		*envRefs = append(*envRefs, EnvReference{EnvKey: key, File: relPath, Line: lineNum})
	}
}

// --- Core helpers ---

// finalize assigns FindingIDs and computes the ReportHash.
func finalize(r Report) Report {
	for i := range r.Findings {
		r.Findings[i].FindingID = makeFindingID(r.Findings[i])
	}
	r.ReportHash = computeHash(r)
	return r
}

// makeFindingID produces a deterministic ID for a finding.
func makeFindingID(f Finding) string {
	return fmt.Sprintf("%s:%s:%d", f.RuleID, f.File, f.Line)
}

// computeHash produces a stable SHA-256 over the report's deterministic content.
// Timestamps are explicitly excluded.
func computeHash(r Report) string {
	type hashFinding struct {
		FindingID    string `json:"finding_id"`
		RuleID       string `json:"rule_id"`
		Severity     string `json:"severity"`
		Confidence   string `json:"confidence"`
		File         string `json:"file"`
		Line         int    `json:"line"`
		SafeEvidence string `json:"safe_evidence"`
	}
	hf := make([]hashFinding, len(r.Findings))
	for i, f := range r.Findings {
		hf[i] = hashFinding{
			FindingID:    f.FindingID,
			RuleID:       f.RuleID,
			Severity:     f.Severity,
			Confidence:   f.Confidence,
			File:         f.File,
			Line:         f.Line,
			SafeEvidence: f.SafeEvidence,
		}
	}
	data, _ := json.Marshal(struct {
		ScannerVersion  string         `json:"scanner_version"`
		CommitSHA       string         `json:"commit_sha"`
		ApplicationRoot string         `json:"application_root"`
		Findings        []hashFinding  `json:"findings"`
		EnvRefs         []EnvReference `json:"env_refs"`
	}{
		ScannerVersion:  r.ScannerVersion,
		CommitSHA:       r.CommitSHA,
		ApplicationRoot: r.ApplicationRoot,
		Findings:        hf,
		EnvRefs:         r.EnvReferences,
	})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// sortAndDedup sorts findings deterministically (file → line → rule) and removes duplicates.
func sortAndDedup(findings []Finding) []Finding {
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].RuleID < findings[j].RuleID
	})
	// Assign IDs after sorting so they reflect final position-independent keys.
	for i := range findings {
		findings[i].FindingID = makeFindingID(findings[i])
	}
	// Deduplicate by FindingID.
	seen := make(map[string]bool, len(findings))
	out := findings[:0]
	for _, f := range findings {
		if !seen[f.FindingID] {
			seen[f.FindingID] = true
			out = append(out, f)
		}
	}
	return out
}

// deduplicateEnvRefs removes duplicate (key, file, line) env refs and sorts them.
func deduplicateEnvRefs(refs []EnvReference) []EnvReference {
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].EnvKey != refs[j].EnvKey {
			return refs[i].EnvKey < refs[j].EnvKey
		}
		if refs[i].File != refs[j].File {
			return refs[i].File < refs[j].File
		}
		return refs[i].Line < refs[j].Line
	})
	type key struct {
		k, f string
		l    int
	}
	seen := make(map[key]bool, len(refs))
	out := refs[:0]
	for _, r := range refs {
		k := key{r.EnvKey, r.File, r.Line}
		if !seen[k] {
			seen[k] = true
			out = append(out, r)
		}
	}
	return out
}

// redactCredentialURL removes any embedded credentials from a URL string.
// It replaces user:password@ with [REDACTED]@ so the secret never appears in output.
func redactCredentialURL(s string) string {
	return reCredentialURLFull.ReplaceAllStringFunc(s, func(match string) string {
		sm := reCredentialURLFull.FindStringSubmatch(match)
		if sm == nil || sm[3] == "" {
			return match
		}
		scheme := strings.ToLower(sm[1])
		host := sm[4]
		path := sm[5]
		return fmt.Sprintf("%s://[REDACTED]@%s%s", scheme, host, path)
	})
}

// containsEnvKey checks for a word-boundary occurrence of key in line.
func containsEnvKey(line, key string) bool {
	idx := 0
	for {
		pos := strings.Index(line[idx:], key)
		if pos < 0 {
			return false
		}
		abs := idx + pos
		// Check left boundary.
		if abs > 0 {
			prev := line[abs-1]
			if prev == '_' || (prev >= 'a' && prev <= 'z') || (prev >= 'A' && prev <= 'Z') || (prev >= '0' && prev <= '9') {
				idx = abs + len(key)
				continue
			}
		}
		// Check right boundary.
		end := abs + len(key)
		if end < len(line) {
			next := line[end]
			if next == '_' || (next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z') || (next >= '0' && next <= '9') {
				idx = end
				continue
			}
		}
		return true
	}
}

// alternatesForProtocol returns the known alternate env keys for a given protocol.
func alternatesForProtocol(protocol string) []string {
	switch strings.ToLower(protocol) {
	case "postgres", "postgresql":
		return postgresAlternates
	case "redis":
		return redisAlternates
	}
	return nil
}
