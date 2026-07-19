package topology

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

const (
	defaultHeartbeatTTL   = 2 * time.Minute
	defaultReservedCPU    = int64(250)
	defaultReservedMemory = int64(256 << 20)
	maxAssignments        = 100
	maxReplicas           = 100
	maxCPUMillicores      = int64(1_000_000)
	maxMemoryBytes        = int64(1 << 50)
	maxRationaleBytes     = 2048
)

type Error struct {
	Code    string
	Status  int
	Message string
}

func (e Error) Error() string { return e.Code + ": " + e.Message }

type EnvironmentFact = topologyv1.EnvironmentFact
type RuntimeFact = topologyv1.RuntimeFact
type NodeFact = topologyv1.NodeFact
type AgentFact = topologyv1.AgentFact
type ServiceFact = topologyv1.ServiceFact
type Facts = topologyv1.PlacementFacts

type FactSource interface {
	PlacementFacts(context.Context, string) (Facts, error)
}

type Store interface {
	Get(context.Context, string) (topologyv1.Plan, error)
	ReplayPlan(context.Context, string, string, string) (topologyv1.Plan, bool, error)
	Apply(context.Context, string, string, string, topologyv1.ApplyRequest, topologyv1.Plan) (topologyv1.Plan, bool, error)
	GetOperatorCapacity(context.Context, string, string) (topologyv1.OperatorCapacity, error)
	ReplayOperatorCapacity(context.Context, string, string, string) (topologyv1.OperatorCapacity, bool, error)
	ApplyOperatorCapacity(context.Context, string, string, string, topologyv1.OperatorCapacityApplyRequest, topologyv1.OperatorCapacity) (topologyv1.OperatorCapacity, bool, error)
}

type Service struct {
	Store          Store
	Facts          FactSource
	Now            func() time.Time
	HeartbeatTTL   time.Duration
	ReservedCPU    int64
	ReservedMemory int64
}

// CapacityOverride is the server-resolved DeploymentPolicy exception for an
// unknown runtime capacity. A scoped override cannot authorize another
// service, environment, or runtime in the same topology draft.
type CapacityOverride struct {
	Allowed       bool
	EnvironmentID string
	ServiceKeys   []string
	RuntimeIDs    []string
}

func (s Service) Preview(ctx context.Context, projectID string, draft topologyv1.Draft) (topologyv1.Preview, error) {
	normalized, planHash, err := normalizeDraft(projectID, draft)
	if err != nil {
		return topologyv1.Preview{}, err
	}
	current, err := s.current(ctx, projectID)
	if err != nil {
		return topologyv1.Preview{}, err
	}
	return topologyv1.Preview{Draft: normalized, PlanHash: planHash, StateHash: current.StateHash}, nil
}

func (s Service) Diff(ctx context.Context, projectID string, draft topologyv1.Draft) (topologyv1.Diff, error) {
	preview, err := s.Preview(ctx, projectID, draft)
	if err != nil {
		return topologyv1.Diff{}, err
	}
	current, err := s.current(ctx, projectID)
	if err != nil {
		return topologyv1.Diff{}, err
	}
	return diff(current, preview.Draft, preview.PlanHash), nil
}

func (s Service) Validate(ctx context.Context, projectID string, draft topologyv1.Draft, allowUnknown bool) (topologyv1.ValidationResult, error) {
	return s.ValidateScoped(ctx, projectID, draft, CapacityOverride{Allowed: allowUnknown})
}

func (s Service) ValidateScoped(ctx context.Context, projectID string, draft topologyv1.Draft, override CapacityOverride) (topologyv1.ValidationResult, error) {
	preview, err := s.Preview(ctx, projectID, draft)
	if err != nil {
		return topologyv1.ValidationResult{}, err
	}
	if s.Facts == nil {
		return topologyv1.ValidationResult{}, unavailable()
	}
	facts, err := s.Facts.PlacementFacts(ctx, projectID)
	if err != nil || facts.ProjectID != projectID {
		return topologyv1.ValidationResult{}, Error{Code: "TOPOLOGY_PROJECT_NOT_FOUND", Status: 404, Message: "project placement facts are unavailable"}
	}
	current, err := s.current(ctx, projectID)
	if err != nil {
		return topologyv1.ValidationResult{}, err
	}
	result := validate(ctx, s, facts, current, preview.Draft, preview.PlanHash, override)
	return result, nil
}

