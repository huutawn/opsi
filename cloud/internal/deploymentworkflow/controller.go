package deploymentworkflow

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"time"
)

type StepResult struct {
	Refs                AuthorityRefs
	ReplaceBuildRefs    bool
	Pending             bool
	Stale               bool
	RollbackRequired    bool
	CleanupRequired     bool
	PreflightHash       string
	PreflightWarnings   []string
	Blocked             bool
	FailureCode         string
	FailureMessage      string
	NextAction          string
	Retryable           bool
	PublicRouteFailures []PublicRouteFailure
}

// Executor adapts the workflow to existing canonical authorities. It returns
// their object IDs and facts; it must not persist a second copy of their state.
type Executor interface {
	Execute(context.Context, Run, State) (StepResult, error)
}

type Controller struct {
	Store         Store
	Executor      Executor
	WorkerID      string
	LeaseDuration time.Duration
	Now           func() time.Time
}

func (c Controller) RunOnce(ctx context.Context) (int, error) {
	if c.Store == nil || c.Executor == nil || c.WorkerID == "" {
		return 0, errors.New("deployment workflow controller is not configured")
	}
	runs, err := c.Store.Runnable(ctx, 20)
	if err != nil {
		return 0, err
	}
	processed := 0
	var firstErr error
	for _, candidate := range runs {
		if err := ctx.Err(); err != nil {
			return processed, err
		}
		if candidate.RetryAfterAt != nil && candidate.RetryAfterAt.After(c.clock()) {
			continue
		}
		run, ok, err := c.Store.AcquireLease(ctx, candidate.ProjectID, candidate.ID, c.WorkerID, c.clock(), c.leaseDuration())
		if err != nil {
			return processed, err
		}
		if !ok {
			continue
		}
		if processErr := c.process(ctx, run); processErr != nil && firstErr == nil {
			firstErr = processErr
		}
		if releaseErr := c.Store.ReleaseLease(ctx, run.ProjectID, run.ID, c.WorkerID); releaseErr != nil && firstErr == nil {
			firstErr = releaseErr
		}
		processed++
	}
	return processed, firstErr
}
func (c Controller) process(ctx context.Context, run Run) error {
	if !Runnable(run.State) {
		return nil
	}
	step := run.State
	execCtx, cancel := context.WithCancel(ctx)
	renewed := make(chan error, 1)
	go c.renewLease(execCtx, cancel, run, renewed)
	result, execErr := c.Executor.Execute(execCtx, run, step)
	cancel()
	if renewalErr := <-renewed; renewalErr != nil {
		return renewalErr
	}
	expected := run.Revision
	previousRefs := run.Refs
	run.UpdatedAt = c.clock()
	event := Event{ID: "evt-controller-" + run.ID + "-" + strconv.FormatUint(run.Revision+1, 10), ProjectID: run.ProjectID, RunID: run.ID, State: step, Level: "info", CreatedAt: run.UpdatedAt}
	if result.ReplaceBuildRefs {
		removeCheckpointKinds(&run.Refs, AuthorityBuildJob, AuthorityBuildRecord)
	}
	mergeRefs(&run.Refs, result.Refs, step)
	if result.PublicRouteFailures != nil {
		run.PublicRouteFailures = append([]PublicRouteFailure(nil), result.PublicRouteFailures...)
	}
	if result.Stale {
		run.State = StateStale
		run.Approval = nil
		run.Failure = &Failure{Step: step, Code: "DEPLOYMENT_PLAN_STALE", Message: result.FailureMessage, NextAction: result.NextAction, Retryable: false}
		event.State = run.State
		event.Level = "error"
		event.Message = result.FailureMessage
		_, err := c.Store.Save(ctx, run, expected, event)
		return err
	}
	if execErr != nil || result.FailureCode != "" {
		if result.RollbackRequired {
			run.State = StateRollingBack
			run.Failure = &Failure{Step: step, Code: result.FailureCode, Message: result.FailureMessage, NextAction: result.NextAction, Retryable: false}
			event.State = run.State
			event.Level = "error"
			event.Message = "Deployment failed; known-good rollback started."
			_, err := c.Store.Save(ctx, run, expected, event)
			return err
		}
		if result.CleanupRequired {
			run.State = StateCleaningUp
			run.Failure = &Failure{Step: step, Code: result.FailureCode, Message: result.FailureMessage, NextAction: result.NextAction, Retryable: false}
			event.State = run.State
			event.Level = "error"
			event.Message = "First deployment failed; exact workload cleanup started. Persistent data is retained."
			_, err := c.Store.Save(ctx, run, expected, event)
			return err
		}
		if result.Retryable && run.Attempt < run.Plan.FailurePolicy.MaxAttempts {
			run.Attempt++
			delay := retryDelay(run.Attempt)
			retryAt := c.clock().Add(delay)
			run.RetryAfterAt = &retryAt
			run.Failure = nil
			event.Level = "warning"
			event.Message = "Retryable authority failure; bounded retry scheduled."
			event.Metadata = map[string]any{"attempt": run.Attempt, "retry_after": retryAt, "failure_code": result.FailureCode}
			_, err := c.Store.Save(ctx, run, expected, event)
			return err
		}
		run.State = StateFailed
		message := result.FailureMessage
		if message == "" {
			message = "The canonical authority rejected the workflow step."
		}
		code := result.FailureCode
		if code == "" {
			code = "DEPLOYMENT_STEP_FAILED"
		}
		run.Failure = &Failure{Step: step, Code: code, Message: message, NextAction: result.NextAction, Retryable: result.Retryable}
		event.State = run.State
		event.Level = "error"
		event.Message = message
		_, err := c.Store.Save(ctx, run, expected, event)
		return err
	}
	if result.Pending {
		if reflect.DeepEqual(previousRefs, run.Refs) {
			return nil
		}
		event.Message = "Waiting for the canonical authority to finish this step."
		_, err := c.Store.Save(ctx, run, expected, event)
		return err
	}
	switch step {
	case StateProvisioning:
		run.State = StateBuilding
		event.Message = "Resources and bindings are ready; build started."
	case StateBuilding:
		run.State = StatePreflighting
		event.Message = "Immutable build records are ready; preflight started."
	case StatePreflighting:
		run.PreflightHash = result.PreflightHash
		run.PreflightWarnings = append([]string(nil), result.PreflightWarnings...)
		if result.Blocked {
			run.State = StateFailed
			run.Failure = &Failure{Step: step, Code: "PREFLIGHT_BLOCKED", Message: "Preflight blocked deployment.", NextAction: result.NextAction, Retryable: false}
			event.Level = "error"
			event.Message = run.Failure.Message
		} else if len(result.PreflightWarnings) > 0 {
			run.State = StateAwaitingWarningAck
			event.Message = "Preflight warnings require acknowledgement."
		} else {
			run.State = StateDeploying
			event.Message = "Preflight passed; deployment started."
		}
	case StateDeploying:
		run.State = StateVerifying
		event.Message = "Deployment finished; verification started."
	case StateVerifying:
		now := c.clock()
		run.State = StateSucceeded
		run.FinishedAt = &now
		event.Message = "Deployment and verification succeeded."
	case StateRollingBack:
		now := c.clock()
		run.State = StateRolledBack
		run.FinishedAt = &now
		event.Message = "Known-good deployment restored."
	case StateCleaningUp:
		now := c.clock()
		run.State = StateFailed
		run.FinishedAt = &now
		event.Level = "error"
		event.Message = "Failed first-deploy workload and exposure were removed; persistent data was retained."
	}
	run.RetryAfterAt = nil
	event.State = run.State
	_, err := c.Store.Save(ctx, run, expected, event)
	return err
}

