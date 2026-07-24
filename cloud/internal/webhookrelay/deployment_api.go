package webhookrelay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	deploymentpolicyv1 "github.com/opsi-dev/opsi/contracts/go/deploymentpolicyv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

type immutableDeploymentStarter interface {
	StartImmutableDeployment(deploymentv1.JobSnapshot, string, string, string) (registry.DeploymentJob, bool, error)
}

type immutableDeploymentReader interface {
	GetDeployment(string, string) (registry.DeploymentJob, error)
	CancelDeployment(string, string, string, string) (registry.DeploymentJob, bool, error)
	RetryDeployment(string, string, string, string) (registry.DeploymentJob, bool, error)
}

type immutableDeploymentReplayReader interface {
	ReplayImmutableDeployment(string, string, string) (registry.DeploymentJob, bool, error)
}

type exposureLifecycleStore interface {
	PreviewExposure(string, string, deploymentv1.ExposureMutationRequest) (deploymentv1.ExposurePreview, error)
	StartExposureRollout(string, string, string, string, deploymentv1.ExposureMutationRequest) (registry.DeploymentJob, bool, error)
}

func (s *Server) handleExposureAPI(w http.ResponseWriter, r *http.Request, projectID string, parts []string, principal auth.VerifyResult) bool {
	if len(parts) < 3 || parts[2] != "exposures" {
		return false
	}
	store, ok := s.Registry.(exposureLifecycleStore)
	if !ok {
		writeRegistryError(w, registry.APIError{Status: 503, Code: "EXPOSURE_UNAVAILABLE", Message: "exposure lifecycle store is unavailable", RequestID: r.Header.Get("X-Request-ID")})
		return true
	}
	if len(parts) == 4 && (parts[3] == "preview" || parts[3] == "diff") && r.Method == http.MethodPost {
		if !s.requireRole(w, r, principal, projectID, "deployment_job", projectID, "owner", "admin", "developer", "viewer") {
			return true
		}
		var request deploymentv1.ExposureMutationRequest
		if !decodeStrictDeploymentJSON(w, r, &request) {
			return true
		}
		preview, err := store.PreviewExposure(projectID, principal.UserID, request)
		writeRegistryResult(w, r, preview, err, http.StatusOK)
		return true
	}
	if len(parts) == 3 && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "deployment_job", projectID, "owner", "admin", "developer") {
			return true
		}
		var request deploymentv1.ExposureMutationRequest
		if !decodeStrictDeploymentJSON(w, r, &request) {
			return true
		}
		job, reused, err := store.StartExposureRollout(projectID, principal.UserID, r.Header.Get("Idempotency-Key"), r.Header.Get("X-Request-ID"), request)
		job.Reused = reused
		if err == nil {
			s.Registry.Audit(job.OrgID, projectID, principal.UserID, "EXPOSURE_ROLLOUT_CREATED", "deployment_job", job.ID, "success", map[string]any{"base_deployment_id": job.BaseDeploymentID, "rollout_id": job.RolloutIntent.RolloutID, "intent_hash": job.RolloutIntent.IntentHash, "exposure_spec_hash": job.ExposureSpec.SpecHash, "reused": reused})
		}
		writeRegistryResult(w, r, job, err, http.StatusAccepted)
		return true
	}
	if len(parts) == 3 && r.Method == http.MethodGet {
		if !s.requireRole(w, r, principal, projectID, "deployment_job", projectID, "owner", "admin", "developer", "viewer", "support") {
			return true
		}
		jobs, err := s.Registry.ListDeployments(projectID)
		if err != nil {
			writeRegistryFailure(w, r, err)
			return true
		}
		filtered := make([]registry.DeploymentJob, 0)
		for _, job := range jobs {
			if job.Mode != "rollout" {
				continue
			}
			if value := r.URL.Query().Get("service_id"); value != "" && value != job.ServiceID {
				continue
			}
			if value := r.URL.Query().Get("environment_id"); value != "" && value != job.EnvironmentID {
				continue
			}
			filtered = append(filtered, job)
		}
		writeRegistryResult(w, r, map[string]any{"exposures": filtered}, nil, http.StatusOK)
		return true
	}
	if len(parts) == 4 && r.Method == http.MethodGet {
		if !s.requireRole(w, r, principal, projectID, "deployment_job", parts[3], "owner", "admin", "developer", "viewer", "support") {
			return true
		}
		reader, ok := s.Registry.(immutableDeploymentReader)
		if !ok {
			return false
		}
		job, err := reader.GetDeployment(projectID, parts[3])
		if err == nil && job.Mode != "rollout" {
			err = registry.ErrNotFound
		}
		writeRegistryResult(w, r, job, err, http.StatusOK)
		return true
	}
	return false
}

