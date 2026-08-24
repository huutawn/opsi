package webhookrelay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"sort"
	"strings"

	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
	"github.com/opsi-dev/opsi/cloud/internal/deploymentworkflow"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/repositoryanalysis"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
	deploymentpolicyv1 "github.com/opsi-dev/opsi/contracts/go/deploymentpolicyv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	verificationv1 "github.com/opsi-dev/opsi/contracts/go/verificationv1"
)

// deploymentWorkflowExecutor adapts DeploymentRun to the existing canonical
// application, resource, build, policy, deployment, and verification authorities.
// It persists no operational state of its own.
type deploymentWorkflowExecutor struct{ server *Server }

func (e deploymentWorkflowExecutor) Execute(ctx context.Context, run deploymentworkflow.Run, step deploymentworkflow.State) (deploymentworkflow.StepResult, error) {
	if e.server == nil || run.Approval == nil || run.Approval.PlanHash != run.Plan.Hash {
		return failedStep("DEPLOYMENT_APPROVAL_MISSING", "The exact deployment plan is no longer approved.", "Analyze and review the plan again.", false), nil
	}
	if stale, err := e.sourceStale(ctx, run); err != nil {
		return failedStep("SOURCE_AUTHORITY_UNAVAILABLE", "The exact repository commit could not be confirmed.", "Restore GitHub App connectivity and retry.", true), err
	} else if stale {
		return deploymentworkflow.StepResult{Stale: true, FailureMessage: "The selected repository ref now resolves to a different commit.", NextAction: "Analyze and review the repository again."}, nil
	}
	if step != deploymentworkflow.StateProvisioning {
		if reason, err := e.checkpointStale(ctx, run); err != nil {
			return failedStep("AUTHORITY_CHECKPOINT_UNAVAILABLE", "A canonical authority checkpoint could not be read.", "Restore authority storage and retry.", true), err
		} else if reason != "" {
			return deploymentworkflow.StepResult{Stale: true, FailureMessage: reason, NextAction: "Analyze and review the current canonical facts again."}, nil
		}
	}
	if step == deploymentworkflow.StateProvisioning {
		current, err := e.server.currentWorkflowAuthority(ctx, run.ProjectID, run.ID)
		if err != nil {
			return failedStep("PLAN_AUTHORITY_UNAVAILABLE", "The approved plan authority could not be confirmed.", "Restore Cloud authority storage and retry.", true), err
		}
		if current != run.Approval.AuthorityRevisions {
			return deploymentworkflow.StepResult{Stale: true, FailureMessage: "A canonical plan authority changed after approval.", NextAction: "Analyze and review the current plan again."}, nil
		}
	}
	switch step {
	case deploymentworkflow.StateProvisioning:
		return e.provision(ctx, run)
	case deploymentworkflow.StateBuilding:
		return e.build(ctx, run)
	case deploymentworkflow.StatePreflighting:
		return e.preflight(ctx, run)
	case deploymentworkflow.StateDeploying:
		return e.deploy(ctx, run)
	case deploymentworkflow.StateVerifying:
		return e.verify(ctx, run)
	case deploymentworkflow.StateRollingBack:
		return e.rollback(ctx, run)
	case deploymentworkflow.StateCleaningUp:
		return e.cleanupFirstDeploy(ctx, run)
	default:
		return failedStep("DEPLOYMENT_STEP_INVALID", "The deployment workflow step is invalid.", "Create a new deployment run.", false), nil
	}
}

