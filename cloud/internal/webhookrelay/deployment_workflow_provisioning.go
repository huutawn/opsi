package webhookrelay

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/opsi-dev/opsi/cloud/internal/deploymentworkflow"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/repositoryanalysis"
	"github.com/opsi-dev/opsi/cloud/internal/resource"
	"github.com/opsi-dev/opsi/cloud/internal/topology"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

var errWorkloadSecretStale = errors.New("workload secret changed after approval")

func (e deploymentWorkflowExecutor) provision(ctx context.Context, run deploymentworkflow.Run) (deploymentworkflow.StepResult, error) {
	apps, err := e.ensureApplications(run)
	if err != nil {
		return workflowFailure(err, "APPLICATION_PROVISIONING_FAILED", "Review the detected application ownership and source binding."), err
	}
	resources, resourceIDs, err := e.ensureResources(ctx, run)
	refs := provisionedResourceRefs(resources, resourceIDs)
	if err != nil {
		return workflowResultWithRefs(workflowFailure(err, "RESOURCE_PROVISIONING_FAILED", "Review resource capacity and the managed resource authority."), refs), err
	}
	plan, err := e.ensureTopology(ctx, run, resources)
	if err != nil {
		return workflowResultWithRefs(workflowFailure(err, "TOPOLOGY_APPLY_FAILED", "Review target capacity and placement facts."), refs), err
	}
	refs.Checkpoints = append(refs.Checkpoints, deploymentworkflow.Checkpoint(deploymentworkflow.AuthorityTopologyPlan, plan.ID, plan.Revision, plan.StateHash, deploymentworkflow.StateProvisioning))
	targets, ok := e.server.Registry.(resource.RuntimeTargetResolver)
	if !ok {
		return workflowResultWithRefs(failedStep("RESOURCE_TARGET_UNAVAILABLE", "The managed-resource target resolver is unavailable.", "Restore the registry authority and retry.", true), refs), nil
	}
	if err := e.server.Resources.ReconcileTopology(ctx, run.ProjectID, plan, targets); err != nil {
		return workflowResultWithRefs(workflowFailure(err, "RESOURCE_RECONCILE_FAILED", "Restore the target Agent and managed-resource authority, then retry."), refs), err
	}
	for _, value := range resources {
		current, getErr := e.server.Resources.Get(ctx, run.ProjectID, value.ID)
		if getErr != nil {
			return workflowResultWithRefs(workflowFailure(getErr, "RESOURCE_READ_FAILED", "Retry after Cloud storage connectivity is restored."), refs), getErr
		}
		if current.Lifecycle == resourcev1.LifecycleFailed || current.Lifecycle == resourcev1.LifecycleDegraded {
			return workflowResultWithRefs(failedStep("MANAGED_RESOURCE_FAILED", "A managed resource failed to become Ready.", "Inspect its factual Agent evidence and correct the target.", true), refs), nil
		}
		if current.Lifecycle != resourcev1.LifecycleReady {
			return deploymentworkflow.StepResult{Pending: true, Refs: refs}, nil
		}
	}
	bindingIDs, secretCheckpoints, err := e.ensureConfigurations(ctx, run, apps, resources)
	if err != nil {
		if errors.Is(err, errWorkloadSecretStale) {
			return workflowResultWithRefs(deploymentworkflow.StepResult{Stale: true, FailureMessage: "An external workload secret changed after approval.", NextAction: "Refresh the secret reference and review the plan again."}, refs), nil
		}
		return workflowResultWithRefs(workflowFailure(err, "SERVICE_CONFIGURATION_FAILED", "Review dependency mappings and generated secret references."), refs), err
	}
	refs.Checkpoints = append(refs.Checkpoints, secretCheckpoints...)
	bindings := make([]resourcev1.Binding, 0, len(bindingIDs))
	for _, id := range bindingIDs {
		binding, getErr := e.server.Resources.GetBinding(ctx, run.ProjectID, id)
		if getErr != nil {
			return workflowResultWithRefs(workflowFailure(getErr, "RESOURCE_BINDING_READ_FAILED", "Retry after binding authority connectivity is restored."), refs), getErr
		}
		bindings = append(bindings, binding)
	}
	bindingCheckpoints, pending, bindingErr := readyBindingCheckpoints(bindings)
	if bindingErr != nil {
		return workflowResultWithRefs(workflowFailure(bindingErr, "RESOURCE_BINDING_FAILED", "Inspect the managed-resource binding evidence and correct the target."), refs), bindingErr
	}
	if pending {
		return deploymentworkflow.StepResult{Pending: true, Refs: refs}, nil
	}
	refs.Checkpoints = append(refs.Checkpoints, bindingCheckpoints...)
	for _, service := range apps {
		configuration, getErr := e.server.Registry.GetServiceConfiguration(run.ProjectID, service.ID)
		if getErr != nil {
			return workflowResultWithRefs(workflowFailure(getErr, "SERVICE_CONFIGURATION_READ_FAILED", "Retry after application configuration storage is restored."), refs), getErr
		}
		refs.Checkpoints = append(refs.Checkpoints, deploymentworkflow.Checkpoint(deploymentworkflow.AuthorityApplication, service.ID, configuration.Revision, authorityStateHash(struct{ GitSHA, ConfigurationHash string }{service.GitSHA, configuration.StateHash}), deploymentworkflow.StateProvisioning))
	}
	return deploymentworkflow.StepResult{Refs: refs}, nil
}

