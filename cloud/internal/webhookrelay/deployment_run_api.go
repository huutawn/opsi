package webhookrelay

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
	"github.com/opsi-dev/opsi/cloud/internal/deploymentworkflow"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/repositoryanalysis"
)

func (s *Server) handleDeploymentRunAPI(w http.ResponseWriter, r *http.Request, projectID string, parts []string, principal auth.VerifyResult) bool {
	if len(parts) < 3 || parts[2] != "deployment-runs" {
		return false
	}
	if len(parts) == 3 {
		switch r.Method {
		case http.MethodGet:
			if !s.requireRole(w, r, principal, projectID, "deployment_run", projectID, "owner", "admin", "developer", "viewer", "support") {
				return true
			}
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			runs, err := s.DeploymentRuns.List(r.Context(), projectID, limit)
			writeRegistryResult(w, r, map[string]any{"deployment_runs": runs}, err, http.StatusOK)
			return true
		case http.MethodPost:
			if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "deployment_run", projectID, "owner", "admin", "developer") {
				return true
			}
			var request struct {
				RepositoryID int64                     `json:"repository_id"`
				SelectedRef  string                    `json:"selected_ref"`
				Target       deploymentworkflow.Target `json:"target"`
			}
			if !decodeJSON(w, r, &request) {
				return true
			}
			repository, err := s.workflowRepository(projectID, request.RepositoryID)
			if err != nil {
				writeRegistryFailure(w, r, err)
				return true
			}
			if request.SelectedRef == "" {
				request.SelectedRef = repository.DefaultBranch
			}
			source := deploymentworkflow.Source{RepositoryID: repository.RepositoryID, InstallationID: repository.InstallationID, Repository: repository.FullName, SelectedRef: request.SelectedRef}
			request.Target = workflowTarget(r.Context(), s.Registry, projectID, request.Target)
			if request.Target.Exposure == "public" && request.Target.Hostname == "" && s.Config.DeploymentDomain != "" {
				request.Target.Hostname = workflowHostname(repository.FullName, projectID, s.Config.DeploymentDomain)
			}
			run, reused, err := s.DeploymentRuns.Create(r.Context(), projectID, principal.UserID, r.Header.Get("Idempotency-Key"), source, request.Target)
			if err != nil {
				writeRegistryFailure(w, r, err)
				return true
			}
			if !reused {
				if analyzed, analysisErr := s.analyzeDeploymentRun(r.Context(), projectID, run.ID); analysisErr == nil {
					run = analyzed
				} else {
					writeRegistryFailure(w, r, analysisErr)
					return true
				}
			}
			writeJSON(w, http.StatusCreated, map[string]any{"deployment_run": run, "reused": reused})
			return true
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return true
		}
	}
	runID := parts[3]
	if len(parts) == 4 && r.Method == http.MethodGet {
		if !s.requireRole(w, r, principal, projectID, "deployment_run", runID, "owner", "admin", "developer", "viewer", "support") {
			return true
		}
		run, err := s.DeploymentRuns.Get(r.Context(), projectID, runID)
		writeRegistryResult(w, r, run, err, http.StatusOK)
		return true
	}
	if len(parts) != 5 {
		return false
	}
	action := parts[4]
	if action == "events" && r.Method == http.MethodGet {
		if !s.requireRole(w, r, principal, projectID, "deployment_run", runID, "owner", "admin", "developer", "viewer", "support") {
			return true
		}
		events, err := s.DeploymentRuns.Events(r.Context(), projectID, runID)
		writeRegistryResult(w, r, map[string]any{"events": events}, err, http.StatusOK)
		return true
	}
	if action == "result" && r.Method == http.MethodGet {
		if !s.requireRole(w, r, principal, projectID, "deployment_run", runID, "owner", "admin", "developer", "viewer", "support") {
			return true
		}
		result, err := s.deploymentRunResult(r.Context(), projectID, runID)
		writeRegistryResult(w, r, result, err, http.StatusOK)
		return true
	}
	if action == "plan" && r.Method == http.MethodPut {
		if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "deployment_run", runID, "owner", "admin", "developer") {
			return true
		}
		var request struct {
			ExpectedPlanHash string                  `json:"expected_plan_hash"`
			Plan             deploymentworkflow.Plan `json:"plan"`
		}
		if !decodeJSON(w, r, &request) {
			return true
		}
		current, currentErr := s.DeploymentRuns.Get(r.Context(), projectID, runID)
		if currentErr != nil {
			writeRegistryFailure(w, r, currentErr)
			return true
		}
		expectedRevision, revisionErr := deploymentRunIfMatch(r.Header.Get("If-Match"))
		if revisionErr != nil {
			writeRegistryFailure(w, r, deploymentworkflow.Error{Code: "DEPLOYMENT_RUN_IF_MATCH_REQUIRED", Status: http.StatusPreconditionRequired, Message: "If-Match must contain the exact deployment run revision.", NextAction: "Refresh the run and retry the edit."})
			return true
		}
		if expectedRevision != current.Revision {
			if replayHash, hashErr := deploymentworkflow.HashPlan(request.Plan); hashErr == nil && replayHash == current.Plan.Hash {
				writeJSON(w, http.StatusOK, current)
				return true
			}
			writeRegistryFailure(w, r, deploymentworkflow.Error{Code: "DEPLOYMENT_RUN_REVISION_STALE", Status: http.StatusConflict, Message: "The deployment run changed after it was loaded.", NextAction: "Refresh and review the latest run."})
			return true
		}
		run, err := s.DeploymentRuns.UpdatePlan(r.Context(), projectID, runID, principal.UserID, request.ExpectedPlanHash, request.Plan)
		writeRegistryResult(w, r, run, err, http.StatusOK)
		return true
	}
	if r.Method != http.MethodPost {
		return false
	}
	if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "deployment_run", runID, "owner", "admin", "developer") {
		return true
	}
	var run deploymentworkflow.Run
	var err error
	switch action {
	case "analyze":
		run, err = s.analyzeDeploymentRun(r.Context(), projectID, runID)
	case "approve":
		var request struct {
			PlanHash string `json:"plan_hash"`
		}
		if !decodeJSON(w, r, &request) {
			return true
		}
		approvedRun, getErr := s.DeploymentRuns.Get(r.Context(), projectID, runID)
		if getErr != nil {
			err = getErr
			break
		}
		if reason := s.workflowSecretStale(r.Context(), approvedRun); reason != "" {
			run, err = s.DeploymentRuns.Invalidate(r.Context(), projectID, runID, reason)
			break
		}
		current, currentErr := s.currentWorkflowAuthority(r.Context(), projectID, runID)
		if currentErr != nil {
			err = currentErr
		} else {
			run, err = s.DeploymentRuns.Approve(r.Context(), projectID, runID, principal.UserID, request.PlanHash, current)
		}
	case "acknowledge":
		var request struct {
			PreflightHash string `json:"preflight_hash"`
		}
		if !decodeJSON(w, r, &request) {
			return true
		}
		run, err = s.DeploymentRuns.Acknowledge(r.Context(), projectID, runID, principal.UserID, request.PreflightHash)
	case "retry":
		run, err = s.DeploymentRuns.Retry(r.Context(), projectID, runID, principal.UserID)
	case "cancel":
		current, currentErr := s.DeploymentRuns.Get(r.Context(), projectID, runID)
		if currentErr != nil {
			err = currentErr
		} else if cancelErr := s.cancelWorkflowJobs(r.Context(), projectID, current, r.Header.Get("X-Request-ID")); cancelErr != nil {
			err = cancelErr
		} else {
			run, err = s.DeploymentRuns.Cancel(r.Context(), projectID, runID, principal.UserID)
		}
	default:
		return false
	}
	writeRegistryResult(w, r, run, err, http.StatusOK)
	return true
}

