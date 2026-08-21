package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	"github.com/opsi-dev/opsi/cli/internal/repository"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

type ToolHandler func(ctx context.Context, s *Server, args map[string]any) (any, error)

func (s *Server) registerHandlers() map[string]ToolHandler {
	return map[string]ToolHandler{
		"project_context":                  s.handleProjectContext,
		"topology":                         s.handleTopology,
		"applications_list":                s.handleApplicationsList,
		"application_get":                  s.handleApplicationGet,
		"application_dependencies":         s.handleApplicationDependencies,
		"managed_resources_list":           s.handleManagedResourcesList,
		"managed_resource_get":             s.handleManagedResourceGet,
		"build_records_list":               s.handleBuildRecordsList,
		"build_record_get":                 s.handleBuildRecordGet,
		"deployments_list":                 s.handleDeploymentsList,
		"deployment_get":                   s.handleDeploymentGet,
		"deployment_preflight":             s.handleDeploymentPreflight,
		"source_risk_report":               s.handleSourceRiskReport,
		"dependency_verification_latest":   s.handleDependencyVerificationLatest,
		"dependency_verification_history":  s.handleDependencyVerificationHistory,
		"source_files_list":                s.handleSourceFilesList,
		"source_file_read":                 s.handleSourceFileRead,
		"source_search":                    s.handleSourceSearch,
	}
}

func getIntArg(args map[string]any, key string, defaultVal, maxVal int) int {
	if args == nil {
		return defaultVal
	}
	val, ok := args[key]
	if !ok || val == nil {
		return defaultVal
	}
	var num int
	switch v := val.(type) {
	case float64:
		num = int(v)
	case int:
		num = v
	case int64:
		num = int(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			num = int(n)
		} else {
			return defaultVal
		}
	default:
		return defaultVal
	}
	if num <= 0 {
		return defaultVal
	}
	if maxVal > 0 && num > maxVal {
		return maxVal
	}
	return num
}

func (s *Server) resolveProjectID(ctx context.Context, client *cloudclient.Client, args map[string]any) (string, error) {
	if p, ok := args["project_id"].(string); ok && strings.TrimSpace(p) != "" {
		return strings.TrimSpace(p), nil
	}
	if strings.TrimSpace(s.DefaultProjectID) != "" {
		return strings.TrimSpace(s.DefaultProjectID), nil
	}
	// Attempt to resolve verified session identity via VerifyPAT
	authInfo, err := client.VerifyPAT(ctx, "")
	if err == nil {
		if projectID, ok := authInfo["project_id"].(string); ok && strings.TrimSpace(projectID) != "" {
			return strings.TrimSpace(projectID), nil
		}
		if orgID, ok := authInfo["org_id"].(string); ok && strings.TrimSpace(orgID) != "" {
			projects, err := client.ListProjects(ctx, orgID)
			if err == nil {
				if len(projects) == 1 {
					return projects[0].ID, nil
				}
				if len(projects) > 1 {
					return "", &DomainError{Code: ErrCodeAmbiguousProject, Message: "multiple projects available in organization; specify project_id in tool arguments"}
				}
			}
		}
	}
	return "", &DomainError{Code: ErrCodeAmbiguousProject, Message: "project context is ambiguous; specify project_id in tool arguments"}
}

func (s *Server) handleProjectContext(ctx context.Context, _ *Server, args map[string]any) (any, error) {
	client, err := s.getCloudClient(ctx)
	if err != nil {
		return nil, err
	}
	projectID, err := s.resolveProjectID(ctx, client, args)
	if err != nil {
		return nil, err
	}

	authInfo, err := client.VerifyPAT(ctx, projectID)
	if err != nil {
		return nil, mapAPIError(err)
	}

	orgID, _ := authInfo["org_id"].(string)
	projectName := projectID
	projectSlug := projectID
	projectStatus := "active"

	if orgID != "" {
		projects, pErr := client.ListProjects(ctx, orgID)
		if pErr == nil {
			for _, p := range projects {
				if p.ID == projectID {
					projectName = p.Name
					projectSlug = p.Slug
					projectStatus = p.Status
					break
				}
			}
		}
	}

	services, _ := client.ListServices(ctx, projectID)
	nodes, _ := client.ListNodes(ctx, projectID)
	resources, _ := client.ListResources(ctx, projectID, "")
	topology, _ := client.GetTopology(ctx, projectID)
	deployments, _ := client.ListDeployments(ctx, projectID)

	depSummary := DeploymentSummary{TotalDeployments: len(deployments)}
	if len(deployments) > 0 {
		latest := deployments[0]
		depSummary.LatestStatus = latest.Status
		depSummary.LatestServiceID = latest.ServiceID
		depSummary.LatestDeployedAt = latest.FinishedAt
		if depSummary.LatestDeployedAt == nil {
			depSummary.LatestDeployedAt = latest.StartedAt
		}
	}

	envFilter, _ := args["environment_id"].(string)

	return ProjectContextResult{
		ProjectID:            projectID,
		OrgID:                orgID,
		Name:                 projectName,
		Slug:                 projectSlug,
		Status:               projectStatus,
		Environment:          envFilter,
		ApplicationCount:     len(services),
		NodeCount:            len(nodes),
		ManagedResourceCount: len(resources),
		TopologyRevision:     topology.Revision,
		TopologyStateHash:    topology.StateHash,
		DeploymentSummary:    depSummary,
	}, nil
}

