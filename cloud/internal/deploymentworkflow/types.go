// Package deploymentworkflow owns the single Repository-to-Running workflow.
// It coordinates canonical authorities and stores only immutable intent plus
// factual authority object references.
package deploymentworkflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/repositoryanalysis"
	resourcecompiler "github.com/opsi-dev/opsi/cloud/internal/resource/connection"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
)

const (
	PlanSchemaVersion         = "opsi.deployment_plan/v3"
	RunSchemaVersion          = "opsi.deployment_run/v3"
	legacyPlanV2SchemaVersion = "opsi.deployment_plan/v2"
	legacyRunV2SchemaVersion  = "opsi.deployment_run/v2"
	legacyPlanV1SchemaVersion = "opsi.deployment_plan/v1"
	legacyRunV1SchemaVersion  = "opsi.deployment_run/v1"
)

type State string

const (
	StateAnalyzing          State = "analyzing"
	StateAwaitingInput      State = "awaiting_input"
	StateAwaitingApproval   State = "awaiting_approval"
	StateProvisioning       State = "provisioning"
	StateBuilding           State = "building"
	StatePreflighting       State = "preflighting"
	StateAwaitingWarningAck State = "awaiting_warning_ack"
	StateDeploying          State = "deploying"
	StateVerifying          State = "verifying"
	StateSucceeded          State = "succeeded"
	StateStale              State = "stale"
	StateFailed             State = "failed"
	StateRollingBack        State = "rolling_back"
	StateCleaningUp         State = "cleaning_up"
	StateRolledBack         State = "rolled_back"
	StateCancelled          State = "cancelled"
)

type Source struct {
	RepositoryID   int64  `json:"repository_id"`
	InstallationID int64  `json:"installation_id"`
	Repository     string `json:"repository"`
	SelectedRef    string `json:"selected_ref"`
	CommitSHA      string `json:"commit_sha"`
}

type AuthorityRevisions struct {
	SourceCommitSHA     string `json:"source_commit_sha"`
	RepositoryUpdatedAt string `json:"repository_updated_at,omitempty"`
	TopologyRevision    uint64 `json:"topology_revision,omitempty"`
	TopologyHash        string `json:"topology_hash,omitempty"`
	PolicyRevision      uint64 `json:"policy_revision,omitempty"`
	PolicyHash          string `json:"policy_hash,omitempty"`
	ResourceRevision    uint64 `json:"resource_revision,omitempty"`
	ResourceHash        string `json:"resource_hash,omitempty"`
}

type Target struct {
	EnvironmentID    string `json:"environment_id"`
	RuntimeID        string `json:"runtime_id,omitempty"`
	NodeID           string `json:"node_id,omitempty"`
	Hostname         string `json:"hostname,omitempty"`
	Exposure         string `json:"exposure"`
	PublicRoutes     string `json:"public_routes,omitempty"`
	CPUMilli         int64  `json:"cpu_milli,omitempty"`
	MemoryBytes      int64  `json:"memory_bytes,omitempty"`
	CPULimitMilli    int64  `json:"cpu_limit_milli,omitempty"`
	MemoryLimitBytes int64  `json:"memory_limit_bytes,omitempty"`
}

const (
	PublicRoutesAutomatic = "automatic"
	PublicRoutesManual    = "manual"
)

type FailurePolicy struct {
	FailFast             bool `json:"fail_fast"`
	RollbackKnownGood    bool `json:"rollback_known_good"`
	RetainPersistentData bool `json:"retain_persistent_data"`
	MaxAttempts          int  `json:"max_attempts"`
}
type ApplicationEnvironmentReview struct {
	ApplicationSourceKey  string `json:"application_source_key"`
	NoEnvironmentRequired bool   `json:"no_environment_required"`
}