func (s *Server) handleDeploymentAPI(w http.ResponseWriter, r *http.Request, projectID string, parts []string, principal auth.VerifyResult) bool {
	if len(parts) < 3 || parts[2] != "deployments" {
		return false
	}
	if len(parts) == 4 && (parts[3] == "preview" || parts[3] == "diff") && r.Method == http.MethodPost {
		if !s.requireRole(w, r, principal, projectID, "deployment_job", projectID, "owner", "admin", "developer", "viewer") {
			return true
		}
		var request deploymentv1.CreateRequest
		if !decodeStrictDeploymentJSON(w, r, &request) {
			return true
		}
		preview, err := s.resolveDeploymentPreview(r, projectID, principal.UserID, request)
		writeRegistryResult(w, r, preview, err, http.StatusOK)
		return true
	}
	if len(parts) == 3 && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "deployment_job", projectID, "owner", "admin", "developer") {
			return true
		}
		var request deploymentv1.CreateRequest
		if !decodeStrictDeploymentJSON(w, r, &request) {
			return true
		}
		request.IdempotencyKey = r.Header.Get("Idempotency-Key")
		if !validDeploymentIdempotencyKey(request.IdempotencyKey) {
			writeRegistryError(w, registry.APIError{Status: 400, Code: "IDEMPOTENCY_KEY_INVALID", Message: "Idempotency-Key must be 1-128 printable characters without whitespace", RequestID: r.Header.Get("X-Request-ID")})
			return true
		}
		if replay, ok := s.Registry.(immutableDeploymentReplayReader); ok {
			payloadHash := hashDeploymentPayload(request.BuildRecordID, request.EnvironmentID, request.Workload.Normalize())
			if job, reused, replayErr := replay.ReplayImmutableDeployment(projectID, request.IdempotencyKey, payloadHash); replayErr != nil {
				writeRegistryFailure(w, r, replayErr)
				return true
			} else if reused {
				job.Reused = true
				writeRegistryResult(w, r, job, nil, http.StatusAccepted)
				return true
			}
		}
		preview, err := s.resolveDeploymentPreview(r, projectID, principal.UserID, request)
		if err != nil {
			writeRegistryFailure(w, r, err)
			return true
		}
		starter, ok := s.Registry.(immutableDeploymentStarter)
		if !ok {
			writeRegistryError(w, registry.APIError{Status: 503, Code: "DEPLOYMENT_UNAVAILABLE", Message: "immutable deployment store is unavailable", RequestID: r.Header.Get("X-Request-ID")})
			return true
		}
		job, reused, err := starter.StartImmutableDeployment(preview.Snapshot, principal.UserID, request.IdempotencyKey, r.Header.Get("X-Request-ID"))
		job.Reused = reused
		if err == nil {
			s.Registry.Audit(job.OrgID, projectID, principal.UserID, "IMMUTABLE_DEPLOYMENT_CREATED", "deployment_job", job.ID, "success", map[string]any{"build_record_id": preview.Snapshot.Authority.BuildRecord.ID, "oci_digest": preview.Snapshot.Image.Digest, "runtime_id": job.RuntimeID, "node_id": job.NodeID, "agent_id": job.AgentID, "spec_hash": job.SpecHash, "reused": reused})
		}
		writeRegistryResult(w, r, job, err, http.StatusAccepted)
		return true
	}
	if len(parts) == 4 && r.Method == http.MethodGet {
		if !s.requireRole(w, r, principal, projectID, "deployment_job", parts[3], "owner", "admin", "developer", "viewer", "support") {
			return true
		}
		reader, ok := s.Registry.(immutableDeploymentReader)
		if !ok {
			writeRegistryError(w, registry.APIError{Status: 503, Code: "DEPLOYMENT_UNAVAILABLE", Message: "deployment store is unavailable", RequestID: r.Header.Get("X-Request-ID")})
			return true
		}
		job, err := reader.GetDeployment(projectID, parts[3])
		writeRegistryResult(w, r, job, err, http.StatusOK)
		return true
	}
	if len(parts) == 5 && parts[4] == "cancel" && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "deployment_job", parts[3], "owner", "admin", "developer") {
			return true
		}
		reader, ok := s.Registry.(immutableDeploymentReader)
		if !ok {
			writeRegistryError(w, registry.APIError{Status: 503, Code: "DEPLOYMENT_UNAVAILABLE", Message: "deployment store is unavailable", RequestID: r.Header.Get("X-Request-ID")})
			return true
		}
		job, reused, err := reader.CancelDeployment(projectID, parts[3], r.Header.Get("Idempotency-Key"), r.Header.Get("X-Request-ID"))
		job.Reused = reused
		if err == nil {
			s.Registry.Audit(job.OrgID, projectID, principal.UserID, "DEPLOYMENT_CANCELLED", "deployment_job", job.ID, "success", map[string]any{"status": job.Status, "reused": reused})
		}
		writeRegistryResult(w, r, job, err, http.StatusOK)
		return true
	}
	if len(parts) == 5 && parts[4] == "retry" && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "deployment_job", parts[3], "owner", "admin", "developer") {
			return true
		}
		reader, ok := s.Registry.(immutableDeploymentReader)
		if !ok {
			writeRegistryError(w, registry.APIError{Status: 503, Code: "DEPLOYMENT_UNAVAILABLE", Message: "deployment store is unavailable", RequestID: r.Header.Get("X-Request-ID")})
			return true
		}
		job, reused, err := reader.RetryDeployment(projectID, parts[3], r.Header.Get("Idempotency-Key"), r.Header.Get("X-Request-ID"))
		job.Reused = reused
		if err == nil {
			s.Registry.Audit(job.OrgID, projectID, principal.UserID, "DEPLOYMENT_RETRY_REQUESTED", "deployment_job", job.ID, "success", map[string]any{"status": job.Status, "attempt_count": job.AttemptCount, "reused": reused})
		}
		writeRegistryResult(w, r, job, err, http.StatusAccepted)
		return true
	}
	return false
}

