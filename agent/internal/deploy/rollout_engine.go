package deploy

import (
	"context"
	"errors"
	"fmt"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

// ReconcileRollout executes or resumes one durable runtime rollout. The WAL is
// written after read-only ownership preflight and before the first mutation.
func (e *Engine) ReconcileRollout(ctx context.Context, intent deploymentv1.RolloutIntent, progress ProgressFunc) (deploymentv1.RolloutRecord, error) {
	if e.Store == nil || e.Reconciler == nil {
		return deploymentv1.RolloutRecord{}, errors.New("rollout reconciliation is not configured")
	}
	canonical, err := intent.Canonicalize()
	if err != nil {
		return deploymentv1.RolloutRecord{}, preMutationRolloutFailure(err, deploymentv1.RolloutCodeInvalid)
	}
	existing, err := e.Store.GetRollout(ctx, canonical.RolloutID)
	if err != nil {
		return deploymentv1.RolloutRecord{}, err
	}
	if existing != nil {
		if existing.Intent.IntentHash != canonical.IntentHash {
			return deploymentv1.RolloutRecord{}, preMutationRolloutFailure(deploymentv1.NewRolloutError(deploymentv1.RolloutCodeConflict, "rollout id already has a different intent", false), deploymentv1.RolloutCodeConflict)
		}
		if existing.TerminalAt != nil {
			return *existing, nil
		}
		if existing.Intent.Operation == deploymentv1.RolloutOperationRollback && existing.State == deploymentv1.RolloutStatePrepared {
			return e.startExplicitRollback(context.WithoutCancel(ctx), *existing, progress)
		}
		return e.resumeRollout(ctx, *existing, nil, progress)
	}
	if err := e.validatePreviousKnownGood(ctx, canonical); err != nil {
		return deploymentv1.RolloutRecord{}, preMutationRolloutFailure(err, deploymentv1.RolloutCodeConflict)
	}
	plan, err := e.Reconciler.PrepareRollout(ctx, canonical.Desired)
	if err != nil {
		return deploymentv1.RolloutRecord{}, preMutationRolloutFailure(err, deploymentv1.RolloutCodePreflightFailed)
	}
	record, err := e.Store.BeginRollout(ctx, canonical, planObservedIdentities(plan))
	if err != nil {
		return deploymentv1.RolloutRecord{}, preMutationRolloutFailure(err, deploymentv1.RolloutCodePreflightFailed)
	}
	if record.TerminalAt != nil {
		return *record, nil
	}
	_ = emitRolloutProgress(progress, *record, PhaseQueued, "durable rollout prepared", 20, nil)
	if record.Intent.Operation == deploymentv1.RolloutOperationRollback && record.State == deploymentv1.RolloutStatePrepared {
		return e.startExplicitRollback(context.WithoutCancel(ctx), *record, progress)
	}
	return e.resumeRollout(ctx, *record, &plan, progress)
}

// ReconcilePending scans a bounded set of WAL records after Agent restart and
// resumes the same rollout IDs from factual runtime state.
func (e *Engine) ReconcilePending(ctx context.Context, progress ProgressFunc) ([]deploymentv1.RolloutRecord, error) {
	if e.Store == nil || e.Reconciler == nil {
		return nil, errors.New("rollout reconciliation is not configured")
	}
	records, err := e.Store.ListNonTerminalRollouts(ctx, 64)
	if err != nil {
		return nil, err
	}
	results := make([]deploymentv1.RolloutRecord, 0, len(records))
	for _, record := range records {
		var result deploymentv1.RolloutRecord
		var reconcileErr error
		if record.Intent.Operation == deploymentv1.RolloutOperationRollback && record.State == deploymentv1.RolloutStatePrepared {
			result, reconcileErr = e.startExplicitRollback(context.WithoutCancel(ctx), record, progress)
		} else {
			result, reconcileErr = e.resumeRollout(ctx, record, nil, progress)
		}
		results = append(results, result)
		if reconcileErr != nil {
			return results, reconcileErr
		}
	}
	return results, nil
}

func (e *Engine) startExplicitRollback(ctx context.Context, record deploymentv1.RolloutRecord, progress ProgressFunc) (deploymentv1.RolloutRecord, error) {
	transitioned, err := e.Store.TransitionRollout(ctx, record.Intent.RolloutID, deploymentv1.RolloutStateRollingBack, nil, record.Resources, nil, false)
	if err != nil {
		return record, err
	}
	_ = emitRolloutProgress(progress, *transitioned, PhaseRollback, "restoring exact previous known-good snapshot", 40, nil)
	return e.resumeRollback(ctx, *transitioned, progress)
}

func (e *Engine) resumeRollout(ctx context.Context, record deploymentv1.RolloutRecord, plan *RolloutPlan, progress ProgressFunc) (deploymentv1.RolloutRecord, error) {
	switch record.State {
	case deploymentv1.RolloutStatePrepared:
		if err := ctx.Err(); err != nil {
			failure := deploymentv1.NewRolloutError(deploymentv1.RolloutCodeCancelledBeforeMutation, "rollout cancelled before Kubernetes mutation", false)
			terminal, transitionErr := e.Store.TransitionRollout(context.WithoutCancel(ctx), record.Intent.RolloutID, deploymentv1.RolloutStateFailed, failure, record.Resources, nil, true)
			if transitionErr != nil {
				return record, transitionErr
			}
			return *terminal, err
		}
		transitioned, err := e.Store.TransitionRollout(ctx, record.Intent.RolloutID, deploymentv1.RolloutStateApplying, nil, record.Resources, nil, false)
		if err != nil {
			return record, err
		}
		record = *transitioned
		_ = emitRolloutProgress(progress, record, PhaseApplying, "applying authoritative runtime snapshot", 40, nil)
		fallthrough
	case deploymentv1.RolloutStateApplying:
		mutationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.RolloutTimeout)
		defer cancel()
		if plan == nil {
			prepared, err := e.Reconciler.PrepareRollout(mutationCtx, record.Intent.Desired)
			if err != nil {
				return e.failAndRollback(mutationCtx, record, err, progress)
			}
			plan = &prepared
		}
		resources, err := e.Reconciler.ApplyRollout(mutationCtx, *plan)
		if err != nil {
			return e.failAndRollback(mutationCtx, record, err, progress)
		}
		transitioned, err := e.Store.TransitionRollout(mutationCtx, record.Intent.RolloutID, deploymentv1.RolloutStateWaiting, nil, resources, nil, false)
		if err != nil {
			return record, err
		}
		record = *transitioned
		_ = emitRolloutProgress(progress, record, PhaseWatching, "waiting for factual workload and local routing readiness", 70, nil)
		fallthrough
	case deploymentv1.RolloutStateWaiting:
		readinessCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.RolloutTimeout)
		defer cancel()
		if plan == nil {
			prepared, err := e.Reconciler.PrepareRollout(readinessCtx, record.Intent.Desired)
			if err != nil {
				return e.failAndRollback(readinessCtx, record, err, progress)
			}
			plan = &prepared
		}
		evidence, resources, err := e.Reconciler.ObserveReadiness(readinessCtx, *plan)
		if err != nil {
			return e.failAndRollback(readinessCtx, record, err, progress)
		}
		if err := evidence.Validate(true, false); err != nil {
			return e.failAndRollback(readinessCtx, record, deploymentv1.NewRolloutError(deploymentv1.RolloutCodeReadinessFailed, err.Error(), false), progress)
		}
		evidenceHash, err := evidence.Hash()
		if err != nil {
			return record, err
		}
		snapshot := deploymentv1.KnownGoodSnapshot{SchemaVersion: deploymentv1.KnownGoodSchemaVersion, ID: record.Intent.RolloutID, Target: record.Intent.Target, Runtime: record.Intent.Desired, Resources: resources, EvidenceHash: evidenceHash, VerifiedAt: evidence.ObservedAt}
		snapshot, err = snapshot.Canonicalize()
		if err != nil {
			return e.failAndRollback(readinessCtx, record, deploymentv1.NewRolloutError(deploymentv1.RolloutCodeKnownGoodCorrupt, err.Error(), false), progress)
		}
		committed, err := e.Store.CommitRolloutSuccess(readinessCtx, record.Intent.RolloutID, snapshot, resources, evidence)
		if err != nil {
			return record, err
		}
		_ = emitRolloutProgress(progress, *committed, PhaseSuccess, "runtime snapshot verified and committed as known-good", 100, nil)
		return *committed, nil
	case deploymentv1.RolloutStateFailed:
		return e.rollback(context.WithoutCancel(ctx), record, progress)
	case deploymentv1.RolloutStateRollingBack:
		return e.resumeRollback(context.WithoutCancel(ctx), record, progress)
	default:
		if record.TerminalAt != nil {
			return record, nil
		}
		return record, deploymentv1.NewRolloutError(deploymentv1.RolloutCodeInvalidTransition, "stored rollout state is not reconcilable", false)
	}
}