func (s Service) Apply(ctx context.Context, projectID, actor, idempotencyKey string, request topologyv1.ApplyRequest, allowUnknown bool) (topologyv1.ApplyResult, error) {
	return s.ApplyScoped(ctx, projectID, actor, idempotencyKey, request, CapacityOverride{Allowed: allowUnknown})
}

func (s Service) ApplyScoped(ctx context.Context, projectID, actor, idempotencyKey string, request topologyv1.ApplyRequest, override CapacityOverride) (topologyv1.ApplyResult, error) {
	if s.Store == nil || strings.TrimSpace(actor) == "" {
		return topologyv1.ApplyResult{}, unavailable()
	}
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return topologyv1.ApplyResult{}, err
	}
	normalized, planHash, err := normalizeDraft(projectID, request.Draft)
	if err != nil {
		return topologyv1.ApplyResult{}, err
	}
	request.Draft = normalized
	payloadHash, err := hashJSON(request)
	if err != nil {
		return topologyv1.ApplyResult{}, unavailable()
	}
	if replay, found, err := s.Store.ReplayPlan(ctx, projectID, idempotencyKey, payloadHash); err != nil || found {
		return topologyv1.ApplyResult{Plan: replay, Reused: found}, err
	}
	validation, err := s.ValidateScoped(ctx, projectID, normalized, override)
	if err != nil {
		return topologyv1.ApplyResult{}, err
	}
	if !validation.Valid {
		return topologyv1.ApplyResult{}, Error{Code: "TOPOLOGY_VALIDATION_FAILED", Status: 409, Message: "topology plan has deterministic validation errors"}
	}
	now := s.clock()
	plan := topologyv1.Plan{
		SchemaVersion: topologyv1.SchemaVersion,
		ProjectID:     projectID, PlanHash: planHash, Assignments: normalized.Assignments,
		CreatedBy: actor, AppliedBy: actor, CreatedAt: now, AppliedAt: now,
	}
	result, reused, err := s.Store.Apply(ctx, actor, idempotencyKey, payloadHash, request, plan)
	return topologyv1.ApplyResult{Plan: result, Reused: reused}, err
}

func (s Service) Get(ctx context.Context, projectID string) (topologyv1.Plan, error) {
	if s.Store == nil {
		return topologyv1.Plan{}, unavailable()
	}
	return s.Store.Get(ctx, projectID)
}

func (s Service) ApplyOperatorCapacity(ctx context.Context, projectID, actor, idempotencyKey string, request topologyv1.OperatorCapacityApplyRequest) (topologyv1.OperatorCapacityApplyResult, error) {
	if s.Store == nil || s.Facts == nil || strings.TrimSpace(actor) == "" {
		return topologyv1.OperatorCapacityApplyResult{}, unavailable()
	}
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return topologyv1.OperatorCapacityApplyResult{}, err
	}
	if err := validateCapacityDraft(request.Draft); err != nil {
		return topologyv1.OperatorCapacityApplyResult{}, err
	}
	payloadHash, err := hashJSON(request)
	if err != nil {
		return topologyv1.OperatorCapacityApplyResult{}, unavailable()
	}
	if replay, found, err := s.Store.ReplayOperatorCapacity(ctx, projectID, idempotencyKey, payloadHash); err != nil || found {
		return topologyv1.OperatorCapacityApplyResult{Capacity: replay, Reused: found}, err
	}
	facts, err := s.Facts.PlacementFacts(ctx, projectID)
	if err != nil || !runtimeOwned(facts, request.Draft.RuntimeID) {
		return topologyv1.OperatorCapacityApplyResult{}, Error{Code: "TOPOLOGY_RUNTIME_NOT_FOUND", Status: 404, Message: "runtime is not available in this project"}
	}
	now := s.clock()
	capacity := topologyv1.OperatorCapacity{
		ProjectID: projectID, RuntimeID: request.Draft.RuntimeID, Source: "operator_declared",
		CPUMillicores: request.Draft.CPUMillicores, MemoryBytes: request.Draft.MemoryBytes,
		ReservedCPUMillicores: request.Draft.ReservedCPUMillicores, ReservedMemoryBytes: request.Draft.ReservedMemoryBytes,
		DeclaredBy: actor, DeclaredAt: now,
	}
	result, reused, err := s.Store.ApplyOperatorCapacity(ctx, actor, idempotencyKey, payloadHash, request, capacity)
	return topologyv1.OperatorCapacityApplyResult{Capacity: result, Reused: reused}, err
}