type Plan struct {
	SchemaVersion                 string                              `json:"schema_version"`
	Hash                          string                              `json:"hash"`
	Source                        Source                              `json:"source"`
	Applications                  []repositoryanalysis.Application    `json:"applications"`
	Resources                     []repositoryanalysis.Resource       `json:"resources"`
	Dependencies                  []repositoryanalysis.Dependency     `json:"dependencies"`
	Bindings                      []repositoryanalysis.Binding        `json:"bindings"`
	Secrets                       []repositoryanalysis.Secret         `json:"secrets"`
	ApplicationEnvironmentReviews []ApplicationEnvironmentReview      `json:"application_environment_reviews,omitempty"`
	Issues                        []repositoryanalysis.Issue          `json:"issues"`
	AnalysisScope                 repositoryanalysis.Scope            `json:"analysis_scope"`
	AnalysisScopeHash             string                              `json:"analysis_scope_hash"`
	EvidenceCoverage              repositoryanalysis.EvidenceCoverage `json:"evidence_coverage"`
	TruncationReason              string                              `json:"truncation_reason,omitempty"`
	Target                        Target                              `json:"target"`
	Authority                     AuthorityRevisions                  `json:"authority_revisions"`
	FailurePolicy                 FailurePolicy                       `json:"failure_policy"`
}
type Approval struct {
	Actor              string             `json:"actor"`
	PlanHash           string             `json:"plan_hash"`
	AuthorityRevisions AuthorityRevisions `json:"authority_revisions"`
	ApprovedAt         time.Time          `json:"approved_at"`
}

type WarningAcknowledgement struct {
	Actor          string    `json:"actor"`
	PreflightHash  string    `json:"preflight_hash"`
	AcknowledgedAt time.Time `json:"acknowledged_at"`
}

type AuthorityCheckpoint struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Revision  uint64 `json:"revision,omitempty"`
	StateHash string `json:"state_hash,omitempty"`
	Step      State  `json:"step"`
}

type AuthorityRefs struct {
	Checkpoints []AuthorityCheckpoint `json:"checkpoints,omitempty"`
}

func (r *AuthorityRefs) UnmarshalJSON(data []byte) error {
	type checkpointShape struct {
		Checkpoints         []AuthorityCheckpoint `json:"checkpoints"`
		ResourceIDs         []string              `json:"resource_ids"`
		BindingIDs          []string              `json:"binding_ids"`
		ApplicationIDs      []string              `json:"application_ids"`
		BuildJobIDs         []string              `json:"build_job_ids"`
		BuildRecordIDs      []string              `json:"build_record_ids"`
		TopologyPlanID      string                `json:"topology_plan_id"`
		DeploymentPolicyIDs []string              `json:"deployment_policy_ids"`
		DeploymentJobIDs    []string              `json:"deployment_job_ids"`
		VerificationIDs     []string              `json:"verification_ids"`
	}
	var value checkpointShape
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	r.Checkpoints = append([]AuthorityCheckpoint(nil), value.Checkpoints...)
	legacy := []struct {
		kind string
		step State
		ids  []string
	}{
		{AuthorityResource, StateProvisioning, value.ResourceIDs}, {AuthorityBinding, StateProvisioning, value.BindingIDs}, {AuthorityApplication, StateProvisioning, value.ApplicationIDs},
		{AuthorityBuildJob, StateBuilding, value.BuildJobIDs}, {AuthorityBuildRecord, StateBuilding, value.BuildRecordIDs}, {AuthorityDeploymentPolicy, StatePreflighting, value.DeploymentPolicyIDs},
		{AuthorityDeploymentJob, StateDeploying, value.DeploymentJobIDs}, {AuthorityVerification, StateVerifying, value.VerificationIDs},
	}
	if value.TopologyPlanID != "" {
		r.Checkpoints = append(r.Checkpoints, AuthorityCheckpoint{Kind: AuthorityTopologyPlan, ID: value.TopologyPlanID, Step: StateProvisioning})
	}
	for _, group := range legacy {
		for _, id := range group.ids {
			if id != "" {
				r.Checkpoints = append(r.Checkpoints, AuthorityCheckpoint{Kind: group.kind, ID: id, Step: group.step})
			}
		}
	}
	return nil
}