func (e deploymentWorkflowExecutor) checkpointStale(ctx context.Context, run deploymentworkflow.Run) (string, error) {
	services, err := e.server.Registry.ListServices(run.ProjectID)
	if err != nil {
		return "", err
	}
	serviceByID := map[string]registry.ServiceRecord{}
	for _, service := range services {
		serviceByID[service.ID] = service
	}
	for _, checkpoint := range run.Refs.Checkpoints {
		var revision uint64
		var stateHash string
		switch checkpoint.Kind {
		case deploymentworkflow.AuthorityApplication:
			service := serviceByID[checkpoint.ID]
			if service.ID == "" {
				return "An approved application no longer exists.", nil
			}
			configuration, getErr := e.server.Registry.GetServiceConfiguration(run.ProjectID, service.ID)
			if getErr != nil {
				return "", getErr
			}
			revision = configuration.Revision
			stateHash = authorityStateHash(struct{ GitSHA, ConfigurationHash string }{service.GitSHA, configuration.StateHash})
		case deploymentworkflow.AuthorityResource:
			resource, getErr := e.server.Resources.Get(ctx, run.ProjectID, checkpoint.ID)
			if getErr != nil {
				return "", getErr
			}
			if resource.Runtime != nil {
				stateHash = resource.Runtime.Spec.SpecHash
			}
		case deploymentworkflow.AuthorityBinding:
			binding, getErr := e.server.Resources.GetBinding(ctx, run.ProjectID, checkpoint.ID)
			if getErr != nil {
				return "", getErr
			}
			stateHash = authorityStateHash(binding)
		case deploymentworkflow.AuthorityTopologyPlan:
			plan, getErr := e.server.Topology.Get(ctx, run.ProjectID)
			if getErr != nil {
				return "", getErr
			}
			if plan.ID != checkpoint.ID {
				return "The approved topology plan was replaced.", nil
			}
			revision, stateHash = plan.Revision, plan.StateHash
		case deploymentworkflow.AuthorityBuildRecord:
			record, getErr := e.server.BuildRecords.Get(ctx, run.ProjectID, checkpoint.ID)
			if getErr != nil {
				return "", getErr
			}
			stateHash = record.Build.OCIDigest
		case deploymentworkflow.AuthorityDeploymentPolicy:
			policy, getErr := e.server.Policies.Get(ctx, run.ProjectID, checkpoint.ID)
			if getErr != nil {
				return "", getErr
			}
			revision, stateHash = policy.Revision, policy.StateHash
		case deploymentworkflow.AuthorityVerification:
			verification, getErr := e.server.Verifications.Get(ctx, run.ProjectID, checkpoint.ID)
			if getErr != nil {
				return "", getErr
			}
			stateHash = authorityStateHash(verification)
		case deploymentworkflow.AuthorityWorkloadSecret:
			found := false
			for _, service := range services {
				metadata, listErr := e.server.Resources.Credentials.ListWorkloadSecrets(ctx, run.ProjectID, service.ID)
				if listErr != nil {
					return "", listErr
				}
				for _, secret := range metadata {
					if secret.ID == checkpoint.ID {
						revision, stateHash, found = secret.Revision, authorityStateHash(secret), true
						break
					}
				}
				if found {
					break
				}
			}
			if !found {
				return "An approved workload secret no longer exists.", nil
			}
		default:
			// Build and deployment jobs are run-owned mutable facts. Their
			// expected checkpoint is refreshed by each polling execution.
			continue
		}
		if checkpoint.Revision != 0 && revision != checkpoint.Revision || checkpoint.StateHash != "" && stateHash != checkpoint.StateHash {
			return "A canonical " + checkpoint.Kind + " fact changed outside this deployment run.", nil
		}
	}
	return "", nil
}

func (e deploymentWorkflowExecutor) sourceStale(ctx context.Context, run deploymentworkflow.Run) (bool, error) {
	if e.server.githubAppClient == nil {
		return false, errors.New("GitHub App client unavailable")
	}
	sha, err := e.server.githubAppClient.ResolveCommit(ctx, run.Plan.Source.InstallationID, run.Plan.Source.Repository, run.Plan.Source.SelectedRef)
	return sha != run.Plan.Source.CommitSHA, err
}

