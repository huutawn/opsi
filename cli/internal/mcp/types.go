package mcp

import (
	"encoding/json"
	"time"
)

const (
	ProtocolVersion = "2024-11-05"
	ServerName      = "opsi-mcp"
	SurfaceVersion  = "mcp-02"
)

// Standard JSON-RPC 2.0 error codes
const (
	JSONRPCParseError     = -32700
	JSONRPCInvalidRequest = -32600
	JSONRPCMethodNotFound = -32601
	JSONRPCInvalidParams  = -32602
	JSONRPCInternalError  = -32603
)

// MCP domain error codes
const (
	ErrCodeAuthRequired              = "AUTH_REQUIRED"
	ErrCodeForbidden                 = "FORBIDDEN"
	ErrCodeNotFound                  = "NOT_FOUND"
	ErrCodeAmbiguousProject          = "AMBIGUOUS_PROJECT"
	ErrCodeSourceSnapshotUnavailable = "SOURCE_SNAPSHOT_UNAVAILABLE"
	ErrCodeSourcePathInvalid         = "SOURCE_PATH_INVALID"
	ErrCodeSourceFileTooLarge        = "SOURCE_FILE_TOO_LARGE"
	ErrCodeSourceBinaryUnsupported   = "SOURCE_BINARY_UNSUPPORTED"
	ErrCodeLimitExceeded             = "LIMIT_EXCEEDED"
	ErrCodeAuthorityUnavailable      = "AUTHORITY_UNAVAILABLE"
	ErrCodeInvalidArgument           = "INVALID_ARGUMENT"
	ErrCodeProposalStale             = "PROPOSAL_STALE"
)

type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      any           `json:"id"`
	Result  any           `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type InitializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      ClientInfo     `json:"clientInfo"`
}

type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
	Instructions    string             `json:"instructions,omitempty"`
}

type ServerCapabilities struct {
	Tools     *ToolsCapability     `json:"tools,omitempty"`
	Resources *ResourcesCapability `json:"resources,omitempty"`
}

type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

type ResourcesCapability struct {
	Subscribe   bool `json:"subscribe,omitempty"`
	ListChanged bool `json:"listChanged,omitempty"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema ToolInputSchema `json:"inputSchema"`
}

type ToolInputSchema struct {
	Type       string                 `json:"type"`
	Properties map[string]PropertyDoc `json:"properties,omitempty"`
	Required   []string               `json:"required,omitempty"`
}

type PropertyDoc struct {
	Type        string                 `json:"type"`
	Description string                 `json:"description"`
	Enum        []string               `json:"enum,omitempty"`
	Default     any                    `json:"default,omitempty"`
	Properties  map[string]PropertyDoc `json:"properties,omitempty"`
	Required    []string               `json:"required,omitempty"`
	Items       *PropertyDoc           `json:"items,omitempty"`
}

// DependencyProposal is deliberately a transport-only advisory object. It is
// never persisted and is not an authority to change a ServiceConfiguration.
type DependencyProposal struct {
	ProjectID     string                       `json:"project_id"`
	EnvironmentID string                       `json:"environment_id"`
	ApplicationID string                       `json:"application_id"`
	Provenance    DependencyProposalProvenance `json:"provenance"`
	Candidate     DependencyCandidate          `json:"candidate"`
	Evidence      []DependencyEvidence         `json:"evidence"`
	Confidence    string                       `json:"confidence"`
}

type DependencyProposalProvenance struct {
	SourceCommit       string `json:"source_commit"`
	ApplicationRoot    string `json:"application_root"`
	AnalysisInputsHash string `json:"analysis_inputs_hash"`
}

type DependencyCandidate struct {
	LogicalName          string                       `json:"logical_name"`
	DependencyKind       string                       `json:"dependency_kind"`
	TargetID             string                       `json:"target_id,omitempty"`
	Protocol             string                       `json:"protocol"`
	Phase                string                       `json:"phase"`
	Required             bool                         `json:"required"`
	AccessContext        string                       `json:"access_context,omitempty"`
	Strategy             string                       `json:"strategy,omitempty"`
	Path                 string                       `json:"path,omitempty"`
	Mappings             []DependencyInjectionMapping `json:"mappings"`
	VerificationContract *DependencyVerificationDoc   `json:"verification_contract,omitempty"`
}