func (s *Server) handleTopology(ctx context.Context, _ *Server, args map[string]any) (any, error) {
	client, err := s.getCloudClient(ctx)
	if err != nil {
		return nil, err
	}
	projectID, err := s.resolveProjectID(ctx, client, args)
	if err != nil {
		return nil, err
	}
	plan, err := client.GetTopology(ctx, projectID)
	if err != nil {
		return nil, mapAPIError(err)
	}
	return plan, nil
}

func (s *Server) handleApplicationsList(ctx context.Context, _ *Server, args map[string]any) (any, error) {
	client, err := s.getCloudClient(ctx)
	if err != nil {
		return nil, err
	}
	projectID, err := s.resolveProjectID(ctx, client, args)
	if err != nil {
		return nil, err
	}
	services, err := client.ListServices(ctx, projectID)
	if err != nil {
		return nil, mapAPIError(err)
	}
	bindings, _ := client.ListGitHubBindings(ctx, projectID)
	bindingMap := make(map[string]cloudclient.GitHubBinding)
	for _, b := range bindings {
		bindingMap[b.ServiceID] = b
		if b.ServiceKey != "" {
			bindingMap[b.ServiceKey] = b
		}
	}

	topology, _ := client.GetTopology(ctx, projectID)
	placementMap := make(map[string]string)
	for _, a := range topology.Assignments {
		placementMap[a.ServiceKey] = a.RuntimeID
	}

	deployments, _ := client.ListDeployments(ctx, projectID)
	latestDepMap := make(map[string]cloudclient.DeploymentJob)
	for _, d := range deployments {
		if _, exists := latestDepMap[d.ServiceID]; !exists {
			latestDepMap[d.ServiceID] = d
		}
	}

	limit := getIntArg(args, "limit", DefaultFileListLimit, 100)

	results := make([]ApplicationSummary, 0, len(services))
	for _, svc := range services {
		var bindingDoc *SourceBinding
		if b, ok := bindingMap[svc.ID]; ok {
			bindingDoc = &SourceBinding{
				RepositoryID:    b.RepositoryID,
				ServiceKey:      b.ServiceKey,
				SelectedRef:     b.SelectedRef,
				ApplicationRoot: b.ApplicationRoot,
				BuildContext:    b.BuildContext,
				BuildStrategy:   b.BuildStrategy,
				DockerfilePath:  b.DockerfilePath,
			}
		} else if b, ok := bindingMap[svc.Name]; ok {
			bindingDoc = &SourceBinding{
				RepositoryID:    b.RepositoryID,
				ServiceKey:      b.ServiceKey,
				SelectedRef:     b.SelectedRef,
				ApplicationRoot: b.ApplicationRoot,
				BuildContext:    b.BuildContext,
				BuildStrategy:   b.BuildStrategy,
				DockerfilePath:  b.DockerfilePath,
			}
		}

		runtimeID := placementMap[svc.ID]
		var latestDepID, latestDepStatus string
		if d, ok := latestDepMap[svc.ID]; ok {
			latestDepID = d.ID
			latestDepStatus = d.Status
		}

		summary := ApplicationSummary{
			ID:                  svc.ID,
			Name:                svc.Name,
			Status:              svc.Status,
			SourceBinding:       bindingDoc,
			PlacementRuntimeID:  runtimeID,
			LatestDeploymentID:  latestDepID,
			LatestDeployStatus:  latestDepStatus,
		}

		// Count dependencies if configuration exists
		cfg, cfgErr := client.GetServiceConfiguration(ctx, projectID, svc.ID)
		if cfgErr == nil {
			summary.DependencyCount = len(cfg.Dependencies)
		}

		results = append(results, summary)
		if len(results) >= limit {
			break
		}
	}

	return map[string]any{"applications": results}, nil
}