func (e deploymentWorkflowExecutor) build(ctx context.Context, run deploymentworkflow.Run) (deploymentworkflow.StepResult, error) {
	services, err := e.server.Registry.ListServices(run.ProjectID)
	if err != nil {
		return workflowFailure(err, "BUILD_SOURCE_UNAVAILABLE", "Restore the application authority and retry."), err
	}
	byID := map[string]registry.ServiceRecord{}
	for _, service := range services {
		byID[service.ID] = service
	}
	refs := deploymentworkflow.AuthorityRefs{}
	pending := false
	for _, applicationID := range run.Refs.IDs(deploymentworkflow.AuthorityApplication) {
		if byID[applicationID].ID == "" {
			return failedStep("BUILD_SOURCE_STALE", "An approved application source no longer exists.", "Analyze and review the plan again.", false), nil
		}
		job, _, createErr := e.server.BuildJobs.Create(ctx, run.ProjectID, applicationID, run.CreatedBy, workflowKey(run.ID, "build", fmt.Sprintf("%s-attempt-%d", applicationID, run.Attempt)))
		if createErr != nil {
			return workflowFailure(createErr, "BUILD_CREATE_FAILED", "Correct the build source and retry."), createErr
		}
		refs.Checkpoints = append(refs.Checkpoints, deploymentworkflow.Checkpoint(deploymentworkflow.AuthorityBuildJob, job.ID, 0, authorityStateHash(job), deploymentworkflow.StateBuilding))
		if job.Status == buildjob.StatusReady {
			if _, dispatchErr := e.server.BuildJobs.Dispatch(ctx, run.ProjectID, applicationID, job.ID); dispatchErr != nil {
				return workflowFailure(dispatchErr, "BUILD_DISPATCH_FAILED", "Restore the configured build executor and retry."), dispatchErr
			}
			pending = true
			continue
		}
		job, err = e.server.BuildJobs.Get(ctx, run.ProjectID, applicationID, job.ID)
		if err != nil {
			return workflowFailure(err, "BUILD_READ_FAILED", "Retry after BuildJob storage is restored."), err
		}
		switch job.Status {
		case buildjob.StatusSucceeded:
			record, recordErr := e.server.BuildRecords.Get(ctx, run.ProjectID, job.BuildRecordID)
			if recordErr != nil {
				return workflowFailure(recordErr, "BUILD_RECORD_READ_FAILED", "Retry after BuildRecord storage is restored."), recordErr
			}
			if record.Workload.SHA != run.Plan.Source.CommitSHA {
				return deploymentworkflow.StepResult{Stale: true, Refs: refs, FailureMessage: "The immutable BuildRecord does not match the approved source commit.", NextAction: "Analyze and review the repository again."}, nil
			}
			refs.Checkpoints = append(refs.Checkpoints, deploymentworkflow.Checkpoint(deploymentworkflow.AuthorityBuildRecord, job.BuildRecordID, 0, record.Build.OCIDigest, deploymentworkflow.StateBuilding))
		case buildjob.StatusFailed, buildjob.StatusCancelled:
			failedRun := run
			mergeWorkflowRefs(&failedRun.Refs, refs)
			if run.Plan.FailurePolicy.FailFast {
				if cancelErr := e.server.cancelWorkflowJobs(ctx, run.ProjectID, failedRun, run.ID); cancelErr != nil {
					return failedStep("FAIL_FAST_BUILD_CANCEL_FAILED", "A build failed and another build could not be stopped safely.", "Inspect active BuildJobs before retrying.", false), cancelErr
				}
			}
			code := firstNonEmpty(job.FailureCode, "BUILD_FAILED")
			return failedStep(code, firstNonEmpty(job.FailureMessageRedacted, "An immutable application build failed."), "Correct the source or build configuration, then retry.", retryableBuildFailure(code)), nil
		default:
			pending = true
		}
	}
	return deploymentworkflow.StepResult{Pending: pending, Refs: refs, ReplaceBuildRefs: run.Attempt > 1}, nil
}

func retryableBuildFailure(code string) bool {
	switch code {
	case "EXECUTOR_UNAVAILABLE", "EXECUTOR_DISPATCH_FAILED", "EXECUTOR_INFRASTRUCTURE_FAILED", "REGISTRY_AUTH_FAILED", "REGISTRY_PUSH_FAILED", "BUILD_JOB_UNAVAILABLE", "GITHUB_UNAVAILABLE":
		return true
	default:
		return false
	}
}

