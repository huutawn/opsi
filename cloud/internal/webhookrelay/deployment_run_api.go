package webhookrelay

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
	"github.com/opsi-dev/opsi/cloud/internal/deploymentworkflow"
	"github.com/opsi-dev/opsi/cloud/internal/publichostname"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/repositoryanalysis"
	"github.com/opsi-dev/opsi/cloud/internal/repositoryexport"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

func (s *Server) handleDeploymentRunAPI(w http.ResponseWriter, r *http.Request, projectID string, parts []string, principal auth.VerifyResult) bool {
	if len(parts) >= 3 && parts[2] == "repository-export" {
		return s.handleRepositoryExportAPI(w, r, projectID, parts, principal)
	}
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
			if err := s.canonicalizeNewDeploymentTarget(&request.Target); err != nil {
				writeRegistryFailure(w, r, err)
				return true
			}
			var reserved publichostname.Allocation
			allocationReused := true
			if request.Target.Exposure == "public" && request.Target.Hostname != "" {
				reserved, allocationReused, err = s.PublicHostnames.Reserve(r.Context(), publichostname.ReserveRequest{Hostname: request.Target.Hostname, OwnerUserID: principal.UserID, ProjectID: projectID, EnvironmentID: request.Target.EnvironmentID, RuntimeID: request.Target.RuntimeID})
				if err = publicHostnameError(err); err != nil {
					writeRegistryFailure(w, r, err)
					return true
				}
			}
			run, reused, err := s.DeploymentRuns.Create(r.Context(), projectID, principal.UserID, r.Header.Get("Idempotency-Key"), source, request.Target)
			if err != nil {
				if reserved.ID != "" && !allocationReused {
					_, _ = s.PublicHostnames.Released(r.Context(), reserved.ID)
				}
				writeRegistryFailure(w, r, err)
				return true
			}
			if reused && reserved.ID != "" && !allocationReused && run.Plan.Target.Hostname != request.Target.Hostname {
				_, _ = s.PublicHostnames.Released(r.Context(), reserved.ID)
			}
			if !reused {
				if analyzed, analysisErr := s.analyzeDeploymentRun(r.Context(), projectID, run.ID, nil); analysisErr == nil {
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
	if action == "resource-recommendation" && r.Method == http.MethodGet {
		if !s.requireRole(w, r, principal, projectID, "deployment_run", runID, "owner", "admin", "developer", "viewer", "support") {
			return true
		}
		engine := s.recommendationEngine()
		recommendation, err := engine.Recommend(r.Context(), projectID, runID)
		writeRegistryResult(w, r, map[string]any{"recommendation": recommendation}, err, http.StatusOK)
		return true
	}
	if action == "plan" && r.Method == http.MethodPut {
		if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "deployment_run", runID, "owner", "admin", "developer") {
			return true
		}
		var request struct {
			ExpectedPlanHash          string                  `json:"expected_plan_hash"`
			ExpectedResourceBasisHash string                  `json:"expected_resource_basis_hash"`
			Plan                      deploymentworkflow.Plan `json:"plan"`
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
		if err := s.canonicalizeUpdatedPlan(&request.Plan, current.Plan); err != nil {
			writeRegistryFailure(w, r, err)
			return true
		}
		var draftAllocation publichostname.Allocation
		draftAllocationReused := true
		if request.Plan.Target.Exposure == "public" && request.Plan.Target.Hostname != "" {
			var reserveErr error
			draftAllocation, draftAllocationReused, reserveErr = s.PublicHostnames.Reserve(r.Context(), publichostname.ReserveRequest{Hostname: request.Plan.Target.Hostname, OwnerUserID: principal.UserID, ProjectID: projectID, EnvironmentID: request.Plan.Target.EnvironmentID, RuntimeID: request.Plan.Target.RuntimeID})
			if reserveErr = publicHostnameError(reserveErr); reserveErr != nil {
				writeRegistryFailure(w, r, reserveErr)
				return true
			}
		}
		engine := s.recommendationEngine()
		run, err := s.DeploymentRuns.UpdatePlanWithBasis(r.Context(), projectID, runID, principal.UserID, request.ExpectedPlanHash, request.ExpectedResourceBasisHash, request.Plan, &engine)
		if err != nil && draftAllocation.ID != "" && !draftAllocationReused {
			_, _ = s.PublicHostnames.Released(r.Context(), draftAllocation.ID)
		}
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
		var request struct {
			Scope *repositoryanalysis.Scope `json:"scope"`
		}
		if !decodeJSON(w, r, &request) {
			return true
		}
		run, err = s.analyzeDeploymentRun(r.Context(), projectID, runID, request.Scope)
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

func (s *Server) recommendationEngine() deploymentworkflow.RecommendationEngine {
	return deploymentworkflow.RecommendationEngine{
		Store:          s.DeploymentRuns.Store,
		Topology:       s.Topology,
		Facts:          s.Registry,
		Resources:      s.Resources,
		Now:            s.now,
		HeartbeatTTL:   s.Topology.HeartbeatTTL,
		ReservedCPU:    s.Topology.ReservedCPU,
		ReservedMemory: s.Topology.ReservedMemory,
	}
}

type repositoryExportPreviewResponse struct {
	repositoryexport.Preview
	ExportEnabled  bool   `json:"export_enabled"`
	DisabledReason string `json:"disabled_reason,omitempty"`
}

func (s *Server) handleRepositoryExportAPI(w http.ResponseWriter, r *http.Request, projectID string, parts []string, principal auth.VerifyResult) bool {
	previewRoute := len(parts) == 4 && parts[3] == "preview"
	if previewRoute && r.Method == http.MethodPost {
		if !s.requireRole(w, r, principal, projectID, "repository_export", projectID, "owner", "admin", "developer", "viewer") {
			return true
		}
		var request struct {
			RunID        string `json:"run_id"`
			TargetBranch string `json:"target_branch,omitempty"`
		}
		if !decodeJSON(w, r, &request) {
			return true
		}
		preview, repository, err := s.repositoryExportPreview(r.Context(), projectID, request.RunID, request.TargetBranch)
		if err != nil {
			writeRegistryFailure(w, r, err)
			return true
		}
		response := repositoryExportPreviewResponse{Preview: preview, ExportEnabled: true}
		if s.githubAppClient == nil {
			response.ExportEnabled = false
			response.DisabledReason = "Connect the project GitHub App installation to export configuration."
		} else if _, _, permissionErr := s.githubAppClient.RepositoryWriteToken(r.Context(), repository.InstallationID, repository.RepositoryID); permissionErr != nil {
			response.ExportEnabled = false
			response.DisabledReason = "Approve Contents: write and Pull requests: write for this GitHub App installation. Deployments remain available without these permissions."
		}
		writeJSON(w, http.StatusOK, response)
		return true
	}
	if len(parts) == 3 && r.Method == http.MethodPost {
		if !requireWriteHeaders(w, r) || !s.requireRole(w, r, principal, projectID, "repository_export", projectID, "owner", "admin", "developer") {
			return true
		}
		var request struct {
			RunID        string `json:"run_id"`
			RunRevision  uint64 `json:"run_revision"`
			PlanHash     string `json:"plan_hash"`
			PreviewHash  string `json:"preview_hash"`
			TargetBranch string `json:"target_branch,omitempty"`
		}
		if !decodeJSON(w, r, &request) {
			return true
		}
		preview, repository, err := s.repositoryExportPreview(r.Context(), projectID, request.RunID, request.TargetBranch)
		if err != nil {
			writeRegistryFailure(w, r, err)
			return true
		}
		if request.RunRevision != preview.RunRevision || request.PlanHash != preview.PlanHash || request.PreviewHash != preview.PreviewHash {
			writeRegistryFailure(w, r, deploymentworkflow.Error{Code: "REPOSITORY_EXPORT_PREVIEW_STALE", Status: http.StatusConflict, Message: "Repository export no longer matches the exact deployment run and preview.", NextAction: "Refresh the export preview and review the diff again."})
			return true
		}
		if s.githubAppClient == nil {
			writeRegistryFailure(w, r, deploymentworkflow.Error{Code: "REPOSITORY_EXPORT_UNAVAILABLE", Status: http.StatusServiceUnavailable, Message: "GitHub App repository export is unavailable."})
			return true
		}
		branch := "opsi/export-" + safeExportPart(request.RunID, 12) + "-" + preview.PreviewHash[:12]
		exported, exportErr := s.githubAppClient.ExportRepositoryConfig(r.Context(), repository.InstallationID, repository.RepositoryID, repository.FullName, strings.ToLower(preview.SourceSHA), preview.TargetBranch, branch, []byte(preview.YAML))
		if errors.Is(exportErr, errGitHubRepositoryWriteDenied) {
			exportErr = deploymentworkflow.Error{Code: "REPOSITORY_EXPORT_PERMISSION_UPGRADE_REQUIRED", Status: http.StatusConflict, Message: "This GitHub App installation has not approved Contents: write and Pull requests: write.", NextAction: "Approve the requested GitHub App permissions, then retry Export configuration. Deployments are unaffected."}
		}
		if exportErr != nil {
			writeRegistryFailure(w, r, exportErr)
			return true
		}
		s.Registry.Audit(principal.OrgID, projectID, principal.UserID, "REPOSITORY_CONFIGURATION_EXPORTED", "repository", strconv.FormatInt(repository.RepositoryID, 10), "success", map[string]any{"run_id": request.RunID, "run_revision": request.RunRevision, "plan_hash": request.PlanHash, "preview_hash": request.PreviewHash, "branch": exported.Branch, "pull_request_number": exported.PullRequestNumber, "reused": exported.Reused})
		writeJSON(w, http.StatusCreated, map[string]any{"repository_export": exported})
		return true
	}
	return false
}

func (s *Server) repositoryExportPreview(ctx context.Context, projectID, runID, targetBranch string) (repositoryexport.Preview, registry.GitHubRepository, error) {
	run, err := s.DeploymentRuns.Get(ctx, projectID, runID)
	if err != nil {
		return repositoryexport.Preview{}, registry.GitHubRepository{}, err
	}
	repository, err := s.workflowRepository(projectID, run.Plan.Source.RepositoryID)
	if err != nil {
		return repositoryexport.Preview{}, registry.GitHubRepository{}, err
	}
	if repository.FullName != run.Plan.Source.Repository || repository.InstallationID != run.Plan.Source.InstallationID {
		return repositoryexport.Preview{}, registry.GitHubRepository{}, deploymentworkflow.Error{Code: "REPOSITORY_EXPORT_SOURCE_MISMATCH", Status: http.StatusNotFound, Message: "Deployment run repository is not owned by this project."}
	}
	if targetBranch == "" {
		targetBranch = repository.DefaultBranch
	}
	if targetBranch == "" || strings.TrimSpace(targetBranch) != targetBranch || strings.IndexAny(targetBranch, " \r\n\x00") >= 0 {
		return repositoryexport.Preview{}, registry.GitHubRepository{}, deploymentworkflow.Error{Code: "REPOSITORY_EXPORT_BRANCH_INVALID", Status: http.StatusBadRequest, Message: "Repository export target branch is invalid."}
	}
	if s.githubAppClient == nil {
		return repositoryexport.Preview{}, registry.GitHubRepository{}, deploymentworkflow.Error{Code: "REPOSITORY_EXPORT_UNAVAILABLE", Status: http.StatusServiceUnavailable, Message: "GitHub App repository export is unavailable."}
	}
	current := []byte(nil)
	exists, existsErr := s.githubAppClient.RepositoryFileExists(ctx, repository.InstallationID, repository.FullName, run.Plan.Source.CommitSHA, repositoryexport.Path)
	if existsErr != nil {
		return repositoryexport.Preview{}, registry.GitHubRepository{}, existsErr
	}
	if exists {
		current, err = s.githubAppClient.ReadFile(ctx, repository.InstallationID, repository.FullName, run.Plan.Source.CommitSHA, repositoryexport.Path, 512<<10)
		if err != nil {
			return repositoryexport.Preview{}, registry.GitHubRepository{}, err
		}
	}
	preview, err := repositoryexport.NewPreview(run, targetBranch, current)
	if err != nil {
		return repositoryexport.Preview{}, registry.GitHubRepository{}, deploymentworkflow.Error{Code: "REPOSITORY_EXPORT_PLAN_INVALID", Status: http.StatusConflict, Message: err.Error(), NextAction: "Review and save a valid deployment plan before exporting."}
	}
	return preview, repository, nil
}

func safeExportPart(value string, limit int) string {
	var b strings.Builder
	for _, character := range strings.ToLower(value) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			b.WriteRune(character)
		}
	}
	result := b.String()
	if len(result) > limit {
		return result[:limit]
	}
	if result == "" {
		return "run"
	}
	return result
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

func (s *Server) analyzeDeploymentRun(ctx context.Context, projectID, runID string, requestedScope *repositoryanalysis.Scope) (deploymentworkflow.Run, error) {
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
	sha := run.Plan.Source.CommitSHA
	if requestedScope == nil || len(sha) != 40 {
		sha, err = s.githubAppClient.ResolveCommit(ctx, repository.InstallationID, repository.FullName, run.Plan.Source.SelectedRef)
		if err != nil {
			return run, err
		}
	}
	scope := run.Analysis.Scope
	if requestedScope != nil {
		scope = *requestedScope
	}
	analysis, err := s.RepositoryAnalyzer.Analyze(ctx, repositoryanalysis.Request{InstallationID: repository.InstallationID, RepositoryID: repository.RepositoryID, Repository: repository.FullName, SelectedRef: run.Plan.Source.SelectedRef, CommitSHA: sha, Scope: scope})
	if err != nil {
		return run, err
	}
	s.recordConnectionAnalysisMetrics(analysis)
	target := workflowTarget(ctx, s.Registry, projectID, run.Plan.Target)
	if target.EnvironmentID == "" || target.RuntimeID == "" {
		analysis.Issues = append(analysis.Issues, repositoryanalysis.Issue{Code: "TARGET_SERVER_REQUIRED", Message: "No Ready project server is available for this deployment.", Resolution: "Connect a server, then analyze again.", Blocking: true})
	}
	if target.Exposure == "public" && target.Hostname == "" {
		analysis.Issues = append(analysis.Issues, repositoryanalysis.Issue{Code: "PUBLIC_HOSTNAME_REQUIRED", Message: "No public subdomain is configured.", Resolution: "Enter one public subdomain label in the deployment form.", Blocking: true})
	}
	if err := s.applyAutomaticPublicRoutes(&analysis, target); err != nil {
		analysis.Issues = append(analysis.Issues, repositoryanalysis.Issue{Code: "AUTO_PUBLIC_ROUTE_INVALID", Message: "Automatic public routes could not be generated.", Resolution: err.Error(), Blocking: true})
	}
	authority, err := s.workflowAuthority(ctx, projectID, repository, sha)
	if err != nil {
		return run, err
	}
	return s.DeploymentRuns.SetAnalysis(ctx, projectID, runID, analysis, authority, target)
}

func (s *Server) recordConnectionAnalysisMetrics(analysis repositoryanalysis.Result) {
	for _, dependency := range analysis.Dependencies {
		for _, injection := range dependency.Injections {
			if strings.HasPrefix(injection.SymbolicSource, "connection.") {
				s.observer.Inc("connection_dialect_" + metricSegment(dependency.Protocol+"_"+injection.SymbolicSource) + "_total")
			}
		}
	}
	for _, issue := range analysis.Issues {
		if issue.Code == "CONNECTION_DIALECT_REQUIRED" {
			s.observer.Inc("connection_dialect_ambiguity_total")
		}
	}
}

func metricSegment(value string) string {
	var result strings.Builder
	separator := false
	for _, character := range strings.ToLower(value) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			result.WriteRune(character)
			separator = false
		} else if result.Len() > 0 && !separator {
			result.WriteByte('_')
			separator = true
		}
	}
	return strings.Trim(result.String(), "_")
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
	if target.Exposure == "public" && target.PublicRoutes == "" {
		target.PublicRoutes = deploymentworkflow.PublicRoutesAutomatic
	}
	if target.CPUMilli == 0 {
		target.CPUMilli = 100
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
	value.ResourceRevision = uint64(len(resources))
	value.ResourceHash, err = workflowResourceAuthorityHash(resources)
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

// workflowResourceAuthorityHash deliberately excludes reconciler-owned
// lifecycle, runtime evidence, and timestamps. Those fields change while an
// otherwise unchanged resource becomes Ready and must not invalidate a plan
// before its owner can approve it. Desired resource identity and specification
// remain part of the authority snapshot, so externally changing either still
// invalidates the reviewed plan.
func workflowResourceAuthorityHash(resources []resourcev1.Resource) (string, error) {
	type desiredResource struct {
		ID            string                   `json:"id"`
		ProjectID     string                   `json:"project_id"`
		EnvironmentID string                   `json:"environment_id"`
		Name          string                   `json:"name"`
		Kind          resourcev1.Kind          `json:"kind"`
		Provider      string                   `json:"provider"`
		Type          resourcev1.Type          `json:"type"`
		Managed       *resourcev1.ManagedSpec  `json:"managed,omitempty"`
		External      *resourcev1.ExternalSpec `json:"external,omitempty"`
		InternalName  string                   `json:"internal_name,omitempty"`
	}
	values := make([]desiredResource, 0, len(resources))
	for _, resource := range resources {
		values = append(values, desiredResource{
			ID: resource.ID, ProjectID: resource.ProjectID, EnvironmentID: resource.EnvironmentID,
			Name: resource.Name, Kind: resource.Kind, Provider: resource.Provider, Type: resource.Type,
			Managed: resource.Managed, External: resource.External, InternalName: resource.InternalName,
		})
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return workflowAuthorityHash(values)
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
