package webhookrelay

import (
	"context"
	"fmt"
	"strings"

	"github.com/opsi-dev/opsi/cloud/internal/deploymentworkflow"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
	verificationv1 "github.com/opsi-dev/opsi/contracts/go/verificationv1"
)

// deploymentRunResult is a read-only projection. Operational status remains
// owned by BuildRecord, DeploymentJob, VerificationRun, and TopologyPlan.
type deploymentRunResult struct {
	RunID         string                           `json:"run_id"`
	State         deploymentworkflow.State         `json:"state"`
	SourceSHA     string                           `json:"source_sha"`
	PublicURL     string                           `json:"public_url,omitempty"`
	Applications  []deploymentRunApplicationResult `json:"applications"`
	Verifications []verificationv1.VerificationRun `json:"verifications"`
	Capacity      []topologyv1.Capacity            `json:"capacity"`
}

type deploymentRunApplicationResult struct {
	ServiceKey            string `json:"service_key"`
	ServiceID             string `json:"service_id"`
	BuildRecordID         string `json:"build_record_id"`
	BuildDigest           string `json:"build_digest"`
	BuildLogURL           string `json:"build_log_url,omitempty"`
	DeploymentJobID       string `json:"deployment_job_id,omitempty"`
	DeploymentStatus      string `json:"deployment_status,omitempty"`
	ApplicationImageID    string `json:"application_image_id,omitempty"`
	AvailableReplicas     int32  `json:"available_replicas,omitempty"`
	ReadinessEvidenceHash string `json:"readiness_evidence_hash,omitempty"`
	DigestMatchesImageID  bool   `json:"digest_matches_image_id"`
}

func (s *Server) deploymentRunResult(ctx context.Context, projectID, runID string) (deploymentRunResult, error) {
	run, err := s.DeploymentRuns.Get(ctx, projectID, runID)
	if err != nil {
		return deploymentRunResult{}, err
	}
	result := deploymentRunResult{RunID: run.ID, State: run.State, SourceSHA: run.Plan.Source.CommitSHA, Applications: []deploymentRunApplicationResult{}, Verifications: []verificationv1.VerificationRun{}, Capacity: []topologyv1.Capacity{}}
	if run.Plan.Target.Hostname != "" {
		result.PublicURL = "https://" + run.Plan.Target.Hostname
	}
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
	for _, id := range run.Refs.IDs(deploymentworkflow.AuthorityBuildRecord) {
		record, getErr := s.BuildRecords.Get(ctx, projectID, id)
		if getErr != nil {
			return deploymentRunResult{}, getErr
		}
		result.Applications = append(result.Applications, buildRecordResult(run, record, deployments[record.ServiceID]))
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

func buildRecordResult(run deploymentworkflow.Run, record buildrecordv1.Record, job registry.DeploymentJob) deploymentRunApplicationResult {
	result := deploymentRunApplicationResult{ServiceKey: record.ServiceKey, ServiceID: record.ServiceID, BuildRecordID: record.ID, BuildDigest: record.Build.OCIDigest}
	if record.Workload.RunID > 0 {
		result.BuildLogURL = fmt.Sprintf("https://github.com/%s/actions/runs/%d", run.Plan.Source.Repository, record.Workload.RunID)
	}
	if job.ID == "" {
		return result
	}
	result.DeploymentJobID, result.DeploymentStatus = job.ID, job.Status
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