func (s *Server) handleApplicationGet(ctx context.Context, _ *Server, args map[string]any) (any, error) {
	client, err := s.getCloudClient(ctx)
	if err != nil {
		return nil, err
	}
	projectID, err := s.resolveProjectID(ctx, client, args)
	if err != nil {
		return nil, err
	}
	appID, _ := args["application_id"].(string)
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return nil, &DomainError{Code: ErrCodeInvalidArgument, Message: "application_id is required"}
	}

	services, err := client.ListServices(ctx, projectID)
	if err != nil {
		return nil, mapAPIError(err)
	}

	var targetSvc *cloudclient.Service
	for i := range services {
		if services[i].ID == appID || services[i].Name == appID {
			targetSvc = &services[i]
			break
		}
	}
	if targetSvc == nil {
		return nil, &DomainError{Code: ErrCodeNotFound, Message: fmt.Sprintf("application %q not found in project %s", appID, projectID)}
	}

	cfg, _ := client.GetServiceConfiguration(ctx, projectID, targetSvc.ID)
	bindings, _ := client.ListGitHubBindings(ctx, projectID)
	var bindingDoc *SourceBinding
	for _, b := range bindings {
		if b.ServiceID == targetSvc.ID || b.ServiceKey == targetSvc.Name {
			bindingDoc = &SourceBinding{
				RepositoryID:    b.RepositoryID,
				ServiceKey:      b.ServiceKey,
				SelectedRef:     b.SelectedRef,
				ApplicationRoot: b.ApplicationRoot,
				BuildContext:    b.BuildContext,
				BuildStrategy:   b.BuildStrategy,
				DockerfilePath:  b.DockerfilePath,
			}
			break
		}
	}

	// Resolve latest BuildRecord
	records, _ := client.ListBuildRecords(ctx, projectID, url.Values{"service_key": {targetSvc.Name}, "limit": {"1"}})
	var currentBuildRecordID, exactCommitSHA string
	if len(records.Records) > 0 {
		currentBuildRecordID = records.Records[0].ID
		exactCommitSHA = records.Records[0].Workload.SHA
	}

	// Safe environment variable key names only
	safeEnvKeys := make([]string, 0, len(cfg.Environment))
	for _, env := range cfg.Environment {
		safeEnvKeys = append(safeEnvKeys, env.Name)
	}
	sort.Strings(safeEnvKeys)

	// Public route summary
	var publicRoute *PublicRouteSummary
	if cfg.PublicRoute != nil {
		publicRoute = &PublicRouteSummary{
			Hostname: cfg.PublicRoute.Hostname,
			Path:     cfg.PublicRoute.Path,
		}
	}

	// Topology placement
	topology, _ := client.GetTopology(ctx, projectID)
	runtimeID := ""
	for _, a := range topology.Assignments {
		if a.ServiceKey == targetSvc.Name || a.ServiceKey == targetSvc.ID {
			runtimeID = a.RuntimeID
			break
		}
	}

	// Dependencies summary
	depDocs := s.buildDependencyDocs(ctx, client, projectID, targetSvc.ID, cfg)

	return ApplicationDetailResult{
		ID:                       targetSvc.ID,
		Name:                     targetSvc.Name,
		Status:                   targetSvc.Status,
		SourceBinding:            bindingDoc,
		ExactCommitSHA:           exactCommitSHA,
		ServiceConfigRevision:    cfg.Revision,
		ServiceConfigStateHash:   cfg.StateHash,
		EnvironmentVariablesSafe: safeEnvKeys,
		PublicRoute:              publicRoute,
		CurrentBuildRecordID:     currentBuildRecordID,
		PlacementRuntimeID:       runtimeID,
		DependenciesSummary:      depDocs,
	}, nil
}