func provisionedResourceRefs(resources map[string]resourcev1.Resource, resourceIDs []string) deploymentworkflow.AuthorityRefs {
	refs := deploymentworkflow.AuthorityRefs{}
	for _, id := range resourceIDs {
		for _, value := range resources {
			if value.ID == id {
				hash := ""
				if value.Runtime != nil {
					hash = value.Runtime.Spec.SpecHash
				}
				refs.Checkpoints = append(refs.Checkpoints, deploymentworkflow.Checkpoint(deploymentworkflow.AuthorityResource, id, 0, hash, deploymentworkflow.StateProvisioning))
				break
			}
		}
	}
	return refs
}

func workflowResultWithRefs(result deploymentworkflow.StepResult, refs deploymentworkflow.AuthorityRefs) deploymentworkflow.StepResult {
	result.Refs = refs
	return result
}

func readyBindingCheckpoints(bindings []resourcev1.Binding) ([]deploymentworkflow.AuthorityCheckpoint, bool, error) {
	ordered := append([]resourcev1.Binding(nil), bindings...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	for _, binding := range ordered {
		if binding.Lifecycle == resourcev1.LifecycleFailed {
			return nil, false, fmt.Errorf("resource binding %q failed: %s", binding.LogicalName, firstNonEmpty(binding.FailureCode, "unknown failure"))
		}
		if binding.Lifecycle != resourcev1.LifecycleReady {
			return nil, true, nil
		}
	}
	checkpoints := make([]deploymentworkflow.AuthorityCheckpoint, 0, len(ordered))
	for _, binding := range ordered {
		checkpoints = append(checkpoints, deploymentworkflow.Checkpoint(deploymentworkflow.AuthorityBinding, binding.ID, 0, authorityStateHash(binding), deploymentworkflow.StateProvisioning))
	}
	return checkpoints, false, nil
}

func (e deploymentWorkflowExecutor) ensureApplications(run deploymentworkflow.Run) (map[string]registry.ServiceRecord, error) {
	existing, err := e.server.Registry.ListServices(run.ProjectID)
	if err != nil {
		return nil, err
	}
	bindings, err := e.server.Registry.ListGitHubServiceBindings(run.ProjectID)
	if err != nil {
		return nil, err
	}
	byName := map[string]registry.ServiceRecord{}
	byService := map[string]registry.GitHubServiceBinding{}
	for _, value := range existing {
		if value.Status != "deleted" {
			byName[value.Name] = value
		}
	}
	for _, value := range bindings {
		if value.Status == registry.GitHubLinkActive {
			byService[value.ServiceID] = value
		}
	}
	result := map[string]registry.ServiceRecord{}
	for _, application := range run.Plan.Applications {
		draft := applicationServiceDraft(run, application)
		service := byName[application.Key]
		if service.ID != "" {
			binding := byService[service.ID]
			if binding.ID != "" && (binding.RepositoryID != run.Plan.Source.RepositoryID || binding.ServiceKey != application.Key) {
				return nil, fmt.Errorf("application name %q is owned by another source", application.Key)
			}
			if binding.ID == "" && !applicationServiceSourceMatches(service, draft) {
				return nil, fmt.Errorf("application name %q is owned by another source", application.Key)
			}
		} else {
			service, err = e.server.Registry.CreateService(run.ProjectID, draft, workflowExecutionKey(run, "app", application.Key))
			if err != nil {
				return nil, err
			}
		}
		if byService[service.ID].ID == "" {
			_, err = e.server.Registry.CreateGitHubServiceBinding(run.ProjectID, registry.GitHubServiceBindingDraft{
				ServiceID: service.ID, RepositoryID: run.Plan.Source.RepositoryID, ServiceKey: application.Key, CreatedBy: run.CreatedBy,
				GitHubSource: registry.GitHubSource{SelectedRef: run.Plan.Source.SelectedRef, ApplicationRoot: application.Root, BuildContext: application.Build.Context, BuildStrategy: application.Build.Strategy, DockerfilePath: application.Build.DockerfilePath},
			})
			if err != nil {
				return nil, err
			}
		}
		result[application.Key] = service
	}
	return result, nil
}

func applicationServiceDraft(run deploymentworkflow.Run, application repositoryanalysis.Application) registry.ServiceDraft {
	return registry.ServiceDraft{
		Name: application.Key, Type: "application", SourceType: "git", RepoURL: "https://github.com/" + run.Plan.Source.Repository + ".git",
		Branch: run.Plan.Source.SelectedRef, GitSHA: run.Plan.Source.CommitSHA, BuildMethod: application.Build.Strategy,
		BuildContext: application.Build.Context, Dockerfile: application.Build.DockerfilePath, Image: application.Build.Image, ContainerPort: application.Port,
		HealthPath: "/health", Replicas: int(firstPositiveInt32(application.Capacity.Replicas, 1)), ResourceRequests: map[string]string{"cpu": fmt.Sprintf("%dm", firstPositiveInt64(application.Capacity.CPUMilli, run.Plan.Target.CPUMilli)), "memory": fmt.Sprintf("%dMi", firstPositiveInt64(application.Capacity.MemoryBytes, run.Plan.Target.MemoryBytes)>>20)},
	}
}

func applicationServiceSourceMatches(service registry.ServiceRecord, draft registry.ServiceDraft) bool {
	return service.Name == draft.Name && service.Type == draft.Type && service.SourceType == draft.SourceType && service.RepoURL == draft.RepoURL && service.Branch == draft.Branch && service.GitSHA == draft.GitSHA && service.BuildMethod == draft.BuildMethod && service.BuildContext == draft.BuildContext && service.Dockerfile == draft.Dockerfile && service.Image == draft.Image && service.ContainerPort == draft.ContainerPort
}

func (e deploymentWorkflowExecutor) ensureResources(ctx context.Context, run deploymentworkflow.Run) (map[string]resourcev1.Resource, []string, error) {
	current, err := e.server.Resources.List(ctx, run.ProjectID, run.Plan.Target.EnvironmentID)
	if err != nil {
		return nil, nil, err
	}
	byName := map[string]resourcev1.Resource{}
	for _, value := range current {
		byName[value.Name] = value
	}
	result := map[string]resourcev1.Resource{}
	ids := []string{}
	for _, detected := range run.Plan.Resources {
		if !detected.Managed || detected.Type == "kafka" {
			continue
		}
		value := byName[detected.LogicalName]
		resourceType := resourcev1.Type(detected.Type)
		if resourceType == "valkey" {
			resourceType = resourcev1.TypeRedis
		}
		if value.ID != "" && (value.Type != resourceType || value.Kind != resourcev1.KindManagedService) {
			return result, ids, fmt.Errorf("resource name %q is owned by an incompatible resource", detected.LogicalName)
		}
		if value.ID == "" {
			definition, ok := resourcev1.Definition(resourceType)
			if !ok || !definition.Provisioning.Implemented || len(definition.Provisioning.Profiles) == 0 || len(definition.Provisioning.Profiles[0].Versions) == 0 {
				return result, ids, fmt.Errorf("managed resource type %q is unsupported", resourceType)
			}
			profile, version := definition.Provisioning.Profiles[0], definition.Provisioning.Profiles[0].Versions[0]
			storage := resourcev1.StorageRequest{}
			if detected.Persistence != nil {
				storage = resourcev1.StorageRequest{Persistent: detected.Persistence.Persistent, SizeBytes: detected.Persistence.SizeBytes, PolicyRef: detected.Persistence.PolicyRef}
			}
			managedCPU, managedMemory := deploymentworkflow.PlannedManagedResourceCapacity(detected.Type)
			value, _, err = e.server.Resources.Create(ctx, run.ProjectID, run.CreatedBy, workflowExecutionKey(run, "resource", detected.LogicalName), resourcev1.CreateRequest{
				EnvironmentID: run.Plan.Target.EnvironmentID, Name: detected.LogicalName, Kind: resourcev1.KindManagedService, Type: resourceType,
				Managed: &resourcev1.ManagedSpec{Type: resourceType, Version: version.Version, Profile: profile.Name, Replicas: 1, CPUMillicores: managedCPU, MemoryBytes: managedMemory, Storage: storage, ServiceConfig: detected.Settings, ConnectionPolicy: resourcev1.ExposurePolicy{Mode: "internal"}},
			})
			if err != nil {
				return result, ids, err
			}
		}
		result[detected.LogicalName] = value
		ids = append(ids, value.ID)
	}
	return result, ids, nil
}

type workflowPlacementFacts struct {
	registry.API
	resources map[string]resourcev1.Resource
}

func (f workflowPlacementFacts) PlacementFacts(ctx context.Context, projectID string) (topology.Facts, error) {
	result, err := f.API.PlacementFacts(ctx, projectID)
	if err != nil {
		return result, err
	}
	for _, value := range f.resources {
		result.Resources = append(result.Resources, topology.ResourceIdentity{ID: value.ID, ProjectID: value.ProjectID, EnvironmentID: value.EnvironmentID, Name: value.Name, Kind: string(value.Kind), Type: string(value.Type), Lifecycle: string(value.Lifecycle)})
	}
	return result, nil
}

func (e deploymentWorkflowExecutor) ensureTopology(ctx context.Context, run deploymentworkflow.Run, resources map[string]resourcev1.Resource) (topologyv1.Plan, error) {
	assignments := map[string]topologyv1.Assignment{}
	current, currentErr := e.server.Topology.Get(ctx, run.ProjectID)
	if currentErr == nil {
		for _, value := range current.Assignments {
			assignments[value.ServiceKey] = value
		}
	} else if !errors.Is(currentErr, topology.ErrNotFound) {
		return topologyv1.Plan{}, currentErr
	}
	for _, application := range run.Plan.Applications {
		cpuReq := firstPositiveInt64(application.Capacity.CPUMilli, run.Plan.Target.CPUMilli)
		memReq := firstPositiveInt64(application.Capacity.MemoryBytes, run.Plan.Target.MemoryBytes)
		cpuLim := firstPositiveInt64(application.Capacity.CPULimitMilli, run.Plan.Target.CPULimitMilli)
		if cpuLim < cpuReq {
			cpuLim = cpuReq
		}
		memLim := firstPositiveInt64(application.Capacity.MemoryLimitBytes, run.Plan.Target.MemoryLimitBytes)
		if memLim < memReq {
			memLim = memReq
		}
		assignments[application.Key] = topologyv1.Assignment{
			ServiceKey:           application.Key,
			EnvironmentID:        run.Plan.Target.EnvironmentID,
			RuntimeID:            run.Plan.Target.RuntimeID,
			Replicas:             firstPositiveInt32(application.Capacity.Replicas, 1),
			CPURequestMillicores: cpuReq,
			MemoryRequestBytes:   memReq,
			CPULimitMillicores:   cpuLim,
			MemoryLimitBytes:     memLim,
			Exposure:             topologyv1.ExposureIntent{Mode: applicationExposure(run, application.Key)},
		}
	}
	for _, value := range resources {
		assignments[value.ID] = topologyv1.Assignment{
			ServiceKey:           value.ID,
			EnvironmentID:        value.EnvironmentID,
			RuntimeID:            run.Plan.Target.RuntimeID,
			Replicas:             value.Managed.Replicas,
			CPURequestMillicores: value.Managed.CPUMillicores,
			MemoryRequestBytes:   value.Managed.MemoryBytes,
			CPULimitMillicores:   value.Managed.CPUMillicores,
			MemoryLimitBytes:     value.Managed.MemoryBytes,
			Exposure:             topologyv1.ExposureIntent{Mode: "none"},
		}
	}
	values := make([]topologyv1.Assignment, 0, len(assignments))
	for _, value := range assignments {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ServiceKey < values[j].ServiceKey })
	draft := topologyv1.Draft{SchemaVersion: topologyv1.SchemaVersion, ProjectID: run.ProjectID, Assignments: values}
	if err := e.server.Resources.ValidateTopologyProvisioning(ctx, run.ProjectID, draft); err != nil {
		return topologyv1.Plan{}, err
	}
	authority := e.server.Topology
	authority.Facts = workflowPlacementFacts{API: e.server.Registry, resources: resources}
	preview, err := authority.Preview(ctx, run.ProjectID, draft)
	if err != nil {
		return topologyv1.Plan{}, err
	}
	if current.ID != "" && current.PlanHash == preview.PlanHash {
		return current, nil
	}
	result, err := authority.Apply(ctx, run.ProjectID, run.CreatedBy, workflowExecutionKey(run, "topology", "apply"), topologyv1.ApplyRequest{Draft: draft, ExpectedRevision: current.Revision, ExpectedStateHash: current.StateHash}, false)
	return result.Plan, err
}

