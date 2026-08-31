// Package repositoryanalysis discovers deployable intent from an immutable
// repository snapshot. It is deliberately read-only and never executes source.
package repositoryanalysis

import "time"

const SchemaVersion = "opsi.repository_analysis/v1"

type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

type Evidence struct {
	Path       string     `json:"path"`
	Kind       string     `json:"kind"`
	Reason     string     `json:"reason"`
	Confidence Confidence `json:"confidence"`
}

type Build struct {
	Context        string `json:"context"`
	DockerfilePath string `json:"dockerfile_path,omitempty"`
	Strategy       string `json:"strategy"`
	Platform       string `json:"platform"`
	Image          string `json:"image,omitempty"`
}

type Application struct {
	SourceKey   string            `json:"source_key"`
	Key         string            `json:"key"`
	Name        string            `json:"name"`
	Root        string            `json:"root"`
	Port        int               `json:"port,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Capacity    Capacity          `json:"capacity,omitempty"`
	Exposure    Exposure          `json:"exposure,omitempty"`
	Build       Build             `json:"build"`
	Confidence  Confidence        `json:"confidence"`
	Reason      string            `json:"reason"`
	Evidence    []Evidence        `json:"evidence"`
}

type Capacity struct {
	Replicas    int32 `json:"replicas,omitempty" yaml:"replicas,omitempty"`
	CPUMilli    int64 `json:"cpu_milli,omitempty" yaml:"cpuMilli,omitempty"`
	MemoryBytes int64 `json:"memory_bytes,omitempty" yaml:"memoryBytes,omitempty"`
}

type Exposure struct {
	Mode            string   `json:"mode,omitempty" yaml:"mode,omitempty"`
	Hostname        string   `json:"hostname,omitempty" yaml:"hostname,omitempty"`
	Path            string   `json:"path,omitempty" yaml:"path,omitempty"`
	AdditionalPaths []string `json:"additional_paths,omitempty" yaml:"additionalPaths,omitempty"`
	Automatic       bool     `json:"automatic,omitempty" yaml:"automatic,omitempty"`
}

type Persistence struct {
	Persistent bool   `json:"persistent" yaml:"persistent"`
	SizeBytes  int64  `json:"size_bytes,omitempty" yaml:"sizeBytes,omitempty"`
	PolicyRef  string `json:"policy_ref,omitempty" yaml:"policyRef,omitempty"`
}

type Resource struct {
	LogicalName    string            `json:"logical_name"`
	Type           string            `json:"type"`
	Managed        bool              `json:"managed"`
	Required       bool              `json:"required"`
	Persistence    *Persistence      `json:"persistence,omitempty"`
	Settings       map[string]string `json:"settings,omitempty"`
	Recommendation string            `json:"recommendation,omitempty"`
	Confidence     Confidence        `json:"confidence"`
	Reason         string            `json:"reason"`
	Evidence       []Evidence        `json:"evidence"`
}

type Injection struct {
	EnvironmentName string `json:"environment_name"`
	SymbolicSource  string `json:"symbolic_source"`
	Template        string `json:"template,omitempty"`
}

type Dependency struct {
	From         string                `json:"from"`
	To           string                `json:"to"`
	Protocol     string                `json:"protocol"`
	Strategy     string                `json:"strategy,omitempty"`
	Path         string                `json:"path,omitempty"`
	ProxyPaths   []string              `json:"proxy_paths,omitempty"`
	Required     bool                  `json:"required"`
	Injections   []Injection           `json:"injections,omitempty"`
	Verification *VerificationContract `json:"verification,omitempty"`
	Confidence   Confidence            `json:"confidence"`
	Reason       string                `json:"reason"`
	Evidence     []Evidence            `json:"evidence"`
}

type VerificationContract struct {
	Type           string `json:"type" yaml:"type"`
	Path           string `json:"path,omitempty" yaml:"path,omitempty"`
	ExpectedStatus int    `json:"expected_status,omitempty" yaml:"expectedStatus,omitempty"`
}

type Binding struct {
	From       string     `json:"from"`
	To         string     `json:"to"`
	Kind       string     `json:"kind"`
	Path       string     `json:"path,omitempty"`
	Confidence Confidence `json:"confidence"`
	Reason     string     `json:"reason"`
	Evidence   []Evidence `json:"evidence"`
}

type Secret struct {
	Name            string     `json:"name"`
	ApplicationKey  string     `json:"application_key"`
	EnvironmentName string     `json:"environment_name"`
	Generated       bool       `json:"generated"`
	SecretRef       string     `json:"secret_ref,omitempty"`
	Revision        uint64     `json:"revision,omitempty"`
	Display         string     `json:"display"`
	Confidence      Confidence `json:"confidence"`
	Reason          string     `json:"reason"`
	Evidence        []Evidence `json:"evidence"`
}

type Issue struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Path       string `json:"path,omitempty"`
	Resolution string `json:"resolution,omitempty"`
	Blocking   bool   `json:"blocking"`
}

// Scope narrows heuristic analysis without changing the immutable source
// revision. Empty roots mean the whole repository. Excludes are repository-
// relative path prefixes and are applied before candidate selection.
type Scope struct {
	ApplicationRoots []string `json:"application_roots,omitempty"`
	ExcludePaths     []string `json:"exclude_paths,omitempty"`
}

type EvidenceCoverage struct {
	CandidatesFound    int   `json:"candidates_found"`
	CandidatesSelected int   `json:"candidates_selected"`
	FilesInspected     int   `json:"files_inspected"`
	BytesInspected     int64 `json:"bytes_inspected"`
}

type Result struct {
	SchemaVersion    string           `json:"schema_version"`
	RepositoryID     int64            `json:"repository_id"`
	Repository       string           `json:"repository"`
	SelectedRef      string           `json:"selected_ref"`
	CommitSHA        string           `json:"commit_sha"`
	Authority        string           `json:"authority"`
	Applications     []Application    `json:"applications"`
	Resources        []Resource       `json:"resources"`
	Dependencies     []Dependency     `json:"dependencies"`
	Bindings         []Binding        `json:"bindings"`
	Secrets          []Secret         `json:"secrets"`
	Issues           []Issue          `json:"issues"`
	Scope            Scope            `json:"scope"`
	ScopeHash        string           `json:"scope_hash"`
	EvidenceCoverage EvidenceCoverage `json:"evidence_coverage"`
	FilesInspected   int              `json:"files_inspected"`
	BytesInspected   int64            `json:"bytes_inspected"`
	Truncated        bool             `json:"truncated"`
	TruncationReason string           `json:"truncation_reason,omitempty"`
	AnalyzedAt       time.Time        `json:"analyzed_at"`
}

func (r Result) NeedsInput() bool {
	for _, issue := range r.Issues {
		if issue.Blocking {
			return true
		}
	}
	return false
}