func (s Service) GetOperatorCapacity(ctx context.Context, projectID, runtimeID string) (topologyv1.OperatorCapacity, error) {
	if s.Store == nil {
		return topologyv1.OperatorCapacity{}, unavailable()
	}
	return s.Store.GetOperatorCapacity(ctx, projectID, runtimeID)
}

func validate(ctx context.Context, s Service, facts Facts, current topologyv1.Plan, draft topologyv1.Draft, planHash string, override CapacityOverride) topologyv1.ValidationResult {
	now := s.clock()
	result := topologyv1.ValidationResult{SchemaVersion: topologyv1.SchemaVersion, ProjectID: draft.ProjectID, PlanHash: planHash, Valid: true, Runtimes: []topologyv1.RuntimeValidation{}, Issues: []topologyv1.Issue{}, ValidatedAt: now}
	environments := map[string]EnvironmentFact{}
	runtimes := map[string]RuntimeFact{}
	services := map[string]ServiceFact{}
	for _, value := range facts.Environments {
		environments[value.ID] = value
	}
	for _, value := range facts.Runtimes {
		runtimes[value.ID] = value
	}
	for _, value := range facts.Services {
		services[value.Key] = value
	}
	requested := requestedByRuntime(draft.Assignments)
	assigned := requestedByRuntime(current.Assignments)
	assignmentOverride := scopedAssignmentOverrides(draft.Assignments, override)
	seenRuntime := map[string]bool{}
	for _, assignment := range draft.Assignments {
		if _, ok := services[assignment.ServiceKey]; !ok {
			addIssue(&result, "TOPOLOGY_SERVICE_NOT_FOUND", "service is not active in this project", assignment.ServiceKey, assignment.RuntimeID)
		}
		runtime, ok := runtimes[assignment.RuntimeID]
		if !ok || runtime.ProjectID != draft.ProjectID {
			addIssue(&result, "TOPOLOGY_RUNTIME_NOT_FOUND", "runtime is not available in this project", assignment.ServiceKey, assignment.RuntimeID)
			continue
		}
		if runtime.EnvironmentID != assignment.EnvironmentID || environments[assignment.EnvironmentID].ProjectID != draft.ProjectID {
			addIssue(&result, "TOPOLOGY_ENVIRONMENT_MISMATCH", "runtime does not belong to the requested environment", assignment.ServiceKey, assignment.RuntimeID)
		}
		if runtime.Status != "ready" || runtime.Type != "k3s" || environments[assignment.EnvironmentID].Status != "active" {
			addIssue(&result, "TOPOLOGY_RUNTIME_INACTIVE", "runtime and environment must be active and ready", assignment.ServiceKey, assignment.RuntimeID)
		}
		if seenRuntime[assignment.RuntimeID] {
			continue
		}
		seenRuntime[assignment.RuntimeID] = true
		rv := validateRuntime(ctx, s, facts, runtime, assigned[assignment.RuntimeID], requested[assignment.RuntimeID], assignmentOverride[assignment.RuntimeID], now)
		result.Runtimes = append(result.Runtimes, rv)
		for _, issue := range rv.Issues {
			result.Issues = append(result.Issues, issue)
			result.Valid = false
		}
	}
	sort.Slice(result.Runtimes, func(i, j int) bool { return result.Runtimes[i].RuntimeID < result.Runtimes[j].RuntimeID })
	if len(result.Issues) > 0 {
		result.Valid = false
	}
	return result
}

