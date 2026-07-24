package cloudrunner

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
	"github.com/opsi-dev/opsi/agent/internal/nodelifecycle"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

type CloudClient interface {
	PollJob(context.Context, string, time.Duration) (*cloudrelay.JobLease, error)
	CompleteDeployment(context.Context, string, string, cloudrelay.DeploymentResult) error
	ProgressDeployment(context.Context, string, string, deploymentv1.Progress) error
	CompleteNodeLifecycle(context.Context, string, string, cloudrelay.NodeLifecycleResult) error
	Heartbeat(context.Context, string, cloudrelay.Heartbeat) error
}

type DeployEngine interface {
	ReconcileRollout(context.Context, deploymentv1.RolloutIntent, deploy.ProgressFunc) (deploymentv1.RolloutRecord, error)
	ReconcilePending(context.Context, deploy.ProgressFunc) ([]deploymentv1.RolloutRecord, error)
}

type ConnectionState struct {
	connected atomic.Bool
}

func (s *ConnectionState) SetConnected(connected bool) {
	if s != nil {
		s.connected.Store(connected)
	}
}

func (s *ConnectionState) Connected() bool {
	return s != nil && s.connected.Load()
}

type Runner struct {
	Client            CloudClient
	Engine            DeployEngine
	NodeLifecycle     NodeLifecycleExecutor
	NodeID            string
	Version           string
	PollInterval      time.Duration
	LongPollWait      time.Duration
	HeartbeatInterval time.Duration
	HealthProbe       HealthProbe
	ConnectionState   *ConnectionState
	Logger            *slog.Logger
}

type NodeLifecycleExecutor interface {
	Execute(context.Context, nodelifecycle.Request) nodelifecycle.Result
}

func (r Runner) Run(ctx context.Context) error {
	if r.Client == nil || r.Engine == nil {
		return errors.New("cloud runner client and engine are required")
	}
	if r.PollInterval <= 0 {
		r.PollInterval = 2 * time.Second
	}
	if r.LongPollWait <= 0 {
		r.LongPollWait = 30 * time.Second
	}
	if r.HeartbeatInterval <= 0 {
		r.HeartbeatInterval = 30 * time.Second
	}
	recoveryCtx, cancelRecovery := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	if _, err := r.Engine.ReconcilePending(recoveryCtx, nil); err != nil {
		r.log().Warn("pending rollout reconciliation failed", "error", err)
	}
	cancelRecovery()
	r.sendHeartbeat(ctx)
	go r.heartbeatLoop(ctx)
	return r.jobLoop(ctx)
}

func (r Runner) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(r.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.sendHeartbeat(ctx)
		}
	}
}

func (r Runner) sendHeartbeat(ctx context.Context) {
	health := ProbeRuntime(ctx, r.HealthProbe)
	err := r.Client.Heartbeat(ctx, r.NodeID, cloudrelay.Heartbeat{
		Version:      r.Version,
		NodeReady:    health.NodeReady,
		K3SStatus:    health.K3SStatus,
		Capabilities: map[string]any{"deploy": health.NodeReady && r.Engine != nil, "node_lifecycle": r.NodeLifecycle != nil},
	})
	if err != nil {
		r.ConnectionState.SetConnected(false)
		r.log().Warn("cloud heartbeat failed", "error", err)
		return
	}
	r.ConnectionState.SetConnected(true)
}

func (r Runner) jobLoop(ctx context.Context) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		lease, err := r.Client.PollJob(ctx, r.NodeID, r.LongPollWait)
		if err != nil {
			r.ConnectionState.SetConnected(false)
			r.log().Warn("cloud job poll failed", "error", err)
			timer.Reset(r.PollInterval)
			continue
		}
		r.ConnectionState.SetConnected(true)
		if lease != nil && lease.Deployment != nil {
			r.handleLease(ctx, *lease.Deployment)
		}
		if lease != nil && lease.NodeLifecycle != nil {
			r.handleNodeLifecycle(ctx, *lease.NodeLifecycle)
		}
		timer.Reset(r.PollInterval)
	}
}

