package deploymentv1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

// Preflight result levels (overall status)
const (
	PreflightStatusPass             = "PASS"
	PreflightStatusPassWithWarnings = "PASS_WITH_WARNINGS"
	PreflightStatusBlocked          = "BLOCKED"
)

// Preflight check severities
const (
	CheckSeverityPass  = "PASS"
	CheckSeverityWarn  = "WARN"
	CheckSeverityBlock = "BLOCK"
)

// Scope kinds
const (
	ScopeKindApplication = "application"
	ScopeKindEnvironment = "environment"
	ScopeKindResource    = "resource"
	ScopeKindServer      = "server"
	ScopeKindTopology    = "topology"
	ScopeKindPolicy      = "policy"
)

// Remediation codes
const (
	RemediationCreateBuild             = "CREATE_BUILD"
	RemediationRebuildRequired         = "REBUILD_REQUIRED"
	RemediationPlanPlacement           = "PLAN_PLACEMENT"
	RemediationWaitForServer           = "WAIT_FOR_SERVER"
	RemediationRealizeDependency       = "REALIZE_DEPENDENCY"
	RemediationWaitForResource         = "WAIT_FOR_RESOURCE"
	RemediationConfigureExposure       = "CONFIGURE_EXPOSURE"
	RemediationResolveRouteConflict    = "RESOLVE_ROUTE_CONFLICT"
	RemediationReviewConfiguration     = "REVIEW_CONFIGURATION"
	RemediationIncludeDependencyTarget = "INCLUDE_DEPENDENCY_TARGET"
	RemediationExplicitMigration       = "EXPLICIT_MIGRATION_REQUIRED"
)

// Check codes - Build
const (
	CodeBuildRecordMissing     = "BUILD_RECORD_MISSING"
	CodeBuildRecordNotAccepted = "BUILD_RECORD_NOT_ACCEPTED"
	CodeBuildDependencyStale   = "BUILD_DEPENDENCY_STALE"
	CodeBuildArtifactInvalid   = "BUILD_ARTIFACT_INVALID"
)

// Check codes - Placement / Runtime
const (
	CodePlacementMissing = "PLACEMENT_MISSING"
	CodeRuntimeNotFound  = "RUNTIME_NOT_FOUND"
	CodeRuntimeNotReady  = "RUNTIME_NOT_READY"
	CodeAgentOffline     = "AGENT_OFFLINE"
	CodeCapacityInvalid  = "CAPACITY_INVALID"
)

// Check codes - Dependency
const (
	CodeDependencyTargetMissing       = "DEPENDENCY_TARGET_MISSING"
	CodeDependencyRequiredUnresolved  = "DEPENDENCY_REQUIRED_UNRESOLVED"
	CodeDependencyOptionalUnavailable = "DEPENDENCY_OPTIONAL_UNAVAILABLE"
	CodeDependencyRealizationMissing  = "DEPENDENCY_REALIZATION_MISSING"
	CodeDependencyBindingStale        = "DEPENDENCY_BINDING_STALE"
	CodeDependencyProjectionInvalid   = "DEPENDENCY_PROJECTION_INVALID"
)

// Check codes - App HTTP
const (
	CodeDependencyInternalTargetUnavailable = "DEPENDENCY_INTERNAL_TARGET_UNAVAILABLE"
	CodeDependencyPublicEndpointMissing     = "DEPENDENCY_PUBLIC_ENDPOINT_MISSING"
	CodeDependencyRouteConflict             = "DEPENDENCY_ROUTE_CONFLICT"
	CodeDependencyAccessContextInvalid      = "DEPENDENCY_ACCESS_CONTEXT_INVALID"
)

// Check codes - Configuration & Review
const (
	CodeConfigurationReviewStale = "CONFIGURATION_REVIEW_STALE"
	CodeTopologyReviewStale      = "TOPOLOGY_REVIEW_STALE"
	CodePolicyReviewStale        = "POLICY_REVIEW_STALE"
	CodePreflightWarningUnack    = "PREFLIGHT_WARNING_UNACKNOWLEDGED"
	CodePreflightReviewStale     = "PREFLIGHT_REVIEW_STALE"
	CodePreflightBlocked         = "PREFLIGHT_BLOCKED"
)