func (s *Server) handleApplicationDependencies(ctx context.Context, _ *Server, args map[string]any) (any, error) {
	client, err := s.getCloudClient(ctx)
	if err != nil {
		return nil, err
	}
	projectID, err := s.resolveProjectID(ctx, client, args)
	if err != nil {
		return nil, err
	}
	appID, _ := args["application_id"].(string)
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return nil, &DomainError{Code: ErrCodeInvalidArgument, Message: "application_id is required"}
	}

	services, err := client.ListServices(ctx, projectID)
	if err != nil {
		return nil, mapAPIError(err)
	}
	targetID := appID
	for _, svc := range services {
		if svc.ID == appID || svc.Name == appID {
			targetID = svc.ID
			break
		}
	}

	cfg, err := client.GetServiceConfiguration(ctx, projectID, targetID)
	if err != nil {
		return nil, mapAPIError(err)
	}

	depDocs := s.buildDependencyDocs(ctx, client, projectID, targetID, cfg)
	return map[string]any{"dependencies": depDocs}, nil
}

func (s *Server) buildDependencyDocs(ctx context.Context, client *cloudclient.Client, projectID, appID string, cfg cloudclient.ServiceConfiguration) []ApplicationDependencyDoc {
	resourceBindings, _ := client.ListResourceBindings(ctx, projectID, "")
	rbMap := make(map[string]cloudclient.ManagedResourceBinding)
	for _, rb := range resourceBindings {
		rbMap[rb.ID] = rb
	}

	docs := make([]ApplicationDependencyDoc, 0, len(cfg.Dependencies))
	for _, dep := range cfg.Dependencies {
		var mappings []DependencyInjectionMapping
		for _, m := range dep.InjectionMappings {
			mappings = append(mappings, DependencyInjectionMapping{
				EnvName:        m.EnvName,
				SymbolicSource: m.SymbolicSource,
			})
		}
		var verContract *DependencyVerificationDoc
		if dep.VerificationContract != nil {
			verContract = &DependencyVerificationDoc{
				Type:           dep.VerificationContract.Type,
				Path:           dep.VerificationContract.Path,
				ExpectedStatus: dep.VerificationContract.ExpectedStatus,
			}
		}

		// Check realization
		realized := false
		var rbID, rbStatus string
		for _, configuredRB := range cfg.ResourceBindings {
			if configuredRB.LogicalName == dep.LogicalName {
				rbID = configuredRB.BindingID
				realized = true
				if actualRB, ok := rbMap[configuredRB.BindingID]; ok {
					rbStatus = string(actualRB.Lifecycle)
				}
				break
			}
		}

		// Check latest verification status
		var latestVerStatus string
		verRun, verErr := client.GetDependencyVerification(ctx, projectID, dep.LogicalName, "", appID)
		if verErr == nil && verRun.ID != "" {
			latestVerStatus = verRun.OverallStatus
		}

		docs = append(docs, ApplicationDependencyDoc{
			LogicalName:              dep.LogicalName,
			TargetKind:               dep.TargetKind,
			TargetIdentity:           dep.TargetIdentity,
			Protocol:                 dep.Protocol,
			Strategy:                 dep.Strategy,
			AccessContext:            dep.AccessContext,
			Path:                     dep.Path,
			Required:                 dep.Required,
			InjectionPhase:           dep.InjectionPhase,
			SymbolicMappings:         mappings,
			VerificationContract:     verContract,
			Realized:                 realized,
			ResourceBindingID:        rbID,
			ResourceBindingStatus:    rbStatus,
			LatestVerificationStatus: latestVerStatus,
		})
	}
	return docs
}