func (e deploymentWorkflowExecutor) preflight(ctx context.Context, run deploymentworkflow.Run) (deploymentworkflow.StepResult, error) {
	policyRefs, err := e.ensurePolicies(ctx, run)
	if err != nil {
		return workflowFailure(err, "POLICY_APPLY_FAILED", "Review the exact build and placement authority."), err
	}
	preflights, err := e.resolvePreflights(ctx, run, false)
	if err != nil {
		return workflowFailure(err, "PREFLIGHT_FAILED", "Correct the factual preflight failure and retry."), err
	}
	result := deploymentworkflow.StepResult{Refs: deploymentworkflow.AuthorityRefs{Checkpoints: policyRefs}}
	for _, value := range preflights {
		if value.Status == deploymentv1.PreflightStatusBlocked {
			result.Blocked = true
		}
		result.PreflightWarnings = append(result.PreflightWarnings, value.WarningIDs()...)
	}
	result.PreflightHash = workflowPreflightHash(preflights)
	sort.Strings(result.PreflightWarnings)
	return result, nil
}

func (e deploymentWorkflowExecutor) ensurePolicies(ctx context.Context, run deploymentworkflow.Run) ([]deploymentworkflow.AuthorityCheckpoint, error) {
	refs := []deploymentworkflow.AuthorityCheckpoint{}
	for _, recordID := range run.Refs.IDs(deploymentworkflow.AuthorityBuildRecord) {
		record, err := e.server.BuildRecords.Get(ctx, run.ProjectID, recordID)
		if err != nil {
			return nil, err
		}
		draft := deploymentpolicyv1.Draft{SchemaVersion: deploymentpolicyv1.SchemaVersion, ProjectID: run.ProjectID, RepositoryID: record.RepositoryID, ServiceKeys: []string{record.ServiceKey}, WorkflowRefs: []string{record.Workload.WorkflowRef}, AllowedEvents: []string{record.Workload.EventName}, AllowedGitRefs: []string{record.Workload.Ref}, EnvironmentID: run.Plan.Target.EnvironmentID, AllowedRuntimeIDs: []string{run.Plan.Target.RuntimeID}, AllowedOCIRepositories: []string{record.Build.OCIRepository}, AllowedPlatforms: []string{record.Build.Platform}, AllowedConfigHashes: []string{record.Build.ConfigHash}, AllowedBuildPlanHashes: []string{record.Build.PlanHash}, Enabled: true}
		if record.Workload.JobWorkflowRef != "" {
			draft.JobWorkflowRefs = []string{record.Workload.JobWorkflowRef}
		}
		if record.Build.PlanHash == "" {
			draft.AllowedBuildPlanHashes = nil
		}
		result, err := e.server.Policies.Apply(ctx, run.ProjectID, run.CreatedBy, workflowKey(run.ID, "policy", record.ServiceKey), deploymentpolicyv1.ApplyRequest{Draft: draft})
		if err != nil {
			return nil, err
		}
		refs = append(refs, deploymentworkflow.Checkpoint(deploymentworkflow.AuthorityDeploymentPolicy, result.Policy.ID, result.Policy.Revision, result.Policy.StateHash, deploymentworkflow.StatePreflighting))
	}
	return refs, nil
}

func (e deploymentWorkflowExecutor) resolvePreflights(ctx context.Context, run deploymentworkflow.Run, acknowledge bool) ([]deploymentv1.PreflightResult, error) {
	batch := make([]string, 0, len(run.Plan.Applications))
	for _, app := range run.Plan.Applications {
		batch = append(batch, app.Key)
	}
	buildRecordIDs := run.Refs.IDs(deploymentworkflow.AuthorityBuildRecord)
	results := make([]deploymentv1.PreflightResult, 0, len(buildRecordIDs))
	for _, recordID := range buildRecordIDs {
		request := deploymentv1.CreateRequest{SchemaVersion: deploymentv1.JobSchemaVersion, BuildRecordID: recordID, EnvironmentID: run.Plan.Target.EnvironmentID, DeploymentBatch: batch}
		preflight, err := e.server.runPreflight(ctx, run.ProjectID, request)
		if err != nil {
			return nil, err
		}
		if acknowledge {
			request.WarningAcknowledgements = preflight.WarningIDs()
		}
		results = append(results, preflight)
	}
	return results, nil
}