type PreflightCheck struct {
	ID                    string            `json:"id"`
	Code                  string            `json:"code"`
	Severity              string            `json:"severity"` // "PASS" | "WARN" | "BLOCK"
	ScopeKind             string            `json:"scope_kind"`
	ScopeID               string            `json:"scope_id"`
	DependencyLogicalName string            `json:"dependency_logical_name,omitempty"`
	TargetSafeID          string            `json:"target_safe_id,omitempty"`
	Message               string            `json:"message"`
	RemediationCode       string            `json:"remediation_code,omitempty"`
	SafeEvidence          map[string]string `json:"safe_evidence,omitempty"`
}

type PreflightResult struct {
	Status               string           `json:"status"` // "PASS" | "PASS_WITH_WARNINGS" | "BLOCKED"
	Checks               []PreflightCheck `json:"checks"`
	AuthorityFingerprint string           `json:"authority_fingerprint"`
	PreflightHash        string           `json:"preflight_hash"`
	GeneratedAt          time.Time        `json:"generated_at"`
}

func (r *PreflightResult) EvaluateStatus() {
	hasBlock := false
	hasWarn := false
	for _, check := range r.Checks {
		switch check.Severity {
		case CheckSeverityBlock:
			hasBlock = true
		case CheckSeverityWarn:
			hasWarn = true
		}
	}
	if hasBlock {
		r.Status = PreflightStatusBlocked
	} else if hasWarn {
		r.Status = PreflightStatusPassWithWarnings
	} else {
		r.Status = PreflightStatusPass
	}
}

func (r *PreflightResult) SortChecks() {
	sort.Slice(r.Checks, func(i, j int) bool {
		if r.Checks[i].ScopeKind != r.Checks[j].ScopeKind {
			return r.Checks[i].ScopeKind < r.Checks[j].ScopeKind
		}
		if r.Checks[i].ScopeID != r.Checks[j].ScopeID {
			return r.Checks[i].ScopeID < r.Checks[j].ScopeID
		}
		if r.Checks[i].DependencyLogicalName != r.Checks[j].DependencyLogicalName {
			return r.Checks[i].DependencyLogicalName < r.Checks[j].DependencyLogicalName
		}
		if r.Checks[i].Code != r.Checks[j].Code {
			return r.Checks[i].Code < r.Checks[j].Code
		}
		return r.Checks[i].ID < r.Checks[j].ID
	})
}

func (r *PreflightResult) WarningIDs() []string {
	ids := make([]string, 0)
	for _, check := range r.Checks {
		if check.Severity == CheckSeverityWarn {
			ids = append(ids, check.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func (r *PreflightResult) BlockIDs() []string {
	ids := make([]string, 0)
	for _, check := range r.Checks {
		if check.Severity == CheckSeverityBlock {
			ids = append(ids, check.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func (r *PreflightResult) ComputeHash(deploymentSet []string, revisions map[string]string) string {
	r.SortChecks()
	type hashCheck struct {
		ID        string `json:"id"`
		Code      string `json:"code"`
		Severity  string `json:"severity"`
		ScopeKind string `json:"scope_kind"`
		ScopeID   string `json:"scope_id"`
		DepName   string `json:"dep_name,omitempty"`
		TargetID  string `json:"target_id,omitempty"`
	}
	checks := make([]hashCheck, len(r.Checks))
	for i, c := range r.Checks {
		checks[i] = hashCheck{
			ID:        c.ID,
			Code:      c.Code,
			Severity:  c.Severity,
			ScopeKind: c.ScopeKind,
			ScopeID:   c.ScopeID,
			DepName:   c.DependencyLogicalName,
			TargetID:  c.TargetSafeID,
		}
	}
	sortedSet := append([]string(nil), deploymentSet...)
	sort.Strings(sortedSet)
	data, _ := json.Marshal(struct {
		Status        string            `json:"status"`
		Checks        []hashCheck       `json:"checks"`
		DeploymentSet []string          `json:"deployment_set"`
		Revisions     map[string]string `json:"revisions"`
	}{
		Status:        r.Status,
		Checks:        checks,
		DeploymentSet: sortedSet,
		Revisions:     revisions,
	})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
