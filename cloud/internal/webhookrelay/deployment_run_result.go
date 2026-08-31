package webhookrelay

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/opsi-dev/opsi/cloud/internal/deploymentworkflow"
	"github.com/opsi-dev/opsi/cloud/internal/publichostname"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
	verificationv1 "github.com/opsi-dev/opsi/contracts/go/verificationv1"
)

// deploymentRunResult is a read-only projection. Operational status remains
// owned by BuildRecord, DeploymentJob, VerificationRun, and TopologyPlan.
type deploymentRunResult struct {
	RunID           string                           `json:"run_id"`
	State           deploymentworkflow.State         `json:"state"`
	SourceSHA       string                           `json:"source_sha"`
	PublicURL       string                           `json:"public_url,omitempty"`
	Applications    []deploymentRunApplicationResult `json:"applications"`
	PublicEndpoints []deploymentRunPublicEndpoint    `json:"public_endpoints,omitempty"`
	PublicHostname  *publichostname.Allocation       `json:"public_hostname,omitempty"`
	Verifications   []verificationv1.VerificationRun `json:"verifications"`
	Capacity        []topologyv1.Capacity            `json:"capacity"`
}

type deploymentRunPublicEndpoint struct {
	ServiceKey string `json:"service_key"`
	ServiceID  string `json:"service_id"`
	Port       int32  `json:"port"`
	Hostname   string `json:"hostname"`
	URL        string `json:"url"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
}

type deploymentRunApplicationResult struct {
	ServiceKey            string `json:"service_key"`
	ServiceID             string `json:"service_id"`
	BuildRecordID         string `json:"build_record_id"`
	BuildDigest           string `json:"build_digest"`
	BuildLogURL           string `json:"build_log_url,omitempty"`
	DeploymentJobID       string `json:"deployment_job_id,omitempty"`
	DeploymentStatus      string `json:"deployment_status,omitempty"`
	ContainerPort         int32  `json:"container_port,omitempty"`
	ApplicationImageID    string `json:"application_image_id,omitempty"`
	AvailableReplicas     int32  `json:"available_replicas,omitempty"`
	ReadinessEvidenceHash string `json:"readiness_evidence_hash,omitempty"`
	DigestMatchesImageID  bool   `json:"digest_matches_image_id"`
	PublicURL             string `json:"public_url,omitempty"`
}

func (s *Server) deploymentRunResult(ctx context.Context, projectID, runID string) (deploymentRunResult, error) {
	run, err := s.DeploymentRuns.Get(ctx, projectID, runID)
	if err != nil {
		return deploymentRunResult{}, err
	}
	result := deploymentRunResult{RunID: run.ID, State: run.State, SourceSHA: run.Plan.Source.CommitSHA, Applications: []deploymentRunApplicationResult{}, PublicEndpoints: []deploymentRunPublicEndpoint{}, Verifications: []verificationv1.VerificationRun{}, Capacity: []topologyv1.Capacity{}}
	reader, _ := s.Registry.(immutableDeploymentReader)
	deployments := map[string]registry.DeploymentJob{}
	if reader != nil {
		for _, id := range run.Refs.IDs(deploymentworkflow.AuthorityDeploymentJob) {
			job, getErr := reader.GetDeployment(projectID, id)
			if getErr != nil {
				return deploymentRunResult{}, getErr
			}
			deployments[job.ServiceID] = job
		}
	}
	published, listErr := s.Registry.ListDeployments(projectID)
	if listErr != nil {
		return deploymentRunResult{}, listErr
	}
	publicURLs := publishedServiceURLs(published)
	latestExposures := latestExposureJobsByService(published)
	var allocation *publichostname.Allocation
	if run.Plan.Target.Hostname != "" {
		value, allocationErr := s.PublicHostnames.GetByHostname(ctx, run.Plan.Target.Hostname)
		if allocationErr == nil {
			allocation = &value
			result.PublicHostname = allocation
		} else if !errors.Is(allocationErr, publichostname.ErrNotFound) {
			return deploymentRunResult{}, allocationErr
		}
	}
	for _, id := range run.Refs.IDs(deploymentworkflow.AuthorityBuildRecord) {
		record, getErr := s.BuildRecords.Get(ctx, projectID, id)
		if getErr != nil {
			return deploymentRunResult{}, getErr
		}
		application := buildRecordResult(run, record, deployments[record.ServiceID])
		if allocation != nil && allocation.Status == publichostname.StatusActive {
			application.PublicURL = publicURLs[record.ServiceID]
		}
		result.Applications = append(result.Applications, application)
		if endpoint, ok := automaticEndpoint(run, application, latestExposures[record.ServiceID], allocation); ok {
			result.PublicEndpoints = append(result.PublicEndpoints, endpoint)
		}
	}
	if len(result.Applications) == 1 && len(result.PublicEndpoints) > 0 && result.PublicEndpoints[0].Status == "ready" {
		result.PublicURL = result.Applications[0].PublicURL
	}
	for _, id := range run.Refs.IDs(deploymentworkflow.AuthorityVerification) {
		verification, getErr := s.Verifications.Get(ctx, projectID, id)
		if getErr != nil {
			return deploymentRunResult{}, getErr
		}
		result.Verifications = append(result.Verifications, verification)
	}
	if plan, getErr := s.Topology.Get(ctx, projectID); getErr == nil && plan.ID == run.Refs.FirstID(deploymentworkflow.AuthorityTopologyPlan) {
		validation, validationErr := s.Topology.Validate(ctx, projectID, topologyv1.Draft{SchemaVersion: topologyv1.SchemaVersion, ProjectID: projectID, Assignments: plan.Assignments}, false)
		if validationErr == nil {
			for _, runtime := range validation.Runtimes {
				result.Capacity = append(result.Capacity, runtime.Capacity)
			}
		}
	}
	return result, nil
}

func automaticEndpoint(run deploymentworkflow.Run, application deploymentRunApplicationResult, job *registry.DeploymentJob, allocation *publichostname.Allocation) (deploymentRunPublicEndpoint, bool) {
	for _, planned := range run.Plan.Applications {
		if planned.Key != application.ServiceKey || !planned.Exposure.Automatic || planned.Exposure.Hostname == "" {
			continue
		}
		port := application.ContainerPort
		if port == 0 {
			port = int32(planned.Port)
		}
		endpoint := deploymentRunPublicEndpoint{ServiceKey: application.ServiceKey, ServiceID: application.ServiceID, Port: port, Hostname: planned.Exposure.Hostname, URL: "https://" + planned.Exposure.Hostname + firstNonEmpty(planned.Exposure.Path, "/"), Status: "publishing"}
		if allocation == nil {
			endpoint.Status, endpoint.Message = "failed", "The public hostname allocation is missing."
			return endpoint, true
		}
		if allocation.Status != publichostname.StatusActive {
			if allocation.Status == publichostname.StatusFailed || allocation.Status == publichostname.StatusReleasePending || allocation.Status == publichostname.StatusReleased {
				endpoint.Status = "failed"
				endpoint.Message = firstNonEmpty(allocation.PublicationError, "DNS publication is not active.")
			}
			return endpoint, true
		}
		if job == nil || job.ExposureSpec == nil {
			if message := publicRouteFailure(run, application.ServiceKey); message != "" {
				endpoint.Status, endpoint.Message = "failed", message
			}
			return endpoint, true
		}
		if job.ExposureSpec.Metadata == nil || job.ExposureSpec.Metadata.Rationale != automaticPublicRouteRationale {
			endpoint.Hostname = job.ExposureSpec.Hostname
			endpoint.URL = "https://" + endpoint.Hostname + job.ExposureSpec.Path
			endpoint.Status = "manual_preserved"
			return endpoint, true
		}
		if job.Status == deploymentv1.StateSucceeded && job.RolloutState == deploymentv1.RolloutStateSucceeded {
			endpoint.Status = "ready"
			return endpoint, true
		}
		if job.Status == deploymentv1.StateFailed || job.Status == deploymentv1.StateCancelled || job.RolloutState == deploymentv1.RolloutStateFailed || job.RolloutState == deploymentv1.RolloutStateRolledBack || job.RolloutState == deploymentv1.RolloutStateRollbackFailed {
			endpoint.Status = "failed"
			endpoint.Message = firstNonEmpty(job.FailureMessageRedacted, "The route rollout failed.")
		}
		return endpoint, true
	}
	return deploymentRunPublicEndpoint{}, false
}

func publicRouteFailure(run deploymentworkflow.Run, serviceKey string) string {
	for _, failure := range run.PublicRouteFailures {
		if failure.ServiceKey == serviceKey {
			return failure.Message
		}
	}
	return ""
}

func buildRecordResult(run deploymentworkflow.Run, record buildrecordv1.Record, job registry.DeploymentJob) deploymentRunApplicationResult {
	result := deploymentRunApplicationResult{ServiceKey: record.ServiceKey, ServiceID: record.ServiceID, BuildRecordID: record.ID, BuildDigest: record.Build.OCIDigest}
	if record.Workload.RunID > 0 {
		result.BuildLogURL = fmt.Sprintf("https://github.com/%s/actions/runs/%d", run.Plan.Source.Repository, record.Workload.RunID)
	}
	if job.ID == "" {
		return result
	}
	result.DeploymentJobID, result.DeploymentStatus = job.ID, job.Status
	if job.Snapshot != nil {
		result.ContainerPort = job.Snapshot.Workload.ContainerPort
	}
	result.ReadinessEvidenceHash = job.ReadinessEvidenceHash
	if job.TerminalResult != nil {
		result.ApplicationImageID = job.TerminalResult.ApplicationImageID
		result.AvailableReplicas = job.TerminalResult.AvailableReplicas
		if result.ReadinessEvidenceHash == "" {
			result.ReadinessEvidenceHash = job.TerminalResult.ReadinessEvidenceHash
		}
		result.DigestMatchesImageID = record.Build.OCIDigest != "" && job.TerminalResult.CurrentDigest == record.Build.OCIDigest && strings.HasSuffix(job.TerminalResult.ApplicationImageID, record.Build.OCIDigest) && job.TerminalResult.Status == deploymentv1.StateSucceeded
	}
	return result
}

func publishedServiceURLs(jobs []registry.DeploymentJob) map[string]string {
	selected := map[string]registry.DeploymentJob{}
	for _, job := range jobs {
		if job.Mode != "rollout" || job.Status != deploymentv1.StateSucceeded || job.RolloutState != deploymentv1.RolloutStateSucceeded || job.ExposureSpec == nil {
			continue
		}
		current, exists := selected[job.ServiceID]
		if !exists || job.UpdatedAt.After(current.UpdatedAt) || job.UpdatedAt.Equal(current.UpdatedAt) && job.ID > current.ID {
			selected[job.ServiceID] = job
		}
	}
	urls := make(map[string]string, len(selected))
	for serviceID, job := range selected {
		exposure := job.ExposureSpec
		urls[serviceID] = "https://" + exposure.Hostname + exposure.Path
	}
	return urls
}

func latestExposureJobsByService(jobs []registry.DeploymentJob) map[string]*registry.DeploymentJob {
	selected := map[string]registry.DeploymentJob{}
	for _, job := range jobs {
		if job.Mode != "rollout" || job.ExposureSpec == nil {
			continue
		}
		current, exists := selected[job.ServiceID]
		if !exists || job.UpdatedAt.After(current.UpdatedAt) || job.UpdatedAt.Equal(current.UpdatedAt) && job.ID > current.ID {
			selected[job.ServiceID] = job
		}
	}
	result := make(map[string]*registry.DeploymentJob, len(selected))
	for serviceID, job := range selected {
		copy := job
		result[serviceID] = &copy
	}
	return result
}