func scopedAssignmentOverrides(assignments []topologyv1.Assignment, override CapacityOverride) map[string]bool {
	result := map[string]bool{}
	for _, assignment := range assignments {
		allowed := override.Allowed
		if override.EnvironmentID != "" && override.EnvironmentID != assignment.EnvironmentID {
			allowed = false
		}
		if len(override.ServiceKeys) > 0 && !containsString(override.ServiceKeys, assignment.ServiceKey) {
			allowed = false
		}
		if len(override.RuntimeIDs) > 0 && !containsString(override.RuntimeIDs, assignment.RuntimeID) {
			allowed = false
		}
		if !allowed {
			result[assignment.RuntimeID] = false
			continue
		}
		if _, exists := result[assignment.RuntimeID]; !exists {
			result[assignment.RuntimeID] = true
		}
	}
	return result
}

type resourcePair struct{ cpu, memory int64 }

func requestedByRuntime(assignments []topologyv1.Assignment) map[string]resourcePair {
	result := map[string]resourcePair{}
	for _, assignment := range assignments {
		pair := result[assignment.RuntimeID]
		pair.cpu += assignment.CPURequestMillicores * int64(assignment.Replicas)
		pair.memory += assignment.MemoryRequestBytes * int64(assignment.Replicas)
		result[assignment.RuntimeID] = pair
	}
	return result
}

