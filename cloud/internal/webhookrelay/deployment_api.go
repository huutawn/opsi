package webhookrelay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"reflect"

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

type previewCleanupStore interface {
	StartPreviewCleanup(string, string, string, string, deploymentv1.PreviewCleanupRequest) (registry.DeploymentJob, bool, error)
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
		if err == nil && !reused {
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
		if err == nil && !reused {
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
	if len(parts) == 5 && parts[4] == "cleanup" && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "deployment_job", parts[3], "owner", "admin", "developer") {
			return true
		}
		store, ok := s.Registry.(previewCleanupStore)
		if !ok {
			writeRegistryError(w, registry.APIError{Status: 503, Code: "PREVIEW_CLEANUP_UNAVAILABLE", Message: "preview cleanup is unavailable", RequestID: r.Header.Get("X-Request-ID")})
			return true
		}
		var request deploymentv1.PreviewCleanupRequest
		if !decodeStrictDeploymentJSON(w, r, &request) {
			return true
		}
		if request.DeploymentID == "" {
			request.DeploymentID = parts[3]
		}
		job, reused, err := store.StartPreviewCleanup(projectID, principal.UserID, r.Header.Get("Idempotency-Key"), r.Header.Get("X-Request-ID"), request)
		job.Reused = reused
		writeRegistryResult(w, r, job, err, http.StatusAccepted)
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
	resolvedRequest := request.Workload == nil
	if resolvedRequest && (request.ExpectedTopologyRevision == 0 || len(request.ExpectedTopologyHash) != 64 || len(request.ExpectedConfigurationStateHash) != 64) {
		return result, registry.APIError{Status: 400, Code: "DEPLOYMENT_EXPECTATION_REQUIRED", Message: "resolved deployment requires expected topology revision/hash and configuration revision/hash", RequestID: r.Header.Get("X-Request-ID")}
	}
	record, err := s.BuildRecords.Get(r.Context(), projectID, request.BuildRecordID)
	if err != nil {
		return result, registry.APIError{Status: 404, Code: "BUILD_RECORD_NOT_FOUND", Message: "accepted BuildRecord was not found", RequestID: r.Header.Get("X-Request-ID")}
	}
	if record.Build.Status != "succeeded" {
		return result, registry.APIError{Status: 409, Code: "BUILD_RECORD_NOT_ACCEPTED", Message: "BuildRecord is not accepted for deployment", RequestID: r.Header.Get("X-Request-ID")}
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
	if request.ExpectedTopologyRevision != 0 && (plan.Revision != request.ExpectedTopologyRevision || plan.PlanHash != request.ExpectedTopologyHash) {
		return result, registry.APIError{Status: 409, Code: "TOPOLOGY_REVIEW_STALE", Message: "TopologyPlan changed after deployment review", NextAction: "review_again", RequestID: r.Header.Get("X-Request-ID")}
	}
	policy, err := s.Policies.Get(r.Context(), projectID, decision.DeploymentPolicyID)
	if err != nil || policy.Revision != decision.DeploymentPolicyRevision || !policy.Draft.Enabled {
		return result, registry.APIError{Status: 409, Code: "ROUTING_POLICY_CHANGED", Message: "DeploymentPolicy changed during deployment resolution", RequestID: r.Header.Get("X-Request-ID")}
	}
	if request.ExpectedDeploymentPolicyRevision != 0 && (policy.Revision != request.ExpectedDeploymentPolicyRevision || policy.PolicyHash != request.ExpectedDeploymentPolicyHash) {
		return result, registry.APIError{Status: 409, Code: "POLICY_REVIEW_STALE", Message: "DeploymentPolicy changed after deployment review", NextAction: "review_again", RequestID: r.Header.Get("X-Request-ID")}
	}
	assignment, ok := deploymentAssignment(plan.Assignments, record.ServiceKey, request.EnvironmentID, decision.RuntimeID)
	if !ok {
		return result, registry.APIError{Status: 409, Code: "WORKLOAD_TOPOLOGY_MISMATCH", Message: "service assignment is unavailable in the active TopologyPlan", RequestID: r.Header.Get("X-Request-ID")}
	}
	services, err := s.Registry.ListServices(projectID)
	if err != nil {
		return result, err
	}
	var service registry.ServiceRecord
	for _, candidate := range services {
		if candidate.ID == record.ServiceID && candidate.Name == record.ServiceKey {
			service = candidate
			break
		}
	}
	if service.ID == "" {
		return result, registry.APIError{Status: 409, Code: "DEPLOYMENT_SERVICE_BINDING_INVALID", Message: "BuildRecord service binding is unavailable", RequestID: r.Header.Get("X-Request-ID")}
	}
	configuration, err := s.Registry.GetServiceConfiguration(projectID, service.ID)
	if err != nil {
		return result, err
	}
	if resolvedRequest && (configuration.Revision != request.ExpectedConfigurationRevision || configuration.StateHash != request.ExpectedConfigurationStateHash) {
		return result, registry.APIError{Status: 409, Code: "CONFIGURATION_REVIEW_STALE", Message: "ServiceConfiguration changed after deployment review", NextAction: "review_again", RequestID: r.Header.Get("X-Request-ID")}
	}
	workload, err := registry.CompileServiceRuntimeSpecs(service, assignment, plan.Assignments, configuration, services)
	if err != nil {
		return result, err
	}
	if request.Workload != nil {
		clientWorkload := request.Workload.Normalize()
		if err := clientWorkload.Validate(); err != nil {
			return result, registry.APIError{Status: 400, Code: "WORKLOAD_SPEC_INVALID", Message: err.Error(), RequestID: r.Header.Get("X-Request-ID")}
		}
		if !reflect.DeepEqual(clientWorkload, workload) {
			return result, registry.APIError{Status: 409, Code: "WORKLOAD_CANONICAL_MISMATCH", Message: "client WorkloadSpec does not exactly match the Cloud-compiled canonical spec", NextAction: "refresh_cli_spec", RequestID: r.Header.Get("X-Request-ID")}
		}
	}
	image, err := deploymentv1.NewImmutableImage(record.Build.OCIRepository, record.Build.OCIDigest)
	if err != nil {
		return result, registry.APIError{Status: 409, Code: "BUILD_ARTIFACT_INVALID", Message: "BuildRecord image identity is invalid", RequestID: r.Header.Get("X-Request-ID")}
	}
	specHash, _ := workload.Hash()
	snapshot := deploymentv1.JobSnapshot{SchemaVersion: deploymentv1.JobSchemaVersion, ProjectID: projectID, Image: image, Workload: workload, SpecHash: specHash, ActorUserID: actor, IdempotencyKey: request.IdempotencyKey, CreatedAt: s.clock(), Authority: deploymentv1.AuthoritySnapshot{BuildRecord: record, TopologyPlanID: plan.ID, TopologyRevision: plan.Revision, TopologyHash: plan.PlanHash, ServiceConfigurationRevision: configuration.Revision, ServiceConfigurationStateHash: configuration.StateHash, DeploymentPolicyID: policy.ID, DeploymentPolicyRevision: policy.Revision, DeploymentPolicyHash: policy.PolicyHash, RoutingDecisionHash: decision.DecisionHash, EnvironmentID: request.EnvironmentID, RuntimeID: decision.RuntimeID, NodeID: decision.NodeID, AgentID: decision.AgentID}}
	snapshot.PayloadHash = hashDeploymentPayload(snapshot)
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

func hashDeploymentPayload(snapshot deploymentv1.JobSnapshot) string {
	data, _ := json.Marshal(struct {
		BuildRecordID          string                    `json:"build_record_id"`
		EnvironmentID          string                    `json:"environment_id"`
		Workload               deploymentv1.WorkloadSpec `json:"workload"`
		TopologyRevision       uint64                    `json:"topology_revision"`
		TopologyHash           string                    `json:"topology_hash"`
		ConfigurationRevision  uint64                    `json:"configuration_revision"`
		ConfigurationStateHash string                    `json:"configuration_state_hash"`
		PolicyRevision         uint64                    `json:"policy_revision"`
		PolicyHash             string                    `json:"policy_hash"`
		RoutingDecisionHash    string                    `json:"routing_decision_hash"`
	}{snapshot.Authority.BuildRecord.ID, snapshot.Authority.EnvironmentID, snapshot.Workload.Normalize(), snapshot.Authority.TopologyRevision, snapshot.Authority.TopologyHash, snapshot.Authority.ServiceConfigurationRevision, snapshot.Authority.ServiceConfigurationStateHash, snapshot.Authority.DeploymentPolicyRevision, snapshot.Authority.DeploymentPolicyHash, snapshot.Authority.RoutingDecisionHash})
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
