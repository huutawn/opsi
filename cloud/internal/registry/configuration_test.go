package registry

import (
	"context"
	"strings"
	"testing"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

func TestCompileServiceRuntimeGeneratesCanonicalInternalHTTPEnvironment(t *testing.T) {
	source, target := configurationServices()
	draft := ServiceConfigurationDraft{Bindings: []ServiceBinding{{Kind: ServiceBindingInternalHTTP, TargetServiceID: target.ID, TargetServiceKey: target.Name, EnvPrefix: "API"}}}
	applied := ServiceConfiguration{ServiceConfigurationDraft: draft, Revision: 1, StateHash: serviceConfigurationHash(draft)}
	assignments := []topologyv1.Assignment{{ServiceKey: source.Name, EnvironmentID: "env-topology", RuntimeID: "rt-topology"}, {ServiceKey: target.Name, EnvironmentID: "env-topology", RuntimeID: "rt-topology"}}
	compiled, err := CompileServiceRuntime(source, assignments[0], assignments, applied, []ServiceRecord{source, target})
	if err != nil {
		t.Fatal(err)
	}
	host := deploymentv1.StableDNSName("opsi", target.Name, assignments[1].RuntimeID) + "." + deploymentv1.StableDNSName("opsi", source.ProjectID, assignments[0].EnvironmentID) + ".svc.cluster.local"
	want := map[string]string{"API_HOST": host, "API_PORT": "8080", "API_URL": "http://" + host + ":8080"}
	for _, item := range compiled.Environment {
		if want[item.Name] != item.Value {
			t.Fatalf("generated %s=%q want %q", item.Name, item.Value, want[item.Name])
		}
		delete(want, item.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing generated environment: %v", want)
	}
}

func TestCompileServiceRuntimeSpecsProducesNextDeploymentAuthoritiesOnly(t *testing.T) {
	source, target := configurationServices()
	source.EnvironmentID, source.RuntimeID = "legacy-env", "legacy-runtime"
	source.Replicas = 9
	source.ResourceRequests = map[string]string{"cpu": "900m", "memory": "900Mi"}
	assignment := topologyv1.Assignment{ServiceKey: source.Name, EnvironmentID: "env-topology", RuntimeID: "rt-topology", Replicas: 2, CPURequestMillicores: 100, MemoryRequestBytes: 128 * 1024 * 1024, Exposure: topologyv1.ExposureIntent{Mode: "none"}}
	workload, err := CompileServiceRuntimeSpecs(source, assignment, []topologyv1.Assignment{assignment}, emptyServiceConfiguration(), []ServiceRecord{source, target})
	if err != nil {
		t.Fatal(err)
	}
	if workload.Replicas != 2 || workload.Resources.Requests.CPU != "100m" || workload.Resources.Limits.Memory != "128Mi" {
		t.Fatalf("topology did not override legacy service resources: %+v", workload)
	}
	if workload.StartupProbe == nil || workload.ReadinessProbe == nil || workload.LivenessProbe == nil || workload.StartupProbe.Path != source.HealthPath || workload.StartupProbe.FailureThreshold != 60 || workload.ReadinessProbe.Path != source.HealthPath || workload.LivenessProbe.Port != int32(source.ContainerPort) {
		t.Fatalf("health probes were not compiled from the service boundary: %+v", workload)
	}
}

func TestCompileServiceRuntimeSpecsCarriesSecretReferencesWithoutValues(t *testing.T) {
	source, target := configurationServices()
	assignment := topologyv1.Assignment{ServiceKey: source.Name, EnvironmentID: "env-1", RuntimeID: "rt-1", Replicas: 1, CPURequestMillicores: 100, MemoryRequestBytes: 128 << 20, Exposure: topologyv1.ExposureIntent{Mode: "none"}}
	draft := ServiceConfigurationDraft{SecretReferences: []deploymentv1.SecretReference{{EnvName: "JWT_KEY", SecretID: "wsecret-1"}}}
	workload, err := CompileServiceRuntimeSpecs(source, assignment, []topologyv1.Assignment{assignment}, appliedConfiguration(draft), []ServiceRecord{source, target})
	if err != nil {
		t.Fatal(err)
	}
	if len(workload.SecretReferences) != 1 || workload.SecretReferences[0].SecretID != "wsecret-1" || len(workload.Environment) != 0 {
		t.Fatalf("workload=%+v", workload)
	}
}

func TestCompileServiceRuntimeSpecsMapsTopologyExposureToCanonicalWorkload(t *testing.T) {
	source, target := configurationServices()
	for _, test := range []struct {
		topology string
		workload string
		draft    ServiceConfigurationDraft
	}{
		{topology: "none", workload: "none"},
		{topology: "internal", workload: "internal"},
		{topology: "public", workload: "internal", draft: ServiceConfigurationDraft{PublicRoute: &PublicRouteIntent{Hostname: "apps.example.com", Path: "/api"}}},
	} {
		t.Run(test.topology, func(t *testing.T) {
			assignment := topologyv1.Assignment{ServiceKey: source.Name, EnvironmentID: "env-1", RuntimeID: "rt-1", Replicas: 1, CPURequestMillicores: 100, MemoryRequestBytes: 128 << 20, Exposure: topologyv1.ExposureIntent{Mode: test.topology}}
			workload, err := CompileServiceRuntimeSpecs(source, assignment, []topologyv1.Assignment{assignment}, appliedConfiguration(test.draft), []ServiceRecord{source, target})
			if err != nil {
				t.Fatal(err)
			}
			if workload.Exposure.Mode != test.workload {
				t.Fatalf("topology exposure %q compiled workload exposure %q", test.topology, workload.Exposure.Mode)
			}
		})
	}
}

func TestCompileServiceRuntimeSpecsPublicFailsClosedWithoutValidRoute(t *testing.T) {
	source, target := configurationServices()
	assignment := topologyv1.Assignment{ServiceKey: source.Name, EnvironmentID: "env-1", RuntimeID: "rt-1", Replicas: 1, CPURequestMillicores: 100, MemoryRequestBytes: 128 << 20, Exposure: topologyv1.ExposureIntent{Mode: "public"}}
	if _, err := CompileServiceRuntimeSpecs(source, assignment, []topologyv1.Assignment{assignment}, emptyServiceConfiguration(), []ServiceRecord{source, target}); !hasAPIErrorCode(err, "PUBLIC_ROUTE_REQUIRED") {
		t.Fatalf("missing route err=%v", err)
	}
	for _, route := range []*PublicRouteIntent{{Hostname: "https://apps.example.com", Path: "/"}, {Hostname: "apps.example.com", Path: "api"}} {
		if _, err := CompileServiceRuntimeSpecs(source, assignment, []topologyv1.Assignment{assignment}, appliedConfiguration(ServiceConfigurationDraft{PublicRoute: route}), []ServiceRecord{source, target}); !hasAPIErrorCode(err, "PUBLIC_ROUTE_INVALID") {
			t.Fatalf("invalid route %+v err=%v", route, err)
		}
	}
}

func TestCompileServiceRuntimeRejectsCrossRuntimeInternalHTTP(t *testing.T) {
	source, target := configurationServices()
	draft := ServiceConfigurationDraft{Bindings: []ServiceBinding{{Kind: ServiceBindingInternalHTTP, TargetServiceID: target.ID, TargetServiceKey: target.Name, EnvPrefix: "API"}}}
	assignments := []topologyv1.Assignment{{ServiceKey: source.Name, EnvironmentID: "env-1", RuntimeID: "rt-1"}, {ServiceKey: target.Name, EnvironmentID: "env-1", RuntimeID: "rt-2"}}
	_, err := CompileServiceRuntime(source, assignments[0], assignments, appliedConfiguration(draft), []ServiceRecord{source, target})
	if !hasAPIErrorCode(err, "MULTI_RUNTIME_NETWORKING_UNSUPPORTED") {
		t.Fatalf("cross-runtime internal binding err=%v", err)
	}
}

func TestBrowserHTTPUsesSameOriginPathOnly(t *testing.T) {
	source, target := configurationServices()
	target.Configuration = appliedConfiguration(ServiceConfigurationDraft{PublicRoute: &PublicRouteIntent{Hostname: "apps.example.com", Path: "/api"}})
	draft := ServiceConfigurationDraft{PublicRoute: &PublicRouteIntent{Hostname: "APPS.EXAMPLE.COM", Path: "/"}, Bindings: []ServiceBinding{{Kind: ServiceBindingBrowserHTTP, TargetServiceID: target.ID, TargetServiceKey: target.Name, EnvName: "API_BASE_URL"}}}
	applied := appliedConfiguration(draft)
	assignment := topologyv1.Assignment{ServiceKey: source.Name, EnvironmentID: source.EnvironmentID, RuntimeID: source.RuntimeID}
	compiled, err := CompileServiceRuntime(source, assignment, []topologyv1.Assignment{assignment}, applied, []ServiceRecord{source, target})
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.Environment) != 1 || compiled.Environment[0].Name != "API_BASE_URL" || compiled.Environment[0].Value != "/api" || strings.Contains(compiled.Environment[0].Value, "localhost") || strings.Contains(compiled.Environment[0].Value, "cluster.local") {
		t.Fatalf("browser environment is not same-origin: %+v", compiled.Environment)
	}
}

func TestServiceConfigurationValidationRoutesEnvironmentAndConflicts(t *testing.T) {
	source, target := configurationServices()
	target.Configuration = appliedConfiguration(ServiceConfigurationDraft{PublicRoute: &PublicRouteIntent{Hostname: "apps.example.com", Path: "/api"}})
	if _, _, err := validateServiceConfiguration(context.Background(), nil, source, ServiceConfigurationDraft{PublicRoute: &PublicRouteIntent{Hostname: "apps.example.com", Path: "/"}}, []ServiceRecord{source, target}); err != nil {
		t.Fatalf("managed / and /api should coexist: %v", err)
	}
	if _, _, err := validateServiceConfiguration(context.Background(), nil, source, ServiceConfigurationDraft{PublicRoute: &PublicRouteIntent{Hostname: "apps.example.com", Path: "/api"}}, []ServiceRecord{source, target}); !hasAPIErrorCode(err, "PUBLIC_ROUTE_CONFLICT") {
		t.Fatalf("exact duplicate err=%v", err)
	}
	if _, _, err := validateServiceConfiguration(context.Background(), nil, source, ServiceConfigurationDraft{Environment: []deploymentv1.EnvironmentVariable{{Name: "API_TOKEN", Value: "must-not-persist"}}}, []ServiceRecord{source, target}); !hasAPIErrorCode(err, "ENVIRONMENT_INVALID") {
		t.Fatalf("secret-like plain env err=%v", err)
	}
	if _, _, err := validateServiceConfiguration(context.Background(), nil, source, ServiceConfigurationDraft{Environment: []deploymentv1.EnvironmentVariable{{Name: "API_HOST", Value: "override"}}, Bindings: []ServiceBinding{{Kind: ServiceBindingInternalHTTP, TargetServiceID: target.ID, TargetServiceKey: target.Name, EnvPrefix: "API"}}}, []ServiceRecord{source, target}); !hasAPIErrorCode(err, "GENERATED_ENV_OVERRIDE") {
		t.Fatalf("generated override err=%v", err)
	}
}

func TestApplyServiceConfigurationRejectsStaleAndReloadsFactualState(t *testing.T) {
	source, target := configurationServices()
	service := NewService()
	service.projects[source.ProjectID] = Project{ID: source.ProjectID}
	service.services[source.ID] = source
	service.services[target.ID] = target
	current := normalizeStoredConfiguration(source.Configuration)
	draft := ServiceConfigurationDraft{Environment: []deploymentv1.EnvironmentVariable{{Name: "LOG_LEVEL", Value: "debug"}}}
	if _, err := service.ApplyServiceConfiguration(source.ProjectID, source.ID, "user-1", "stale", ServiceConfigurationApplyRequest{Draft: draft, ExpectedRevision: current.Revision + 1, ExpectedStateHash: current.StateHash}); !hasAPIErrorCode(err, "SERVICE_CONFIGURATION_STALE") {
		t.Fatalf("stale apply err=%v", err)
	}
	result, err := service.ApplyServiceConfiguration(source.ProjectID, source.ID, "user-1", "apply", ServiceConfigurationApplyRequest{Draft: draft, ExpectedRevision: current.Revision, ExpectedStateHash: current.StateHash})
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := service.GetServiceConfiguration(source.ProjectID, source.ID)
	if err != nil || reloaded.StateHash != result.Configuration.StateHash || reloaded.Revision != 1 {
		t.Fatalf("reloaded=%+v result=%+v err=%v", reloaded, result, err)
	}
	if len(service.deployments) != 0 {
		t.Fatalf("configuration apply created deployment state: %+v", service.deployments)
	}
}

func TestServiceConfigurationDiffTracksGeneratedEnvironmentSemantically(t *testing.T) {
	source, target := configurationServices()
	service := NewService()
	service.projects[source.ProjectID] = Project{ID: source.ProjectID}
	source.Configuration = appliedConfiguration(ServiceConfigurationDraft{Bindings: []ServiceBinding{{Kind: ServiceBindingInternalHTTP, TargetServiceID: target.ID, TargetServiceKey: target.Name, EnvPrefix: "API"}}})
	service.services[source.ID] = source
	service.services[target.ID] = target

	unchanged, err := service.DiffServiceConfiguration(source.ProjectID, source.ID, source.Configuration.ServiceConfigurationDraft)
	if err != nil || len(unchanged.Changes) != 0 {
		t.Fatalf("unchanged diff=%+v err=%v", unchanged, err)
	}
	removed, err := service.DiffServiceConfiguration(source.ProjectID, source.ID, ServiceConfigurationDraft{})
	if err != nil {
		t.Fatal(err)
	}
	generatedRemovals := 0
	for _, change := range removed.Changes {
		if change.Kind == "generated_environment" && change.Action == "remove" {
			generatedRemovals++
		}
	}
	if generatedRemovals != 3 {
		t.Fatalf("generated removals=%d diff=%+v", generatedRemovals, removed)
	}
}

func configurationServices() (ServiceRecord, ServiceRecord) {
	source := ServiceRecord{ID: "svc-web", ProjectID: "proj-1", EnvironmentID: "env-1", RuntimeID: "rt-1", Name: "web", ContainerPort: 3000, HealthPath: "/health", Configuration: emptyServiceConfiguration()}
	target := ServiceRecord{ID: "svc-api", ProjectID: "proj-1", EnvironmentID: "env-1", RuntimeID: "rt-1", Name: "api", ContainerPort: 8080, HealthPath: "/health", Configuration: emptyServiceConfiguration()}
	return source, target
}

func appliedConfiguration(draft ServiceConfigurationDraft) ServiceConfiguration {
	draft = normalizeServiceConfigurationDraft(draft)
	return ServiceConfiguration{ServiceConfigurationDraft: draft, Revision: 1, StateHash: serviceConfigurationHash(draft)}
}

func hasAPIErrorCode(err error, code string) bool {
	apiErr, ok := err.(APIError)
	return ok && apiErr.Code == code
}
