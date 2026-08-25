package registry

import (
	"context"
	"fmt"
	"strconv"

	resourcecompiler "github.com/opsi-dev/opsi/cloud/internal/resource/connection"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
)

type DependencyRealizationProjection struct {
	EnvName        string `json:"env_name"`
	SymbolicSource string `json:"symbolic_source"`
	Template       string `json:"template,omitempty"`
	Sensitivity    string `json:"sensitivity"`
	ValuePreview   string `json:"value_preview"`
}

type DependencyRealizationPlanItem struct {
	LogicalName    string                            `json:"logical_name"`
	TargetKind     string                            `json:"target_kind"`
	TargetIdentity string                            `json:"target_identity"`
	Protocol       string                            `json:"protocol"`
	Strategy       string                            `json:"strategy,omitempty"`
	AccessContext  string                            `json:"access_context,omitempty"`
	Path           string                            `json:"path,omitempty"`
	InjectionPhase string                            `json:"injection_phase"`
	ProviderType   string                            `json:"provider_type,omitempty"`
	BindingAction  string                            `json:"binding_action"` // "create" | "reused" | "none"
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
func PlanDependencyRealization(ctx context.Context, sourceServiceID string, config serviceconfigurationv1.Configuration, bindings []resourcev1.Binding, getTarget func(ctx context.Context, targetID string) (resourcev1.Resource, error), getAppTarget ...func(ctx context.Context, targetID string) (DependencyTargetFacts, error)) (DependencyReviewResult, error) {
	result := DependencyReviewResult{
		Dependencies: make([]DependencyRealizationPlanItem, 0, len(config.Dependencies)),
		Conflicts:    []string{},
	}

	var getApp func(ctx context.Context, targetID string) (DependencyTargetFacts, error)
	if len(getAppTarget) > 0 && getAppTarget[0] != nil {
		getApp = getAppTarget[0]
	}
	selectedBindings := make(map[string]string, len(config.ResourceBindings))
	for _, selected := range config.ResourceBindings {
		selectedBindings[selected.LogicalName] = selected.BindingID
	}

	for _, dep := range config.Dependencies {
		item := DependencyRealizationPlanItem{
			LogicalName:    dep.LogicalName,
			TargetKind:     dep.TargetKind,
			TargetIdentity: dep.TargetIdentity,
			Protocol:       dep.Protocol,
			Strategy:       dep.Strategy,
			AccessContext:  dep.AccessContext,
			Path:           dep.Path,
			InjectionPhase: dep.InjectionPhase,
			BindingAction:  "none",
			Status:         "ready",
			Projections:    make([]DependencyRealizationProjection, 0, len(dep.InjectionMappings)),
		}

		if dep.TargetKind == "managed_resource" {
			target, err := getTarget(ctx, dep.TargetIdentity)
			if err != nil {
				return result, configurationError("DEPENDENCY_TARGET_NOT_FOUND", dep.TargetIdentity, "target resource not found")
			}
			item.ProviderType = string(target.Type)

			selectedID := selectedBindings[dep.LogicalName]
			matched, bindingFound := resourcecompiler.SelectBinding(bindings, resourcecompiler.BindingIdentity{
				SourceServiceID: sourceServiceID, TargetResourceID: dep.TargetIdentity, LogicalName: dep.LogicalName,
				Protocol: dep.Protocol, Lifecycle: resourcev1.LifecycleReady, SelectedBindingID: selectedID,
			})
			var matchingBinding *resourcev1.Binding
			if bindingFound {
				matchingBinding = &matched
			} else if selectedID != "" {
				return result, configurationError("DEPENDENCY_BINDING_INVALID", dep.LogicalName, "selected resource binding no longer matches the dependency authority")
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
			if matchingBinding != nil && matchingBinding.Database != "" {
				db = matchingBinding.Database
			}

			for _, m := range dep.InjectionMappings {
				proj := DependencyRealizationProjection{
					EnvName:        m.EnvName,
					SymbolicSource: m.SymbolicSource,
					Template:       m.Template,
				}
				descriptor, lookupErr := resourcecompiler.LookupSource(dep.Protocol, m.SymbolicSource, m.Template)
				if lookupErr != nil {
					return result, configurationErrorCause("DEPENDENCY_SYMBOLIC_SOURCE_INVALID", m.EnvName, "connection mapping is invalid", lookupErr)
				}
				proj.Sensitivity = string(descriptor.Sensitivity)
				if descriptor.Sensitivity == resourcev1.ValueSecret {
					proj.ValuePreview = fmt.Sprintf("[managed %s %s · redacted]", target.Type, resourcecompiler.CanonicalSource(dep.Protocol, m.SymbolicSource))
				} else {
					compiled, compileErr := resourcecompiler.CompileConnection(dep.Protocol, m.SymbolicSource, m.Template, resourcecompiler.ConnectionFacts{Host: host, Port: port, Database: db})
					if compileErr != nil && item.BindingAction != "create" {
						return result, configurationErrorCause("DEPENDENCY_CONNECTION_COMPILE_FAILED", m.EnvName, "connection mapping could not be compiled", compileErr)
					}
					if compileErr == nil {
						proj.ValuePreview = compiled.Value
					}
				}
				item.Projections = append(item.Projections, proj)
			}
		} else if dep.TargetKind == "application" {
			item.BindingAction = "none"
			item.Status = "ready"
			var appFacts DependencyTargetFacts
			if getApp != nil {
				facts, err := getApp(ctx, dep.TargetIdentity)
				if err != nil || !facts.Exists || facts.Deleted {
					return result, configurationError("DEPENDENCY_TARGET_NOT_FOUND", dep.TargetIdentity, "target application not found")
				}
				appFacts = facts
			}

			internalHost := ""
			internalPort := strconv.Itoa(appFacts.ContainerPort)
			internalURL := ""
			if appFacts.ServiceKey != "" {
				internalHost = fmt.Sprintf("<internal-dns:%s>", appFacts.ServiceKey)
				internalURL = fmt.Sprintf("http://<internal-dns:%s>:%s", appFacts.ServiceKey, internalPort)
			}

			publicHost := ""
			publicPort := "443"
			publicScheme := "https"
			publicURL := ""
			targetPath := "/"
			if appFacts.PublicRoute != nil {
				publicHost = appFacts.PublicRoute.Hostname
				targetPath = appFacts.PublicRoute.Path
				if targetPath == "" {
					targetPath = "/"
				}
				if targetPath == "/" {
					publicURL = "https://" + publicHost
				} else {
					publicURL = "https://" + publicHost + targetPath
				}
			}

			for _, m := range dep.InjectionMappings {
				proj := DependencyRealizationProjection{
					EnvName:        m.EnvName,
					SymbolicSource: m.SymbolicSource,
					Template:       m.Template,
					Sensitivity:    "non_secret",
				}
				switch m.SymbolicSource {
				case "application.internal_url":
					proj.ValuePreview = internalURL
				case "application.internal_host":
					proj.ValuePreview = internalHost
				case "application.internal_port":
					proj.ValuePreview = internalPort
				case "application.public_url":
					proj.ValuePreview = publicURL
				case "application.public_host":
					proj.ValuePreview = publicHost
				case "application.public_port":
					proj.ValuePreview = publicPort
				case "application.public_scheme":
					proj.ValuePreview = publicScheme
				case "application.path":
					if dep.Strategy == serviceconfigurationv1.StrategySameOrigin && dep.Path != "" {
						proj.ValuePreview = dep.Path
					} else {
						proj.ValuePreview = targetPath
					}
				case "application.url":
					if dep.Strategy == serviceconfigurationv1.StrategySameOrigin {
						if dep.Path != "" {
							proj.ValuePreview = dep.Path
						} else {
							proj.ValuePreview = targetPath
						}
					} else if dep.Strategy == serviceconfigurationv1.StrategyPublicHTTP {
						proj.ValuePreview = publicURL
					} else if dep.Strategy == serviceconfigurationv1.StrategyInternalHTTP {
						proj.ValuePreview = internalURL
					}
				}
				item.Projections = append(item.Projections, proj)
			}
		}

		result.Dependencies = append(result.Dependencies, item)
	}

	return result, nil
}