func (e *Engine) failAndRollback(ctx context.Context, record deploymentv1.RolloutRecord, cause error, progress ProgressFunc) (deploymentv1.RolloutRecord, error) {
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.RolloutTimeout)
	defer cancel()
	previousExists := record.Intent.PreviousKnownGoodID != ""
	failure := boundedRolloutFailure(cause)
	if !previousExists {
		failure = deploymentv1.NewRolloutError(deploymentv1.RolloutCodeNoKnownGood, "desired runtime failed and no previous known-good snapshot exists", false)
	}
	failed, err := e.Store.TransitionRollout(recoveryCtx, record.Intent.RolloutID, deploymentv1.RolloutStateFailed, failure, record.Resources, nil, !previousExists)
	if err != nil {
		return record, err
	}
	_ = emitRolloutProgress(progress, *failed, PhaseFailed, failure.Error(), 80, failure)
	if !previousExists {
		return *failed, failure
	}
	return e.rollback(recoveryCtx, *failed, progress)
}

func (e *Engine) rollback(ctx context.Context, record deploymentv1.RolloutRecord, progress ProgressFunc) (deploymentv1.RolloutRecord, error) {
	rolling, err := e.Store.TransitionRollout(ctx, record.Intent.RolloutID, deploymentv1.RolloutStateRollingBack, record.Error, record.Resources, nil, false)
	if err != nil {
		return record, err
	}
	_ = emitRolloutProgress(progress, *rolling, PhaseRollback, "restoring exact previous known-good snapshot", 85, record.Error)
	return e.resumeRollback(ctx, *rolling, progress)
}