func validateRuntime(ctx context.Context, s Service, facts Facts, runtime RuntimeFact, assigned, requested resourcePair, allowUnknown bool, now time.Time) topologyv1.RuntimeValidation {
	rv := topologyv1.RuntimeValidation{RuntimeID: runtime.ID, Eligible: true, Issues: []topologyv1.Issue{}}
	nodes := make([]NodeFact, 0, 2)
	for _, node := range facts.Nodes {
		if node.ProjectID == runtime.ProjectID && node.RuntimeID == runtime.ID {
			nodes = append(nodes, node)
		}
	}
	if len(nodes) > 1 {
		rv.Eligible = false
		rv.Issues = append(rv.Issues, topologyv1.Issue{Code: "TOPOLOGY_RUNTIME_MULTI_NODE_UNSUPPORTED", Message: "R5-009 supports exactly one node per K3s runtime", RuntimeID: runtime.ID, Severity: "error"})
	}
	eligible := make([]struct {
		node  NodeFact
		agent AgentFact
	}, 0, 2)
	staleHeartbeat := false
	for _, node := range nodes {
		if node.Status != "healthy" {
			continue
		}
		if !heartbeatFresh(node.LastSeenAt, now, s.heartbeatTTL()) {
			staleHeartbeat = true
			continue
		}
		for _, agent := range facts.Agents {
			if agent.ProjectID == runtime.ProjectID && agent.RuntimeID == runtime.ID && agent.NodeID == node.ID && agent.Status == "active" && capabilityEnabled(agent.Capabilities, "deploy") {
				if !heartbeatFresh(agent.LastSeenAt, now, s.heartbeatTTL()) {
					staleHeartbeat = true
					continue
				}
				eligible = append(eligible, struct {
					node  NodeFact
					agent AgentFact
				}{node, agent})
			}
		}
	}
	if len(eligible) == 0 {
		rv.Eligible = false
		if staleHeartbeat {
			rv.Issues = append(rv.Issues, topologyv1.Issue{Code: "TOPOLOGY_HEARTBEAT_STALE", Message: "runtime node or deploy Agent heartbeat is stale", RuntimeID: runtime.ID, Severity: "error"})
		} else {
			rv.Issues = append(rv.Issues, topologyv1.Issue{Code: "TOPOLOGY_AGENT_MISSING", Message: "runtime has no fresh healthy deploy Agent", RuntimeID: runtime.ID, Severity: "error"})
		}
	} else if len(eligible) > 1 {
		rv.Eligible = false
		rv.Issues = append(rv.Issues, topologyv1.Issue{Code: "TOPOLOGY_AGENT_AMBIGUOUS", Message: "runtime has more than one eligible deploy Agent", RuntimeID: runtime.ID, Severity: "error"})
	} else {
		rv.Capacity.NodeID, rv.Capacity.AgentID = eligible[0].node.ID, eligible[0].agent.ID
		rv.Capacity.ObservedAt = eligible[0].node.LastSeenAt
		rv.Capacity.HeartbeatAgeSeconds = int64(now.Sub(eligible[0].node.LastSeenAt.UTC()).Seconds())
		rv.Capacity.HeartbeatFresh = true
	}
	rv.Capacity.RuntimeID = runtime.ID
	rv.Capacity.AssignedCPUMillicores, rv.Capacity.AssignedMemoryBytes = assigned.cpu, assigned.memory
	rv.Capacity.RequestedCPUMillicores, rv.Capacity.RequestedMemoryBytes = requested.cpu, requested.memory
	capacity, err := s.Store.GetOperatorCapacity(ctx, runtime.ProjectID, runtime.ID)
	if err == nil {
		rv.Capacity.Source = capacity.Source
		rv.Capacity.SourceRevision = capacity.Revision
		rv.Capacity.CPUCapacityMillicores, rv.Capacity.MemoryCapacityBytes = capacity.CPUMillicores, capacity.MemoryBytes
		rv.Capacity.ReservedCPUMillicores, rv.Capacity.ReservedMemoryBytes = capacity.ReservedCPUMillicores, capacity.ReservedMemoryBytes
	} else if len(eligible) == 1 && eligible[0].node.CPUCores > 0 && eligible[0].node.MemoryMB > 0 {
		rv.Capacity.Source = "agent_observed"
		rv.Capacity.CPUCapacityMillicores = int64(eligible[0].node.CPUCores) * 1000
		rv.Capacity.MemoryCapacityBytes = int64(eligible[0].node.MemoryMB) << 20
		rv.Capacity.ReservedCPUMillicores, rv.Capacity.ReservedMemoryBytes = s.reservedCPU(), s.reservedMemory()
	} else {
		rv.Capacity.Source = "unknown"
		rv.Capacity.UnknownCapacity = true
		rv.Capacity.UnknownCapacityPolicyOverride = allowUnknown
		if !allowUnknown {
			rv.Eligible = false
			rv.Issues = append(rv.Issues, topologyv1.Issue{Code: "TOPOLOGY_CAPACITY_UNKNOWN", Message: "runtime capacity is unknown and policy does not allow an override", RuntimeID: runtime.ID, Severity: "error"})
		}
		return rv
	}
	rv.Capacity.AvailableCPUMillicores = rv.Capacity.CPUCapacityMillicores - rv.Capacity.ReservedCPUMillicores
	rv.Capacity.AvailableMemoryBytes = rv.Capacity.MemoryCapacityBytes - rv.Capacity.ReservedMemoryBytes
	if requested.cpu > rv.Capacity.AvailableCPUMillicores || requested.memory > rv.Capacity.AvailableMemoryBytes {
		rv.Capacity.Oversubscribed = true
		rv.Eligible = false
		rv.Issues = append(rv.Issues, topologyv1.Issue{Code: "TOPOLOGY_CAPACITY_EXCEEDED", Message: "requested resources exceed capacity after reserved headroom", RuntimeID: runtime.ID, Severity: "error"})
	}
	return rv
}