const (
	AuthorityResource         = "resource"
	AuthorityBinding          = "resource_binding"
	AuthorityApplication      = "application"
	AuthorityBuildJob         = "build_job"
	AuthorityBuildRecord      = "build_record"
	AuthorityTopologyPlan     = "topology_plan"
	AuthorityDeploymentPolicy = "deployment_policy"
	AuthorityDeploymentJob    = "deployment_job"
	AuthorityVerification     = "verification"
	AuthorityWorkloadSecret   = "workload_secret"
)

func (r AuthorityRefs) IDs(kind string) []string {
	values := []string{}
	for _, checkpoint := range r.Checkpoints {
		if checkpoint.Kind == kind && checkpoint.ID != "" {
			values = append(values, checkpoint.ID)
		}
	}
	return values
}

func (r AuthorityRefs) FirstID(kind string) string {
	for _, checkpoint := range r.Checkpoints {
		if checkpoint.Kind == kind {
			return checkpoint.ID
		}
	}
	return ""
}

func Checkpoint(kind, id string, revision uint64, stateHash string, step State) AuthorityCheckpoint {
	return AuthorityCheckpoint{Kind: kind, ID: id, Revision: revision, StateHash: stateHash, Step: step}
}

func Checkpoints(kind string, step State, ids ...string) AuthorityRefs {
	refs := AuthorityRefs{}
	for _, id := range ids {
		if id != "" {
			refs.Checkpoints = append(refs.Checkpoints, Checkpoint(kind, id, 0, "", step))
		}
	}
	return refs
}

