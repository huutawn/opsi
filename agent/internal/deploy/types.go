package deploy

import (
	"time"
)

const (
	PhaseQueued   = "queued"
	PhasePulling  = "pulling"
	PhaseApplying = "applying"
	PhaseWatching = "watching"
	PhaseSuccess  = "success"
	PhaseRollback = "rollback"
	PhaseFailed   = "failed"

	StatusQueued              = "queued"
	StatusRunning             = "running"
	StatusSuccess             = "success"
	StatusRolledBack          = "rolled_back"
	StatusFailed              = "failed"
	StatusFailedAfterRollback = "failed_after_rollback"
)

type Record struct {
	DeployID              string
	ProjectID             string
	ServiceID             string
	ServiceName           string
	StartedAt             time.Time
	FinishedAt            time.Time
	GitSHA                string
	ImageTag              string
	Status                string
	Duration              time.Duration
	Error                 string
	TriggeredBy           string
	MigrationRan          bool
	RollbackSafe          bool
	RollbackReason        string
	SpecHash              string
	ImageID               string
	Namespace             string
	DeploymentName        string
	KubernetesServiceName string
	AvailableReplicas     int32
}

// ServiceRecord retains historical SQLite columns while executable deployment
// authority comes only from immutable AgentCommand and RolloutIntent values.
type ServiceRecord struct {
	ID                            string
	ProjectID                     string
	Name                          string
	Type                          string
	Namespace                     string
	RepoURL                       string
	Branch                        string
	BuildContext                  string
	Dockerfile                    string
	ManifestPath                  string
	WatchPaths                    []string
	ContainerPort                 int
	HealthPath                    string
	Replicas                      int
	TerminationGracePeriodSeconds int
	ResourceRequestsJSON          string
	ResourceLimitsJSON            string
	CurrentImage                  string
	Health                        string
	UpdatedAt                     time.Time
}
