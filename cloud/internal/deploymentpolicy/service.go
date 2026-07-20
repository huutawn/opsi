package deploymentpolicy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/buildrecord"
	"github.com/opsi-dev/opsi/cloud/internal/topology"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
	deploymentpolicyv1 "github.com/opsi-dev/opsi/contracts/go/deploymentpolicyv1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

const maxListItems = 50

var (
	policyServiceKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	policyEvent      = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	policyRef        = regexp.MustCompile(`^refs/(heads|tags|pull)/[A-Za-z0-9._/@-]+$`)
	policyOpaque     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	policyWorkflow   = regexp.MustCompile(`^[A-Za-z0-9._@:/-]{1,512}$`)
	policyPlatform   = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9_]+(?:/[a-z0-9_.-]+)?$`)
	policyHash       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	policyOCI        = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?(?::[0-9]{1,5})?(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)+$`)
)

var ErrNotFound = errors.New("deployment policy not found")

type Error struct {
	Code    string
	Status  int
	Message string
}

func (e Error) Error() string { return e.Code + ": " + e.Message }

type Store interface {
	Get(context.Context, string, string) (deploymentpolicyv1.Policy, error)
	List(context.Context, string) ([]deploymentpolicyv1.Policy, error)
	ReplayPolicy(context.Context, string, string, string, string) (deploymentpolicyv1.Policy, bool, error)
	Apply(context.Context, string, string, string, deploymentpolicyv1.ApplyRequest, deploymentpolicyv1.Policy) (deploymentpolicyv1.Policy, bool, error)
	Disable(context.Context, string, string, string, string, string, deploymentpolicyv1.DisableRequest, time.Time) (deploymentpolicyv1.Policy, bool, error)
}

type BuildRecordSource interface {
	Get(context.Context, string, string) (buildrecordv1.Record, error)
}

type Service struct {
	Store        Store
	BuildRecords BuildRecordSource
	Bindings     buildrecord.BindingResolver
	Topology     topology.Service
	Now          func() time.Time
}

func (s Service) Preview(ctx context.Context, projectID string, draft deploymentpolicyv1.Draft) (deploymentpolicyv1.Preview, error) {
	normalized, hash, err := normalizeDraft(projectID, draft)
	if err != nil {
		return deploymentpolicyv1.Preview{}, err
	}
	if err := s.validateAuthority(ctx, projectID, normalized); err != nil {
		return deploymentpolicyv1.Preview{}, err
	}
	return deploymentpolicyv1.Preview{Draft: normalized, PolicyHash: hash}, nil
}

func (s Service) Diff(ctx context.Context, projectID, policyID string, draft deploymentpolicyv1.Draft) (deploymentpolicyv1.Diff, error) {
	preview, err := s.Preview(ctx, projectID, draft)
	if err != nil {
		return deploymentpolicyv1.Diff{}, err
	}
	result := deploymentpolicyv1.Diff{PolicyID: policyID, ProposedHash: preview.PolicyHash, Changes: []deploymentpolicyv1.DiffEntry{}}
	if policyID == "" {
		result.Changes = []deploymentpolicyv1.DiffEntry{{Field: "policy", After: preview.Draft}}
		return result, nil
	}
	current, err := s.Get(ctx, projectID, policyID)
	if err != nil {
		return result, err
	}
	result.CurrentRevision = current.Revision
	result.CurrentHash = current.StateHash
	if current.PolicyHash != preview.PolicyHash {
		result.Changes = []deploymentpolicyv1.DiffEntry{{Field: "policy", Before: current.Draft, After: preview.Draft}}
	}
	return result, nil
}

