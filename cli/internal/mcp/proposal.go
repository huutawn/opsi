package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"

	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
)

const (
	proposalStatusValid             = "VALID"
	proposalStatusValidWithWarnings = "VALID_WITH_WARNINGS"
	proposalStatusInvalid           = "INVALID"
	proposalStatusStale             = "STALE"
	proposalStatusNoChange          = "NO_CHANGE_PROPOSED"

	targetResolved  = "RESOLVED"
	targetAmbiguous = "TARGET_AMBIGUOUS"
	targetNotFound  = "TARGET_NOT_FOUND"

	proposalActionNone  = "NONE"
	maxProposalTargets  = 50
	maxProposalRisks    = 20
	maxProposalEvidence = 20
	maxEvidenceExcerpt  = 512
)

type dependencyAnalysisState struct {
	projectID string
	appID     string
	envID     string
	cfg       cloudclient.ServiceConfiguration
	context   DependencyAnalysisContext
}

func hashCanonical(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func trimEvidence(evidence []DependencyEvidence) ([]DependencyEvidence, error) {
	if len(evidence) > maxProposalEvidence {
		return nil, &DomainError{Code: ErrCodeLimitExceeded, Message: fmt.Sprintf("evidence exceeds maximum of %d", maxProposalEvidence)}
	}
	result := append([]DependencyEvidence(nil), evidence...)
	for i := range result {
		result[i].Type = strings.TrimSpace(result[i].Type)
		result[i].File = strings.TrimSpace(result[i].File)
		result[i].Symbol = strings.TrimSpace(result[i].Symbol)
		result[i].Reason = strings.TrimSpace(result[i].Reason)
		if result[i].Type == "" || result[i].File == "" || result[i].Line < 1 || result[i].Reason == "" {
			return nil, &DomainError{Code: ErrCodeInvalidArgument, Message: "each evidence item requires type, file, positive line, and reason"}
		}
		if !map[string]bool{"ENV_REFERENCE": true, "IMPORT_USAGE": true, "CLIENT_LIBRARY": true, "RELATIVE_HTTP_PATH": true, "URL_LITERAL": true, "CONFIG_KEY": true, "SOURCE_RISK_FINDING": true, "EXISTING_DEPENDENCY": true, "EXISTING_APPLICATION_TARGET": true, "EXISTING_RESOURCE_TARGET": true}[result[i].Type] {
			return nil, &DomainError{Code: ErrCodeInvalidArgument, Message: "evidence type is not supported"}
		}
		result[i].SafeExcerpt, _ = RedactSourceSecrets(result[i].SafeExcerpt)
		if len(result[i].SafeExcerpt) > maxEvidenceExcerpt {
			result[i].SafeExcerpt = result[i].SafeExcerpt[:maxEvidenceExcerpt]
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Type+"\x00"+result[i].File+fmt.Sprintf("%09d", result[i].Line)+"\x00"+result[i].Symbol < result[j].Type+"\x00"+result[j].File+fmt.Sprintf("%09d", result[j].Line)+"\x00"+result[j].Symbol
	})
	return result, nil
}

func normalizeCandidate(candidate DependencyCandidate) DependencyCandidate {
	candidate.LogicalName = strings.TrimSpace(candidate.LogicalName)
	candidate.DependencyKind = strings.TrimSpace(candidate.DependencyKind)
	candidate.TargetID = strings.TrimSpace(candidate.TargetID)
	candidate.Protocol = strings.TrimSpace(candidate.Protocol)
	candidate.Phase = strings.TrimSpace(candidate.Phase)
	candidate.AccessContext = strings.TrimSpace(candidate.AccessContext)
	candidate.Strategy = strings.TrimSpace(candidate.Strategy)
	candidate.Path = strings.TrimSpace(candidate.Path)
	candidate.Mappings = append([]DependencyInjectionMapping(nil), candidate.Mappings...)
	for i := range candidate.Mappings {
		candidate.Mappings[i].EnvName = strings.TrimSpace(candidate.Mappings[i].EnvName)
		candidate.Mappings[i].SymbolicSource = strings.TrimSpace(candidate.Mappings[i].SymbolicSource)
	}
	sort.Slice(candidate.Mappings, func(i, j int) bool { return candidate.Mappings[i].EnvName < candidate.Mappings[j].EnvName })
	return candidate
}

func proposalHash(analysisInputsHash string, candidate DependencyCandidate, evidence []DependencyEvidence, confidence string) string {
	return hashCanonical(struct {
		AnalysisInputsHash string               `json:"analysis_inputs_hash"`
		Candidate          DependencyCandidate  `json:"candidate"`
		Evidence           []DependencyEvidence `json:"evidence"`
		Confidence         string               `json:"confidence"`
	}{analysisInputsHash, normalizeCandidate(candidate), evidence, confidence})
}

func (s *Server) dependencyAnalysisState(ctx context.Context, args map[string]any) (dependencyAnalysisState, error) {
	client, err := s.getCloudClient(ctx)
	if err != nil {
		return dependencyAnalysisState{}, err
	}
	projectID, err := s.resolveProjectID(ctx, client, args)
	if err != nil {
		return dependencyAnalysisState{}, err
	}
	appRef, _ := args["application_id"].(string)
	appRef = strings.TrimSpace(appRef)
	if appRef == "" {
		return dependencyAnalysisState{}, &DomainError{Code: ErrCodeInvalidArgument, Message: "application_id is required"}
	}
	envID, _ := args["environment_id"].(string)
	envID = strings.TrimSpace(envID)
	if envID == "" {
		return dependencyAnalysisState{}, &DomainError{Code: ErrCodeInvalidArgument, Message: "environment_id is required"}
	}

	services, err := client.ListServices(ctx, projectID)
	if err != nil {
		return dependencyAnalysisState{}, mapAPIError(err)
	}
	var app *cloudclient.Service
	for i := range services {
		if services[i].ID == appRef || services[i].Name == appRef {
			app = &services[i]
			break
		}
	}
	if app == nil {
		return dependencyAnalysisState{}, &DomainError{Code: ErrCodeNotFound, Message: "application not found"}
	}
	cfg, err := client.GetServiceConfiguration(ctx, projectID, app.ID)
	if err != nil {
		return dependencyAnalysisState{}, mapAPIError(err)
	}

	bindings, err := client.ListGitHubBindings(ctx, projectID)
	if err != nil {
		return dependencyAnalysisState{}, mapAPIError(err)
	}
	var binding *cloudclient.GitHubBinding
	for i := range bindings {
		if bindings[i].ServiceID == app.ID || bindings[i].ServiceKey == app.Name {
			binding = &bindings[i]
			break
		}
	}
	if binding == nil {
		return dependencyAnalysisState{}, &DomainError{Code: ErrCodeSourceSnapshotUnavailable, Message: "application has no source binding"}
	}
	records, err := client.ListBuildRecords(ctx, projectID, url.Values{"service_key": {app.Name}, "limit": {"1"}})
	if err != nil || len(records.Records) == 0 || strings.TrimSpace(records.Records[0].Workload.SHA) == "" {
		return dependencyAnalysisState{}, &DomainError{Code: ErrCodeSourceSnapshotUnavailable, Message: "application has no exact BuildRecord source commit"}
	}
	record := records.Records[0]
	repoRoot := s.RepoRoot
	if repoRoot == "" {
		return dependencyAnalysisState{}, &DomainError{Code: ErrCodeSourceSnapshotUnavailable, Message: "local source repository is unavailable"}
	}
	if err := s.SourceService.VerifyCommitExists(ctx, repoRoot, record.Workload.SHA); err != nil {
		return dependencyAnalysisState{}, &DomainError{Code: ErrCodeSourceSnapshotUnavailable, Message: "exact BuildRecord source snapshot is unavailable"}
	}

	appDetail := ApplicationDetailResult{ID: app.ID, Name: app.Name, Status: app.Status, SourceBinding: &SourceBinding{
		RepositoryID: binding.RepositoryID, ServiceKey: binding.ServiceKey, SelectedRef: binding.SelectedRef,
		ApplicationRoot: binding.ApplicationRoot, BuildContext: binding.BuildContext, BuildStrategy: binding.BuildStrategy, DockerfilePath: binding.DockerfilePath,
	}, ExactCommitSHA: record.Workload.SHA, ServiceConfigRevision: cfg.Revision, ServiceConfigStateHash: cfg.StateHash, CurrentBuildRecordID: record.ID}
	for _, env := range cfg.Environment {
		appDetail.EnvironmentVariablesSafe = append(appDetail.EnvironmentVariablesSafe, env.Name)
	}
	sort.Strings(appDetail.EnvironmentVariablesSafe)
	if cfg.PublicRoute != nil {
		appDetail.PublicRoute = &PublicRouteSummary{Hostname: cfg.PublicRoute.Hostname, Path: cfg.PublicRoute.Path}
	}
	appDetail.DependenciesSummary = s.buildDependencyDocs(ctx, client, projectID, app.ID, cfg)

	topology, _ := client.GetTopology(ctx, projectID)
	resources, err := client.ListResources(ctx, projectID, envID)
	if err != nil {
		return dependencyAnalysisState{}, mapAPIError(err)
	}
	targets := DependencyCompatibleTargets{}
	for _, resource := range resources {
		if len(targets.ManagedResources) == maxProposalTargets {
			break
		}
		protocol := string(resource.Type)
		if protocol != serviceconfigurationv1.ProtocolPostgres && protocol != serviceconfigurationv1.ProtocolRedis {
			continue
		}
		targets.ManagedResources = append(targets.ManagedResources, DependencyResourceTarget{ID: resource.ID, Name: resource.Name, Protocol: protocol, EnvironmentID: resource.EnvironmentID, Lifecycle: string(resource.Lifecycle)})
	}
	for _, service := range services {
		if service.ID == app.ID || len(targets.Applications) == maxProposalTargets {
			continue
		}
		target := DependencyApplicationTarget{ID: service.ID, Name: service.Name}
		if targetCfg, getErr := client.GetServiceConfiguration(ctx, projectID, service.ID); getErr == nil && targetCfg.PublicRoute != nil {
			target.PublicRoute = &PublicRouteSummary{Hostname: targetCfg.PublicRoute.Hostname, Path: targetCfg.PublicRoute.Path}
		}
		targets.Applications = append(targets.Applications, target)
	}
	sort.Slice(targets.ManagedResources, func(i, j int) bool { return targets.ManagedResources[i].ID < targets.ManagedResources[j].ID })
	sort.Slice(targets.Applications, func(i, j int) bool { return targets.Applications[i].ID < targets.Applications[j].ID })

	riskFindings := []DependencyRiskFinding{}
	if report, riskErr := client.GetSourceRiskReport(ctx, projectID, app.ID, record.Workload.SHA); riskErr == nil {
		for _, finding := range report.Findings {
			if len(riskFindings) == maxProposalRisks {
				break
			}
			safe, _ := RedactSourceSecrets(finding.SafeEvidence)
			riskFindings = append(riskFindings, DependencyRiskFinding{RuleID: finding.RuleID, Severity: finding.Severity, Confidence: finding.Confidence, File: finding.File, Line: finding.Line, SafeEvidence: safe})
		}
	}
	verification := []DependencyVerificationState{}
	for _, dep := range cfg.Dependencies {
		if len(verification) == maxProposalRisks {
			break
		}
		if run, runErr := client.GetDependencyVerification(ctx, projectID, dep.LogicalName, envID, app.ID); runErr == nil && run.ID != "" {
			verification = append(verification, DependencyVerificationState{LogicalName: dep.LogicalName, Status: run.OverallStatus})
		}
	}

	depsHash := hashCanonical(cfg.Dependencies)
	hashInputs := struct {
		Commit           string                      `json:"commit"`
		Root             string                      `json:"root"`
		Environment      string                      `json:"environment"`
		ConfigRevision   uint64                      `json:"config_revision"`
		ConfigHash       string                      `json:"config_hash"`
		DependenciesHash string                      `json:"dependencies_hash"`
		Targets          DependencyCompatibleTargets `json:"targets"`
		TopologyRevision uint64                      `json:"topology_revision"`
		TopologyHash     string                      `json:"topology_hash"`
		RiskIdentity     []DependencyRiskFinding     `json:"risk_identity"`
	}{record.Workload.SHA, binding.ApplicationRoot, envID, cfg.Revision, cfg.StateHash, depsHash, targets, topology.Revision, topology.StateHash, riskFindings}
	inputsHash := hashCanonical(hashInputs)
	analysis := DependencyAnalysisContext{Application: appDetail, Source: DependencySourceProvenance{BuildRecordID: record.ID, CommitSHA: record.Workload.SHA, ApplicationRoot: binding.ApplicationRoot, BuildContext: binding.BuildContext, BuildStrategy: binding.BuildStrategy}, CurrentDependencies: appDetail.DependenciesSummary, CompatibleTargets: targets, SourceRiskFindings: riskFindings, Verification: verification, Authority: DependencyAuthoritySnapshot{ServiceConfigurationRevision: cfg.Revision, ServiceConfigurationStateHash: cfg.StateHash, DependencyContractHash: depsHash, TopologyRevision: topology.Revision, TopologyStateHash: topology.StateHash, AnalysisInputsHash: inputsHash}, Bounds: DependencyContextBounds{MaximumTargets: maxProposalTargets, MaximumRiskFindings: maxProposalRisks, MaximumEvidence: maxProposalEvidence, MaximumEvidenceExcerpt: maxEvidenceExcerpt}}
	return dependencyAnalysisState{projectID: projectID, appID: app.ID, envID: envID, cfg: cfg, context: analysis}, nil
}

func (s *Server) handleDependencyAnalysisContext(ctx context.Context, _ *Server, args map[string]any) (any, error) {
	state, err := s.dependencyAnalysisState(ctx, args)
	if err != nil {
		return nil, err
	}
	return state.context, nil
}

func parseProposal(args map[string]any) (DependencyProposal, error) {
	value, ok := args["proposal"]
	if !ok {
		return DependencyProposal{}, &DomainError{Code: ErrCodeInvalidArgument, Message: "proposal is required"}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return DependencyProposal{}, &DomainError{Code: ErrCodeInvalidArgument, Message: "proposal must be a JSON object"}
	}
	var proposal DependencyProposal
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&proposal); err != nil {
		return DependencyProposal{}, &DomainError{Code: ErrCodeInvalidArgument, Message: "proposal is malformed"}
	}
	proposal.ProjectID = strings.TrimSpace(proposal.ProjectID)
	proposal.EnvironmentID = strings.TrimSpace(proposal.EnvironmentID)
	proposal.ApplicationID = strings.TrimSpace(proposal.ApplicationID)
	proposal.Provenance.SourceCommit = strings.TrimSpace(proposal.Provenance.SourceCommit)
	proposal.Provenance.ApplicationRoot = strings.TrimSpace(proposal.Provenance.ApplicationRoot)
	proposal.Provenance.AnalysisInputsHash = strings.TrimSpace(proposal.Provenance.AnalysisInputsHash)
	proposal.Confidence = strings.TrimSpace(proposal.Confidence)
	if proposal.ProjectID == "" || proposal.EnvironmentID == "" || proposal.ApplicationID == "" || proposal.Provenance.SourceCommit == "" || proposal.Provenance.ApplicationRoot == "" || proposal.Provenance.AnalysisInputsHash == "" {
		return DependencyProposal{}, &DomainError{Code: ErrCodeInvalidArgument, Message: "proposal identity and provenance fields are required"}
	}
	if proposal.Confidence != "HIGH" && proposal.Confidence != "MEDIUM" && proposal.Confidence != "LOW" {
		return DependencyProposal{}, &DomainError{Code: ErrCodeInvalidArgument, Message: "confidence must be HIGH, MEDIUM, or LOW"}
	}
	var errEvidence error
	proposal.Evidence, errEvidence = trimEvidence(proposal.Evidence)
	if errEvidence != nil {
		return DependencyProposal{}, errEvidence
	}
	proposal.Candidate = normalizeCandidate(proposal.Candidate)
	if proposal.Candidate.LogicalName == "" || proposal.Candidate.DependencyKind == "" || proposal.Candidate.Protocol == "" || proposal.Candidate.Phase == "" {
		return DependencyProposal{}, &DomainError{Code: ErrCodeInvalidArgument, Message: "candidate logical_name, dependency_kind, protocol, and phase are required"}
	}
	return proposal, nil
}

