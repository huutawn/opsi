package deploy

import (
	"errors"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
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

var ErrLegacyDeploymentRetired = errors.New("LEGACY_DEPLOYMENT_RETIRED")

type Request struct {
	Production *deploymentv1.AgentCommand
}

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

func (r Request) Validate() error {
	if r.Production == nil {
		return ErrLegacyDeploymentRetired
	}
	if r.Production.SchemaVersion != deploymentv1.CommandSchemaVersion {
		return errors.New("unsupported production deployment command schema")
	}
	if err := r.Production.Image.Validate(); err != nil {
		return err
	}
	if err := r.Production.Workload.Validate(); err != nil {
		return err
	}
	hash, err := r.Production.Workload.Hash()
	if err != nil || hash != r.Production.SpecHash {
		return errors.New("production workload spec hash mismatch")
	}
	if r.Production.JobID == "" || r.Production.ProjectID == "" || r.Production.NodeID == "" || r.Production.AgentID == "" || r.Production.LeaseToken == "" {
		return errors.New("production deployment identity is incomplete")
	}
	if len(r.Production.Workload.SecretReferences) != 0 {
		return errors.New("SECRET_REFERENCE_UNRESOLVED")
	}
	return nil
}