func (s Service) Apply(ctx context.Context, projectID, actor, key string, request deploymentpolicyv1.ApplyRequest) (deploymentpolicyv1.ApplyResult, error) {
	if s.Store == nil || strings.TrimSpace(actor) == "" {
		return deploymentpolicyv1.ApplyResult{}, unavailable()
	}
	if err := validateKey(key); err != nil {
		return deploymentpolicyv1.ApplyResult{}, err
	}
	normalized, policyHash, err := normalizeDraft(projectID, request.Draft)
	if err != nil {
		return deploymentpolicyv1.ApplyResult{}, err
	}
	request.Draft = normalized
	payloadHash, err := hashJSON(request)
	if err != nil {
		return deploymentpolicyv1.ApplyResult{}, unavailable()
	}
	if replay, found, err := s.Store.ReplayPolicy(ctx, projectID, "policy_apply", key, payloadHash); err != nil || found {
		return deploymentpolicyv1.ApplyResult{Policy: replay, Reused: found}, err
	}
	if err := s.validateAuthority(ctx, projectID, normalized); err != nil {
		return deploymentpolicyv1.ApplyResult{}, err
	}
	now := s.clock()
	policy := deploymentpolicyv1.Policy{SchemaVersion: deploymentpolicyv1.SchemaVersion, ID: request.PolicyID, PolicyHash: policyHash, Draft: normalized, CreatedBy: actor, AppliedBy: actor, CreatedAt: now, AppliedAt: now}
	result, reused, err := s.Store.Apply(ctx, actor, key, payloadHash, request, policy)
	return deploymentpolicyv1.ApplyResult{Policy: result, Reused: reused}, err
}

func (s Service) Disable(ctx context.Context, projectID, policyID, actor, key string, request deploymentpolicyv1.DisableRequest) (deploymentpolicyv1.ApplyResult, error) {
	if s.Store == nil || strings.TrimSpace(actor) == "" {
		return deploymentpolicyv1.ApplyResult{}, unavailable()
	}
	if err := validateKey(key); err != nil {
		return deploymentpolicyv1.ApplyResult{}, err
	}
	payloadHash, err := hashJSON(struct {
		PolicyID string
		Request  deploymentpolicyv1.DisableRequest
	}{PolicyID: policyID, Request: request})
	if err != nil {
		return deploymentpolicyv1.ApplyResult{}, unavailable()
	}
	if replay, found, err := s.Store.ReplayPolicy(ctx, projectID, "policy_disable", key, payloadHash); err != nil || found {
		return deploymentpolicyv1.ApplyResult{Policy: replay, Reused: found}, err
	}
	result, reused, err := s.Store.Disable(ctx, projectID, policyID, actor, key, payloadHash, request, s.clock())
	return deploymentpolicyv1.ApplyResult{Policy: result, Reused: reused}, err
}

func (s Service) Get(ctx context.Context, projectID, policyID string) (deploymentpolicyv1.Policy, error) {
	if s.Store == nil {
		return deploymentpolicyv1.Policy{}, unavailable()
	}
	return s.Store.Get(ctx, projectID, policyID)
}
func (s Service) List(ctx context.Context, projectID string) ([]deploymentpolicyv1.Policy, error) {
	if s.Store == nil {
		return nil, unavailable()
	}
	return s.Store.List(ctx, projectID)
}

