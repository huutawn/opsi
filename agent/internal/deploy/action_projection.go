package deploy

import (
	"context"
	"errors"

	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

// ActionProjection exposes only the authoritative immutable rollout snapshot to ActionPlane.
// It cannot create a deployment job or accept caller-supplied manifests.
type ActionProjection struct {
	Store interface {
		CurrentKnownGood(context.Context, deploymentv1.RuntimeTarget) (*deploymentv1.KnownGoodSnapshot, error)
	}
	Adapter ProductionAdapter
}

type ActionWorkloadIdentity struct {
	Namespace      string
	DeploymentName string
	ServiceName    string
	IngressName    string
	Selector       map[string]string
	Snapshot       *deploymentv1.KnownGoodSnapshot
}

func (p ActionProjection) WorkloadIdentity(ctx context.Context, target actionv1.TargetIdentity) (ActionWorkloadIdentity, error) {
	if p.Store == nil {
		return ActionWorkloadIdentity{}, errors.New("authoritative rollout store is unavailable")
	}
	snapshot, err := p.Store.CurrentKnownGood(ctx, deploymentv1.RuntimeTarget{ProjectID: target.ProjectID, EnvironmentID: target.EnvironmentID, RuntimeID: target.RuntimeID, ServiceKey: target.ServiceID, NodeID: target.NodeID})
	if err != nil {
		return ActionWorkloadIdentity{}, err
	}
	if snapshot == nil || !actionTargetMatches(snapshot.Target, target) || !actionTargetMatches(snapshot.Runtime.Target, target) {
		return ActionWorkloadIdentity{}, errors.New("authoritative workload snapshot not found")
	}
	_, resources, namespace, err := renderProductionResources(snapshot.Runtime.AgentCommand())
	if err != nil {
		return ActionWorkloadIdentity{}, err
	}
	ingressName := ""
	if snapshot.Runtime.HasExternalExposure() {
		ingressName = stableDNSName("opsi-ingress", snapshot.Runtime.Exposure.ServiceKey, snapshot.Runtime.Exposure.RuntimeID)
	}
	return ActionWorkloadIdentity{Namespace: namespace, DeploymentName: resources.DeploymentName, ServiceName: resources.ServiceName, IngressName: ingressName, Selector: cloneStringMap(resources.Selector), Snapshot: snapshot}, nil
}

func (p ActionProjection) ReconcileGateway(ctx context.Context, target actionv1.TargetIdentity) error {
	identity, err := p.WorkloadIdentity(ctx, target)
	if err != nil {
		return err
	}
	if !identity.Snapshot.Runtime.HasExternalExposure() {
		return errors.New("authoritative snapshot has no gateway exposure")
	}
	plan, err := p.Adapter.PrepareRollout(ctx, identity.Snapshot.Runtime)
	if err != nil {
		return err
	}
	for index, object := range plan.DesiredObjects {
		if object.Kind != "Ingress" {
			continue
		}
		_, err := p.Adapter.ApplyRollout(ctx, RolloutPlan{Snapshot: plan.Snapshot, Command: plan.Command, Resources: plan.Resources, Exposure: plan.Exposure, DesiredObjects: []rolloutObject{object}, Observed: []rolloutObservation{plan.Observed[index]}})
		return err
	}
	return errors.New("authoritative gateway object is missing")
}

func actionTargetMatches(snapshot deploymentv1.RuntimeTarget, target actionv1.TargetIdentity) bool {
	return snapshot.ProjectID == target.ProjectID && snapshot.NodeID == target.NodeID && snapshot.ServiceKey == target.ServiceID && snapshot.EnvironmentID == target.EnvironmentID && snapshot.RuntimeID == target.RuntimeID
}
