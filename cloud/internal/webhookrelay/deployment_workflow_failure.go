package webhookrelay

import (
	"context"

	"github.com/opsi-dev/opsi/cloud/internal/deploymentworkflow"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

func (e deploymentWorkflowExecutor) rollback(_ context.Context, run deploymentworkflow.Run) (deploymentworkflow.StepResult, error) {
	refs := deploymentworkflow.AuthorityRefs{}
	reader, ok := e.server.Registry.(immutableDeploymentReader)
	if !ok {
		return failedStep("ROLLBACK_UNAVAILABLE", "The immutable deployment authority is unavailable.", "Restore the exact known-good deployment manually.", false), nil
	}
	deploymentJobIDs := run.Refs.IDs(deploymentworkflow.AuthorityDeploymentJob)
	jobs := make([]registry.DeploymentJob, 0, len(deploymentJobIDs))
	for _, jobID := range deploymentJobIDs {
		job, err := reader.GetDeployment(run.ProjectID, jobID)
		if err != nil {
			return workflowFailure(err, "ROLLBACK_READ_FAILED", "Restore the exact known-good deployment manually."), err
		}
		jobs = append(jobs, job)
	}
	rollbacks := map[string]registry.DeploymentJob{}
	for _, job := range jobs {
		if job.Action == deploymentv1.RolloutOperationRollback {
			rollbacks[job.BaseDeploymentID] = job
		}
	}
	pending := false
	for _, source := range jobs {
		if source.Action != deploymentv1.RolloutOperationApply || !source.RollbackEligible {
			continue
		}
		job := rollbacks[source.ID]
		var err error
		if job.ID == "" {
			job, err = e.server.Registry.RollbackDeployment(run.ProjectID, source.ID, run.CreatedBy, workflowKey(run.ID, "rollback", source.ID), run.ID)
			if err != nil {
				return workflowFailure(err, "ROLLBACK_FAILED", "Restore the exact known-good deployment manually."), err
			}
		}
		refs.Checkpoints = append(refs.Checkpoints, deploymentworkflow.Checkpoint(deploymentworkflow.AuthorityDeploymentJob, job.ID, job.RolloutVersion, firstNonEmpty(job.RolloutStateHash, job.SpecHash), deploymentworkflow.StateRollingBack))
		if job.Status == deploymentv1.StateFailed || job.Status == deploymentv1.StateCancelled {
			return failedStep(firstNonEmpty(job.FailureCode, "ROLLBACK_FAILED"), "Known-good rollback failed.", "Restore the exact known-good deployment manually.", false), nil
		}
		pending = pending || job.Status != deploymentv1.StateSucceeded
	}
	return deploymentworkflow.StepResult{Pending: pending, Refs: refs}, nil
}

func (e deploymentWorkflowExecutor) cleanupFirstDeploy(_ context.Context, run deploymentworkflow.Run) (deploymentworkflow.StepResult, error) {
	store, ok := e.server.Registry.(firstDeployCleanupStore)
	reader, readable := e.server.Registry.(immutableDeploymentReader)
	if !ok || !readable {
		return failedStep("FIRST_DEPLOY_CLEANUP_UNAVAILABLE", "The canonical cleanup rollout authority is unavailable.", "Remove only the failed Opsi-owned workload and exposure; retain persistent data.", false), nil
	}
	jobs := []registry.DeploymentJob{}
	for _, id := range run.Refs.IDs(deploymentworkflow.AuthorityDeploymentJob) {
		job, err := reader.GetDeployment(run.ProjectID, id)
		if err != nil {
			return workflowFailure(err, "FIRST_DEPLOY_CLEANUP_READ_FAILED", "Restore deployment authority and retry cleanup."), err
		}
		jobs = append(jobs, job)
	}
	cleanups := map[string]registry.DeploymentJob{}
	for _, job := range jobs {
		if job.Action == deploymentv1.RolloutOperationFirstDeployCleanup {
			cleanups[job.BaseDeploymentID] = job
		}
	}
	refs := deploymentworkflow.AuthorityRefs{}
	pending := false
	for _, source := range jobs {
		if source.Action != deploymentv1.RolloutOperationApply || source.RollbackEligible {
			continue
		}
		cleanup := cleanups[source.ID]
		var err error
		if cleanup.ID == "" {
			cleanup, _, err = store.StartFirstDeployCleanup(run.ProjectID, run.CreatedBy, workflowKey(run.ID, "first-cleanup", source.ID), run.ID, source.ID)
			if err != nil {
				return workflowFailure(err, "FIRST_DEPLOY_CLEANUP_FAILED", "Inspect the exact failed deployment before manual cleanup."), err
			}
		}
		refs.Checkpoints = append(refs.Checkpoints, deploymentworkflow.Checkpoint(deploymentworkflow.AuthorityDeploymentJob, cleanup.ID, cleanup.RolloutVersion, firstNonEmpty(cleanup.RolloutStateHash, cleanup.SpecHash), deploymentworkflow.StateCleaningUp))
		if cleanup.Status == deploymentv1.StateFailed || cleanup.Status == deploymentv1.StateCancelled {
			return failedStep(firstNonEmpty(cleanup.FailureCode, "FIRST_DEPLOY_CLEANUP_FAILED"), "Failed rollout cleanup did not finish safely.", "Inspect the exact Opsi-owned objects; do not delete persistent data.", false), nil
		}
		pending = pending || cleanup.Status != deploymentv1.StateSucceeded
	}
	return deploymentworkflow.StepResult{Pending: pending, Refs: refs}, nil
}