func (s Service) Route(ctx context.Context, projectID string, request deploymentpolicyv1.RoutingRequest) (deploymentpolicyv1.RoutingDecision, error) {
	decision := deploymentpolicyv1.RoutingDecision{SchemaVersion: deploymentpolicyv1.SchemaVersion, ProjectID: projectID, BuildRecordID: request.BuildRecordID, EnvironmentID: request.EnvironmentID, DecidedAt: s.clock()}
	if s.BuildRecords == nil || s.Bindings == nil || s.Store == nil {
		return decision, unavailable()
	}
	record, err := s.BuildRecords.Get(ctx, projectID, request.BuildRecordID)
	if err != nil {
		return reject(decision, "ROUTING_BUILD_RECORD_NOT_FOUND", "accepted BuildRecord was not found")
	}
	decision.ServiceKey = record.ServiceKey
	if record.SchemaVersion != buildrecordv1.SchemaVersion || record.ProjectID != projectID || record.RepositoryID != record.Workload.RepositoryID || record.RepositoryOwnerID != record.Workload.RepositoryOwnerID || record.Build.Status != "succeeded" {
		return reject(decision, "ROUTING_BUILD_RECORD_INVALID", "BuildRecord status or stored workload identity is invalid")
	}
	binding, err := s.Bindings.ResolveBuildBinding(ctx, record.RepositoryID, record.ServiceKey)
	if err != nil || binding.ProjectID != projectID || binding.RepositoryID != record.RepositoryID || binding.RepositoryOwnerID != record.RepositoryOwnerID || binding.BindingID != record.ActiveBindingID || binding.ServiceID != record.ServiceID {
		return reject(decision, "ROUTING_BINDING_INACTIVE", "repository claim or service binding is no longer active")
	}
	plan, err := s.Topology.Get(ctx, projectID)
	if err != nil {
		return reject(decision, "ROUTING_TOPOLOGY_MISSING", "no active TopologyPlan is available")
	}
	decision.TopologyPlanID = plan.ID
	decision.TopologyRevision = plan.Revision
	serviceAssignments := make([]topologyv1.Assignment, 0, 1)
	for _, assignment := range plan.Assignments {
		if assignment.ServiceKey == record.ServiceKey {
			serviceAssignments = append(serviceAssignments, assignment)
		}
	}
	if len(serviceAssignments) == 0 {
		return reject(decision, "ROUTING_TOPOLOGY_MISMATCH", "TopologyPlan has no assignment for the BuildRecord service")
	}
	assignments := make([]topologyv1.Assignment, 0, 1)
	for _, assignment := range serviceAssignments {
		if assignment.EnvironmentID == request.EnvironmentID {
			assignments = append(assignments, assignment)
		}
	}
	if len(assignments) == 0 {
		return reject(decision, "ROUTING_POLICY_MISMATCH", "no active DeploymentPolicy exact-matches the BuildRecord and environment")
	}
	if len(assignments) != 1 {
		return reject(decision, "ROUTING_TOPOLOGY_MISMATCH", "TopologyPlan must contain exactly one matching service assignment")
	}
	assignment := assignments[0]
	policies, err := s.Store.List(ctx, projectID)
	if err != nil {
		return decision, err
	}
	matches := make([]deploymentpolicyv1.Policy, 0, 2)
	for _, policy := range policies {
		if policy.Draft.Enabled && policyMatches(policy.Draft, record, request.EnvironmentID) && contains(policy.Draft.AllowedRuntimeIDs, assignment.RuntimeID) {
			matches = append(matches, policy)
		}
	}
	if len(matches) == 0 {
		return reject(decision, "ROUTING_POLICY_MISMATCH", "no active DeploymentPolicy exact-matches the BuildRecord, environment, and topology runtime")
	}
	if len(matches) > 1 {
		return reject(decision, "ROUTING_POLICY_AMBIGUOUS", "more than one active DeploymentPolicy exact-matches the route")
	}
	policy := matches[0]
	decision.DeploymentPolicyID = policy.ID
	decision.DeploymentPolicyRevision = policy.Revision
	decision.RuntimeID = assignment.RuntimeID
	validation, err := s.Topology.ValidateScoped(ctx, projectID, topologyv1.Draft{SchemaVersion: topologyv1.SchemaVersion, ProjectID: projectID, Assignments: plan.Assignments}, topology.CapacityOverride{
		Allowed:       policy.Draft.AllowUnknownCapacity,
		EnvironmentID: policy.Draft.EnvironmentID,
		ServiceKeys:   policy.Draft.ServiceKeys,
		RuntimeIDs:    policy.Draft.AllowedRuntimeIDs,
	})
	if err != nil {
		return decision, err
	}
	var runtime *topologyv1.RuntimeValidation
	for i := range validation.Runtimes {
		if validation.Runtimes[i].RuntimeID == assignment.RuntimeID {
			runtime = &validation.Runtimes[i]
			break
		}
	}
	if runtime == nil || !runtime.Eligible {
		code := "ROUTING_RUNTIME_INELIGIBLE"
		message := "runtime failed health, heartbeat, capacity, or Agent validation"
		if runtime != nil && len(runtime.Issues) > 0 {
			code = runtime.Issues[0].Code
			message = runtime.Issues[0].Message
		}
		return reject(decision, code, message)
	}
	decision.NodeID = runtime.Capacity.NodeID
	decision.AgentID = runtime.Capacity.AgentID
	decision.UnknownCapacityOverride = runtime.Capacity.UnknownCapacityPolicyOverride
	if decision.AgentID == "" || decision.NodeID == "" {
		return reject(decision, "ROUTING_AGENT_MISSING", "eligible runtime did not resolve exactly one deploy Agent")
	}
	decision.Eligible = true
	decision.DecisionCode = "ROUTING_ELIGIBLE"
	decision.Message = "BuildRecord is eligible for this runtime and Agent"
	decision.DecisionHash = decisionHash(decision)
	return decision, nil
}