func (e deploymentWorkflowExecutor) deploy(ctx context.Context, run deploymentworkflow.Run) (deploymentworkflow.StepResult, error) {
	starter, ok := e.server.Registry.(immutableDeploymentStarter)
	reader, readable := e.server.Registry.(immutableDeploymentReader)
	if !ok || !readable {
		return failedStep("DEPLOYMENT_UNAVAILABLE", "The immutable deployment authority is unavailable.", "Restore Cloud deployment storage and retry.", true), nil
	}
	preflights, err := e.resolvePreflights(ctx, run, true)
	if err != nil {
		return workflowFailure(err, "PREFLIGHT_REFRESH_FAILED", "Refresh the run and review current preflight facts."), err
	}
	if hash := workflowPreflightHash(preflights); hash != run.PreflightHash {
		return deploymentworkflow.StepResult{Stale: true, FailureMessage: "Preflight facts changed after review.", NextAction: "Review and approve the current plan again."}, nil
	}
	refs := deploymentworkflow.AuthorityRefs{}
	pending, rollbackEligible := false, false
	buildRecordIDs := run.Refs.IDs(deploymentworkflow.AuthorityBuildRecord)
	deploymentJobIDs := run.Refs.IDs(deploymentworkflow.AuthorityDeploymentJob)
	for index, recordID := range buildRecordIDs {
		var job registry.DeploymentJob
		if index < len(deploymentJobIDs) {
			job, err = reader.GetDeployment(run.ProjectID, deploymentJobIDs[index])
			if err == nil && job.Status == deploymentv1.StateFailed && run.Attempt > 1 {
				job, _, err = reader.RetryDeployment(run.ProjectID, job.ID, workflowKey(run.ID, "deployment-retry", fmt.Sprintf("%s-attempt-%d", job.ID, run.Attempt)), run.ID)
			}
		} else {
			request := deploymentv1.CreateRequest{SchemaVersion: deploymentv1.JobSchemaVersion, BuildRecordID: recordID, EnvironmentID: run.Plan.Target.EnvironmentID, DeploymentBatch: applicationKeys(run), ExpectedPreflightHash: preflights[index].PreflightHash, WarningAcknowledgements: preflights[index].WarningIDs(), IdempotencyKey: workflowKey(run.ID, "deploy", recordID)}
			plan, authorityErr := e.server.Topology.Get(ctx, run.ProjectID)
			if authorityErr != nil {
				return workflowFailure(authorityErr, "TOPOLOGY_READ_FAILED", "Review current placement and retry."), authorityErr
			}
			record, authorityErr := e.server.BuildRecords.Get(ctx, run.ProjectID, recordID)
			if authorityErr != nil {
				return workflowFailure(authorityErr, "BUILD_RECORD_READ_FAILED", "Rebuild the application and retry."), authorityErr
			}
			configuration, authorityErr := e.server.Registry.GetServiceConfiguration(run.ProjectID, record.ServiceID)
			if authorityErr != nil {
				return workflowFailure(authorityErr, "CONFIGURATION_READ_FAILED", "Review the application configuration and retry."), authorityErr
			}
			decision, authorityErr := e.server.Policies.Route(ctx, run.ProjectID, deploymentpolicyv1.RoutingRequest{BuildRecordID: recordID, EnvironmentID: run.Plan.Target.EnvironmentID})
			if authorityErr != nil || !decision.Eligible {
				return workflowFailure(authorityErr, "ROUTING_FAILED", "Review the deployment policy and placement facts."), authorityErr
			}
			policy, authorityErr := e.server.Policies.Get(ctx, run.ProjectID, decision.DeploymentPolicyID)
			if authorityErr != nil {
				return workflowFailure(authorityErr, "POLICY_READ_FAILED", "Review the deployment policy and retry."), authorityErr
			}
			request.ExpectedTopologyRevision, request.ExpectedTopologyHash = plan.Revision, plan.PlanHash
			request.ExpectedConfigurationRevision, request.ExpectedConfigurationStateHash = configuration.Revision, configuration.StateHash
			request.ExpectedDeploymentPolicyRevision, request.ExpectedDeploymentPolicyHash = policy.Revision, policy.PolicyHash
			previewRequest := httptest.NewRequest("POST", "/api/projects/"+run.ProjectID+"/deployments", nil).WithContext(ctx)
			preview, previewErr := e.server.resolveDeploymentPreview(previewRequest, run.ProjectID, run.CreatedBy, request)
			if previewErr != nil {
				return workflowFailure(previewErr, "DEPLOYMENT_PREVIEW_FAILED", "Review the changed canonical authority and retry."), previewErr
			}
			job, _, err = starter.StartImmutableDeployment(preview.Snapshot, run.CreatedBy, request.IdempotencyKey, run.ID)
		}
		if err != nil {
			return workflowFailure(err, "DEPLOYMENT_START_FAILED", "Restore the target Agent and retry."), err
		}
		refs.Checkpoints = append(refs.Checkpoints, deploymentworkflow.Checkpoint(deploymentworkflow.AuthorityDeploymentJob, job.ID, job.RolloutVersion, firstNonEmpty(job.RolloutStateHash, job.SpecHash), deploymentworkflow.StateDeploying))
		switch job.Status {
		case deploymentv1.StateSucceeded:
			rollbackEligible = rollbackEligible || job.RollbackEligible
		case deploymentv1.StateFailed, deploymentv1.StateCancelled:
			failedRun := run
			mergeWorkflowRefs(&failedRun.Refs, refs)
			if cancelErr := e.server.cancelWorkflowDeployments(run.ProjectID, failedRun, run.ID); cancelErr != nil {
				return failedStep("FAIL_FAST_CANCEL_FAILED", "A service failed and another rollout could not be stopped safely.", "Inspect active deployment jobs and stop the remaining rollout before retrying.", false), cancelErr
			}
			return deploymentworkflow.StepResult{Refs: refs, RollbackRequired: rollbackEligible && run.Plan.FailurePolicy.RollbackKnownGood, CleanupRequired: !rollbackEligible, FailureCode: firstNonEmpty(job.FailureCode, "DEPLOYMENT_FAILED"), FailureMessage: firstNonEmpty(job.FailureMessageRedacted, "An immutable deployment failed."), NextAction: "Inspect the factual deployment evidence before creating a new run."}, nil
		default:
			pending = true
		}
	}
	return deploymentworkflow.StepResult{Pending: pending, Refs: refs}, nil
}