func (s *Server) handleManagedResourcesList(ctx context.Context, _ *Server, args map[string]any) (any, error) {
	client, err := s.getCloudClient(ctx)
	if err != nil {
		return nil, err
	}
	projectID, err := s.resolveProjectID(ctx, client, args)
	if err != nil {
		return nil, err
	}
	envID, _ := args["environment_id"].(string)
	resources, err := client.ListResources(ctx, projectID, envID)
	if err != nil {
		return nil, mapAPIError(err)
	}

	limit := getIntArg(args, "limit", DefaultFileListLimit, 100)

	allBindings, _ := client.ListResourceBindings(ctx, projectID, envID)
	bindingCountMap := make(map[string]int)
	for _, b := range allBindings {
		bindingCountMap[b.Target.ID]++
		bindingCountMap[b.LogicalName]++
	}

	results := make([]ManagedResourceSummary, 0, len(resources))
	for _, res := range resources {
		runtimeID := ""
		if res.Runtime != nil {
			runtimeID = res.Runtime.Spec.Assignment.RuntimeID
		}
		var replicas int32
		var cpuMillicores, memoryBytes int64
		version := ""
		if res.Managed != nil {
			replicas = res.Managed.Replicas
			cpuMillicores = res.Managed.CPUMillicores
			memoryBytes = res.Managed.MemoryBytes
			version = res.Managed.Version
		}

		bCount := bindingCountMap[res.ID]
		if bCount == 0 {
			bCount = bindingCountMap[res.Name]
		}

		results = append(results, ManagedResourceSummary{
			ID:            res.ID,
			Name:          res.Name,
			Kind:          string(res.Kind),
			Type:          string(res.Type),
			Version:       version,
			Lifecycle:     string(res.Lifecycle),
			EnvironmentID: res.EnvironmentID,
			RuntimeID:     runtimeID,
			Replicas:      replicas,
			CPUMillicores: cpuMillicores,
			MemoryBytes:   memoryBytes,
			BindingCount:  bCount,
		})
		if len(results) >= limit {
			break
		}
	}
	return map[string]any{"resources": results}, nil
}

func (s *Server) handleManagedResourceGet(ctx context.Context, _ *Server, args map[string]any) (any, error) {
	client, err := s.getCloudClient(ctx)
	if err != nil {
		return nil, err
	}
	projectID, err := s.resolveProjectID(ctx, client, args)
	if err != nil {
		return nil, err
	}
	resourceID, _ := args["resource_id"].(string)
	resourceID = strings.TrimSpace(resourceID)
	if resourceID == "" {
		return nil, &DomainError{Code: ErrCodeInvalidArgument, Message: "resource_id is required"}
	}

	res, err := client.GetResource(ctx, projectID, resourceID)
	if err != nil {
		return nil, mapAPIError(err)
	}

	runtimeID := ""
	if res.Runtime != nil {
		runtimeID = res.Runtime.Spec.Assignment.RuntimeID
	}
	endpoint := fmt.Sprintf("%s.%s.svc.cluster.local", res.Name, res.EnvironmentID)

	version := ""
	if res.Managed != nil {
		version = res.Managed.Version
	}

	allBindings, _ := client.ListResourceBindings(ctx, projectID, res.EnvironmentID)
	bindingCount := 0
	for _, b := range allBindings {
		if b.Target.ID == res.ID || b.LogicalName == res.Name {
			bindingCount++
		}
	}

	return ManagedResourceDetailResult{
		ID:            res.ID,
		Name:          res.Name,
		Kind:          string(res.Kind),
		Type:          string(res.Type),
		Version:       version,
		Lifecycle:     string(res.Lifecycle),
		EnvironmentID: res.EnvironmentID,
		RuntimeID:     runtimeID,
		Endpoint:      endpoint,
		BindingCount:  bindingCount,
	}, nil
}

func (s *Server) handleBuildRecordsList(ctx context.Context, _ *Server, args map[string]any) (any, error) {
	client, err := s.getCloudClient(ctx)
	if err != nil {
		return nil, err
	}
	projectID, err := s.resolveProjectID(ctx, client, args)
	if err != nil {
		return nil, err
	}

	query := url.Values{}
	if k, ok := args["service_key"].(string); ok && k != "" {
		query.Set("service_key", k)
	}
	if sha, ok := args["sha"].(string); ok && sha != "" {
		query.Set("sha", sha)
	}
	if st, ok := args["status"].(string); ok && st != "" {
		query.Set("status", st)
	}
	if c, ok := args["cursor"].(string); ok && c != "" {
		query.Set("cursor", c)
	}
	limit := getIntArg(args, "limit", DefaultFileListLimit, 100)
	query.Set("limit", strconv.Itoa(limit))

	result, err := client.ListBuildRecords(ctx, projectID, query)
	if err != nil {
		return nil, mapAPIError(err)
	}
	return result, nil
}

func (s *Server) handleBuildRecordGet(ctx context.Context, _ *Server, args map[string]any) (any, error) {
	client, err := s.getCloudClient(ctx)
	if err != nil {
		return nil, err
	}
	projectID, err := s.resolveProjectID(ctx, client, args)
	if err != nil {
		return nil, err
	}
	recordID, _ := args["build_record_id"].(string)
	recordID = strings.TrimSpace(recordID)
	if recordID == "" {
		return nil, &DomainError{Code: ErrCodeInvalidArgument, Message: "build_record_id is required"}
	}

	record, err := client.GetBuildRecord(ctx, projectID, recordID)
	if err != nil {
		return nil, mapAPIError(err)
	}
	return record, nil
}