func candidateTargetResolution(candidate DependencyCandidate, targets DependencyCompatibleTargets) (string, bool) {
	var matches int
	if candidate.DependencyKind == serviceconfigurationv1.TargetKindManagedResource {
		for _, target := range targets.ManagedResources {
			if target.Protocol == candidate.Protocol {
				matches++
			}
			if target.ID == candidate.TargetID && target.Protocol == candidate.Protocol {
				return targetResolved, true
			}
		}
	} else if candidate.DependencyKind == serviceconfigurationv1.TargetKindApplication {
		for _, target := range targets.Applications {
			if target.ID == candidate.TargetID {
				return targetResolved, true
			}
			matches++
		}
	}
	if candidate.TargetID == "" && matches > 1 {
		return targetAmbiguous, false
	}
	return targetNotFound, false
}

func candidateDependency(candidate DependencyCandidate) serviceconfigurationv1.ApplicationDependency {
	mappings := make([]serviceconfigurationv1.DependencyInjectionMapping, 0, len(candidate.Mappings))
	for _, mapping := range candidate.Mappings {
		mappings = append(mappings, serviceconfigurationv1.DependencyInjectionMapping{EnvName: mapping.EnvName, SymbolicSource: mapping.SymbolicSource})
	}
	return serviceconfigurationv1.ApplicationDependency{LogicalName: candidate.LogicalName, TargetKind: candidate.DependencyKind, TargetIdentity: candidate.TargetID, Protocol: candidate.Protocol, Strategy: candidate.Strategy, AccessContext: candidate.AccessContext, Path: candidate.Path, Required: candidate.Required, InjectionPhase: candidate.Phase, InjectionMappings: mappings, VerificationContract: func() *serviceconfigurationv1.DependencyVerificationContract {
		if candidate.VerificationContract == nil {
			return nil
		}
		return &serviceconfigurationv1.DependencyVerificationContract{Type: candidate.VerificationContract.Type, Path: candidate.VerificationContract.Path, ExpectedStatus: candidate.VerificationContract.ExpectedStatus}
	}()}
}