func (e *Engine) resumeRollback(ctx context.Context, record deploymentv1.RolloutRecord, progress ProgressFunc) (deploymentv1.RolloutRecord, error) {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.RolloutTimeout)
	defer cancel()
	previous, err := e.Store.GetKnownGood(rollbackCtx, record.Intent.PreviousKnownGoodID)
	if err != nil || previous == nil || previous.SnapshotHash != record.Intent.PreviousKnownGoodHash || previous.Target != record.Intent.Target {
		return e.rollbackFailed(rollbackCtx, record, deploymentv1.NewRolloutError(deploymentv1.RolloutCodeKnownGoodCorrupt, "previous known-good snapshot is missing or does not match the WAL reference", false), progress)
	}
	plan, err := e.Reconciler.PrepareRollout(rollbackCtx, previous.Runtime)
	if err != nil {
		return e.rollbackFailed(rollbackCtx, record, err, progress)
	}
	resources, err := e.Reconciler.ApplyRollout(rollbackCtx, plan)
	if err != nil {
		return e.rollbackFailed(rollbackCtx, record, err, progress)
	}
	evidence, resources, err := e.Reconciler.ObserveReadiness(rollbackCtx, plan)
	if err != nil {
		return e.rollbackFailed(rollbackCtx, record, err, progress)
	}
	if err := evidence.Validate(true, false); err != nil {
		return e.rollbackFailed(rollbackCtx, record, deploymentv1.NewRolloutError(deploymentv1.RolloutCodeReadinessFailed, "previous known-good did not pass post-rollback readiness", false), progress)
	}
	rolledBack, err := e.Store.TransitionRollout(rollbackCtx, record.Intent.RolloutID, deploymentv1.RolloutStateRolledBack, record.Error, resources, &evidence, true)
	if err != nil {
		return record, err
	}
	_ = emitRolloutProgress(progress, *rolledBack, PhaseRollback, "exact previous known-good snapshot restored", 100, record.Error)
	if record.Error != nil {
		return *rolledBack, record.Error
	}
	return *rolledBack, nil
}