type DependencyEvidence struct {
	Type        string `json:"type"`
	File        string `json:"file"`
	Line        int    `json:"line"`
	SafeExcerpt string `json:"safe_excerpt,omitempty"`
	Symbol      string `json:"symbol,omitempty"`
	Reason      string `json:"reason"`
}

type DependencyAnalysisContext struct {
	Application         ApplicationDetailResult       `json:"application"`
	Source              DependencySourceProvenance    `json:"source"`
	CurrentDependencies []ApplicationDependencyDoc    `json:"current_dependencies"`
	CompatibleTargets   DependencyCompatibleTargets   `json:"compatible_targets"`
	SourceRiskFindings  []DependencyRiskFinding       `json:"source_risk_findings,omitempty"`
	Verification        []DependencyVerificationState `json:"verification,omitempty"`
	Authority           DependencyAuthoritySnapshot   `json:"authority"`
	Bounds              DependencyContextBounds       `json:"bounds"`
}

type DependencySourceProvenance struct {
	BuildRecordID   string `json:"build_record_id"`
	CommitSHA       string `json:"commit_sha"`
	ApplicationRoot string `json:"application_root"`
	BuildContext    string `json:"build_context"`
	BuildStrategy   string `json:"build_strategy"`
}

type DependencyCompatibleTargets struct {
	Applications     []DependencyApplicationTarget `json:"applications"`
	ManagedResources []DependencyResourceTarget    `json:"managed_resources"`
}

type DependencyApplicationTarget struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	PublicRoute *PublicRouteSummary `json:"public_route,omitempty"`
}

type DependencyResourceTarget struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Protocol      string `json:"protocol"`
	EnvironmentID string `json:"environment_id"`
	Lifecycle     string `json:"lifecycle"`
}

type DependencyRiskFinding struct {
	RuleID       string `json:"rule_id"`
	Severity     string `json:"severity"`
	Confidence   string `json:"confidence"`
	File         string `json:"file"`
	Line         int    `json:"line"`
	SafeEvidence string `json:"safe_evidence"`
}

type DependencyVerificationState struct {
	LogicalName string `json:"logical_name"`
	Status      string `json:"status"`
}

type DependencyAuthoritySnapshot struct {
	ServiceConfigurationRevision  uint64 `json:"service_configuration_revision"`
	ServiceConfigurationStateHash string `json:"service_configuration_state_hash"`
	DependencyContractHash        string `json:"dependency_contract_hash"`
	TopologyRevision              uint64 `json:"topology_revision"`
	TopologyStateHash             string `json:"topology_state_hash"`
	AnalysisInputsHash            string `json:"analysis_inputs_hash"`
}

type DependencyContextBounds struct {
	MaximumTargets         int `json:"maximum_targets"`
	MaximumRiskFindings    int `json:"maximum_risk_findings"`
	MaximumEvidence        int `json:"maximum_evidence"`
	MaximumEvidenceExcerpt int `json:"maximum_evidence_excerpt_bytes"`
}

type DependencyProposalValidation struct {
	Status             string                     `json:"status"`
	Action             string                     `json:"action"`
	TargetResolution   string                     `json:"target_resolution"`
	AnalysisInputsHash string                     `json:"analysis_inputs_hash"`
	ProposalHash       string                     `json:"proposal_hash"`
	Issues             []DependencyProposalIssue  `json:"issues,omitempty"`
	Warnings           []DependencyProposalIssue  `json:"warnings,omitempty"`
	SemanticDiff       []DependencySemanticChange `json:"semantic_diff,omitempty"`
	Impact             string                     `json:"impact,omitempty"`
}

type DependencyProposalIssue struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