func (s *Server) handleDeploymentsList(ctx context.Context, _ *Server, args map[string]any) (any, error) {
	client, err := s.getCloudClient(ctx)
	if err != nil {
		return nil, err
	}
	projectID, err := s.resolveProjectID(ctx, client, args)
	if err != nil {
		return nil, err
	}

	deployments, err := client.ListDeployments(ctx, projectID)
	if err != nil {
		return nil, mapAPIError(err)
	}

	serviceID, _ := args["service_id"].(string)
	environmentID, _ := args["environment_id"].(string)
	limit := getIntArg(args, "limit", DefaultFileListLimit, 100)

	filtered := make([]cloudclient.DeploymentJob, 0)
	for _, d := range deployments {
		if serviceID != "" && d.ServiceID != serviceID {
			continue
		}
		if environmentID != "" && d.EnvironmentID != environmentID {
			continue
		}
		filtered = append(filtered, d)
		if len(filtered) >= limit {
			break
		}
	}
	return map[string]any{"deployments": filtered}, nil
}

func (s *Server) handleDeploymentGet(ctx context.Context, _ *Server, args map[string]any) (any, error) {
	client, err := s.getCloudClient(ctx)
	if err != nil {
		return nil, err
	}
	projectID, err := s.resolveProjectID(ctx, client, args)
	if err != nil {
		return nil, err
	}
	deploymentID, _ := args["deployment_id"].(string)
	deploymentID = strings.TrimSpace(deploymentID)
	if deploymentID == "" {
		return nil, &DomainError{Code: ErrCodeInvalidArgument, Message: "deployment_id is required"}
	}

	deployment, err := client.GetDeployment(ctx, projectID, deploymentID)
	if err != nil {
		return nil, mapAPIError(err)
	}
	return deployment, nil
}

func (s *Server) handleDeploymentPreflight(ctx context.Context, _ *Server, args map[string]any) (any, error) {
	client, err := s.getCloudClient(ctx)
	if err != nil {
		return nil, err
	}
	projectID, err := s.resolveProjectID(ctx, client, args)
	if err != nil {
		return nil, err
	}
	buildRecordID, _ := args["build_record_id"].(string)
	environmentID, _ := args["environment_id"].(string)

	if strings.TrimSpace(buildRecordID) == "" {
		return nil, &DomainError{Code: ErrCodeInvalidArgument, Message: "build_record_id is required"}
	}

	req := deploymentv1.CreateRequest{
		BuildRecordID: strings.TrimSpace(buildRecordID),
		EnvironmentID: strings.TrimSpace(environmentID),
	}

	result, err := client.PreflightDeployment(ctx, projectID, req)
	if err != nil {
		return nil, mapAPIError(err)
	}
	return result, nil
}

func (s *Server) handleSourceRiskReport(ctx context.Context, _ *Server, args map[string]any) (any, error) {
	client, err := s.getCloudClient(ctx)
	if err != nil {
		return nil, err
	}
	projectID, err := s.resolveProjectID(ctx, client, args)
	if err != nil {
		return nil, err
	}

	if reportID, ok := args["report_id"].(string); ok && strings.TrimSpace(reportID) != "" {
		report, err := client.GetSourceRiskReportByID(ctx, projectID, strings.TrimSpace(reportID))
		if err != nil {
			return nil, mapAPIError(err)
		}
		return report, nil
	}

	appID, _ := args["application_id"].(string)
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return nil, &DomainError{Code: ErrCodeInvalidArgument, Message: "application_id is required"}
	}
	commitSHA, _ := args["commit_sha"].(string)

	services, _ := client.ListServices(ctx, projectID)
	for _, svc := range services {
		if svc.ID == appID || svc.Name == appID {
			appID = svc.ID
			break
		}
	}

	report, err := client.GetSourceRiskReport(ctx, projectID, appID, commitSHA)
	if err != nil {
		return nil, mapAPIError(err)
	}
	return report, nil
}