func (s *Server) resolveDeploymentPreview(r *http.Request, projectID, actor string, request deploymentv1.CreateRequest) (deploymentv1.Preview, error) {
	result := deploymentv1.Preview{SchemaVersion: deploymentv1.JobSchemaVersion, Changes: []string{}, ResolvedAt: s.clock()}
	if request.SchemaVersion != deploymentv1.JobSchemaVersion || request.BuildRecordID == "" || request.EnvironmentID == "" {
		return result, registry.APIError{Status: 400, Code: "DEPLOYMENT_REQUEST_INVALID", Message: "schema_version, build_record_id, and environment_id are required", RequestID: r.Header.Get("X-Request-ID")}
	}
	request.Workload = request.Workload.Normalize()
	if err := request.Workload.Validate(); err != nil {
		return result, registry.APIError{Status: 400, Code: "WORKLOAD_SPEC_INVALID", Message: err.Error(), RequestID: r.Header.Get("X-Request-ID")}
	}
	if len(request.Workload.SecretReferences) != 0 {
		return result, registry.APIError{Status: 409, Code: "SECRET_REFERENCE_UNRESOLVED", Message: "no canonical secret reference resolver is configured", RequestID: r.Header.Get("X-Request-ID")}
	}
	record, err := s.BuildRecords.Get(r.Context(), projectID, request.BuildRecordID)
	if err != nil {
		return result, registry.APIError{Status: 404, Code: "BUILD_RECORD_NOT_FOUND", Message: "accepted BuildRecord was not found", RequestID: r.Header.Get("X-Request-ID")}
	}
	if record.Build.Status != "succeeded" || record.ServiceKey != request.Workload.ServiceKey {
		return result, registry.APIError{Status: 409, Code: "BUILD_RECORD_SERVICE_MISMATCH", Message: "BuildRecord service does not match WorkloadSpec", RequestID: r.Header.Get("X-Request-ID")}
	}
	decision, err := s.Policies.Route(r.Context(), projectID, deploymentpolicyv1.RoutingRequest{BuildRecordID: record.ID, EnvironmentID: request.EnvironmentID})
	if err != nil {
		return result, err
	}
	if !decision.Eligible {
		return result, registry.APIError{Status: 409, Code: decision.DecisionCode, Message: decision.Message, RequestID: r.Header.Get("X-Request-ID")}
	}
	plan, err := s.Topology.Get(r.Context(), projectID)
	if err != nil || plan.ID != decision.TopologyPlanID || plan.Revision != decision.TopologyRevision {
		return result, registry.APIError{Status: 409, Code: "ROUTING_TOPOLOGY_CHANGED", Message: "TopologyPlan changed during deployment resolution", RequestID: r.Header.Get("X-Request-ID")}
	}
	policy, err := s.Policies.Get(r.Context(), projectID, decision.DeploymentPolicyID)
	if err != nil || policy.Revision != decision.DeploymentPolicyRevision || !policy.Draft.Enabled {
		return result, registry.APIError{Status: 409, Code: "ROUTING_POLICY_CHANGED", Message: "DeploymentPolicy changed during deployment resolution", RequestID: r.Header.Get("X-Request-ID")}
	}
	assignment, ok := deploymentAssignment(plan.Assignments, record.ServiceKey, request.EnvironmentID, decision.RuntimeID)
	if !ok || !workloadMatchesTopology(request.Workload, assignment) {
		return result, registry.APIError{Status: 409, Code: "WORKLOAD_TOPOLOGY_MISMATCH", Message: "replicas and resource requests must exactly match the active TopologyPlan", RequestID: r.Header.Get("X-Request-ID")}
	}
	image, err := deploymentv1.NewImmutableImage(record.Build.OCIRepository, record.Build.OCIDigest)
	if err != nil {
		return result, registry.APIError{Status: 409, Code: "BUILD_ARTIFACT_INVALID", Message: "BuildRecord image identity is invalid", RequestID: r.Header.Get("X-Request-ID")}
	}
	specHash, _ := request.Workload.Hash()
	payloadHash := hashDeploymentPayload(request.BuildRecordID, request.EnvironmentID, request.Workload)
	snapshot := deploymentv1.JobSnapshot{SchemaVersion: deploymentv1.JobSchemaVersion, ProjectID: projectID, Image: image, Workload: request.Workload, SpecHash: specHash, ActorUserID: actor, IdempotencyKey: request.IdempotencyKey, PayloadHash: payloadHash, CreatedAt: s.clock(), Authority: deploymentv1.AuthoritySnapshot{BuildRecord: record, TopologyPlanID: plan.ID, TopologyRevision: plan.Revision, TopologyHash: plan.PlanHash, DeploymentPolicyID: policy.ID, DeploymentPolicyRevision: policy.Revision, DeploymentPolicyHash: policy.PolicyHash, RoutingDecisionHash: decision.DecisionHash, EnvironmentID: request.EnvironmentID, RuntimeID: decision.RuntimeID, NodeID: decision.NodeID, AgentID: decision.AgentID}}
	result.Snapshot = snapshot
	result.Eligible = true
	result.DecisionCode = decision.DecisionCode
	result.Message = decision.Message
	for _, job := range mustListDeployments(s.Registry, projectID) {
		if job.Status == deploymentv1.StateSucceeded && job.ServiceID == record.ServiceID && job.Snapshot != nil {
			current := *job.Snapshot
			result.Current = &current
			break
		}
	}
	if result.Current == nil || result.Current.SpecHash != snapshot.SpecHash {
		result.Changes = append(result.Changes, "workload_spec")
	}
	if result.Current == nil || result.Current.Image.Reference != snapshot.Image.Reference {
		result.Changes = append(result.Changes, "image_digest")
	}
	if result.Current == nil || result.Current.Authority.RuntimeID != snapshot.Authority.RuntimeID {
		result.Changes = append(result.Changes, "target_runtime")
	}
	return result, nil
}

func decodeStrictDeploymentJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeRegistryError(w, registry.APIError{Status: 400, Code: "INVALID_JSON", Message: "deployment request body is invalid", RequestID: r.Header.Get("X-Request-ID")})
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeRegistryError(w, registry.APIError{Status: 400, Code: "INVALID_JSON", Message: "deployment request must contain one JSON value", RequestID: r.Header.Get("X-Request-ID")})
		return false
	}
	return true
}

func validDeploymentIdempotencyKey(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if char <= ' ' || char == 127 {
			return false
		}
	}
	return true
}

func deploymentAssignment(assignments []topologyv1.Assignment, serviceKey, environmentID, runtimeID string) (topologyv1.Assignment, bool) {
	for _, assignment := range assignments {
		if assignment.ServiceKey == serviceKey && assignment.EnvironmentID == environmentID && assignment.RuntimeID == runtimeID {
			return assignment, true
		}
	}
	return topologyv1.Assignment{}, false
}

func workloadMatchesTopology(workload deploymentv1.WorkloadSpec, assignment topologyv1.Assignment) bool {
	cpu, cpuOK := cpuMillicores(workload.Resources.Requests.CPU)
	memory, memoryOK := memoryBytes(workload.Resources.Requests.Memory)
	limitCPU, limitCPUOK := cpuMillicores(workload.Resources.Limits.CPU)
	limitMemory, limitMemoryOK := memoryBytes(workload.Resources.Limits.Memory)
	exposureCompatible := (assignment.Exposure.Mode == "none" || assignment.Exposure.Mode == "internal") && workload.Exposure.Mode == assignment.Exposure.Mode
	return cpuOK && memoryOK && limitCPUOK && limitMemoryOK && exposureCompatible && workload.Replicas == assignment.Replicas && cpu == assignment.CPURequestMillicores && memory == assignment.MemoryRequestBytes && limitCPU >= cpu && limitMemory >= memory
}