type Failure struct {
	Step       State  `json:"step"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	NextAction string `json:"next_action,omitempty"`
	Retryable  bool   `json:"retryable"`
}

type PublicRouteFailure struct {
	ServiceKey string `json:"service_key"`
	Message    string `json:"message"`
}

type Run struct {
	SchemaVersion          string                    `json:"schema_version"`
	ID                     string                    `json:"id"`
	ProjectID              string                    `json:"project_id"`
	CreatedBy              string                    `json:"created_by"`
	State                  State                     `json:"state"`
	Plan                   Plan                      `json:"plan"`
	Analysis               repositoryanalysis.Result `json:"analysis"`
	Approval               *Approval                 `json:"approval,omitempty"`
	WarningAcknowledgement *WarningAcknowledgement   `json:"warning_acknowledgement,omitempty"`
	PreflightHash          string                    `json:"preflight_hash,omitempty"`
	PreflightWarnings      []string                  `json:"preflight_warnings,omitempty"`
	Refs                   AuthorityRefs             `json:"authority_refs"`
	Failure                *Failure                  `json:"failure,omitempty"`
	PublicRouteFailures    []PublicRouteFailure      `json:"public_route_failures,omitempty"`
	Attempt                int                       `json:"attempt"`
	RetryAfterAt           *time.Time                `json:"retry_after_at,omitempty"`
	Revision               uint64                    `json:"revision"`
	CreatedAt              time.Time                 `json:"created_at"`
	UpdatedAt              time.Time                 `json:"updated_at"`
	FinishedAt             *time.Time                `json:"finished_at,omitempty"`
}

type Event struct {
	ID        string         `json:"id"`
	ProjectID string         `json:"project_id"`
	RunID     string         `json:"run_id"`
	State     State          `json:"state"`
	Level     string         `json:"level"`
	Message   string         `json:"message"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

func HashPlan(plan Plan) (string, error) {
	// Hashing must be pure. A shallow copy aliases slice backing arrays and
	// sorting those slices can silently rewrite a caller's draft plan.
	encoded, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	var copy Plan
	if err := json.Unmarshal(encoded, &copy); err != nil {
		return "", err
	}
	if err := normalizePlanCapacity(&copy); err != nil {
		return "", err
	}
	copy.Hash = ""
	sort.Slice(copy.Applications, func(i, j int) bool { return copy.Applications[i].Key < copy.Applications[j].Key })
	sort.Slice(copy.Resources, func(i, j int) bool { return copy.Resources[i].LogicalName < copy.Resources[j].LogicalName })
	sort.Slice(copy.Dependencies, func(i, j int) bool {
		a, b := copy.Dependencies[i], copy.Dependencies[j]
		if a.From == b.From {
			return a.To < b.To
		}
		return a.From < b.From
	})
	for i := range copy.Applications {
		sortEvidence(copy.Applications[i].Evidence)
	}
	for i := range copy.Resources {
		sortEvidence(copy.Resources[i].Evidence)
	}
	for i := range copy.Dependencies {
		sort.Slice(copy.Dependencies[i].Injections, func(j, k int) bool {
			return copy.Dependencies[i].Injections[j].EnvironmentName < copy.Dependencies[i].Injections[k].EnvironmentName
		})
		sort.Strings(copy.Dependencies[i].ProxyPaths)
		sortEvidence(copy.Dependencies[i].Evidence)
	}
	sort.Slice(copy.Bindings, func(i, j int) bool {
		return copy.Bindings[i].From+"\x00"+copy.Bindings[i].To+"\x00"+copy.Bindings[i].Kind+"\x00"+copy.Bindings[i].Path < copy.Bindings[j].From+"\x00"+copy.Bindings[j].To+"\x00"+copy.Bindings[j].Kind+"\x00"+copy.Bindings[j].Path
	})
	for i := range copy.Bindings {
		sortEvidence(copy.Bindings[i].Evidence)
	}
	sort.Slice(copy.Secrets, func(i, j int) bool {
		return copy.Secrets[i].ApplicationKey+"\x00"+copy.Secrets[i].Name < copy.Secrets[j].ApplicationKey+"\x00"+copy.Secrets[j].Name
	})
	for i := range copy.Secrets {
		sortEvidence(copy.Secrets[i].Evidence)
	}
	sort.Slice(copy.ApplicationEnvironmentReviews, func(i, j int) bool {
		return copy.ApplicationEnvironmentReviews[i].ApplicationSourceKey < copy.ApplicationEnvironmentReviews[j].ApplicationSourceKey
	})
	sort.Slice(copy.Issues, func(i, j int) bool {
		return copy.Issues[i].Code+"\x00"+copy.Issues[i].Path+"\x00"+copy.Issues[i].Message < copy.Issues[j].Code+"\x00"+copy.Issues[j].Path+"\x00"+copy.Issues[j].Message
	})
	data, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func sortEvidence(values []repositoryanalysis.Evidence) {
	sort.Slice(values, func(i, j int) bool {
		return values[i].Path+"\x00"+values[i].Kind+"\x00"+values[i].Reason < values[j].Path+"\x00"+values[j].Kind+"\x00"+values[j].Reason
	})
}

func ValidatePlan(plan Plan) error {
	if plan.SchemaVersion != PlanSchemaVersion || plan.Source.RepositoryID <= 0 || plan.Source.InstallationID <= 0 || plan.Source.Repository == "" || len(plan.Source.CommitSHA) != 40 || len(plan.Applications) == 0 {
		return errors.New("deployment plan identity is invalid")
	}
	if plan.FailurePolicy.MaxAttempts < 1 || plan.FailurePolicy.MaxAttempts > 5 {
		return errors.New("deployment plan retry policy is invalid")
	}
	if plan.Target.Exposure != "public" && plan.Target.Exposure != "internal" {
		return errors.New("deployment plan exposure is invalid")
	}
	if plan.Target.Exposure == "public" && plan.Target.PublicRoutes != "" && plan.Target.PublicRoutes != PublicRoutesAutomatic && plan.Target.PublicRoutes != PublicRoutesManual {
		return errors.New("deployment plan public route policy is invalid")
	}
	if err := validateTargetCapacity(plan.Target); err != nil {
		return err
	}
	if plan.Target.Exposure == "public" {
		hostname, err := exposurev1.NormalizeHostname(plan.Target.Hostname)
		if err != nil || hostname != plan.Target.Hostname {
			return errors.New("public deployment hostname is invalid")
		}
	}
	applications := map[string]bool{}
	for i := range plan.Applications {
		application := &plan.Applications[i]
		if application.Key == "" || application.SourceKey == "" || applications[application.Key] || application.Build.Strategy == "" || application.Build.Context == "" || application.Port < 1 || application.Port > 65535 {
			return errors.New("deployment plan application intent is invalid")
		}
		if err := ValidateApplicationCapacity(application.Capacity); err != nil {
			return fmt.Errorf("application %s capacity invalid: %w", application.Key, err)
		}
		applications[application.Key] = true
		if application.Exposure.Mode == "public" {
			hostname, err := exposurev1.NormalizeHostname(application.Exposure.Hostname)
			if err != nil || hostname != application.Exposure.Hostname {
				return errors.New("deployment plan application hostname is invalid")
			}
		}
	}
	resources := map[string]bool{}
	for _, resource := range plan.Resources {
		if resource.LogicalName == "" || resource.Type == "" || resources[resource.LogicalName] {
			return errors.New("deployment plan resource intent is invalid")
		}
		if resource.Managed {
			resourceType := resourcev1.Type(resource.Type)
			if resourceType == "valkey" {
				resourceType = resourcev1.TypeRedis
			}
			definition, ok := resourcev1.Definition(resourceType)
			if !ok || !definition.Provisioning.Implemented {
				return errors.New("deployment plan managed resource type is unsupported")
			}
			persistence := resource.Persistence
			if definition.Storage.Required && (persistence == nil || !persistence.Persistent) {
				return errors.New("deployment plan managed resource requires persistent storage")
			}
			if persistence != nil {
				if persistence.SizeBytes < 0 || persistence.SizeBytes > resourcev1.MaxManagedStorageBytes || persistence.Persistent != (persistence.SizeBytes > 0) || (!persistence.Persistent && persistence.PolicyRef != "") {
					return errors.New("deployment plan managed resource storage intent is invalid")
				}
				if persistence.Persistent && !definition.Storage.Supported {
					return errors.New("deployment plan managed resource storage is unsupported")
				}
				if resourceType == resourcev1.TypePostgres && persistence.PolicyRef != resourcev1.StoragePolicyDefault {
					return errors.New("deployment plan managed PostgreSQL storage policy is invalid")
				}
			}
		}
		resources[resource.LogicalName] = true
	}
	for _, dependency := range plan.Dependencies {
		if !applications[dependency.From] || dependency.To == dependency.From || (!applications[dependency.To] && !resources[dependency.To]) || dependency.Protocol == "" {
			return errors.New("deployment plan dependency intent is invalid")
		}
		if dependency.Protocol != serviceconfigurationv1.ProtocolHTTP && !resourcecompiler.ValidManagedProtocol(dependency.Protocol) {
			return errors.New("deployment plan managed dependency protocol is unsupported")
		}
		if dependency.Required && dependency.Protocol != "postgres" && dependency.Protocol != "redis" && dependency.Protocol != "nats" && dependency.Verification == nil {
			return errors.New("required dependency verification contract is missing")
		}
		if len(dependency.ProxyPaths) > 0 {
			if dependency.Protocol != serviceconfigurationv1.ProtocolHTTP || dependency.Strategy != serviceconfigurationv1.StrategyInternalHTTP || len(dependency.ProxyPaths) > exposurev1.MaxAdditionalPaths+1 {
				return errors.New("deployment plan application proxy paths are invalid")
			}
			seenPaths := map[string]bool{}
			for _, path := range dependency.ProxyPaths {
				canonicalPath, err := exposurev1.NormalizePath(path)
				if err != nil || canonicalPath == "/" || seenPaths[canonicalPath] {
					return errors.New("deployment plan application proxy paths are invalid")
				}
				seenPaths[canonicalPath] = true
			}
			if !seenPaths[dependency.Path] {
				return errors.New("deployment plan primary application proxy path is missing")
			}
		}
		seenMappings := map[string]bool{}
		for _, mapping := range dependency.Injections {
			if mapping.EnvironmentName == "" || mapping.SymbolicSource == "" || seenMappings[mapping.EnvironmentName] {
				return errors.New("deployment plan dependency mapping is invalid")
			}
			seenMappings[mapping.EnvironmentName] = true
			if dependency.Protocol == "http" {
				if !resourcecompiler.ValidApplicationSource(dependency.Strategy, mapping.SymbolicSource, mapping.Template) {
					return errors.New("deployment plan application mapping is invalid")
				}
			} else if _, err := resourcecompiler.LookupSource(dependency.Protocol, mapping.SymbolicSource, mapping.Template); err != nil {
				return fmt.Errorf("deployment plan connection mapping is invalid: %w", err)
			}
		}
	}
	for _, binding := range plan.Bindings {
		if !applications[binding.From] || !applications[binding.To] || binding.From == binding.To || binding.Kind == "" {
			return errors.New("deployment plan binding intent is invalid")
		}
	}
	for _, secret := range plan.Secrets {
		if !deploymentv1.IsValidEnvironmentName(secret.EnvironmentName) {
			return errors.New("deployment plan secret environment name is invalid")
		}
		if secret.Generated && (!strings.HasPrefix(secret.SecretRef, "generated://") || secret.Display != "Generated and securely stored" || secret.ApplicationKey == "" || secret.EnvironmentName == "") {
			return errors.New("generated secret must contain only a symbolic reference")
		}
		if !applications[secret.ApplicationKey] || secret.Name == "" || secret.EnvironmentName == "" || (!secret.Generated && strings.ContainsAny(secret.SecretRef, "\x00\r\n")) {
			return errors.New("deployment plan secret reference is invalid")
		}
	}
	if err := validateApplicationRuntimeConfiguration(plan); err != nil {
		return err
	}
	expected, err := HashPlan(plan)
	if err != nil || plan.Hash != expected {
		return errors.New("deployment plan hash is invalid")
	}
	return nil
}

func Terminal(state State) bool {
	return state == StateSucceeded || state == StateStale || state == StateFailed || state == StateRolledBack || state == StateCancelled
}
func Runnable(state State) bool {
	return state == StateProvisioning || state == StateBuilding || state == StatePreflighting || state == StateDeploying || state == StateVerifying || state == StateRollingBack || state == StateCleaningUp
}

// normalizeStoredRun is the one-way v1/v2 read migration. Old nonterminal runs
// cannot enter the v3 controller because their approval did not cover the
// application environment review. Terminal history is projected for reads
// without fabricating a user confirmation.
func normalizeStoredRun(run Run) Run {
	if run.Plan.SchemaVersion == PlanSchemaVersion && run.SchemaVersion == RunSchemaVersion {
		return run
	}
	isV1 := run.Plan.SchemaVersion == legacyPlanV1SchemaVersion || run.SchemaVersion == legacyRunV1SchemaVersion
	isV2 := run.Plan.SchemaVersion == legacyPlanV2SchemaVersion || run.SchemaVersion == legacyRunV2SchemaVersion
	if !isV1 && !isV2 {
		return run
	}
	originalState := run.State
	run.SchemaVersion = RunSchemaVersion
	run.Plan.SchemaVersion = PlanSchemaVersion
	if !Terminal(originalState) {
		run.State = StateStale
		run.Approval = nil
		run.WarningAcknowledgement = nil
		code := "DEPLOYMENT_PLAN_V2_STALE"
		message := "This run used deployment plan v2 and must be analyzed and reviewed again."
		if isV1 {
			code = "DEPLOYMENT_PLAN_V1_STALE"
			message = "This run used deployment plan v1 and must be analyzed and reviewed again."
		}
		run.Failure = &Failure{Step: originalState, Code: code, Message: message, NextAction: "Analyze and review the repository again.", Retryable: false}
	}
	_ = func() error {
		hash, err := HashPlan(run.Plan)
		if err == nil {
			run.Plan.Hash = hash
		}
		return err
	}()
	return run
}
