package actionplane

import (
	"context"
	"errors"

	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
)

type Runtime interface {
	CurrentState(context.Context, actionv1.TargetIdentity, actionv1.ActionKind, actionv1.ActionParameters) (actionv1.CurrentState, error)
	RestartWorkload(context.Context, actionv1.TargetIdentity) error
	ScaleWorkload(context.Context, actionv1.TargetIdentity, int32) error
	GatewayReconcile(context.Context, actionv1.TargetIdentity) error
	ResolveIncident(context.Context, actionv1.TargetIdentity, string) error
	PostCheck(context.Context, actionv1.TargetIdentity, actionv1.ActionKind, actionv1.ActionParameters, actionv1.CurrentState) (actionv1.CurrentState, error)
}

type approvedPrincipalKey struct{}

func withApprovedPrincipal(ctx context.Context, principal string) context.Context {
	return context.WithValue(ctx, approvedPrincipalKey{}, principal)
}
func ApprovedPrincipal(ctx context.Context) string {
	value, _ := ctx.Value(approvedPrincipalKey{}).(string)
	return value
}

func executeTyped(ctx context.Context, runtime Runtime, plan actionv1.ActionPlan) error {
	if runtime == nil {
		return errors.New("action runtime is unavailable")
	}
	switch plan.Kind {
	case actionv1.ActionRestartWorkload:
		return runtime.RestartWorkload(ctx, plan.Target)
	case actionv1.ActionScaleWorkload:
		return runtime.ScaleWorkload(ctx, plan.Target, plan.Parameters.ScaleWorkload.Replicas)
	case actionv1.ActionGatewayReconcile:
		return runtime.GatewayReconcile(ctx, plan.Target)
	case actionv1.ActionIncidentResolve:
		return runtime.ResolveIncident(ctx, plan.Target, plan.Parameters.IncidentResolve.IncidentID)
	default:
		return errors.New("action kind is not executable")
	}
}