type DependencySemanticChange struct {
	Action string `json:"action"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

type CallToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type CallToolResult struct {
	Content []ContentItem `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type ContentItem struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`
}

type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

type ReadResourceParams struct {
	URI string `json:"uri"`
}

type ReadResourceResult struct {
	Contents []ResourceContent `json:"contents"`
}

type ResourceContent struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Source DTOs

type SourceFileItem struct {
	Path        string `json:"path"` // relative to ApplicationRoot
	SizeBytes   int64  `json:"size_bytes"`
	IsBinary    bool   `json:"is_binary"`
	IsDirectory bool   `json:"is_directory"`
}

type SourceFilesResult struct {
	ApplicationID   string           `json:"application_id"`
	CommitSHA       string           `json:"commit_sha"`
	ApplicationRoot string           `json:"application_root"`
	TotalFiles      int              `json:"total_files"`
	Files           []SourceFileItem `json:"files"`
	NextCursor      string           `json:"next_cursor,omitempty"`
}

type SourceFileResult struct {
	ApplicationID   string `json:"application_id"`
	CommitSHA       string `json:"commit_sha"`
	ApplicationRoot string `json:"application_root"`
	RelativePath    string `json:"relative_path"`
	SizeBytes       int64  `json:"size_bytes"`
	Content         string `json:"content,omitempty"`
	Truncated       bool   `json:"truncated"`
	IsBinary        bool   `json:"is_binary"`
	Redacted        bool   `json:"redacted"`
}

type SourceSearchMatch struct {
	File         string `json:"file"` // relative to ApplicationRoot
	LineNumber   int    `json:"line_number"`
	MatchSnippet string `json:"match_snippet"`
}

type SourceSearchResult struct {
	ApplicationID   string              `json:"application_id"`
	CommitSHA       string              `json:"commit_sha"`
	ApplicationRoot string              `json:"application_root"`
	Query           string              `json:"query"`
	Matches         []SourceSearchMatch `json:"matches"`
	MatchesCount    int                 `json:"matches_count"`
	FilesScanned    int                 `json:"files_scanned"`
	BytesScanned    int64               `json:"bytes_scanned"`
	Truncated       bool                `json:"truncated"`
}

// Safe context DTOs

type ProjectContextResult struct {
	ProjectID            string            `json:"project_id"`
	OrgID                string            `json:"org_id"`
	Name                 string            `json:"name"`
	Slug                 string            `json:"slug"`
	Status               string            `json:"status"`
	Environment          string            `json:"environment,omitempty"`
	ApplicationCount     int               `json:"application_count"`
	NodeCount            int               `json:"node_count"`
	ManagedResourceCount int               `json:"managed_resource_count"`
	TopologyRevision     uint64            `json:"topology_revision"`
	TopologyStateHash    string            `json:"topology_state_hash"`
	DeploymentSummary    DeploymentSummary `json:"deployment_summary"`
}

type DeploymentSummary struct {
	TotalDeployments int        `json:"total_deployments"`
	LatestStatus     string     `json:"latest_status,omitempty"`
	LatestServiceID  string     `json:"latest_service_id,omitempty"`
	LatestDeployedAt *time.Time `json:"latest_deployed_at,omitempty"`
}

type ApplicationSummary struct {
	ID                 string         `json:"id"`
	Name               string         `json:"name"`
	Status             string         `json:"status"`
	SourceBinding      *SourceBinding `json:"source_binding,omitempty"`
	CurrentBuildRecord string         `json:"current_build_record_id,omitempty"`
	CurrentCommitSHA   string         `json:"current_commit_sha,omitempty"`
	PlacementRuntimeID string         `json:"placement_runtime_id,omitempty"`
	DependencyCount    int            `json:"dependency_count"`
	LatestDeploymentID string         `json:"latest_deployment_id,omitempty"`
	LatestDeployStatus string         `json:"latest_deployment_status,omitempty"`
}

type SourceBinding struct {
	RepositoryID    int64  `json:"repository_id"`
	ServiceKey      string `json:"service_key"`
	SelectedRef     string `json:"selected_ref"`
	ApplicationRoot string `json:"application_root"`
	BuildContext    string `json:"build_context"`
	BuildStrategy   string `json:"build_strategy"`
	DockerfilePath  string `json:"dockerfile_path,omitempty"`
}

type ApplicationDetailResult struct {
	ID                        string                     `json:"id"`
	Name                      string                     `json:"name"`
	Status                    string                     `json:"status"`
	SourceBinding             *SourceBinding             `json:"source_binding,omitempty"`
	ExactCommitSHA            string                     `json:"exact_commit_sha,omitempty"`
	ServiceConfigRevision     uint64                     `json:"service_config_revision"`
	ServiceConfigStateHash    string                     `json:"service_config_state_hash"`
	EnvironmentVariablesSafe  []string                   `json:"environment_variables_safe"`
	PublicRoute               *PublicRouteSummary        `json:"public_route,omitempty"`
	CurrentBuildRecordID      string                     `json:"current_build_record_id,omitempty"`
	PlacementRuntimeID        string                     `json:"placement_runtime_id,omitempty"`
	DependenciesSummary       []ApplicationDependencyDoc `json:"dependencies_summary,omitempty"`
	LatestVerificationSummary *VerificationSummary       `json:"latest_verification_summary,omitempty"`
}

type PublicRouteSummary struct {
	Hostname string `json:"hostname"`
	Path     string `json:"path"`
}

type ApplicationDependencyDoc struct {
	LogicalName              string                       `json:"logical_name"`
	TargetKind               string                       `json:"target_kind"`
	TargetIdentity           string                       `json:"target_identity"`
	Protocol                 string                       `json:"protocol"`
	Strategy                 string                       `json:"strategy,omitempty"`
	AccessContext            string                       `json:"access_context,omitempty"`
	Path                     string                       `json:"path,omitempty"`
	Required                 bool                         `json:"required"`
	InjectionPhase           string                       `json:"injection_phase"`
	SymbolicMappings         []DependencyInjectionMapping `json:"symbolic_mappings,omitempty"`
	VerificationContract     *DependencyVerificationDoc   `json:"verification_contract,omitempty"`
	Realized                 bool                         `json:"realized"`
	ResourceBindingID        string                       `json:"resource_binding_id,omitempty"`
	ResourceBindingStatus    string                       `json:"resource_binding_status,omitempty"`
	LatestVerificationStatus string                       `json:"latest_verification_status,omitempty"`
}

type DependencyInjectionMapping struct {
	EnvName        string `json:"env_name"`
	SymbolicSource string `json:"symbolic_source"`
}

type DependencyVerificationDoc struct {
	Type           string `json:"type"`
	Path           string `json:"path"`
	ExpectedStatus int    `json:"expected_status"`
}

type VerificationSummary struct {
	OverallStatus string    `json:"overall_status"`
	FailureCode   string    `json:"failure_code,omitempty"`
	StartedAt     time.Time `json:"started_at"`
}

type ManagedResourceSummary struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Type          string `json:"type"`
	Version       string `json:"version,omitempty"`
	Lifecycle     string `json:"lifecycle"`
	EnvironmentID string `json:"environment_id"`
	RuntimeID     string `json:"runtime_id,omitempty"`
	Replicas      int32  `json:"replicas,omitempty"`
	CPUMillicores int64  `json:"cpu_millicores,omitempty"`
	MemoryBytes   int64  `json:"memory_bytes,omitempty"`
	BindingCount  int    `json:"binding_count"`
}

type ManagedResourceDetailResult struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Type           string `json:"type"`
	Version        string `json:"version,omitempty"`
	Lifecycle      string `json:"lifecycle"`
	EnvironmentID  string `json:"environment_id"`
	RuntimeID      string `json:"runtime_id,omitempty"`
	Endpoint       string `json:"safe_endpoint,omitempty"`
	BindingCount   int    `json:"binding_count"`
	BackupSummary  string `json:"backup_summary,omitempty"`
	CutoverSummary string `json:"cutover_summary,omitempty"`
}