func heartbeatFresh(lastSeen *time.Time, now time.Time, ttl time.Duration) bool {
	if lastSeen == nil {
		return false
	}
	seen := lastSeen.UTC()
	return !seen.After(now) && now.Sub(seen) <= ttl
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func normalizeDraft(projectID string, draft topologyv1.Draft) (topologyv1.Draft, string, error) {
	if draft.SchemaVersion != topologyv1.SchemaVersion || draft.ProjectID != projectID || projectID == "" {
		return topologyv1.Draft{}, "", Error{Code: "TOPOLOGY_CONTRACT_INVALID", Status: 400, Message: "schema version and project identity must match the request path"}
	}
	if len(draft.Assignments) == 0 || len(draft.Assignments) > maxAssignments {
		return topologyv1.Draft{}, "", Error{Code: "TOPOLOGY_ASSIGNMENTS_INVALID", Status: 400, Message: "assignments must contain between 1 and 100 services"}
	}
	// Requests may be reused concurrently by CLI retries; normalization must not
	// mutate the caller-owned assignment slice.
	draft.Assignments = append([]topologyv1.Assignment(nil), draft.Assignments...)
	seen := map[string]bool{}
	for i := range draft.Assignments {
		a := &draft.Assignments[i]
		a.ServiceKey, a.EnvironmentID, a.RuntimeID = strings.TrimSpace(a.ServiceKey), strings.TrimSpace(a.EnvironmentID), strings.TrimSpace(a.RuntimeID)
		a.Exposure.Mode, a.Rationale.Summary = strings.TrimSpace(a.Exposure.Mode), strings.TrimSpace(a.Rationale.Summary)
		if a.ServiceKey == "" || a.EnvironmentID == "" || a.RuntimeID == "" || seen[a.ServiceKey] {
			return topologyv1.Draft{}, "", invalid("TOPOLOGY_ASSIGNMENT_INVALID", "service, environment, and runtime must be non-empty and service keys unique")
		}
		seen[a.ServiceKey] = true
		if a.Replicas < 1 || a.Replicas > maxReplicas || a.CPURequestMillicores < 1 || a.CPURequestMillicores > maxCPUMillicores || a.MemoryRequestBytes < 1 || a.MemoryRequestBytes > maxMemoryBytes {
			return topologyv1.Draft{}, "", invalid("TOPOLOGY_RESOURCES_INVALID", "replica, CPU, and memory requests are outside bounded values")
		}
		if a.CPURequestMillicores > math.MaxInt64/int64(a.Replicas) || a.MemoryRequestBytes > math.MaxInt64/int64(a.Replicas) {
			return topologyv1.Draft{}, "", invalid("TOPOLOGY_RESOURCES_INVALID", "resource request overflow")
		}
		if a.Exposure.Mode != "none" && a.Exposure.Mode != "internal" && a.Exposure.Mode != "public" {
			return topologyv1.Draft{}, "", invalid("TOPOLOGY_EXPOSURE_INVALID", "exposure mode must be none, internal, or public")
		}
		if len(a.Rationale.Summary) > maxRationaleBytes {
			return topologyv1.Draft{}, "", invalid("TOPOLOGY_RATIONALE_INVALID", "rationale is too large")
		}
	}
	sort.Slice(draft.Assignments, func(i, j int) bool { return draft.Assignments[i].ServiceKey < draft.Assignments[j].ServiceKey })
	hash, err := hashJSON(draft)
	return draft, hash, err
}

func diff(current topologyv1.Plan, proposed topologyv1.Draft, proposedHash string) topologyv1.Diff {
	result := topologyv1.Diff{ProjectID: proposed.ProjectID, CurrentRevision: current.Revision, CurrentHash: current.StateHash, ProposedHash: proposedHash, Changes: []topologyv1.DiffEntry{}}
	before, after := map[string]topologyv1.Assignment{}, map[string]topologyv1.Assignment{}
	for _, value := range current.Assignments {
		before[value.ServiceKey] = value
	}
	for _, value := range proposed.Assignments {
		after[value.ServiceKey] = value
	}
	keys := make([]string, 0, len(before)+len(after))
	seen := map[string]bool{}
	for key := range before {
		seen[key] = true
		keys = append(keys, key)
	}
	for key := range after {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		b, bok := before[key]
		a, aok := after[key]
		if bok && aok && hashMust(b) == hashMust(a) {
			continue
		}
		entry := topologyv1.DiffEntry{ServiceKey: key, Change: "updated"}
		if bok {
			copy := b
			entry.Before = &copy
		} else {
			entry.Change = "added"
		}
		if aok {
			copy := a
			entry.After = &copy
		} else {
			entry.Change = "removed"
		}
		result.Changes = append(result.Changes, entry)
	}
	return result
}

func (s Service) current(ctx context.Context, projectID string) (topologyv1.Plan, error) {
	if s.Store == nil {
		return topologyv1.Plan{}, unavailable()
	}
	plan, err := s.Store.Get(ctx, projectID)
	if errors.Is(err, ErrNotFound) {
		return topologyv1.Plan{ProjectID: projectID}, nil
	}
	return plan, err
}

var ErrNotFound = errors.New("topology not found")

func validateCapacityDraft(d topologyv1.OperatorCapacityDraft) error {
	if strings.TrimSpace(d.RuntimeID) == "" || d.CPUMillicores < 1 || d.CPUMillicores > maxCPUMillicores || d.MemoryBytes < 1 || d.MemoryBytes > maxMemoryBytes || d.ReservedCPUMillicores < 0 || d.ReservedCPUMillicores >= d.CPUMillicores || d.ReservedMemoryBytes < 0 || d.ReservedMemoryBytes >= d.MemoryBytes {
		return invalid("TOPOLOGY_CAPACITY_INVALID", "operator-declared capacity is outside bounded values")
	}
	return nil
}

func validateIdempotencyKey(value string) error {
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

func hashJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
func hashMust(value any) string { hash, _ := hashJSON(value); return hash }
func stateHash(id string, revision uint64, payloadHash string) string {
	return hashMust(struct {
		ID          string `json:"id"`
		Revision    uint64 `json:"revision"`
		PayloadHash string `json:"payload_hash"`
	}{id, revision, payloadHash})
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
	return Error{Code: "TOPOLOGY_UNAVAILABLE", Status: 503, Message: "topology service is unavailable"}
}
func addIssue(result *topologyv1.ValidationResult, code, message, serviceKey, runtimeID string) {
	result.Issues = append(result.Issues, topologyv1.Issue{Code: code, Message: message, ServiceKey: serviceKey, RuntimeID: runtimeID, Severity: "error"})
	result.Valid = false
}
func runtimeOwned(facts Facts, runtimeID string) bool {
	for _, runtime := range facts.Runtimes {
		if runtime.ID == runtimeID && runtime.ProjectID == facts.ProjectID {
			return true
		}
	}
	return false
}
func capabilityEnabled(values map[string]any, key string) bool {
	if values == nil {
		return false
	}
	value, ok := values[key]
	if !ok {
		return false
	}
	enabled, ok := value.(bool)
	return ok && enabled
}
func (s Service) clock() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s Service) heartbeatTTL() time.Duration {
	if s.HeartbeatTTL >= 30*time.Second && s.HeartbeatTTL <= 30*time.Minute {
		return s.HeartbeatTTL
	}
	return defaultHeartbeatTTL
}
func (s Service) reservedCPU() int64 {
	if s.ReservedCPU >= 0 && s.ReservedCPU <= maxCPUMillicores {
		return s.ReservedCPU
	}
	return defaultReservedCPU
}
func (s Service) reservedMemory() int64 {
	if s.ReservedMemory >= 0 && s.ReservedMemory <= maxMemoryBytes {
		return s.ReservedMemory
	}
	return defaultReservedMemory
}