func policyMatches(policy deploymentpolicyv1.Draft, record buildrecordv1.Record, environmentID string) bool {
	if policy.ProjectID != record.ProjectID || policy.RepositoryID != record.RepositoryID || policy.EnvironmentID != environmentID || !contains(policy.ServiceKeys, record.ServiceKey) || !contains(policy.WorkflowRefs, record.Workload.WorkflowRef) || !contains(policy.AllowedEvents, record.Workload.EventName) || !contains(policy.AllowedGitRefs, record.Workload.Ref) || !contains(policy.AllowedPlatforms, record.Build.Platform) || !contains(policy.AllowedConfigHashes, record.Build.ConfigHash) || !contains(policy.AllowedBuildPlanHashes, record.Build.PlanHash) {
		return false
	}
	if record.Workload.JobWorkflowRef == "" {
		if len(policy.JobWorkflowRefs) > 0 {
			return false
		}
	} else if !contains(policy.JobWorkflowRefs, record.Workload.JobWorkflowRef) {
		return false
	}
	if contains(policy.AllowedOCIRepositories, record.Build.OCIRepository) {
		return true
	}
	for _, prefix := range policy.AllowedOCIPrefixes {
		if record.Build.OCIRepository == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(record.Build.OCIRepository, strings.TrimSuffix(prefix, "/")+"/") {
			return true
		}
	}
	return false
}

func (s Service) validateAuthority(ctx context.Context, projectID string, draft deploymentpolicyv1.Draft) error {
	if s.Topology.Facts == nil || s.Bindings == nil {
		return unavailable()
	}
	facts, err := s.Topology.Facts.PlacementFacts(ctx, projectID)
	if err != nil || facts.ProjectID != projectID {
		return Error{Code: "DEPLOYMENT_POLICY_PROJECT_NOT_FOUND", Status: 404, Message: "project policy facts are unavailable"}
	}
	environmentFound := false
	for _, environment := range facts.Environments {
		if environment.ID == draft.EnvironmentID && environment.ProjectID == projectID {
			environmentFound = true
			break
		}
	}
	if !environmentFound {
		return Error{Code: "DEPLOYMENT_POLICY_ENVIRONMENT_INVALID", Status: 400, Message: "environment is not owned by this project"}
	}
	for _, runtimeID := range draft.AllowedRuntimeIDs {
		found := false
		for _, runtime := range facts.Runtimes {
			if runtime.ID == runtimeID && runtime.ProjectID == projectID && runtime.EnvironmentID == draft.EnvironmentID {
				found = true
				break
			}
		}
		if !found {
			return Error{Code: "DEPLOYMENT_POLICY_RUNTIME_INVALID", Status: 400, Message: "allowed runtime is not owned by the policy environment"}
		}
	}
	for _, serviceKey := range draft.ServiceKeys {
		binding, err := s.Bindings.ResolveBuildBinding(ctx, draft.RepositoryID, serviceKey)
		if err != nil || binding.ProjectID != projectID || binding.RepositoryID != draft.RepositoryID || binding.ServiceKey != serviceKey {
			return Error{Code: "DEPLOYMENT_POLICY_BINDING_INVALID", Status: 409, Message: "repository claim and service binding must be active"}
		}
	}
	return nil
}