func (s *Server) handleDependencyVerificationLatest(ctx context.Context, _ *Server, args map[string]any) (any, error) {
	client, err := s.getCloudClient(ctx)
	if err != nil {
		return nil, err
	}
	projectID, err := s.resolveProjectID(ctx, client, args)
	if err != nil {
		return nil, err
	}

	depName, _ := args["dependency_logical_name"].(string)
	depName = strings.TrimSpace(depName)
	if depName == "" {
		return nil, &DomainError{Code: ErrCodeInvalidArgument, Message: "dependency_logical_name is required"}
	}
	appID, _ := args["application_id"].(string)
	appID = strings.TrimSpace(appID)
	envID, _ := args["environment_id"].(string)

	if appID != "" {
		services, _ := client.ListServices(ctx, projectID)
		for _, svc := range services {
			if svc.ID == appID || svc.Name == appID {
				appID = svc.ID
				break
			}
		}
	}

	run, err := client.GetDependencyVerification(ctx, projectID, depName, envID, appID)
	if err != nil {
		return nil, mapAPIError(err)
	}
	return run, nil
}

func (s *Server) handleDependencyVerificationHistory(ctx context.Context, _ *Server, args map[string]any) (any, error) {
	client, err := s.getCloudClient(ctx)
	if err != nil {
		return nil, err
	}
	projectID, err := s.resolveProjectID(ctx, client, args)
	if err != nil {
		return nil, err
	}
	deploymentID, _ := args["deployment_job_id"].(string)
	deploymentID = strings.TrimSpace(deploymentID)
	if deploymentID == "" {
		return nil, &DomainError{Code: ErrCodeInvalidArgument, Message: "deployment_job_id is required"}
	}

	runs, err := client.ListDependencyVerifications(ctx, projectID, deploymentID)
	if err != nil {
		return nil, mapAPIError(err)
	}
	limit := getIntArg(args, "limit", DefaultFileListLimit, 100)
	if len(runs) > limit {
		runs = runs[:limit]
	}
	return map[string]any{"runs": runs}, nil
}

func (s *Server) resolveSourceSnapshot(ctx context.Context, client *cloudclient.Client, projectID, appID, buildRecordID, commitSHA string) (string, string, string, error) {
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return "", "", "", &DomainError{Code: ErrCodeInvalidArgument, Message: "application_id is required"}
	}

	services, err := client.ListServices(ctx, projectID)
	if err != nil {
		return "", "", "", mapAPIError(err)
	}
	var targetSvc *cloudclient.Service
	for i := range services {
		if services[i].ID == appID || services[i].Name == appID {
			targetSvc = &services[i]
			break
		}
	}
	if targetSvc == nil {
		return "", "", "", &DomainError{Code: ErrCodeNotFound, Message: fmt.Sprintf("application %q not found", appID)}
	}

	bindings, _ := client.ListGitHubBindings(ctx, projectID)
	var binding *cloudclient.GitHubBinding
	for i := range bindings {
		if bindings[i].ServiceID == targetSvc.ID || bindings[i].ServiceKey == targetSvc.Name {
			binding = &bindings[i]
			break
		}
	}

	applicationRoot := ""
	if binding != nil {
		applicationRoot = binding.ApplicationRoot
	}

	resolvedSHA := strings.TrimSpace(commitSHA)
	if resolvedSHA == "" && buildRecordID != "" {
		record, bErr := client.GetBuildRecord(ctx, projectID, strings.TrimSpace(buildRecordID))
		if bErr == nil && record.Workload.SHA != "" {
			resolvedSHA = record.Workload.SHA
		}
	}
	if resolvedSHA == "" {
		records, _ := client.ListBuildRecords(ctx, projectID, url.Values{"service_key": {targetSvc.Name}, "limit": {"1"}})
		if len(records.Records) > 0 && records.Records[0].Workload.SHA != "" {
			resolvedSHA = records.Records[0].Workload.SHA
		}
	}
	if resolvedSHA == "" {
		return "", "", "", &DomainError{Code: ErrCodeSourceSnapshotUnavailable, Message: "exact commit SHA cannot be resolved for application"}
	}

	repoRoot := s.RepoRoot
	if repoRoot == "" {
		var detectErr error
		repoRoot, detectErr = repository.Root(ctx, s.GitRunner, ".")
		if detectErr != nil || repoRoot == "" {
			return "", "", "", &DomainError{Code: ErrCodeSourceSnapshotUnavailable, Message: "local git repository is unavailable"}
		}
	}

	return repoRoot, resolvedSHA, applicationRoot, nil
}

