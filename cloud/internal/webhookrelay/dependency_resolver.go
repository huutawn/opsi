package webhookrelay

import (
	"context"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/resource"
)

type DependencyResolverAdapter struct {
	Registry  registry.API
	Resources resource.Service
}

func (a DependencyResolverAdapter) ResolveDependencyTarget(ctx context.Context, projectID, targetIdentity, targetKind string) (registry.DependencyTargetFacts, error) {
	if targetKind == "managed_resource" {
		res, err := a.Resources.Get(ctx, projectID, targetIdentity)
		if err != nil {
			return registry.DependencyTargetFacts{}, nil
		}
		deleted := res.Lifecycle == "deleted" || res.Lifecycle == "retiring" || res.Lifecycle == "retired"
		return registry.DependencyTargetFacts{
			Exists:        true,
			ProjectID:     res.ProjectID,
			EnvironmentID: res.EnvironmentID,
			TargetKind:    "managed_resource",
			Deleted:       deleted,
		}, nil
	}

	apps, err := a.Registry.ListServices(projectID)
	if err != nil {
		return registry.DependencyTargetFacts{}, err
	}
	for _, app := range apps {
		if app.ID == targetIdentity {
			return registry.DependencyTargetFacts{
				Exists:        true,
				ProjectID:     app.ProjectID,
				EnvironmentID: app.EnvironmentID,
				TargetKind:    "application",
				Deleted:       app.Status == "deleted",
			}, nil
		}
	}
	return registry.DependencyTargetFacts{}, nil
}