func (c Controller) renewLease(ctx context.Context, cancel context.CancelFunc, run Run, result chan<- error) {
	interval := c.leaseDuration() / 3
	if interval <= 0 || interval > 10*time.Second {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			result <- nil
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				result <- nil
				return
			}
			ok, err := c.Store.RenewLease(ctx, run.ProjectID, run.ID, c.WorkerID, c.clock(), c.leaseDuration())
			if err != nil {
				cancel()
				result <- err
				return
			}
			if !ok {
				cancel()
				result <- errors.New("deployment workflow lease was lost")
				return
			}
		}
	}
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second << min(attempt-1, 4)
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}
func mergeRefs(dst *AuthorityRefs, src AuthorityRefs, step State) {
	byIdentity := map[string]AuthorityCheckpoint{}
	for _, checkpoint := range append(append([]AuthorityCheckpoint(nil), dst.Checkpoints...), src.Checkpoints...) {
		if checkpoint.Kind == "" || checkpoint.ID == "" {
			continue
		}
		if checkpoint.Step == "" {
			checkpoint.Step = step
		}
		byIdentity[checkpoint.Kind+"\x00"+checkpoint.ID] = checkpoint
	}
	dst.Checkpoints = dst.Checkpoints[:0]
	for _, checkpoint := range byIdentity {
		dst.Checkpoints = append(dst.Checkpoints, checkpoint)
	}
	sort.Slice(dst.Checkpoints, func(i, j int) bool {
		return dst.Checkpoints[i].Kind+"\x00"+dst.Checkpoints[i].ID < dst.Checkpoints[j].Kind+"\x00"+dst.Checkpoints[j].ID
	})
}
func removeCheckpointKinds(refs *AuthorityRefs, kinds ...string) {
	removed := map[string]bool{}
	for _, kind := range kinds {
		removed[kind] = true
	}
	out := refs.Checkpoints[:0]
	for _, checkpoint := range refs.Checkpoints {
		if !removed[checkpoint.Kind] {
			out = append(out, checkpoint)
		}
	}
	refs.Checkpoints = out
}
func (c Controller) clock() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}
func (c Controller) leaseDuration() time.Duration {
	if c.LeaseDuration > 0 {
		return c.LeaseDuration
	}
	return 30 * time.Second
}
