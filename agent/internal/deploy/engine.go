package deploy

import (
	"context"
	"errors"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

type ProgressFunc func(*ProgressEvent) error

type ProgressEvent struct {
	OperationID string
	ProjectID   string
	ServiceID   string
	ServiceName string
	Phase       string
	Message     string
	Percent     int32
	Err         error
	Rollout     *deploymentv1.RolloutRecord
}

type Engine struct {
	Store          Store
	Production     ProductionRuntime
	Reconciler     RolloutRuntime
	RolloutTimeout time.Duration
	PollInterval   time.Duration
}

type EngineConfig struct {
	Production     ProductionRuntime
	Reconciler     RolloutRuntime
	RolloutTimeout time.Duration
	PollInterval   time.Duration
}

func NewEngine(store Store, cfg EngineConfig) *Engine {
	engine := &Engine{
		Store:          store,
		Production:     cfg.Production,
		Reconciler:     cfg.Reconciler,
		RolloutTimeout: durationOrDefault(cfg.RolloutTimeout, 10*time.Minute),
		PollInterval:   durationOrDefault(cfg.PollInterval, 5*time.Second),
	}
	if engine.Reconciler == nil {
		if runtime, ok := cfg.Production.(RolloutRuntime); ok {
			engine.Reconciler = runtime
		}
	}
	return engine
}

func (e *Engine) Deploy(ctx context.Context, req Request, progress ProgressFunc) (Record, error) {
	if err := req.Validate(); err != nil {
		return Record{}, err
	}
	return e.deployProduction(ctx, req, progress)
}

func (e *Engine) deployProduction(ctx context.Context, req Request, progress ProgressFunc) (Record, error) {
	if e.Store == nil || e.Production == nil {
		return Record{}, errors.New("production deployment runtime is not configured")
	}
	command := *req.Production
	revisionKey := productionRevisionKey(command)
	if existing, err := e.Store.FindSuccessful(ctx, command.ProjectID, command.Workload.ServiceKey, revisionKey); err != nil {
		return Record{}, err
	} else if existing != nil {
		_ = emit(progress, *existing, PhaseSuccess, "deployment already reconciled for this job and spec", 100, nil)
		return *existing, nil
	}
	record := Record{DeployID: command.JobID, ProjectID: command.ProjectID, ServiceID: command.Workload.ServiceKey, ServiceName: command.Workload.ServiceKey, StartedAt: time.Now().UTC(), GitSHA: revisionKey, ImageTag: command.Image.Reference, Status: StatusQueued, TriggeredBy: "cloud", SpecHash: command.SpecHash}
	if err := e.Store.UpsertService(ctx, ServiceRecord{ID: record.ServiceID, ProjectID: record.ProjectID, Name: record.ServiceName, Type: "application", CurrentImage: command.Image.Reference, Health: "deploying", UpdatedAt: record.StartedAt}); err != nil {
		return Record{}, err
	}
	if err := e.Store.Insert(ctx, record); err != nil {
		return Record{}, err
	}
	result, err := e.Production.Deploy(ctx, command, progress)
	result.GitSHA = revisionKey
	result.SpecHash = command.SpecHash
	if updateErr := e.Store.Update(ctx, result); updateErr != nil && err == nil {
		return result, updateErr
	}
	return result, err
}

func productionRevisionKey(command deploymentv1.AgentCommand) string {
	return command.JobID + "#" + command.Image.Reference + "#" + command.SpecHash
}

func emit(progress ProgressFunc, record Record, phase, message string, percent int32, err error) error {
	if progress == nil {
		return nil
	}
	return progress(&ProgressEvent{OperationID: record.DeployID, ProjectID: record.ProjectID, ServiceID: record.ServiceID, ServiceName: record.ServiceName, Phase: phase, Message: message, Percent: percent, Err: err})
}

func durationOrDefault(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}