func deploymentRunIfMatch(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "W/") {
		value = strings.TrimPrefix(value, "W/")
	}
	value = strings.Trim(value, `"`)
	if value == "" {
		return 0, fmt.Errorf("missing If-Match")
	}
	return strconv.ParseUint(value, 10, 64)
}

func (s *Server) cancelWorkflowJobs(ctx context.Context, projectID string, run deploymentworkflow.Run, requestID string) error {
	for _, jobID := range run.Refs.IDs(deploymentworkflow.AuthorityBuildJob) {
		found := false
		for _, applicationID := range run.Refs.IDs(deploymentworkflow.AuthorityApplication) {
			job, err := s.BuildJobs.Get(ctx, projectID, applicationID, jobID)
			if err != nil {
				if buildjob.Code(err) == "BUILD_JOB_NOT_FOUND" {
					continue
				}
				return err
			}
			found = true
			if job.Status != buildjob.StatusSucceeded && job.Status != buildjob.StatusFailed && job.Status != buildjob.StatusCancelled {
				if _, err := s.BuildJobs.Cancel(ctx, projectID, applicationID, jobID); err != nil {
					return err
				}
			}
			break
		}
		if !found {
			return fmt.Errorf("build job %s is outside the deployment authority", jobID)
		}
	}
	return s.cancelWorkflowDeployments(projectID, run, requestID)
}