func (e deploymentWorkflowExecutor) ensureConfigurations(ctx context.Context, run deploymentworkflow.Run, apps map[string]registry.ServiceRecord, resources map[string]resourcev1.Resource) ([]string, []deploymentworkflow.AuthorityCheckpoint, error) {
	existingBindings, err := e.server.Resources.ListBindings(ctx, run.ProjectID, run.Plan.Target.EnvironmentID)
	if err != nil {
		return nil, nil, err
	}
	published, err := e.server.Registry.ListDeployments(run.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	manualRoutes := latestExposureByService(published)
	bindings := map[string]resourcev1.Binding{}
	for _, dependency := range run.Plan.Dependencies {
		consumer, ok := apps[dependency.From]
		target, resourceTarget := resources[dependency.To]
		if !ok || !resourceTarget {
			continue
		}
		request := resourcev1.CreateBindingRequest{EnvironmentID: run.Plan.Target.EnvironmentID, Source: resourcev1.EndpointReference{Kind: resourcev1.KindApplication, ID: consumer.ID}, Target: resourcev1.EndpointReference{Kind: resourcev1.KindManagedService, ID: target.ID}, Protocol: resourcev1.Protocol(dependency.Protocol), LogicalName: dependency.To}
		binding, ok := reusableResourceBinding(existingBindings, request)
		if !ok {
			binding, _, err = e.server.Resources.CreateBinding(ctx, run.ProjectID, workflowExecutionKey(run, "binding", dependency.From+"-"+dependency.To), request)
			if err != nil {
				return nil, nil, err
			}
			existingBindings = append(existingBindings, binding)
		}
		bindings[dependency.From+"\x00"+dependency.To] = binding
	}
	ids := make([]string, 0, len(bindings))
	secretCheckpoints := []deploymentworkflow.AuthorityCheckpoint{}
	for _, binding := range bindings {
		ids = append(ids, binding.ID)
	}
	sort.Strings(ids)
	applicationKeys := make([]string, 0, len(apps))
	for key := range apps {
		applicationKeys = append(applicationKeys, key)
	}
	sort.Strings(applicationKeys)
	for _, key := range applicationKeys {
		service := apps[key]
		draft := serviceconfigurationv1.ServiceConfigurationDraft{SchemaVersion: serviceconfigurationv1.SchemaVersion}
		for _, application := range run.Plan.Applications {
			if application.Key != key {
				continue
			}
			for name, value := range application.Environment {
				draft.Environment = append(draft.Environment, deploymentv1.EnvironmentVariable{Name: name, Value: value})
			}
		}
		if applicationExposure(run, key) == "public" && applicationHostname(run, key) != "" {
			if existing := manualRoutes[service.ID]; existing != nil && (existing.Metadata == nil || existing.Metadata.Rationale != automaticPublicRouteRationale) {
				draft.PublicRoute = &serviceconfigurationv1.PublicRouteIntent{Hostname: existing.Hostname, Path: existing.Path}
			} else {
				draft.PublicRoute = &serviceconfigurationv1.PublicRouteIntent{Hostname: applicationHostname(run, key), Path: applicationPath(run, key)}
			}
		}
		for _, secret := range run.Plan.Secrets {
			if secret.ApplicationKey != key {
				continue
			}
			if e.server.Resources.Credentials == nil {
				return nil, nil, errors.New("workload secret authority is unavailable")
			}
			secretID := strings.TrimPrefix(secret.SecretRef, "workload-secret://")
			if secret.Generated {
				secretID = workflowSecretID(run.ProjectID, service.ID, secret.Name)
				if _, err := e.server.Resources.Credentials.EnsureWorkloadSecret(ctx, resourcev1.WorkloadSecretSpec{CredentialID: secretID, ProjectID: run.ProjectID, ServiceID: service.ID, LogicalName: secret.Name}); err != nil {
					return nil, nil, err
				}
			}
			metadata, err := e.server.Resources.Credentials.GetWorkloadSecret(ctx, run.ProjectID, service.ID, secret.Name)
			if err != nil && !secret.Generated {
				plannedScope := "planned:" + secret.ApplicationKey
				planned, plannedErr := e.server.Resources.Credentials.GetWorkloadSecret(ctx, run.ProjectID, plannedScope, secret.Name)
				if plannedErr == nil && planned.Reference == secret.SecretRef && planned.Revision == secret.Revision {
					metadata, err = e.server.Resources.Credentials.BindWorkloadSecret(ctx, run.ProjectID, plannedScope, service.ID, secret.Name)
				}
			}
			if err != nil {
				return nil, nil, err
			}
			if !secret.Generated && (metadata.Reference != secret.SecretRef || metadata.Revision != secret.Revision) {
				return nil, nil, fmt.Errorf("%w: %s", errWorkloadSecretStale, secret.Name)
			}
			draft.SecretReferences = append(draft.SecretReferences, deploymentv1.SecretReference{EnvName: secret.EnvironmentName, SecretID: secretID})
			secretCheckpoints = append(secretCheckpoints, deploymentworkflow.Checkpoint(deploymentworkflow.AuthorityWorkloadSecret, metadata.ID, metadata.Revision, authorityStateHash(metadata), deploymentworkflow.StateProvisioning))
		}
		for _, dependency := range run.Plan.Dependencies {
			if dependency.From != key {
				continue
			}
			targetKind, targetID := "application", ""
			if target, ok := apps[dependency.To]; ok {
				targetID = target.ID
			} else if target, ok := resources[dependency.To]; ok {
				targetKind, targetID = "managed_resource", target.ID
			}
			if targetID == "" {
				continue
			}
			logicalName := dependencyLogicalName(dependency)
			dep := serviceconfigurationv1.ApplicationDependency{LogicalName: logicalName, TargetKind: targetKind, TargetIdentity: targetID, Protocol: dependency.Protocol, Strategy: dependency.Strategy, Path: dependency.Path, Required: dependency.Required, InjectionPhase: "runtime"}
			if dependency.Verification != nil {
				dep.VerificationContract = &serviceconfigurationv1.DependencyVerificationContract{Type: dependency.Verification.Type, Path: dependency.Verification.Path, ExpectedStatus: dependency.Verification.ExpectedStatus}
			}
			if dependency.Strategy == "same_origin" {
				dep.AccessContext = serviceconfigurationv1.AccessContextBrowser
			}
			for _, injection := range dependency.Injections {
				dep.InjectionMappings = append(dep.InjectionMappings, serviceconfigurationv1.DependencyInjectionMapping{EnvName: injection.EnvironmentName, SymbolicSource: injection.SymbolicSource, Template: injection.Template})
			}
			draft.Dependencies = append(draft.Dependencies, dep)
			if binding, ok := bindings[key+"\x00"+dependency.To]; ok {
				draft.ResourceBindings = append(draft.ResourceBindings, serviceconfigurationv1.ResourceBinding{LogicalName: logicalName, BindingID: binding.ID})
			}
		}
		for _, binding := range run.Plan.Bindings {
			if binding.From != key {
				continue
			}
			target, ok := apps[binding.To]
			if !ok {
				return nil, nil, fmt.Errorf("binding target %q is missing", binding.To)
			}
			draft.Bindings = append(draft.Bindings, serviceconfigurationv1.Binding{Kind: binding.Kind, TargetServiceID: target.ID, TargetServiceKey: binding.To, Path: binding.Path})
		}
		preview, err := e.server.Registry.PreviewServiceConfiguration(run.ProjectID, service.ID, draft)
		if err != nil {
			return nil, nil, err
		}
		if preview.CurrentStateHash == preview.DraftStateHash {
			continue
		}
		_, err = e.server.Registry.ApplyServiceConfiguration(run.ProjectID, service.ID, run.CreatedBy, workflowExecutionKey(run, "config", key), registry.ServiceConfigurationApplyRequest{Draft: preview.Configuration, ExpectedRevision: preview.CurrentRevision, ExpectedStateHash: preview.CurrentStateHash})
		if err != nil {
			return nil, nil, err
		}
	}
	return ids, secretCheckpoints, nil
}

func reusableResourceBinding(bindings []resourcev1.Binding, request resourcev1.CreateBindingRequest) (resourcev1.Binding, bool) {
	candidates := make([]resourcev1.Binding, 0, len(bindings))
	for _, binding := range bindings {
		if binding.EnvironmentID == request.EnvironmentID && binding.Source == request.Source && binding.Target == request.Target && binding.Protocol == request.Protocol && binding.LogicalName == strings.TrimSpace(request.LogicalName) && (binding.Lifecycle == resourcev1.LifecycleReady || binding.Lifecycle == resourcev1.LifecycleProvisioning) {
			candidates = append(candidates, binding)
		}
	}
	if len(candidates) == 0 {
		return resourcev1.Binding{}, false
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Lifecycle != candidates[j].Lifecycle {
			return candidates[i].Lifecycle == resourcev1.LifecycleReady
		}
		return candidates[i].ID < candidates[j].ID
	})
	return candidates[0], true
}

func firstPositiveInt32(value, fallback int32) int32 {
	if value > 0 {
		return value
	}
	return fallback
}

func firstPositiveInt64(value, fallback int64) int64 {
	if value > 0 {
		return value
	}
	return fallback
}
