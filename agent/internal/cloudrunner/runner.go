package cloudrunner

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	"github.com/opsi-dev/opsi/agent/internal/config"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
)

type CloudClient interface {
	PollDeployment(context.Context, string, time.Duration) (*cloudrelay.DeploymentLease, error)
	CompleteDeployment(context.Context, string, string, cloudrelay.DeploymentResult) error
	Heartbeat(context.Context, string, cloudrelay.Heartbeat) error
}

type DeployEngine interface {
	Deploy(context.Context, deploy.Request, deploy.ProgressFunc) (deploy.Record, error)
}

type Runner struct {
	Client            CloudClient
	Engine            DeployEngine
	NodeID            string
	Version           string
	DeploymentConfig  config.DeploymentConfig
	PollInterval      time.Duration
	LongPollWait      time.Duration
	HeartbeatInterval time.Duration
	Logger            *slog.Logger
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
	go r.heartbeatLoop(ctx)
	return r.jobLoop(ctx)
}

func (r Runner) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(r.HeartbeatInterval)
	defer ticker.Stop()
	r.sendHeartbeat(ctx)
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
	err := r.Client.Heartbeat(ctx, r.NodeID, cloudrelay.Heartbeat{
		Version:      r.Version,
		NodeReady:    true,
		K3SStatus:    "ready",
		Capabilities: map[string]any{"deploy": true},
	})
	if err != nil {
		r.log().Warn("cloud heartbeat failed", "error", err)
	}
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
		lease, err := r.Client.PollDeployment(ctx, r.NodeID, r.LongPollWait)
		if err != nil {
			r.log().Warn("cloud deployment poll failed", "error", err)
			timer.Reset(r.PollInterval)
			continue
		}
		if lease != nil {
			r.handleLease(ctx, *lease)
		}
		timer.Reset(r.PollInterval)
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

func (r Runner) execute(ctx context.Context, lease cloudrelay.DeploymentLease) cloudrelay.DeploymentResult {
	if lease.Action != "" && lease.Action != "deploy" {
		return cloudrelay.DeploymentResult{Status: "failed", FailureCode: "ACTION_UNSUPPORTED", FailureMessageRedacted: "deployment action is not supported", RollbackEligible: false}
	}
	req, err := RequestFromLease(lease, r.DeploymentConfig)
	if err != nil {
		return cloudrelay.DeploymentResult{Status: "failed", FailureCode: failureCode(err), FailureMessageRedacted: err.Error(), RollbackEligible: false}
	}
	record, err := r.Engine.Deploy(ctx, req, func(event *deploy.ProgressEvent) error {
		r.log().Info("cloud deployment progress", "deployment_id", lease.Deployment.ID, "phase", event.Phase, "percent", event.Percent)
		return nil
	})
	if err != nil {
		if record.Status == "" {
			record.Status = deploy.StatusFailed
		}
		record.Error = err.Error()
	}
	return ResultFromRecord(record, err, lease)
}

func (r Runner) log() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}