func (e *Engine) rollbackFailed(ctx context.Context, record deploymentv1.RolloutRecord, cause error, progress ProgressFunc) (deploymentv1.RolloutRecord, error) {
	failure := boundedRolloutFailure(cause)
	failed, err := e.Store.TransitionRollout(ctx, record.Intent.RolloutID, deploymentv1.RolloutStateRollbackFailed, failure, record.Resources, nil, true)
	if err != nil {
		return record, err
	}
	_ = emitRolloutProgress(progress, *failed, PhaseFailed, "rollback failed: "+failure.Error(), 100, failure)
	return *failed, failure
}

func (e *Engine) validatePreviousKnownGood(ctx context.Context, intent deploymentv1.RolloutIntent) error {
	current, err := e.Store.CurrentKnownGood(ctx, intent.Target)
	if err != nil {
		return err
	}
	expectedID, expectedHash := intent.PreviousKnownGoodID, intent.PreviousKnownGoodHash
	if intent.Operation == deploymentv1.RolloutOperationRollback {
		expectedID, expectedHash = intent.ExpectedKnownGoodID, intent.ExpectedKnownGoodHash
	}
	if current == nil && expectedID == "" {
		return nil
	}
	if current == nil || current.ID != expectedID || current.SnapshotHash != expectedHash {
		return deploymentv1.NewRolloutError(deploymentv1.RolloutCodeConflict, "previous known-good reference is stale", false)
	}
	return nil
}

func planObservedIdentities(plan RolloutPlan) []deploymentv1.ResourceIdentity {
	result := make([]deploymentv1.ResourceIdentity, 0, len(plan.Observed))
	for _, observation := range plan.Observed {
		result = append(result, resourceIdentity(observation))
	}
	return result
}

func boundedRolloutFailure(err error) *deploymentv1.RolloutError {
	var failure *deploymentv1.RolloutError
	if errors.As(err, &failure) {
		return deploymentv1.NewRolloutError(failure.Code, RedactSensitive(failure.Message), failure.Retryable)
	}
	return deploymentv1.NewRolloutError(deploymentv1.RolloutCodeRuntimeFailed, RedactSensitive(err.Error()), false)
}

func preMutationRolloutFailure(err error, fallbackCode string) *deploymentv1.RolloutError {
	var typed *deploymentv1.RolloutError
	if errors.As(err, &typed) && typed.Code != "" {
		failure := deploymentv1.NewRolloutError(typed.Code, typed.Message, typed.Retryable)
		failure.FailurePhase = deploymentv1.FailurePhasePreMutation
		return failure
	}
	var storeFailure *rolloutStoreError
	if errors.As(err, &storeFailure) && storeFailure.Code != "" {
		failure := deploymentv1.NewRolloutError(storeFailure.Code, storeFailure.Msg, false)
		failure.FailurePhase = deploymentv1.FailurePhasePreMutation
		return failure
	}
	message := "rollout preflight failed"
	if err != nil {
		message = err.Error()
	}
	failure := deploymentv1.NewRolloutError(fallbackCode, message, false)
	failure.FailurePhase = deploymentv1.FailurePhasePreMutation
	return failure
}

func emitRolloutProgress(progress ProgressFunc, rollout deploymentv1.RolloutRecord, phase, message string, percent int32, err error) error {
	if progress == nil {
		return nil
	}
	record := rollout
	return progress(&ProgressEvent{OperationID: rollout.Intent.RolloutID, ProjectID: rollout.Intent.Target.ProjectID, ServiceID: rollout.Intent.Target.ServiceKey, ServiceName: rollout.Intent.Target.ServiceKey, Phase: phase, Message: message, Percent: percent, Err: err, Rollout: &record})
}

func rolloutErrorCode(err error) string {
	var failure *deploymentv1.RolloutError
	if errors.As(err, &failure) {
		return failure.Code
	}
	return fmt.Sprintf("%T", err)
}
