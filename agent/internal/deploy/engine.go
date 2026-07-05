package deploy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type GitClient interface {
	Clone(ctx context.Context, repoURL, branch, gitSHA, dest string) error
}

type Builder interface {
	Build(ctx context.Context, workDir, dockerfile, imageTag string) error
	Push(ctx context.Context, imageTag string) error
}

type K3sAdapter interface {
	Apply(ctx context.Context, manifestPath, namespace, serviceName, imageTag string) error
	WatchRollout(ctx context.Context, service, namespace string, timeout, interval time.Duration) error
	Rollback(ctx context.Context, service, namespace string) error
}

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
}

type Engine struct {
	Store          Store
	Git            GitClient
	Builder        Builder
	K3s            K3sAdapter
	BuildRoot      string
	RolloutTimeout time.Duration
	PollInterval   time.Duration
}

func NewEngine(store Store, cfg EngineConfig) *Engine {
	return &Engine{
		Store:          store,
		Git:            cfg.Git,
		Builder:        cfg.Builder,
		K3s:            cfg.K3s,
		BuildRoot:      firstNonEmpty(cfg.BuildRoot, "/tmp/opsi-builds"),
		RolloutTimeout: durationOrDefault(cfg.RolloutTimeout, 10*time.Minute),
		PollInterval:   durationOrDefault(cfg.PollInterval, 5*time.Second),
	}
}

type EngineConfig struct {
	Git            GitClient
	Builder        Builder
	K3s            K3sAdapter
	BuildRoot      string
	RolloutTimeout time.Duration
	PollInterval   time.Duration
}