func normalizeDraft(projectID string, d deploymentpolicyv1.Draft) (deploymentpolicyv1.Draft, string, error) {
	if d.SchemaVersion != deploymentpolicyv1.SchemaVersion || d.ProjectID != projectID || !policyOpaque.MatchString(projectID) || d.RepositoryID == 0 || d.RepositoryID > uint64(1<<63-1) || !policyOpaque.MatchString(strings.TrimSpace(d.EnvironmentID)) {
		return d, "", invalid("DEPLOYMENT_POLICY_CONTRACT_INVALID", "schema, project, repository, and environment are required")
	}
	d.EnvironmentID = strings.TrimSpace(d.EnvironmentID)
	lists := []struct {
		values *[]string
		kind   string
	}{{&d.ServiceKeys, "service"}, {&d.WorkflowRefs, "workflow"}, {&d.JobWorkflowRefs, "workflow"}, {&d.AllowedEvents, "event"}, {&d.AllowedGitRefs, "ref"}, {&d.AllowedRuntimeIDs, "opaque"}, {&d.AllowedOCIRepositories, "oci"}, {&d.AllowedOCIPrefixes, "oci_prefix"}, {&d.AllowedPlatforms, "platform"}, {&d.AllowedConfigHashes, "hash"}, {&d.AllowedBuildPlanHashes, "hash"}}
	for _, list := range lists {
		normalized, err := normalizeList(*list.values, list.kind)
		if err != nil {
			return d, "", err
		}
		*list.values = normalized
	}
	for _, required := range [][]string{d.ServiceKeys, d.WorkflowRefs, d.AllowedEvents, d.AllowedGitRefs, d.AllowedRuntimeIDs, d.AllowedPlatforms, d.AllowedConfigHashes, d.AllowedBuildPlanHashes} {
		if len(required) == 0 {
			return d, "", invalid("DEPLOYMENT_POLICY_LIST_INVALID", "required policy allowlists must not be empty")
		}
	}
	if len(d.AllowedOCIRepositories) == 0 && len(d.AllowedOCIPrefixes) == 0 {
		return d, "", invalid("DEPLOYMENT_POLICY_OCI_INVALID", "at least one exact OCI repository or bounded prefix is required")
	}
	for _, prefix := range d.AllowedOCIPrefixes {
		if !strings.HasSuffix(prefix, "/") || strings.ContainsAny(prefix, "*?[](){}|\\") {
			return d, "", invalid("DEPLOYMENT_POLICY_OCI_INVALID", "OCI prefixes must end with slash and contain no wildcard or expression syntax")
		}
	}
	hash, err := hashJSON(d)
	return d, hash, err
}

func normalizeList(values []string, kind string) ([]string, error) {
	if len(values) > maxListItems {
		return nil, invalid("DEPLOYMENT_POLICY_LIST_INVALID", "policy lists are bounded to 50 exact values")
	}
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 512 || strings.ContainsAny(value, "\r\n\x00*?[]{}()|\\$;`<>&") || !validPolicyValue(kind, value) {
			return nil, invalid("DEPLOYMENT_POLICY_VALUE_INVALID", "policy values must be non-empty bounded single-line strings")
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result, nil
}

func validPolicyValue(kind, value string) bool {
	switch kind {
	case "service":
		return policyServiceKey.MatchString(value)
	case "workflow":
		return policyWorkflow.MatchString(value)
	case "event":
		return policyEvent.MatchString(value)
	case "ref":
		return policyRef.MatchString(value)
	case "opaque":
		return policyOpaque.MatchString(value)
	case "oci", "oci_prefix":
		return policyOCI.MatchString(strings.TrimSuffix(value, "/"))
	case "platform":
		return policyPlatform.MatchString(value)
	case "hash":
		return policyHash.MatchString(value)
	default:
		return false
	}
}
func validateKey(value string) error {
	value = strings.TrimSpace(value)
	if len(value) < 8 || len(value) > 128 {
		return invalid("IDEMPOTENCY_KEY_INVALID", "idempotency key must be between 8 and 128 characters")
	}
	for _, r := range value {
		if !(r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
			return invalid("IDEMPOTENCY_KEY_INVALID", "idempotency key contains unsupported characters")
		}
	}
	return nil
}
func reject(decision deploymentpolicyv1.RoutingDecision, code, message string) (deploymentpolicyv1.RoutingDecision, error) {
	decision.Eligible = false
	decision.DecisionCode = code
	decision.Message = message
	decision.DecisionHash = decisionHash(decision)
	return decision, Error{Code: code, Status: 409, Message: message}
}
func decisionHash(decision deploymentpolicyv1.RoutingDecision) string {
	copy := decision
	copy.DecidedAt = time.Time{}
	copy.DecisionHash = ""
	hash, _ := hashJSON(copy)
	return hash
}
func contains(values []string, target string) bool {
	i := sort.SearchStrings(values, target)
	return i < len(values) && values[i] == target
}
func hashJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func stateHash(id string, revision uint64, policyHash string) string {
	hash, _ := hashJSON(struct {
		ID         string `json:"id"`
		Revision   uint64 `json:"revision"`
		PolicyHash string `json:"policy_hash"`
	}{id, revision, policyHash})
	return hash
}
func newID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}
func invalid(code, message string) error { return Error{Code: code, Status: 400, Message: message} }
func unavailable() error {
	return Error{Code: "DEPLOYMENT_POLICY_UNAVAILABLE", Status: 503, Message: "deployment policy service is unavailable"}
}
func (s Service) clock() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