func (e deploymentWorkflowExecutor) verify(ctx context.Context, run deploymentworkflow.Run) (deploymentworkflow.StepResult, error) {
	refs := deploymentworkflow.AuthorityRefs{}
	apps, err := e.server.Registry.ListServices(run.ProjectID)
	if err != nil {
		return workflowFailure(err, "VERIFICATION_AUTHORITY_UNAVAILABLE", "Restore application facts and retry."), err
	}
	byName := map[string]registry.ServiceRecord{}
	for _, app := range apps {
		byName[app.Name] = app
	}
	reader, readable := e.server.Registry.(immutableDeploymentReader)
	if !readable {
		return failedStep("VERIFICATION_DEPLOYMENT_AUTHORITY_MISSING", "Deployment facts are unavailable for fail-closed verification.", "Restore deployment authority storage and retry.", true), nil
	}
	deploymentByService := map[string]string{}
	for _, jobID := range run.Refs.IDs(deploymentworkflow.AuthorityDeploymentJob) {
		job, getErr := reader.GetDeployment(run.ProjectID, jobID)
		if getErr != nil {
			return workflowFailure(getErr, "VERIFICATION_DEPLOYMENT_READ_FAILED", "Restore deployment authority storage and retry."), getErr
		}
		deploymentByService[job.ServiceID] = job.ID
	}
	buildByService := map[string]buildrecordv1.Record{}
	for _, recordID := range run.Refs.IDs(deploymentworkflow.AuthorityBuildRecord) {
		record, getErr := e.server.BuildRecords.Get(ctx, run.ProjectID, recordID)
		if getErr != nil {
			return workflowFailure(getErr, "VERIFICATION_BUILD_RECORD_MISSING", "Restore immutable build records and retry."), getErr
		}
		buildByService[record.ServiceID] = record
	}
	for _, application := range run.Plan.Applications {
		service := byName[application.Key]
		if service.ID == "" {
			return failedStep("VERIFICATION_APPLICATION_MISSING", "A planned application is missing from canonical authority.", "Analyze and review the repository again.", false), nil
		}
		jobID := deploymentByService[service.ID]
		if jobID == "" {
			return failedStep("VERIFICATION_DEPLOYMENT_MISSING", "A planned application has no factual deployment job.", "Retry the deployment step.", true), nil
		}
		job, getErr := reader.GetDeployment(run.ProjectID, jobID)
		if getErr != nil {
			return workflowFailure(getErr, "VERIFICATION_DEPLOYMENT_READ_FAILED", "Restore deployment authority storage and retry."), getErr
		}
		record := buildByService[service.ID]
		if record.ID == "" {
			return failedStep("VERIFICATION_BUILD_RECORD_MISSING", "A planned application has no immutable BuildRecord.", "Rebuild the application.", true), nil
		}
		if job.Status != deploymentv1.StateSucceeded || job.TerminalResult == nil || job.TerminalResult.Status != deploymentv1.StateSucceeded || job.TerminalResult.AvailableReplicas < 1 || job.TerminalResult.ReadinessEvidenceHash == "" {
			return failedStep("VERIFICATION_ROLLOUT_UNHEALTHY", "Application rollout health is not factually successful.", "Inspect the deployment job readiness evidence and retry.", true), nil
		}
		if record.Build.OCIDigest == "" || !strings.HasSuffix(job.TerminalResult.ApplicationImageID, record.Build.OCIDigest) || job.TerminalResult.CurrentDigest != record.Build.OCIDigest {
			return failedStep("VERIFICATION_IMAGE_DIGEST_MISMATCH", "Kubernetes imageID does not match the exact immutable BuildRecord digest.", "Inspect registry and rollout image facts, then redeploy the exact BuildRecord.", false), nil
		}
	}
	for _, dependency := range run.Plan.Dependencies {
		if !dependency.Required {
			continue
		}
		consumer := byName[dependency.From]
		if consumer.ID == "" {
			return failedStep("VERIFICATION_CONSUMER_MISSING", "A required dependency consumer is missing.", "Analyze and review dependency ownership again.", false), nil
		}
		deploymentID := deploymentByService[consumer.ID]
		if deploymentID == "" {
			return failedStep("VERIFICATION_DEPLOYMENT_MISSING", "A required dependency consumer has no successful deployment.", "Retry the deployment step.", true), nil
		}
		if dependency.Protocol != "postgres" && dependency.Protocol != "redis" && dependency.Verification == nil {
			return failedStep("VERIFICATION_CONTRACT_MISSING", "A required dependency has no verification contract.", "Add a verification contract in Review plan.", false), nil
		}
		verification, verifyErr := e.server.ExecuteDependencyVerification(ctx, run.ProjectID, run.Plan.Target.EnvironmentID, consumer.ID, verificationv1.VerifyDependencyRequest{DependencyLogicalName: dependencyLogicalName(dependency), DeploymentJobID: deploymentID}, run.CreatedBy)
		if verifyErr != nil {
			return workflowFailure(verifyErr, "VERIFICATION_FAILED", "Inspect dependency and workload health evidence, then retry."), verifyErr
		}
		refs.Checkpoints = append(refs.Checkpoints, deploymentworkflow.Checkpoint(deploymentworkflow.AuthorityVerification, verification.ID, 0, authorityStateHash(verification), deploymentworkflow.StateVerifying))
		if verification.OverallStatus != verificationv1.RunStatusVerified {
			code := firstNonEmpty(verification.FailureCode, "VERIFICATION_INCOMPLETE")
			message := "Post-deployment verification did not produce VERIFIED evidence."
			return failedStep(code, message, "Configure the missing consumer assertion or inspect failed dependency evidence, then retry.", true), nil
		}
	}
	return deploymentworkflow.StepResult{Refs: refs}, nil
}