func (s *Server) handleSourceFilesList(ctx context.Context, _ *Server, args map[string]any) (any, error) {
	client, err := s.getCloudClient(ctx)
	if err != nil {
		return nil, err
	}
	projectID, err := s.resolveProjectID(ctx, client, args)
	if err != nil {
		return nil, err
	}

	appID, _ := args["application_id"].(string)
	buildRecordID, _ := args["build_record_id"].(string)
	commitSHA, _ := args["commit_sha"].(string)
	pathPrefix, _ := args["path_prefix"].(string)
	cursor, _ := args["cursor"].(string)
	limit := getIntArg(args, "limit", DefaultFileListLimit, AbsoluteFileListLimit)

	repoRoot, resolvedSHA, appRoot, err := s.resolveSourceSnapshot(ctx, client, projectID, appID, buildRecordID, commitSHA)
	if err != nil {
		return nil, err
	}

	res, err := s.SourceService.ListFiles(ctx, repoRoot, resolvedSHA, appRoot, pathPrefix, limit, cursor)
	if err != nil {
		return nil, err
	}
	res.ApplicationID = appID
	return res, nil
}

func (s *Server) handleSourceFileRead(ctx context.Context, _ *Server, args map[string]any) (any, error) {
	client, err := s.getCloudClient(ctx)
	if err != nil {
		return nil, err
	}
	projectID, err := s.resolveProjectID(ctx, client, args)
	if err != nil {
		return nil, err
	}

	appID, _ := args["application_id"].(string)
	relativePath, _ := args["relative_path"].(string)
	buildRecordID, _ := args["build_record_id"].(string)
	commitSHA, _ := args["commit_sha"].(string)
	maxBytes := getIntArg(args, "max_bytes", DefaultMaxFileBytes, AbsoluteMaxFileBytes)

	if strings.TrimSpace(relativePath) == "" {
		return nil, &DomainError{Code: ErrCodeInvalidArgument, Message: "relative_path is required"}
	}

	repoRoot, resolvedSHA, appRoot, err := s.resolveSourceSnapshot(ctx, client, projectID, appID, buildRecordID, commitSHA)
	if err != nil {
		return nil, err
	}

	res, err := s.SourceService.ReadFile(ctx, repoRoot, resolvedSHA, appRoot, relativePath, maxBytes)
	if err != nil {
		return nil, err
	}
	res.ApplicationID = appID
	return res, nil
}

func (s *Server) handleSourceSearch(ctx context.Context, _ *Server, args map[string]any) (any, error) {
	client, err := s.getCloudClient(ctx)
	if err != nil {
		return nil, err
	}
	projectID, err := s.resolveProjectID(ctx, client, args)
	if err != nil {
		return nil, err
	}

	appID, _ := args["application_id"].(string)
	query, _ := args["query"].(string)
	buildRecordID, _ := args["build_record_id"].(string)
	commitSHA, _ := args["commit_sha"].(string)
	pathPrefix, _ := args["path_prefix"].(string)
	limit := getIntArg(args, "limit", DefaultSearchLimit, AbsoluteSearchLimit)

	if strings.TrimSpace(query) == "" {
		return nil, &DomainError{Code: ErrCodeInvalidArgument, Message: "query is required"}
	}

	repoRoot, resolvedSHA, appRoot, err := s.resolveSourceSnapshot(ctx, client, projectID, appID, buildRecordID, commitSHA)
	if err != nil {
		return nil, err
	}

	res, err := s.SourceService.Search(ctx, repoRoot, resolvedSHA, appRoot, query, pathPrefix, limit)
	if err != nil {
		return nil, err
	}
	res.ApplicationID = appID
	return res, nil
}

type DomainError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *DomainError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func mapAPIError(err error) error {
	var apiErr *cloudclient.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case 401:
			return &DomainError{Code: ErrCodeAuthRequired, Message: "Cloud authentication required or PAT invalid"}
		case 403:
			return &DomainError{Code: ErrCodeForbidden, Message: "permission denied for requested resource"}
		case 404:
			return &DomainError{Code: ErrCodeNotFound, Message: "requested resource not found"}
		default:
			return &DomainError{Code: ErrCodeAuthorityUnavailable, Message: apiErr.Message}
		}
	}
	return &DomainError{Code: ErrCodeAuthorityUnavailable, Message: err.Error()}
}