func (r Runner) handleNodeLifecycle(ctx context.Context, lease cloudrelay.NodeLifecycleLease) {
	result := r.executeNodeLifecycle(ctx, lease)
	for attempt := 0; attempt < 3; attempt++ {
		err := r.Client.CompleteNodeLifecycle(ctx, r.NodeID, lease.ID, result)
		if err == nil {
			return
		}
		r.log().Warn("cloud node lifecycle result report failed", "job_id", lease.ID, "attempt", attempt+1, "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
}

func (r Runner) handleLease(ctx context.Context, lease cloudrelay.DeploymentLease) {
	result := r.execute(ctx, lease)
	for attempt := 0; attempt < 3; attempt++ {
		err := r.Client.CompleteDeployment(ctx, r.NodeID, lease.Deployment.ID, result)
		if err == nil {
			return
		}
		r.log().Warn("cloud deployment result report failed", "deployment_id", lease.Deployment.ID, "attempt", attempt+1, "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
}

func (r Runner) executeNodeLifecycle(ctx context.Context, lease cloudrelay.NodeLifecycleLease) cloudrelay.NodeLifecycleResult {
	if r.NodeLifecycle == nil {
		return cloudrelay.NodeLifecycleResult{Status: "unsupported", LeaseToken: lease.LeaseToken, FailureCode: "NODE_LIFECYCLE_UNSUPPORTED", FailureMessageRedacted: "node lifecycle executor is not configured"}
	}
	result := r.NodeLifecycle.Execute(ctx, nodelifecycle.Request{
		Action:         lease.Action,
		TargetNodeID:   lease.TargetNodeID,
		TargetNodeName: lease.TargetName,
		ConfirmRemove:  lease.ConfirmRemove,
	})
	return cloudrelay.NodeLifecycleResult{Status: result.Status, LeaseToken: lease.LeaseToken, FailureCode: result.FailureCode, FailureMessageRedacted: result.FailureMessageRedacted, Verified: result.Verified}
}

func (r Runner) execute(ctx context.Context, lease cloudrelay.DeploymentLease) cloudrelay.DeploymentResult {
	if lease.Command == nil || lease.Command.Rollout == nil {
		return deploymentFailure(lease, "LEGACY_DEPLOYMENT_RETIRED", "deployment commands without RolloutIntent are retired")
	}
	return r.executeRollout(ctx, lease)
}

func (r Runner) executeRollout(ctx context.Context, lease cloudrelay.DeploymentLease) cloudrelay.DeploymentResult {
	intent, err := RolloutIntentFromLease(lease, r.NodeID)
	if err != nil {
		return deploymentFailure(lease, "ROLLOUT_COMMAND_INVALID", err.Error())
	}
	var progressMu sync.Mutex
	var latest *deploymentv1.Progress
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				progressMu.Lock()
				if latest != nil {
					copy := *latest
					progressMu.Unlock()
					if err := r.Client.ProgressDeployment(ctx, r.NodeID, lease.Deployment.ID, copy); err != nil {
						r.log().Warn("cloud rollout lease heartbeat failed", "deployment_id", lease.Deployment.ID, "error", err)
					}
				} else {
					progressMu.Unlock()
				}
			}
		}
	}()
	record, reconcileErr := r.Engine.ReconcileRollout(ctx, intent, func(event *deploy.ProgressEvent) error {
		if event == nil || event.Rollout == nil {
			return nil
		}
		progress := progressFromRollout(*event.Rollout, lease.LeaseToken, event.Percent, event.Message)
		progressMu.Lock()
		latest = &progress
		progressMu.Unlock()
		if err := r.Client.ProgressDeployment(ctx, r.NodeID, lease.Deployment.ID, progress); err != nil {
			r.log().Warn("cloud rollout progress report failed", "deployment_id", lease.Deployment.ID, "state", progress.State, "error", err)
		}
		return nil
	})
	close(done)
	return resultFromRollout(intent, record, reconcileErr, lease)
}

func deploymentFailure(lease cloudrelay.DeploymentLease, code, message string) cloudrelay.DeploymentResult {
	result := cloudrelay.DeploymentResult{Status: "failed", LeaseToken: lease.LeaseToken, FailureCode: code, FailureMessageRedacted: deploy.RedactSensitive(message), RollbackEligible: false}
	if lease.Command != nil {
		result.SchemaVersion = deploymentv1.ResultSchemaVersion
		result.SpecHash = lease.Command.SpecHash
		result.ApplicationImage = lease.Command.Image.Reference
	}
	return result
}

func (r Runner) log() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}