func (s *Server) handleValidateDependencyProposal(ctx context.Context, _ *Server, args map[string]any) (any, error) {
	proposal, err := parseProposal(args)
	if err != nil {
		return nil, err
	}
	state, err := s.dependencyAnalysisState(ctx, map[string]any{"project_id": proposal.ProjectID, "environment_id": proposal.EnvironmentID, "application_id": proposal.ApplicationID})
	if err != nil {
		return nil, err
	}
	result := DependencyProposalValidation{Action: proposalActionNone, AnalysisInputsHash: state.context.Authority.AnalysisInputsHash, ProposalHash: proposalHash(proposal.Provenance.AnalysisInputsHash, proposal.Candidate, proposal.Evidence, proposal.Confidence)}
	if proposal.Provenance.SourceCommit != state.context.Source.CommitSHA || proposal.Provenance.ApplicationRoot != state.context.Source.ApplicationRoot || proposal.Provenance.AnalysisInputsHash != state.context.Authority.AnalysisInputsHash {
		result.Status, result.TargetResolution = proposalStatusStale, targetNotFound
		result.Issues = []DependencyProposalIssue{{Code: ErrCodeProposalStale, Message: "proposal provenance no longer matches the current exact analysis inputs"}}
		return result, nil
	}

	resolution, targetOK := candidateTargetResolution(proposal.Candidate, state.context.CompatibleTargets)
	result.TargetResolution = resolution
	if !targetOK {
		result.Status = proposalStatusInvalid
		code, message := "DEPENDENCY_TARGET_NOT_FOUND", "candidate target is not a compatible current target"
		if resolution == targetAmbiguous {
			code, message = targetAmbiguous, "multiple compatible targets exist; select none until factual evidence distinguishes a target"
		}
		if proposal.Candidate.TargetID != "" && resolution == targetNotFound {
			code, message = ErrCodeForbidden, "candidate target is not available in this authorized analysis context"
		}
		result.Issues = []DependencyProposalIssue{{Code: code, Field: "candidate.target_id", Message: message}}
		return result, nil
	}

	dep := candidateDependency(proposal.Candidate)
	for _, current := range state.cfg.Dependencies {
		if current.LogicalName == dep.LogicalName && reflect.DeepEqual(serviceconfigurationv1.Normalize(serviceconfigurationv1.ServiceConfigurationDraft{Dependencies: []serviceconfigurationv1.ApplicationDependency{current}}).Dependencies[0], serviceconfigurationv1.Normalize(serviceconfigurationv1.ServiceConfigurationDraft{Dependencies: []serviceconfigurationv1.ApplicationDependency{dep}}).Dependencies[0]) {
			result.Status = proposalStatusNoChange
			return result, nil
		}
	}
	draft := state.cfg.ServiceConfigurationDraft
	replaced := false
	for i := range draft.Dependencies {
		if draft.Dependencies[i].LogicalName == dep.LogicalName {
			draft.Dependencies[i] = dep
			replaced = true
			break
		}
	}
	if !replaced {
		draft.Dependencies = append(draft.Dependencies, dep)
	}
	client, err := s.getCloudClient(ctx)
	if err != nil {
		return nil, err
	}
	validation, err := client.ValidateServiceConfiguration(ctx, state.projectID, state.appID, draft)
	if err != nil {
		return nil, mapAPIError(err)
	}
	if !validation.Valid {
		result.Status = proposalStatusInvalid
		for _, issue := range validation.Issues {
			result.Issues = append(result.Issues, DependencyProposalIssue{Code: issue.Code, Field: issue.Field, Message: issue.Message})
		}
		return result, nil
	}
	diff, err := client.DiffServiceConfiguration(ctx, state.projectID, state.appID, draft)
	if err != nil {
		return nil, mapAPIError(err)
	}
	for _, change := range diff.Changes {
		result.SemanticDiff = append(result.SemanticDiff, DependencySemanticChange{Action: change.Action, Kind: change.Kind, Name: change.Name, Before: change.Before, After: change.After})
	}
	result.Status = proposalStatusValid
	if dep.InjectionPhase == serviceconfigurationv1.InjectionPhaseBuild {
		result.Impact = "NEW_BUILD_RECORD_REQUIRED_IF_APPLIED"
	} else {
		result.Impact = "CONFIGURATION_REDEPLOY_REQUIRED_IF_APPLIED"
	}
	return result, nil
}