func (e *Engine) Deploy(ctx context.Context, req Request, progress ProgressFunc) (Record, error) {
	req = req.WithDefaults()
	if err := req.Validate(); err != nil {
		return Record{}, err
	}
	if e.Store == nil || e.Git == nil || e.Builder == nil || e.K3s == nil {
		return Record{}, errors.New("deployment engine is not fully configured")
	}
	if err := e.Store.UpsertService(ctx, ServiceRecordFromRequest(req)); err != nil {
		return Record{}, err
	}
	if existing, err := e.Store.FindSuccessful(ctx, req.ProjectID, req.ServiceID, req.GitSHA); err != nil {
		return Record{}, err
	} else if existing != nil {
		_ = emit(progress, *existing, PhaseSuccess, "deployment already succeeded for git sha", 100, nil)
		return *existing, nil
	}

	deployID, err := newDeployID()
	if err != nil {
		return Record{}, err
	}
	startedAt := time.Now().UTC()
	record := Record{
		DeployID:    deployID,
		ProjectID:   req.ProjectID,
		ServiceID:   req.ServiceID,
		ServiceName: req.ServiceName,
		StartedAt:   startedAt,
		GitSHA:      req.GitSHA,
		ImageTag:    req.ImageTag,
		Status:      StatusQueued,
		TriggeredBy: req.TriggeredBy,
	}
	if err := e.Store.Insert(ctx, record); err != nil {
		return Record{}, err
	}

	workDir := filepath.Join(e.BuildRoot, req.ProjectID, deployID)
	defer os.RemoveAll(workDir)

	if err := emit(progress, record, PhaseQueued, "deployment queued", 0, nil); err != nil {
		return record, err
	}
	record.Status = StatusRunning
	_ = e.Store.Update(ctx, record)

	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return e.fail(ctx, record, progress, PhaseFailed, StatusFailed, err)
	}
	if err := emit(progress, record, PhaseCloning, "cloning source", 10, nil); err != nil {
		return record, err
	}
	if err := e.Git.Clone(ctx, req.RepoURL, req.Branch, req.GitSHA, workDir); err != nil {
		return e.fail(ctx, record, progress, PhaseFailed, StatusFailed, err)
	}

	buildPath, err := safeRelPath(workDir, req.BuildContext)
	if err != nil {
		return e.fail(ctx, record, progress, PhaseFailed, StatusFailed, fmt.Errorf("build_context is invalid: %w", err))
	}
	dockerfilePath, err := safeRelPath(workDir, req.Dockerfile)
	if err != nil {
		return e.fail(ctx, record, progress, PhaseFailed, StatusFailed, fmt.Errorf("dockerfile is invalid: %w", err))
	}
	if err := emit(progress, record, PhaseBuilding, "building image", 35, nil); err != nil {
		return record, err
	}
	if err := e.Builder.Build(ctx, buildPath, dockerfilePath, req.ImageTag); err != nil {
		return e.fail(ctx, record, progress, PhaseFailed, StatusFailed, err)
	}
	if req.Registry != "" {
		if err := emit(progress, record, PhaseBuilding, "pushing image", 55, nil); err != nil {
			return record, err
		}
		if err := e.Builder.Push(ctx, req.ImageTag); err != nil {
			return e.fail(ctx, record, progress, PhaseFailed, StatusFailed, err)
		}
	}

	manifestPath, err := safeRelPath(workDir, req.ManifestPath)
	if err != nil {
		return e.fail(ctx, record, progress, PhaseFailed, StatusFailed, fmt.Errorf("manifest_path is invalid: %w", err))
	}
	renderedManifestPath := filepath.Join(workDir, ".opsi-rendered-manifest.yaml")
	if err := renderManifestFile(manifestPath, renderedManifestPath, manifestOptions{ResourceRequestsJSON: req.ResourceRequestsJSON, ResourceLimitsJSON: req.ResourceLimitsJSON, TerminationGracePeriodSeconds: req.TerminationGracePeriodSeconds, ContainerPort: req.ContainerPort, HealthPath: req.HealthPath, Replicas: req.Replicas, IngressEnabled: req.IngressEnabled, BindingDependencies: req.DependsOn}); err != nil {
		return e.fail(ctx, record, progress, PhaseFailed, StatusFailed, err)
	}
	if err := emit(progress, record, PhaseApplying, "applying manifest", 70, nil); err != nil {
		return record, err
	}
	if err := e.K3s.Apply(ctx, renderedManifestPath, req.Namespace, req.ServiceName, req.ImageTag); err != nil {
		return e.fail(ctx, record, progress, PhaseFailed, StatusFailed, err)
	}

	if err := emit(progress, record, PhaseWatching, "watching rollout", 85, nil); err != nil {
		return record, err
	}
	if err := e.K3s.WatchRollout(ctx, req.ServiceName, req.Namespace, e.RolloutTimeout, e.PollInterval); err != nil {
		var rolloutFailure RolloutFailure
		decision := ClassifyFailure(err.Error(), false, 0)
		if errors.As(err, &rolloutFailure) {
			decision = ClassifyRolloutFailure(rolloutFailure)
		}
		record.RollbackSafe = decision.RollbackSafe
		record.RollbackReason = decision.Reason
		if !decision.RollbackSafe {
			return e.fail(ctx, record, progress, PhaseFailed, StatusFailed, err)
		}
		_ = emit(progress, record, PhaseRollback, "rollout failed; rolling back", 90, err)
		rollbackErr := e.K3s.Rollback(ctx, req.ServiceName, req.Namespace)
		if rollbackErr != nil {
			return e.fail(ctx, record, progress, PhaseFailed, StatusFailedAfterRollback, fmt.Errorf("rollout: %w; rollback: %w", err, rollbackErr))
		}
		if verifyErr := e.K3s.WatchRollout(ctx, req.ServiceName, req.Namespace, e.RolloutTimeout, e.PollInterval); verifyErr != nil {
			return e.fail(ctx, record, progress, PhaseFailed, StatusFailedAfterRollback, fmt.Errorf("rollout: %w; rollback verification: %w", err, verifyErr))
		}
		return e.fail(ctx, record, progress, PhaseRollback, StatusRolledBack, err)
	}

	record.Status = StatusSuccess
	record.FinishedAt = time.Now().UTC()
	record.Duration = record.FinishedAt.Sub(record.StartedAt)
	if err := e.Store.Update(ctx, record); err != nil {
		return record, err
	}
	return record, emit(progress, record, PhaseSuccess, "deployment succeeded", 100, nil)
}

func (e *Engine) fail(ctx context.Context, record Record, progress ProgressFunc, phase, status string, cause error) (Record, error) {
	record.Status = status
	record.FinishedAt = time.Now().UTC()
	record.Duration = record.FinishedAt.Sub(record.StartedAt)
	redacted := RedactSensitive(cause.Error())
	record.Error = redacted
	_ = e.Store.Update(ctx, record)
	_ = emit(progress, record, phase, redacted, 100, errors.New(redacted))
	return record, cause
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

func newDeployID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "dep_" + hex.EncodeToString(b[:]), nil
}