func cpuMillicores(value string) (int64, bool) {
	if strings.HasSuffix(value, "m") {
		return parsePositiveInt(strings.TrimSuffix(value, "m"))
	}
	cores, ok := parsePositiveInt(value)
	return cores * 1000, ok
}

func memoryBytes(value string) (int64, bool) {
	multipliers := []struct {
		suffix string
		value  int64
	}{{"Ti", 1 << 40}, {"Gi", 1 << 30}, {"Mi", 1 << 20}, {"Ki", 1 << 10}}
	for _, item := range multipliers {
		if strings.HasSuffix(value, item.suffix) {
			number, ok := parsePositiveInt(strings.TrimSuffix(value, item.suffix))
			if !ok || number > (1<<63-1)/item.value {
				return 0, false
			}
			return number * item.value, true
		}
	}
	return parsePositiveInt(value)
}

func parsePositiveInt(value string) (int64, bool) {
	if value == "" {
		return 0, false
	}
	var result int64
	for _, char := range value {
		if char < '0' || char > '9' || result > (1<<63-1-int64(char-'0'))/10 {
			return 0, false
		}
		result = result*10 + int64(char-'0')
	}
	return result, result > 0
}

func hashDeploymentPayload(buildRecordID, environmentID string, workload deploymentv1.WorkloadSpec) string {
	data, _ := json.Marshal(struct {
		BuildRecordID string                    `json:"build_record_id"`
		EnvironmentID string                    `json:"environment_id"`
		Workload      deploymentv1.WorkloadSpec `json:"workload"`
	}{buildRecordID, environmentID, workload.Normalize()})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func mustListDeployments(api registry.API, projectID string) []registry.DeploymentJob {
	jobs, err := api.ListDeployments(projectID)
	if err != nil {
		return nil
	}
	return jobs
}