func (s *Server) cancelWorkflowDeployments(projectID string, run deploymentworkflow.Run, requestID string) error {
	reader, ok := s.Registry.(immutableDeploymentReader)
	deploymentIDs := run.Refs.IDs(deploymentworkflow.AuthorityDeploymentJob)
	if !ok || len(deploymentIDs) == 0 {
		return nil
	}
	for _, deploymentID := range deploymentIDs {
		job, err := reader.GetDeployment(projectID, deploymentID)
		if err != nil {
			return err
		}
		if job.Status == "succeeded" || job.Status == "failed" || job.Status == "cancelled" {
			continue
		}
		if _, _, err := reader.CancelDeployment(projectID, deploymentID, workflowKey(run.ID, "cancel", deploymentID), requestID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) analyzeDeploymentRun(ctx context.Context, projectID, runID string) (deploymentworkflow.Run, error) {
	run, err := s.DeploymentRuns.Get(ctx, projectID, runID)
	if err != nil {
		return run, err
	}
	repository, err := s.workflowRepository(projectID, run.Plan.Source.RepositoryID)
	if err != nil {
		return run, err
	}
	if s.githubAppClient == nil || s.RepositoryAnalyzer.Repository == nil {
		return run, deploymentworkflow.Error{Code: "REPOSITORY_ANALYSIS_UNAVAILABLE", Status: 503, Message: "GitHub App repository analysis is unavailable.", NextAction: "Connect the project GitHub App installation."}
	}
	sha, err := s.githubAppClient.ResolveCommit(ctx, repository.InstallationID, repository.FullName, run.Plan.Source.SelectedRef)
	if err != nil {
		return run, err
	}
	analysis, err := s.RepositoryAnalyzer.Analyze(ctx, repositoryanalysis.Request{InstallationID: repository.InstallationID, RepositoryID: repository.RepositoryID, Repository: repository.FullName, SelectedRef: run.Plan.Source.SelectedRef, CommitSHA: sha})
	if err != nil {
		return run, err
	}
	target := workflowTarget(ctx, s.Registry, projectID, run.Plan.Target)
	if target.Exposure == "public" && target.Hostname == "" && s.Config.DeploymentDomain != "" {
		target.Hostname = workflowHostname(repository.FullName, projectID, s.Config.DeploymentDomain)
	}
	if target.EnvironmentID == "" || target.RuntimeID == "" {
		analysis.Issues = append(analysis.Issues, repositoryanalysis.Issue{Code: "TARGET_SERVER_REQUIRED", Message: "No Ready project server is available for this deployment.", Resolution: "Connect a server, then analyze again.", Blocking: true})
	}
	if target.Exposure == "public" && target.Hostname == "" {
		analysis.Issues = append(analysis.Issues, repositoryanalysis.Issue{Code: "PUBLIC_HOSTNAME_REQUIRED", Message: "No public deployment hostname is configured.", Resolution: "Enter a hostname in Review plan or configure OPSI_CLOUD_DEPLOYMENT_DOMAIN.", Blocking: true})
	}
	authority, err := s.workflowAuthority(ctx, projectID, repository, sha)
	if err != nil {
		return run, err
	}
	return s.DeploymentRuns.SetAnalysis(ctx, projectID, runID, analysis, authority, target)
}

func (s *Server) workflowRepository(projectID string, repositoryID int64) (registry.GitHubRepository, error) {
	repositories, err := s.Registry.ListGitHubRepositories(projectID)
	if err != nil {
		return registry.GitHubRepository{}, err
	}
	for _, repository := range repositories {
		if repository.RepositoryID == repositoryID && repository.ClaimStatus == registry.GitHubLinkActive && repository.Status == registry.GitHubRepositoryActive && !repository.Archived && !repository.Disabled {
			return repository, nil
		}
	}
	return registry.GitHubRepository{}, registry.APIError{Status: 404, Code: "GITHUB_REPOSITORY_NOT_FOUND", Message: "Repository is not actively claimed by this project."}
}

func (s *Server) workflowSecretStale(ctx context.Context, run deploymentworkflow.Run) string {
	hasExternal := false
	for _, secret := range run.Plan.Secrets {
		if !secret.Generated {
			hasExternal = true
			break
		}
	}
	if !hasExternal {
		return ""
	}
	services, err := s.Registry.ListServices(run.ProjectID)
	if err != nil {
		return "Workload-secret authority could not confirm the approved references."
	}
	serviceByName := map[string]string{}
	for _, service := range services {
		if service.Status != "deleted" {
			serviceByName[service.Name] = service.ID
		}
	}
	for _, secret := range run.Plan.Secrets {
		if secret.Generated {
			continue
		}
		if s.Resources.Credentials == nil {
			return "Workload-secret authority could not confirm the approved references."
		}
		scope := serviceByName[secret.ApplicationKey]
		if scope == "" {
			scope = "planned:" + secret.ApplicationKey
		}
		metadata, getErr := s.Resources.Credentials.GetWorkloadSecret(ctx, run.ProjectID, scope, secret.Name)
		if getErr != nil || metadata.Reference != secret.SecretRef || metadata.Revision != secret.Revision {
			return "An external workload secret changed after the draft was reviewed."
		}
	}
	return ""
}

func workflowTarget(ctx context.Context, authority registry.API, projectID string, target deploymentworkflow.Target) deploymentworkflow.Target {
	if target.Exposure == "" {
		target.Exposure = "public"
	}
	if target.CPUMilli == 0 {
		target.CPUMilli = 250
	}
	if target.MemoryBytes == 0 {
		target.MemoryBytes = 256 << 20
	}
	facts, err := authority.PlacementFacts(ctx, projectID)
	if err != nil {
		return target
	}
	if target.EnvironmentID == "" {
		for _, environment := range facts.Environments {
			if environment.Status == "active" {
				target.EnvironmentID = environment.ID
				break
			}
		}
	}
	if target.RuntimeID == "" {
		for _, runtime := range facts.Runtimes {
			if runtime.EnvironmentID == target.EnvironmentID && runtime.Status == "ready" {
				target.RuntimeID = runtime.ID
				break
			}
		}
	}
	if target.NodeID == "" {
		for _, node := range facts.Nodes {
			if node.RuntimeID == target.RuntimeID && node.Status == "healthy" {
				target.NodeID = node.ID
				break
			}
		}
	}
	return target
}

func workflowHostname(repository, projectID, domain string) string {
	_, name, found := strings.Cut(repository, "/")
	if !found {
		name = repository
	}
	digest := sha256.Sum256([]byte(projectID + "\x00" + repository))
	return fmt.Sprintf("%s-%x.%s", safeDNSLabel(name), digest[:3], domain)
}

func (s *Server) workflowAuthority(ctx context.Context, projectID string, repository registry.GitHubRepository, sha string) (deploymentworkflow.AuthorityRevisions, error) {
	value := deploymentworkflow.AuthorityRevisions{SourceCommitSHA: sha, RepositoryUpdatedAt: repository.UpdatedAt.UTC().Format(time.RFC3339Nano)}
	if plan, err := s.Topology.Get(ctx, projectID); err == nil {
		value.TopologyRevision = plan.Revision
		value.TopologyHash = plan.StateHash
	}
	resources, err := s.Resources.List(ctx, projectID, "")
	if err != nil {
		return value, err
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].ID < resources[j].ID })
	value.ResourceRevision = uint64(len(resources))
	value.ResourceHash, err = workflowAuthorityHash(resources)
	if err != nil {
		return value, err
	}
	policies, err := s.Policies.List(ctx, projectID)
	if err != nil {
		return value, err
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].ID < policies[j].ID })
	for _, policy := range policies {
		if policy.Revision > value.PolicyRevision {
			value.PolicyRevision = policy.Revision
		}
	}
	value.PolicyHash, err = workflowAuthorityHash(policies)
	if err != nil {
		return value, err
	}
	return value, nil
}

func workflowAuthorityHash(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest[:]), nil
}

func (s *Server) currentWorkflowAuthority(ctx context.Context, projectID, runID string) (deploymentworkflow.AuthorityRevisions, error) {
	run, err := s.DeploymentRuns.Get(ctx, projectID, runID)
	if err != nil {
		return deploymentworkflow.AuthorityRevisions{}, err
	}
	repository, err := s.workflowRepository(projectID, run.Plan.Source.RepositoryID)
	if err != nil {
		return deploymentworkflow.AuthorityRevisions{}, err
	}
	if s.githubAppClient == nil {
		return deploymentworkflow.AuthorityRevisions{}, deploymentworkflow.Error{Code: "REPOSITORY_ANALYSIS_UNAVAILABLE", Status: 503, Message: "GitHub App repository analysis is unavailable."}
	}
	sha, err := s.githubAppClient.ResolveCommit(ctx, repository.InstallationID, repository.FullName, run.Plan.Source.SelectedRef)
	if err != nil {
		return deploymentworkflow.AuthorityRevisions{}, err
	}
	return s.workflowAuthority(ctx, projectID, repository, sha)
}
