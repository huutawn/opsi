package actionplane

import (
	"errors"
	"math"

	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
)

func Catalog() []actionv1.CatalogAction {
	return []actionv1.CatalogAction{
		{Kind: actionv1.ActionRestartWorkload, Risk: actionv1.RiskR2, Summary: "Restart an Opsi-owned Deployment"},
		{Kind: actionv1.ActionScaleWorkload, Risk: actionv1.RiskR2, Summary: "Scale an Opsi-owned Deployment"},
		{Kind: actionv1.ActionGatewayReconcile, Risk: actionv1.RiskR2, Summary: "Reconcile an authoritative Opsi gateway"},
		{Kind: actionv1.ActionIncidentResolve, Risk: actionv1.RiskR1, Summary: "Resolve one incident in its project"},
	}
}

func ValidateKind(kind actionv1.ActionKind) error {
	if !kind.Valid() {
		return errors.New("action is not in the v1 catalog")
	}
	return nil
}

func ValidateRisk(risk actionv1.RiskClass) error {
	if risk == actionv1.RiskR4 {
		return errors.New("R4 actions are prohibited")
	}
	if risk != actionv1.RiskR1 && risk != actionv1.RiskR2 && risk != actionv1.RiskR3 {
		return errors.New("unknown risk class")
	}
	return nil
}

func RiskFor(kind actionv1.ActionKind, parameters actionv1.ActionParameters, state actionv1.CurrentState) actionv1.RiskClass {
	switch kind {
	case actionv1.ActionIncidentResolve:
		return actionv1.RiskR1
	case actionv1.ActionScaleWorkload:
		if parameters.ScaleWorkload != nil && (parameters.ScaleWorkload.Replicas == 0 || state.Workload == nil || math.Abs(float64(parameters.ScaleWorkload.Replicas-state.Workload.DesiredReplicas)) > 5) {
			return actionv1.RiskR3
		}
		return actionv1.RiskR2
	case actionv1.ActionRestartWorkload, actionv1.ActionGatewayReconcile:
		return actionv1.RiskR2
	default:
		return actionv1.RiskR4
	}
}
