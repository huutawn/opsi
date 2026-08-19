package registry

import (
	"context"
	"fmt"
	"strconv"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
)

type DependencyRealizationProjection struct {
	EnvName        string `json:"env_name"`
	SymbolicSource string `json:"symbolic_source"`
	Sensitivity    string `json:"sensitivity"`
	ValuePreview   string `json:"value_preview"`
}

type DependencyRealizationPlanItem struct {
	LogicalName    string                            `json:"logical_name"`
	TargetKind     string                            `json:"target_kind"`
	TargetIdentity string                            `json:"target_identity"`
	Protocol       string                            `json:"protocol"`
	InjectionPhase string                            `json:"injection_phase"`
	ProviderType   string                            `json:"provider_type"`
	BindingAction  string                            `json:"binding_action"` // "create" | "reused"
	BindingID      string                            `json:"binding_id,omitempty"`
	Projections    []DependencyRealizationProjection `json:"projections"`
	Status         string                            `json:"status"` // "ready" | "pending_binding"
}

type DependencyReviewResult struct {
	Dependencies []DependencyRealizationPlanItem `json:"dependencies"`
	Conflicts    []string                        `json:"conflicts,omitempty"`
}

type DependencyApplyRequest struct {
	ExpectedRevision uint64 `json:"expected_revision,omitempty"`
}

type DependencyApplyResult struct {
	Realized []DependencyRealizationPlanItem `json:"realized"`
	Reused   bool                            `json:"reused"`
}

// PlanDependencyRealization builds the deterministic zero-mutation dependency realization plan.
func PlanDependencyRealization(ctx context.Context, config serviceconfigurationv1.Configuration, bindings []resourcev1.Binding, getTarget func(ctx context.Context, targetID string) (resourcev1.Resource, error)) (DependencyReviewResult, error) {
	result := DependencyReviewResult{
		Dependencies: make([]DependencyRealizationPlanItem, 0, len(config.Dependencies)),
		Conflicts:    []string{},
	}

	for _, dep := range config.Dependencies {
		item := DependencyRealizationPlanItem{
			LogicalName:    dep.LogicalName,
			TargetKind:     dep.TargetKind,
			TargetIdentity: dep.TargetIdentity,
			Protocol:       dep.Protocol,
			InjectionPhase: dep.InjectionPhase,
			Projections:    make([]DependencyRealizationProjection, 0, len(dep.InjectionMappings)),
		}

		if dep.TargetKind == "managed_resource" {
			target, err := getTarget(ctx, dep.TargetIdentity)
			if err != nil {
				return result, configurationError("DEPENDENCY_TARGET_NOT_FOUND", dep.TargetIdentity, "target resource not found")
			}
			item.ProviderType = string(target.Type)

			// Find matching binding
			var matchingBinding *resourcev1.Binding
			for i := range bindings {
				b := &bindings[i]
				if b.Target.ID == dep.TargetIdentity && b.LogicalName == dep.LogicalName && b.Protocol == resourcev1.Protocol(dep.Protocol) && b.Lifecycle != resourcev1.LifecycleDeleting {
					matchingBinding = b
					break
				}
			}

			if matchingBinding != nil {
				item.BindingAction = "reused"
				item.BindingID = matchingBinding.ID
				item.Status = "ready"
			} else {
				item.BindingAction = "create"
				item.Status = "pending_binding"
			}

			host := ""
			port := ""
			db := ""
			if target.Runtime != nil {
				host = target.Runtime.Spec.Connection.Host
				port = strconv.Itoa(int(target.Runtime.Spec.Connection.Port))
				db = target.Runtime.Spec.Connection.Database
			}

			for _, m := range dep.InjectionMappings {
				proj := DependencyRealizationProjection{
					EnvName:        m.EnvName,
					SymbolicSource: m.SymbolicSource,
				}
				switch m.SymbolicSource {
				case "resource.host":
					proj.Sensitivity = "non_secret"
					proj.ValuePreview = host
				case "resource.port":
					proj.Sensitivity = "non_secret"
					proj.ValuePreview = port
				case "credential.database":
					proj.Sensitivity = "non_secret"
					proj.ValuePreview = db
				case "credential.username":
					proj.Sensitivity = "secret"
					proj.ValuePreview = fmt.Sprintf("[managed %s role]", target.Type)
				case "credential.password":
					proj.Sensitivity = "secret"
					proj.ValuePreview = fmt.Sprintf("[managed %s password]", target.Type)
				case "connection.url":
					proj.Sensitivity = "secret"
					proj.ValuePreview = fmt.Sprintf("[managed %s connection url]", target.Type)
				}
				item.Projections = append(item.Projections, proj)
			}
		}

		result.Dependencies = append(result.Dependencies, item)
	}

	return result, nil
}
