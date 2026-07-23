package cloudrunner

import (
	"errors"
	"fmt"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

func RolloutIntentFromLease(lease cloudrelay.DeploymentLease, nodeID string) (deploymentv1.RolloutIntent, error) {
	if lease.Command == nil || lease.Command.Rollout == nil {
		return deploymentv1.RolloutIntent{}, errors.New("rollout command is required")
	}
	command := lease.Command
	intent, err := command.Rollout.Canonicalize()
	if err != nil {
		return deploymentv1.RolloutIntent{}, fmt.Errorf("invalid rollout intent: %w", err)
	}
	if command.SchemaVersion != deploymentv1.CommandSchemaVersion || command.JobID != lease.Deployment.ID || command.JobID != intent.Desired.DeploymentJobID ||
		command.LeaseToken == "" || command.LeaseToken != lease.LeaseToken || lease.Action != intent.Operation ||
		command.ProjectID != intent.Target.ProjectID || command.EnvironmentID != intent.Target.EnvironmentID ||
		command.RuntimeID != intent.Target.RuntimeID || command.NodeID != nodeID || command.NodeID != intent.Target.NodeID ||
		command.AgentID != intent.Target.AgentID || command.Attempt < intent.Attempt || command.Attempt < 1 {
		return deploymentv1.RolloutIntent{}, errors.New("rollout command target or attempt does not match its immutable intent")
	}
	commandHash, hashErr := command.Workload.Hash()
	if hashErr != nil || command.Image != intent.Desired.Image || command.SpecHash != intent.Desired.WorkloadSpecHash || commandHash != intent.Desired.WorkloadSpecHash {
		return deploymentv1.RolloutIntent{}, errors.New("rollout command workload or image does not match its immutable intent")
	}
	return intent, nil
}

func RequestFromLease(lease cloudrelay.DeploymentLease) (deploy.Request, error) {
	if lease.Command == nil {
		return deploy.Request{}, deploy.ErrLegacyDeploymentRetired
	}
	if lease.Command.Rollout != nil {
		return deploy.Request{}, errors.New("rollout command must use rollout reconciliation")
	}
	if lease.Command.JobID != lease.Deployment.ID || lease.Command.LeaseToken == "" || lease.Command.LeaseToken != lease.LeaseToken || lease.Action != "deploy" {
		return deploy.Request{}, errors.New("immutable deployment command does not match its lease")
	}
	request := deploy.Request{Production: lease.Command}
	return request, request.Validate()
}
