package webhookrelay

import (
	"context"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/deploymentworkflow"
)

// RunDeploymentWorkflow resumes durable deployment runs until ctx is closed.
// Operational state remains in the canonical authorities referenced by each run.
func (s *Server) RunDeploymentWorkflow(ctx context.Context, workerID string) {
	controller := deploymentworkflow.Controller{Store: s.DeploymentRuns.Store, Executor: deploymentWorkflowExecutor{server: s}, WorkerID: workerID, LeaseDuration: 30 * time.Second}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if _, err := controller.RunOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			if s.observer != nil {
				s.observer.Inc("deployment_workflow_controller_errors_total")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