func workflowPreflightHash(values []deploymentv1.PreflightResult) string {
	type fact struct {
		Hash     string
		Warnings []string
	}
	facts := make([]fact, 0, len(values))
	for _, value := range values {
		warnings := value.WarningIDs()
		sort.Strings(warnings)
		facts = append(facts, fact{Hash: value.PreflightHash, Warnings: warnings})
	}
	sort.Slice(facts, func(i, j int) bool { return facts[i].Hash < facts[j].Hash })
	data, _ := json.Marshal(facts)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func workflowSecretID(projectID, serviceID, name string) string {
	sum := sha256.Sum256([]byte(projectID + "\x00" + serviceID + "\x00" + name))
	return "wsecret-" + hex.EncodeToString(sum[:12])
}

func workflowKey(runID, authority, subject string) string {
	sum := sha256.Sum256([]byte(authority + "\x00" + subject))
	return runID + "-" + authority + "-" + hex.EncodeToString(sum[:6])
}

func applicationKeys(run deploymentworkflow.Run) []string {
	values := make([]string, 0, len(run.Plan.Applications))
	for _, app := range run.Plan.Applications {
		values = append(values, app.Key)
	}
	return values
}

func applicationExposure(run deploymentworkflow.Run, key string) string {
	for _, application := range run.Plan.Applications {
		if application.Key == key && application.Exposure.Mode != "" {
			return application.Exposure.Mode
		}
	}
	if run.Plan.Target.Exposure != "public" {
		return run.Plan.Target.Exposure
	}
	for _, dependency := range run.Plan.Dependencies {
		if dependency.From == key && dependency.Strategy == "same_origin" {
			return "public"
		}
	}
	if len(run.Plan.Applications) == 1 {
		return "public"
	}
	return "internal"
}

func applicationHostname(run deploymentworkflow.Run, key string) string {
	for _, application := range run.Plan.Applications {
		if application.Key == key && application.Exposure.Hostname != "" {
			return application.Exposure.Hostname
		}
	}
	return run.Plan.Target.Hostname
}

func authorityStateHash(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func mergeWorkflowRefs(destination *deploymentworkflow.AuthorityRefs, additional deploymentworkflow.AuthorityRefs) {
	seen := map[string]bool{}
	for _, checkpoint := range destination.Checkpoints {
		seen[checkpoint.Kind+"\x00"+checkpoint.ID] = true
	}
	for _, checkpoint := range additional.Checkpoints {
		key := checkpoint.Kind + "\x00" + checkpoint.ID
		if !seen[key] {
			destination.Checkpoints = append(destination.Checkpoints, checkpoint)
			seen[key] = true
		}
	}
}

func dependencyLogicalName(dependency repositoryanalysis.Dependency) string {
	if dependency.Strategy == "same_origin" {
		suffix := strings.Trim(strings.ReplaceAll(dependency.Path, "/", "-"), "-")
		if suffix != "" {
			return dependency.To + "-" + suffix
		}
	}
	return dependency.To
}

func failedStep(code, message, next string, retryable bool) deploymentworkflow.StepResult {
	return deploymentworkflow.StepResult{FailureCode: code, FailureMessage: message, NextAction: next, Retryable: retryable}
}

func workflowFailure(err error, code, next string) deploymentworkflow.StepResult {
	message := "The canonical authority rejected the workflow step."
	if err != nil {
		message = err.Error()
	}
	return failedStep(code, message, next, true)
}
