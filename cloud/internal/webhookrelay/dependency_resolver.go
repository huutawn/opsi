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
		deleted := res.Lifecycle == "deleted" || res.Lifecycle == "retiring" || res.Lifecycle == "retired" || res.Lifecycle == "deleting"
		host := ""
		port := int32(0)
		db := ""
		if res.Runtime != nil {
			host = res.Runtime.Spec.Connection.Host
			port = res.Runtime.Spec.Connection.Port
			db = res.Runtime.Spec.Connection.Database
		}
		return registry.DependencyTargetFacts{
			Exists:        true,
			ProjectID:     res.ProjectID,
			EnvironmentID: res.EnvironmentID,
			TargetKind:    "managed_resource",
			ResourceType:  string(res.Type),
			Lifecycle:     string(res.Lifecycle),
			Host:          host,
			Port:          port,
			Database:      db,
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
