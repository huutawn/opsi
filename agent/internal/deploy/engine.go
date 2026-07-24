package deploy

import (
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
	Reconciler     RolloutRuntime
	RolloutTimeout time.Duration
	PollInterval   time.Duration
}

type EngineConfig struct {
	Reconciler     RolloutRuntime
	RolloutTimeout time.Duration
	PollInterval   time.Duration
}

func NewEngine(store Store, cfg EngineConfig) *Engine {
	engine := &Engine{
		Store:          store,
		Reconciler:     cfg.Reconciler,
		RolloutTimeout: durationOrDefault(cfg.RolloutTimeout, 10*time.Minute),
		PollInterval:   durationOrDefault(cfg.PollInterval, 5*time.Second),
	}
	return engine
}

func durationOrDefault(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}
