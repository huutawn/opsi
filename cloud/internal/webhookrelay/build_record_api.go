package webhookrelay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/buildrecord"
	"github.com/opsi-dev/opsi/cloud/internal/deploymentpolicy"
	"github.com/opsi-dev/opsi/cloud/internal/githuboidc"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
	deploymentpolicyv1 "github.com/opsi-dev/opsi/contracts/go/deploymentpolicyv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

const (
	maxBuildRecordBodyBytes     = 64 << 10
	buildRecordMaxConcurrency   = 8
	buildRecordGlobalLimit      = 120
	buildRecordTokenLimit       = 30
	buildRecordRateWindow       = time.Minute
	buildRecordRateRetrySeconds = 60
)

func (s *Server) handleBuildRecordSubmission(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.URL.RawQuery != "" || r.Header.Get("Cookie") != "" {
		writeRegistryError(w, registry.APIError{Status: 400, Code: "OIDC_REQUEST_INVALID", Message: "OIDC submission does not accept query parameters or cookies", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	release, ok := s.admitBuildRecordSubmission(w, r)
	if !ok {
		return
	}
	defer release()
	token := bearerToken(r)
	if len(r.Header.Values("Authorization")) != 1 || token == "" || strings.ContainsAny(token, " \t\r\n") {
		writeRegistryError(w, registry.APIError{Status: 401, Code: "OIDC_AUTH_REQUIRED", Message: "Authorization bearer OIDC token is required", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	if s.OIDC == nil || s.oidcInitError != nil {
		writeRegistryError(w, registry.APIError{Status: 503, Code: "OIDC_UNAVAILABLE", Message: "OIDC verification is unavailable", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	identity, err := s.OIDC.Verify(r.Context(), token)
	if err != nil {
		writeRegistryError(w, registry.APIError{Status: 401, Code: "OIDC_AUTH_INVALID", Message: "OIDC token is invalid", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	var submission buildrecordv1.Submission
	if !decodeStrictBuildRecordJSON(w, r, &submission) {
		return
	}
	identity, submission, err = s.authorizeBuildIdentity(r.Context(), identity, submission)
	if err != nil {
		writeBuildRecordFailure(w, r, err)
		return
	}
	record, reused, err := s.BuildRecords.Submit(r.Context(), identity, submission)
	if err != nil {
		writeBuildRecordFailure(w, r, err)
		return
	}
	status := http.StatusCreated
	if reused {
		status = http.StatusOK
	}
	delivery, _, err := s.ensureAutomaticDelivery(r.Context(), record)
	if err != nil {
		writeAutomaticDeliveryFailure(w, r, err)
		return
	}
	writeJSON(w, status, map[string]any{"record": record, "reused": reused, "delivery": delivery, "delivery_pending": false})
}

func (s *Server) authorizeBuildIdentity(ctx context.Context, identity githuboidc.VerifiedIdentity, submission buildrecordv1.Submission) (githuboidc.VerifiedIdentity, buildrecordv1.Submission, error) {
	if identity.EventName != "pull_request" {
		return identity, submission, nil
	}
	binding, err := s.BuildRecords.Bindings.ResolveBuildBinding(ctx, identity.RepositoryID, submission.ServiceKey)
	if err != nil {
		return identity, submission, buildrecord.Error{Code: "PR_BINDING_INVALID", Status: 403, Message: "active same-repository binding is required"}
	}
	repositories, err := s.Registry.ListGitHubRepositories(binding.ProjectID)
	if err != nil {
		return identity, submission, buildrecord.Error{Code: "PR_AUTHORITY_UNAVAILABLE", Status: 503, Message: "GitHub repository authority is unavailable"}
	}
	var repository registry.GitHubRepository
	for _, candidate := range repositories {
		if candidate.RepositoryID == int64(identity.RepositoryID) {
			repository = candidate
			break
		}
	}
	if repository.RepositoryID == 0 || s.githubAppClient == nil {
		return identity, submission, buildrecord.Error{Code: "PR_AUTHORITY_UNAVAILABLE", Status: 503, Message: "GitHub App pull request authority is unavailable"}
	}
	prNumber, ok := pullRequestNumber(identity.Ref)
	if !ok {
		return identity, submission, buildrecord.Error{Code: "PR_REF_INVALID", Status: 403, Message: "pull request ref is invalid"}
	}
	pr, err := s.githubAppClient.PullRequest(ctx, repository.InstallationID, repository.FullName, prNumber)
	if err != nil || pr.State != "open" || pr.BaseRef != repository.DefaultBranch || pr.BaseRepositoryID != identity.RepositoryID || pr.HeadRepositoryID != identity.RepositoryID || repository.DefaultBranch != "main" {
		return identity, submission, buildrecord.Error{Code: "PR_NOT_SAME_REPOSITORY", Status: 403, Message: "only an open same-repository PR targeting the default main branch is eligible"}
	}
	identity.SHA = pr.HeadSHA
	submission.SHA = pr.HeadSHA
	return identity, submission, nil
}

func (s *Server) ensureAutomaticDelivery(ctx context.Context, record buildrecordv1.Record) (*registry.DeploymentJob, bool, error) {
	if record.Workload.EventName != "push" && record.Workload.EventName != "pull_request" {
		return nil, false, nil
	}
	// Automatic delivery is opt-in; an unavailable or empty policy store cannot
	// authorize a deployment and must not make BuildRecord acceptance fail.
	if s.Policies.Store == nil || s.Policies.BuildRecords == nil || s.Registry == nil {
		return nil, false, nil
	}
	if record.Workload.EventName == "push" {
		repositories, err := s.Registry.ListGitHubRepositories(record.ProjectID)
		if err != nil {
			if errors.Is(err, registry.ErrNotFound) {
				return nil, false, nil
			}
			return nil, false, err
		}
		var found bool
		for _, repository := range repositories {
			if repository.RepositoryID == int64(record.RepositoryID) {
				found = repository.DefaultBranch == "main" && repository.OwnerID == int64(record.RepositoryOwnerID)
				break
			}
		}
		if !found {
			return nil, false, nil
		}
	}
	decision, err := s.Policies.Route(ctx, record.ProjectID, deploymentpolicyv1.RoutingRequest{BuildRecordID: record.ID, Automatic: true})
	if err != nil || !decision.Eligible {
		var policyErr deploymentpolicy.Error
		if isAutomaticDisabled(err) || errors.As(err, &policyErr) && (policyErr.Code == "ROUTING_AUTOMATIC_DISABLED" || policyErr.Code == "ROUTING_BUILD_RECORD_NOT_FOUND") || !decision.Eligible && decision.DecisionCode == "ROUTING_AUTOMATIC_DISABLED" {
			return nil, false, nil
		}
		return nil, false, err
	}
	plan, err := s.Topology.Get(ctx, record.ProjectID)
	if err != nil {
		return nil, false, err
	}
	policy, err := s.Policies.Get(ctx, record.ProjectID, decision.DeploymentPolicyID)
	if err != nil {
		return nil, false, err
	}
	services, err := s.Registry.ListServices(record.ProjectID)
	if err != nil {
		return nil, false, err
	}
	var service registry.ServiceRecord
	for _, candidate := range services {
		if candidate.ID == record.ServiceID {
			service = candidate
			break
		}
	}
	assignment, ok := deploymentAssignment(plan.Assignments, record.ServiceKey, decision.EnvironmentID)
	if !ok || assignment.RuntimeID != decision.RuntimeID || service.ID == "" {
		return nil, false, registry.APIError{Status: 409, Code: "ROUTING_SERVICE_AUTHORITY_INVALID", Message: "canonical service or topology assignment is unavailable"}
	}
	configuration, err := s.Registry.GetServiceConfiguration(record.ProjectID, service.ID)
	if err != nil {
		return nil, false, err
	}
	workload, err := registry.CompileServiceRuntimeSpecs(service, assignment, plan.Assignments, configuration, services)
	if err != nil {
		return nil, false, registry.APIError{Status: 409, Code: "WORKLOAD_AUTHORITY_INVALID", Message: "canonical service workload is invalid"}
	}
	managedEnvironment, err := s.Resources.ApplicationEnvironment(ctx, record.ProjectID, decision.EnvironmentID, service.ID)
	if err != nil {
		return nil, false, err
	}
	workload.Environment = append(workload.Environment, managedEnvironment...)
	sort.Slice(workload.Environment, func(i, j int) bool { return workload.Environment[i].Name < workload.Environment[j].Name })
	if err := deploymentv1.ValidateEnvironment(workload.Environment, nil); err != nil {
		return nil, false, registry.APIError{Status: 409, Code: "MANAGED_RESOURCE_SPEC_INVALID", Message: err.Error()}
	}
	image, err := deploymentv1.NewImmutableImage(record.Build.OCIRepository, record.Build.OCIDigest)
	if err != nil {
		return nil, false, err
	}
	s.associateRegistryPullCredential(image, &workload)
	specHash, _ := workload.Hash()
	snapshot := deploymentv1.JobSnapshot{SchemaVersion: deploymentv1.JobSchemaVersion, ProjectID: record.ProjectID, Image: image, Workload: workload, SpecHash: specHash, Authority: deploymentv1.AuthoritySnapshot{BuildRecord: record, TopologyPlanID: plan.ID, TopologyRevision: plan.Revision, TopologyHash: plan.PlanHash, ServiceConfigurationRevision: configuration.Revision, ServiceConfigurationStateHash: configuration.StateHash, DeploymentPolicyID: policy.ID, DeploymentPolicyRevision: policy.Revision, DeploymentPolicyHash: policy.PolicyHash, RoutingDecisionHash: decision.DecisionHash, EnvironmentID: decision.EnvironmentID, RuntimeID: decision.RuntimeID, NodeID: decision.NodeID, AgentID: decision.AgentID}}
	if record.Workload.EventName == "pull_request" {
		prNumber, ok := pullRequestNumber(record.Workload.Ref)
		if !ok || !policy.Draft.Preview.Enabled {
			return nil, false, nil
		}
		preview, err := s.previewAuthority(ctx, record, service, policy.Draft.Preview, prNumber)
		if err != nil {
			return nil, false, err
		}
		if workload.Replicas > preview.MaxReplicas || !quantityAtLeast(preview.CPU, workload.Resources.Limits.CPU, true) || !quantityAtLeast(preview.Memory, workload.Resources.Limits.Memory, false) {
			return nil, false, registry.APIError{Status: 409, Code: "PREVIEW_QUOTA_TOO_SMALL", Message: "preview policy quota is below the canonical workload limits"}
		}
		snapshot.Preview = &preview
	}
	snapshot.PayloadHash = hashDeploymentPayload(snapshot)
	keyPrefix := "main"
	if snapshot.Preview != nil {
		keyPrefix = "preview"
	}
	starter, ok := s.Registry.(immutableDeploymentStarter)
	if !ok {
		return nil, false, automaticDeliveryError(registry.APIError{Status: 503, Code: "DEPLOYMENT_UNAVAILABLE", Message: "immutable deployment store is unavailable"})
	}
	job, reused, err := starter.StartImmutableDeployment(snapshot, "github-actions", keyPrefix+":"+record.ID, "automatic-"+record.ID)
	if err != nil {
		return nil, reused, automaticDeliveryError(err)
	}
	job.Reused = reused
	if !reused {
		action := "MAIN_DEPLOYMENT_CREATED"
		if snapshot.Preview != nil {
			action = "PR_PREVIEW_CREATED"
		}
		s.Registry.Audit(job.OrgID, record.ProjectID, "github-actions", action, "deployment_job", job.ID, "success", map[string]any{"build_record_id": record.ID, "reused": false, "oci_digest": record.Build.OCIDigest})
	}
	return &job, reused, nil
}

func automaticDeliveryError(err error) error {
	var apiErr registry.APIError
	if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 {
		return registry.APIError{Status: apiErr.Status, Code: apiErr.Code, Message: "automatic delivery authority rejected the request"}
	}
	return registry.APIError{Status: http.StatusServiceUnavailable, Code: "AUTOMATIC_DELIVERY_PENDING", Message: "BuildRecord was accepted but automatic delivery is pending"}
}

func writeAutomaticDeliveryFailure(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr registry.APIError
	if errors.As(err, &apiErr) {
		apiErr.RequestID = r.Header.Get("X-Request-ID")
		writeRegistryError(w, apiErr)
		return
	}
	var policyErr deploymentpolicy.Error
	if errors.As(err, &policyErr) {
		writeRegistryError(w, registry.APIError{Status: policyErr.Status, Code: policyErr.Code, Message: "automatic delivery authority rejected the request", RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	writeRegistryError(w, registry.APIError{Status: http.StatusInternalServerError, Code: "AUTOMATIC_DELIVERY_FAILED", Message: "automatic delivery failed", RequestID: r.Header.Get("X-Request-ID")})
}

func quantityAtLeast(limit, required string, cpu bool) bool {
	parse := func(value string) (int64, bool) {
		multiplier := int64(1)
		if cpu && strings.HasSuffix(value, "m") {
			value = strings.TrimSuffix(value, "m")
		} else if cpu {
			multiplier = 1000
		} else {
			for suffix, factor := range map[string]int64{"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40} {
				if strings.HasSuffix(value, suffix) {
					value = strings.TrimSuffix(value, suffix)
					multiplier = factor
					break
				}
			}
		}
		valueInt, err := strconv.ParseInt(value, 10, 64)
		if err != nil || valueInt < 0 || valueInt > (1<<63-1)/multiplier {
			return 0, false
		}
		return valueInt * multiplier, true
	}
	left, lok := parse(limit)
	right, rok := parse(required)
	return lok && rok && left >= right
}

func (s *Server) previewAuthority(ctx context.Context, record buildrecordv1.Record, service registry.ServiceRecord, policy deploymentpolicyv1.PreviewPolicy, prNumber int) (deploymentv1.PreviewSpec, error) {
	if s.githubAppClient == nil {
		return deploymentv1.PreviewSpec{}, registry.APIError{Status: 503, Code: "PR_AUTHORITY_UNAVAILABLE", Message: "GitHub App pull request authority is unavailable"}
	}
	repositories, err := s.Registry.ListGitHubRepositories(record.ProjectID)
	if err != nil {
		return deploymentv1.PreviewSpec{}, err
	}
	var repository registry.GitHubRepository
	for _, candidate := range repositories {
		if candidate.RepositoryID == int64(record.RepositoryID) {
			repository = candidate
			break
		}
	}
	pr, err := s.githubAppClient.PullRequest(ctx, repository.InstallationID, repository.FullName, prNumber)
	if err != nil || pr.State != "open" || pr.HeadRepositoryID != record.RepositoryID || pr.BaseRepositoryID != record.RepositoryID || pr.HeadSHA != record.Workload.SHA {
		return deploymentv1.PreviewSpec{}, registry.APIError{Status: 403, Code: "PR_AUTHORITY_REJECTED", Message: "GitHub App PR authority does not match the accepted BuildRecord"}
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%s:%s", record.RepositoryID, prNumber, service.Name, record.ProjectID)))
	namespace := "opsi-preview-" + hex.EncodeToString(hash[:])[:24]
	hostHash := hex.EncodeToString(hash[:])[:10]
	hostname := "pr-" + strconv.Itoa(prNumber) + "-" + safeDNSLabel(service.Name) + "-" + hostHash + "." + strings.TrimSuffix(policy.HostnameSuffix, ".")
	created := record.CreatedAt
	return deploymentv1.PreviewSpec{Namespace: namespace, Hostname: hostname, RepositoryID: record.RepositoryID, RepositoryOwnerID: record.RepositoryOwnerID, PRNumber: prNumber, HeadSHA: record.Workload.SHA, ServiceKey: service.Name, CPU: policy.CPU, Memory: policy.Memory, MaxReplicas: policy.MaxReplicas, CreatedAt: created, ExpiresAt: created.Add(time.Duration(policy.TTLSeconds) * time.Second)}, nil
}

func pullRequestNumber(ref string) (int, bool) {
	const prefix, suffix = "refs/pull/", "/merge"
	if !strings.HasPrefix(ref, prefix) || !strings.HasSuffix(ref, suffix) {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(ref, prefix), suffix)
	number, err := strconv.Atoi(raw)
	if err != nil || number < 1 || number > 1_000_000_000 {
		return 0, false
	}
	return number, true
}

func safeDNSLabel(value string) string {
	value = strings.ToLower(value)
	var out strings.Builder
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-' {
			out.WriteRune(char)
		}
	}
	value = strings.Trim(out.String(), "-")
	if len(value) > 40 {
		value = strings.Trim(value[:40], "-")
	}
	if value == "" {
		return "service"
	}
	return value
}

func isAutomaticDisabled(err error) bool {
	var apiErr registry.APIError
	return errors.As(err, &apiErr) && apiErr.Code == "ROUTING_AUTOMATIC_DISABLED"
}

func (s *Server) admitBuildRecordSubmission(w http.ResponseWriter, r *http.Request) (func(), bool) {
	if s.buildRecordSlots == nil {
		s.buildRecordSlots = make(chan struct{}, buildRecordMaxConcurrency)
	}
	select {
	case s.buildRecordSlots <- struct{}{}:
	case <-r.Context().Done():
		return func() {}, false
	default:
		writeBuildRecordRateLimit(w, r, "BUILD_RECORD_BUSY", "BuildRecord submission concurrency is saturated", 1)
		return func() {}, false
	}
	release := func() { <-s.buildRecordSlots }
	if s.limits == nil || !s.limits.Allow("build-record:global", buildRecordGlobalLimit, buildRecordRateWindow) {
		release()
		writeBuildRecordRateLimit(w, r, "BUILD_RECORD_RATE_LIMITED", "BuildRecord submission rate limit exceeded", buildRecordRateRetrySeconds)
		return func() {}, false
	}
	token := bearerToken(r)
	if token != "" {
		if !s.limits.Allow("build-record:token:"+tokenHash(token), buildRecordTokenLimit, buildRecordRateWindow) {
			release()
			writeBuildRecordRateLimit(w, r, "BUILD_RECORD_RATE_LIMITED", "BuildRecord submission rate limit exceeded", buildRecordRateRetrySeconds)
			return func() {}, false
		}
	}
	return release, true
}

func writeBuildRecordRateLimit(w http.ResponseWriter, r *http.Request, code, message string, retryAfter int) {
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	writeRegistryError(w, registry.APIError{Status: http.StatusTooManyRequests, Code: code, Message: message, RequestID: r.Header.Get("X-Request-ID")})
}

func (s *Server) handleBuildRecordRead(w http.ResponseWriter, r *http.Request, projectID string, parts []string, principal auth.VerifyResult) bool {
	if len(parts) < 3 || parts[2] != "build-records" {
		return false
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return true
	}
	if !s.requireRole(w, r, principal, projectID, "build_record", projectID, "owner", "admin", "developer", "viewer", "support") {
		return true
	}
	if len(parts) == 3 {
		limit, err := strconv.Atoi(firstNonEmpty(r.URL.Query().Get("limit"), "50"))
		if err != nil {
			writeRegistryError(w, registry.APIError{Status: 400, Code: "BUILD_RECORD_LIST_INVALID", Message: "limit is invalid", RequestID: r.Header.Get("X-Request-ID")})
			return true
		}
		repositoryID := uint64(0)
		if raw := r.URL.Query().Get("repository_id"); raw != "" {
			repositoryID, err = strconv.ParseUint(raw, 10, 64)
			if err != nil || repositoryID == 0 {
				writeRegistryError(w, registry.APIError{Status: 400, Code: "BUILD_RECORD_LIST_INVALID", Message: "repository_id is invalid", RequestID: r.Header.Get("X-Request-ID")})
				return true
			}
		}
		result, err := s.BuildRecords.List(r.Context(), projectID, buildrecord.ListFilter{ServiceKey: r.URL.Query().Get("service_key"), RepositoryID: repositoryID, SHA: r.URL.Query().Get("sha"), Status: r.URL.Query().Get("status"), Limit: limit, Cursor: r.URL.Query().Get("cursor")})
		if err != nil {
			writeBuildRecordFailure(w, r, err)
			return true
		}
		writeJSON(w, http.StatusOK, result)
		return true
	}
	if len(parts) == 4 {
		record, err := s.BuildRecords.Get(r.Context(), projectID, parts[3])
		if err != nil {
			writeBuildRecordFailure(w, r, err)
			return true
		}
		writeJSON(w, http.StatusOK, record)
		return true
	}
	http.NotFound(w, r)
	return true
}

func decodeStrictBuildRecordJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBuildRecordBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeRegistryError(w, registry.APIError{Status: 400, Code: "INVALID_JSON", Message: "build record request body is invalid", RequestID: r.Header.Get("X-Request-ID")})
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeRegistryError(w, registry.APIError{Status: 400, Code: "INVALID_JSON", Message: "build record request body must contain one JSON value", RequestID: r.Header.Get("X-Request-ID")})
		return false
	}
	return true
}

func writeBuildRecordFailure(w http.ResponseWriter, r *http.Request, err error) {
	var typed buildrecord.Error
	if errors.As(err, &typed) {
		writeRegistryError(w, registry.APIError{Status: typed.Status, Code: typed.Code, Message: typed.Message, RequestID: r.Header.Get("X-Request-ID")})
		return
	}
	writeRegistryError(w, registry.APIError{Status: 500, Code: "INTERNAL", Message: "Internal server error.", RequestID: r.Header.Get("X-Request-ID")})
}
