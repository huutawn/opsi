package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

const (
	ServiceConfigurationSchemaVersion = "opsi.service_configuration/v1"
	ServiceBindingInternalHTTP        = "internal_http"
	ServiceBindingBrowserHTTP         = "browser_http"
)

type PublicRouteIntent struct {
	Hostname string `json:"hostname"`
	Path     string `json:"path"`
}

type ServiceConfigurationDraft struct {
	SchemaVersion string                             `json:"schema_version"`
	Environment   []deploymentv1.EnvironmentVariable `json:"environment,omitempty"`
	PublicRoute   *PublicRouteIntent                 `json:"public_route,omitempty"`
	Bindings      []ServiceBinding                   `json:"bindings,omitempty"`
}

type ServiceConfiguration struct {
	ServiceConfigurationDraft
	Revision  uint64     `json:"revision"`
	StateHash string     `json:"state_hash"`
	AppliedBy string     `json:"applied_by,omitempty"`
	AppliedAt *time.Time `json:"applied_at,omitempty"`
}

type GeneratedEnvironment struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Binding int    `json:"binding"`
}

type ServiceConfigurationPreview struct {
	Configuration        ServiceConfigurationDraft `json:"configuration"`
	GeneratedEnvironment []GeneratedEnvironment    `json:"generated_environment,omitempty"`
	CurrentRevision      uint64                    `json:"current_revision"`
	CurrentStateHash     string                    `json:"current_state_hash"`
	DraftStateHash       string                    `json:"draft_state_hash"`
}

type ServiceConfigurationIssue struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

type ServiceConfigurationValidation struct {
	Valid  bool                        `json:"valid"`
	Issues []ServiceConfigurationIssue `json:"issues,omitempty"`
}

type ServiceConfigurationChange struct {
	Kind   string `json:"kind"`
	Action string `json:"action"`
	Name   string `json:"name,omitempty"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

type ServiceConfigurationDiff struct {
	Changes []ServiceConfigurationChange `json:"changes"`
}

type ServiceConfigurationApplyRequest struct {
	Draft             ServiceConfigurationDraft `json:"draft"`
	ExpectedRevision  uint64                    `json:"expected_revision"`
	ExpectedStateHash string                    `json:"expected_state_hash"`
}

type ServiceConfigurationApplyResult struct {
	Configuration ServiceConfiguration `json:"configuration"`
	Reused        bool                 `json:"reused"`
}

type CompiledServiceRuntime struct {
	Environment []deploymentv1.EnvironmentVariable `json:"environment"`
	PublicRoute *PublicRouteIntent                 `json:"public_route,omitempty"`
}

type configurationReplay struct {
	PayloadHash string
	Result      ServiceConfigurationApplyResult
}

func emptyServiceConfiguration() ServiceConfiguration {
	draft := normalizeServiceConfigurationDraft(ServiceConfigurationDraft{})
	return ServiceConfiguration{ServiceConfigurationDraft: draft, StateHash: serviceConfigurationHash(draft)}
}

func normalizeServiceConfigurationDraft(draft ServiceConfigurationDraft) ServiceConfigurationDraft {
	draft.SchemaVersion = ServiceConfigurationSchemaVersion
	draft.Environment = append([]deploymentv1.EnvironmentVariable(nil), draft.Environment...)
	draft.Bindings = append([]ServiceBinding(nil), draft.Bindings...)
	sort.Slice(draft.Environment, func(i, j int) bool { return draft.Environment[i].Name < draft.Environment[j].Name })
	if draft.PublicRoute != nil {
		route := *draft.PublicRoute
		if hostname, err := exposurev1.NormalizeHostname(route.Hostname); err == nil {
			route.Hostname = hostname
		}
		if path, err := exposurev1.NormalizePath(route.Path); err == nil {
			route.Path = path
		}
		draft.PublicRoute = &route
	}
	for i := range draft.Bindings {
		binding := &draft.Bindings[i]
		binding.EnvPrefix = strings.TrimSpace(binding.EnvPrefix)
		binding.EnvName = strings.TrimSpace(binding.EnvName)
		if binding.Kind == ServiceBindingBrowserHTTP && binding.Path == "" {
			binding.Path = "/api"
		}
		if path, err := exposurev1.NormalizePath(binding.Path); err == nil && binding.Path != "" {
			binding.Path = path
		}
	}
	sort.Slice(draft.Bindings, func(i, j int) bool {
		first, second := draft.Bindings[i], draft.Bindings[j]
		return first.Kind+"\x00"+first.TargetServiceID+"\x00"+first.TargetServiceKey+"\x00"+first.EnvPrefix+"\x00"+first.EnvName+"\x00"+first.Path < second.Kind+"\x00"+second.TargetServiceID+"\x00"+second.TargetServiceKey+"\x00"+second.EnvPrefix+"\x00"+second.EnvName+"\x00"+second.Path
	})
	return draft
}

func serviceConfigurationHash(draft ServiceConfigurationDraft) string {
	data, _ := json.Marshal(normalizeServiceConfigurationDraft(draft))
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validateServiceConfiguration(source ServiceRecord, draft ServiceConfigurationDraft, services []ServiceRecord) (ServiceConfigurationDraft, []GeneratedEnvironment, error) {
	draft = normalizeServiceConfigurationDraft(draft)
	if err := deploymentv1.ValidateEnvironment(draft.Environment, nil); err != nil {
		return draft, nil, configurationError("ENVIRONMENT_INVALID", "environment", err.Error())
	}
	if len(draft.Bindings) > 64 {
		return draft, nil, configurationError("BINDING_COUNT_EXCEEDED", "bindings", "binding count exceeds the WorkloadSpec environment bound")
	}
	if draft.PublicRoute != nil {
		hostname, err := exposurev1.NormalizeHostname(draft.PublicRoute.Hostname)
		if err != nil {
			return draft, nil, configurationError("PUBLIC_ROUTE_INVALID", "public_route.hostname", err.Error())
		}
		path, err := exposurev1.NormalizePath(draft.PublicRoute.Path)
		if err != nil {
			return draft, nil, configurationError("PUBLIC_ROUTE_INVALID", "public_route.path", err.Error())
		}
		draft.PublicRoute = &PublicRouteIntent{Hostname: hostname, Path: path}
		for _, service := range services {
			if service.ID == source.ID || service.Configuration.PublicRoute == nil {
				continue
			}
			other := service.Configuration.PublicRoute
			if other.Hostname == hostname && exposurev1.ManagedPathsConflict(other.Path, path) {
				return draft, nil, configurationError("PUBLIC_ROUTE_CONFLICT", "public_route", "hostname and path are already used by another Opsi-managed service")
			}
		}
	}
	targets := make(map[string]ServiceRecord, len(services))
	for _, service := range services {
		targets[service.ID] = service
	}
	manual := make(map[string]struct{}, len(draft.Environment))
	for _, item := range draft.Environment {
		manual[item.Name] = struct{}{}
	}
	generated := make([]GeneratedEnvironment, 0, len(draft.Bindings)*3)
	generatedNames := map[string]struct{}{}
	for index, binding := range draft.Bindings {
		if binding.TargetServiceID == source.ID {
			return draft, nil, configurationError("SELF_CONNECTION", fmt.Sprintf("bindings[%d]", index), "a service cannot connect to itself")
		}
		target, ok := targets[binding.TargetServiceID]
		if !ok || target.ProjectID != source.ProjectID {
			return draft, nil, configurationError("TARGET_SERVICE_NOT_FOUND", fmt.Sprintf("bindings[%d].target_service_id", index), "target service must belong to the project")
		}
		if binding.TargetServiceKey != target.Name {
			return draft, nil, configurationError("TARGET_SERVICE_KEY_MISMATCH", fmt.Sprintf("bindings[%d].target_service_key", index), "target service key does not match the factual service")
		}
		if target.ContainerPort < 1 || target.ContainerPort > 65535 {
			return draft, nil, configurationError("TARGET_PORT_MISSING", fmt.Sprintf("bindings[%d]", index), "target service must declare a container port")
		}
		var values []deploymentv1.EnvironmentVariable
		switch binding.Kind {
		case ServiceBindingInternalHTTP:
			if binding.EnvPrefix == "" {
				return draft, nil, configurationError("ENV_PREFIX_REQUIRED", fmt.Sprintf("bindings[%d].env_prefix", index), "internal HTTP requires env_prefix")
			}
			namespace := deploymentv1.StableDNSName("opsi", source.ProjectID, source.EnvironmentID)
			resource := deploymentv1.StableDNSName("opsi", target.Name, source.RuntimeID)
			host := resource + "." + namespace + ".svc.cluster.local"
			values = []deploymentv1.EnvironmentVariable{{Name: binding.EnvPrefix + "_HOST", Value: host}, {Name: binding.EnvPrefix + "_PORT", Value: strconv.Itoa(target.ContainerPort)}, {Name: binding.EnvPrefix + "_URL", Value: "http://" + host + ":" + strconv.Itoa(target.ContainerPort)}}
		case ServiceBindingBrowserHTTP:
			if draft.PublicRoute == nil || target.Configuration.PublicRoute == nil {
				return draft, nil, configurationError("BROWSER_ROUTE_REQUIRED", fmt.Sprintf("bindings[%d]", index), "browser HTTP requires public routes on source and target")
			}
			if draft.PublicRoute.Hostname != target.Configuration.PublicRoute.Hostname {
				return draft, nil, configurationError("BROWSER_HOSTNAME_MISMATCH", fmt.Sprintf("bindings[%d]", index), "browser HTTP requires the same public hostname")
			}
			path, err := exposurev1.NormalizePath(binding.Path)
			if err != nil {
				return draft, nil, configurationError("BROWSER_PATH_INVALID", fmt.Sprintf("bindings[%d].path", index), err.Error())
			}
			if path != target.Configuration.PublicRoute.Path {
				return draft, nil, configurationError("BROWSER_PATH_MISMATCH", fmt.Sprintf("bindings[%d].path", index), "browser path must match the target public route")
			}
			values = []deploymentv1.EnvironmentVariable{{Name: binding.EnvName, Value: path}}
		default:
			return draft, nil, configurationError("BINDING_KIND_INVALID", fmt.Sprintf("bindings[%d].kind", index), "binding kind must be internal_http or browser_http")
		}
		if err := deploymentv1.ValidateEnvironment(values, nil); err != nil {
			return draft, nil, configurationError("GENERATED_ENV_INVALID", fmt.Sprintf("bindings[%d]", index), err.Error())
		}
		for _, item := range values {
			if _, exists := manual[item.Name]; exists {
				return draft, nil, configurationError("GENERATED_ENV_OVERRIDE", item.Name, "generated environment cannot be overridden by user environment")
			}
			if _, exists := generatedNames[item.Name]; exists {
				return draft, nil, configurationError("GENERATED_ENV_DUPLICATE", item.Name, "generated environment names must be unique")
			}
			generatedNames[item.Name] = struct{}{}
			generated = append(generated, GeneratedEnvironment{Name: item.Name, Value: item.Value, Binding: index})
		}
	}
	if len(draft.Environment)+len(generated) > 64 {
		return draft, nil, configurationError("ENVIRONMENT_COUNT_EXCEEDED", "environment", "user and generated environment exceed the WorkloadSpec bound")
	}
	return draft, generated, nil
}

func CompileServiceRuntime(source ServiceRecord, assignment topologyv1.Assignment, applied ServiceConfiguration, services []ServiceRecord) (CompiledServiceRuntime, error) {
	draft, generated, err := validateServiceConfiguration(source, applied.ServiceConfigurationDraft, services)
	if err != nil {
		return CompiledServiceRuntime{}, err
	}
	targets := make(map[string]ServiceRecord, len(services))
	for _, service := range services {
		targets[service.ID] = service
	}
	environment := append([]deploymentv1.EnvironmentVariable(nil), draft.Environment...)
	for _, item := range generated {
		binding := draft.Bindings[item.Binding]
		target := targets[binding.TargetServiceID]
		value := item.Value
		if binding.Kind == ServiceBindingInternalHTTP {
			namespace := deploymentv1.StableDNSName("opsi", source.ProjectID, assignment.EnvironmentID)
			resource := deploymentv1.StableDNSName("opsi", target.Name, assignment.RuntimeID)
			host := resource + "." + namespace + ".svc.cluster.local"
			switch {
			case strings.HasSuffix(item.Name, "_HOST"):
				value = host
			case strings.HasSuffix(item.Name, "_URL"):
				value = "http://" + host + ":" + strconv.Itoa(target.ContainerPort)
			}
		}
		environment = append(environment, deploymentv1.EnvironmentVariable{Name: item.Name, Value: value})
	}
	sort.Slice(environment, func(i, j int) bool { return environment[i].Name < environment[j].Name })
	if err := deploymentv1.ValidateEnvironment(environment, nil); err != nil {
		return CompiledServiceRuntime{}, err
	}
	return CompiledServiceRuntime{Environment: environment, PublicRoute: draft.PublicRoute}, nil
}

// CompileServiceRuntimeSpecs prepares the next deployment authority without
// creating a deployment job or contacting the cluster.
func CompileServiceRuntimeSpecs(source ServiceRecord, assignment topologyv1.Assignment, applied ServiceConfiguration, services []ServiceRecord, deploymentJobID string) (deploymentv1.WorkloadSpec, *exposurev1.ExposureSpec, error) {
	compiled, err := CompileServiceRuntime(source, assignment, applied, services)
	if err != nil {
		return deploymentv1.WorkloadSpec{}, nil, err
	}
	cpu := source.ResourceRequests["cpu"]
	if cpu == "" {
		cpu = strconv.FormatInt(assignment.CPURequestMillicores, 10) + "m"
	}
	memory := source.ResourceRequests["memory"]
	if memory == "" {
		memory = strconv.FormatInt((assignment.MemoryRequestBytes+1024*1024-1)/(1024*1024), 10) + "Mi"
	}
	workload := deploymentv1.WorkloadSpec{SchemaVersion: deploymentv1.WorkloadSchemaVersion, ServiceKey: source.Name, Replicas: assignment.Replicas, ApplicationContainerName: deploymentv1.ApplicationContainer, ContainerPort: int32(source.ContainerPort), Resources: deploymentv1.Resources{Requests: deploymentv1.ResourceValues{CPU: cpu, Memory: memory}, Limits: deploymentv1.ResourceValues{CPU: cpu, Memory: memory}}, TerminationGracePeriodSecond: 30, Environment: compiled.Environment, Exposure: deploymentv1.ExposureIntent{Mode: "none"}}
	if compiled.PublicRoute != nil {
		workload.Exposure.Mode = "internal"
	}
	if err := workload.Validate(); err != nil {
		return deploymentv1.WorkloadSpec{}, nil, err
	}
	if compiled.PublicRoute == nil {
		return workload, nil, nil
	}
	exposure, err := (exposurev1.ExposureSpec{SchemaVersion: exposurev1.SchemaVersion, ProjectID: source.ProjectID, EnvironmentID: assignment.EnvironmentID, RuntimeID: assignment.RuntimeID, ServiceKey: source.Name, DeploymentJobID: deploymentJobID, Hostname: compiled.PublicRoute.Hostname, Path: compiled.PublicRoute.Path, ServicePort: int32(source.ContainerPort), TLS: exposurev1.TLSConfig{Mode: exposurev1.TLSDisabled}}).Canonicalize()
	if err != nil {
		return deploymentv1.WorkloadSpec{}, nil, err
	}
	return workload, &exposure, nil
}

func configurationError(code, field, message string) error {
	return APIError{Status: 422, Code: code, Message: message, NextAction: field}
}

func configurationDiff(current ServiceConfigurationDraft, next ServiceConfigurationDraft, currentGenerated, nextGenerated []GeneratedEnvironment) ServiceConfigurationDiff {
	changes := []ServiceConfigurationChange{}
	currentBindings := map[string]ServiceBinding{}
	nextBindings := map[string]ServiceBinding{}
	for _, binding := range current.Bindings {
		currentBindings[bindingKey(binding)] = binding
	}
	for _, binding := range next.Bindings {
		nextBindings[bindingKey(binding)] = binding
	}
	for key := range currentBindings {
		if _, ok := nextBindings[key]; !ok {
			changes = append(changes, ServiceConfigurationChange{Kind: "connection", Action: "remove", Name: key})
		}
	}
	for key := range nextBindings {
		if _, ok := currentBindings[key]; !ok {
			changes = append(changes, ServiceConfigurationChange{Kind: "connection", Action: "add", Name: key})
		}
	}
	currentGeneratedByName := make(map[string]string, len(currentGenerated))
	nextGeneratedByName := make(map[string]string, len(nextGenerated))
	for _, item := range currentGenerated {
		currentGeneratedByName[item.Name] = item.Value
	}
	for _, item := range nextGenerated {
		nextGeneratedByName[item.Name] = item.Value
	}
	for name, value := range currentGeneratedByName {
		if nextValue, ok := nextGeneratedByName[name]; !ok || nextValue != value {
			changes = append(changes, ServiceConfigurationChange{Kind: "generated_environment", Action: "remove", Name: name, Before: value, After: nextValue})
		}
	}
	for name, value := range nextGeneratedByName {
		if currentValue, ok := currentGeneratedByName[name]; !ok || currentValue != value {
			changes = append(changes, ServiceConfigurationChange{Kind: "generated_environment", Action: "set", Name: name, Before: currentValue, After: value})
		}
	}
	if routeString(current.PublicRoute) != routeString(next.PublicRoute) {
		changes = append(changes, ServiceConfigurationChange{Kind: "public_route", Action: "change", Before: routeString(current.PublicRoute), After: routeString(next.PublicRoute)})
	}
	currentEnv := map[string]string{}
	nextEnv := map[string]string{}
	for _, item := range current.Environment {
		currentEnv[item.Name] = item.Value
	}
	for _, item := range next.Environment {
		nextEnv[item.Name] = item.Value
	}
	for name, value := range currentEnv {
		if nextValue, ok := nextEnv[name]; !ok || nextValue != value {
			changes = append(changes, ServiceConfigurationChange{Kind: "user_environment", Action: "remove", Name: name, Before: value, After: nextValue})
		}
	}
	for name, value := range nextEnv {
		if currentValue, ok := currentEnv[name]; !ok || currentValue != value {
			changes = append(changes, ServiceConfigurationChange{Kind: "user_environment", Action: "set", Name: name, Before: currentValue, After: value})
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		return changes[i].Kind+changes[i].Name+changes[i].Action < changes[j].Kind+changes[j].Name+changes[j].Action
	})
	return ServiceConfigurationDiff{Changes: changes}
}

func bindingKey(binding ServiceBinding) string {
	return binding.Kind + ":" + binding.TargetServiceID + ":" + binding.TargetServiceKey + ":" + binding.EnvPrefix + ":" + binding.EnvName + ":" + binding.Path
}
func routeString(route *PublicRouteIntent) string {
	if route == nil {
		return ""
	}
	return route.Hostname + route.Path
}

func (s *Service) GetServiceConfiguration(projectID, serviceID string) (ServiceConfiguration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	service, ok := s.services[serviceID]
	if !ok || service.ProjectID != projectID {
		return ServiceConfiguration{}, ErrNotFound
	}
	return normalizeStoredConfiguration(service.Configuration), nil
}

func (s *Service) PreviewServiceConfiguration(projectID, serviceID string, draft ServiceConfigurationDraft) (ServiceConfigurationPreview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	service, services, err := s.configurationScopeLocked(projectID, serviceID)
	if err != nil {
		return ServiceConfigurationPreview{}, err
	}
	normalized, generated, err := validateServiceConfiguration(service, draft, services)
	if err != nil {
		return ServiceConfigurationPreview{}, err
	}
	current := normalizeStoredConfiguration(service.Configuration)
	return ServiceConfigurationPreview{Configuration: normalized, GeneratedEnvironment: generated, CurrentRevision: current.Revision, CurrentStateHash: current.StateHash, DraftStateHash: serviceConfigurationHash(normalized)}, nil
}

func (s *Service) ValidateServiceConfiguration(projectID, serviceID string, draft ServiceConfigurationDraft) (ServiceConfigurationValidation, error) {
	_, err := s.PreviewServiceConfiguration(projectID, serviceID, draft)
	if err == nil {
		return ServiceConfigurationValidation{Valid: true}, nil
	}
	var apiErr APIError
	if errors.As(err, &apiErr) && apiErr.Status == 422 {
		return ServiceConfigurationValidation{Issues: []ServiceConfigurationIssue{{Code: apiErr.Code, Field: apiErr.NextAction, Message: apiErr.Message}}}, nil
	}
	return ServiceConfigurationValidation{}, err
}

func (s *Service) DiffServiceConfiguration(projectID, serviceID string, draft ServiceConfigurationDraft) (ServiceConfigurationDiff, error) {
	preview, err := s.PreviewServiceConfiguration(projectID, serviceID, draft)
	if err != nil {
		return ServiceConfigurationDiff{}, err
	}
	current, err := s.GetServiceConfiguration(projectID, serviceID)
	if err != nil {
		return ServiceConfigurationDiff{}, err
	}
	currentPreview, err := s.PreviewServiceConfiguration(projectID, serviceID, current.ServiceConfigurationDraft)
	if err != nil {
		return ServiceConfigurationDiff{}, err
	}
	return configurationDiff(current.ServiceConfigurationDraft, preview.Configuration, currentPreview.GeneratedEnvironment, preview.GeneratedEnvironment), nil
}

func (s *Service) ApplyServiceConfiguration(projectID, serviceID, actorUserID, key string, request ServiceConfigurationApplyRequest) (ServiceConfigurationApplyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	payload, _ := json.Marshal(request)
	payloadSum := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(payloadSum[:])
	replayKey := "service-configuration:" + projectID + ":" + serviceID + ":" + key
	if replay, ok := s.idempotency[replayKey].(configurationReplay); ok {
		if replay.PayloadHash != payloadHash {
			return ServiceConfigurationApplyResult{}, APIError{Status: 409, Code: "IDEMPOTENCY_CONFLICT", Message: "idempotency key was already used with a different configuration"}
		}
		result := replay.Result
		result.Reused = true
		return result, nil
	}
	service, services, err := s.configurationScopeLocked(projectID, serviceID)
	if err != nil {
		return ServiceConfigurationApplyResult{}, err
	}
	current := normalizeStoredConfiguration(service.Configuration)
	if request.ExpectedRevision != current.Revision || request.ExpectedStateHash != current.StateHash {
		return ServiceConfigurationApplyResult{}, APIError{Status: 409, Code: "SERVICE_CONFIGURATION_STALE", Message: "service configuration revision or state hash is stale"}
	}
	normalized, _, err := validateServiceConfiguration(service, request.Draft, services)
	if err != nil {
		return ServiceConfigurationApplyResult{}, err
	}
	now := s.clock()
	configuration := ServiceConfiguration{ServiceConfigurationDraft: normalized, Revision: current.Revision + 1, StateHash: serviceConfigurationHash(normalized), AppliedBy: actorUserID, AppliedAt: &now}
	service.Configuration = configuration
	service.UpdatedAt = now
	s.services[serviceID] = service
	result := ServiceConfigurationApplyResult{Configuration: configuration}
	s.idempotency[replayKey] = configurationReplay{PayloadHash: payloadHash, Result: result}
	return result, nil
}

func (s *Service) configurationScopeLocked(projectID, serviceID string) (ServiceRecord, []ServiceRecord, error) {
	service, ok := s.services[serviceID]
	if !ok || service.ProjectID != projectID {
		return ServiceRecord{}, nil, ErrNotFound
	}
	services := make([]ServiceRecord, 0)
	for _, candidate := range s.services {
		if candidate.ProjectID == projectID && candidate.Status != "deleted" {
			candidate.Configuration = normalizeStoredConfiguration(candidate.Configuration)
			services = append(services, candidate)
		}
	}
	service.Configuration = normalizeStoredConfiguration(service.Configuration)
	return service, services, nil
}

func normalizeStoredConfiguration(configuration ServiceConfiguration) ServiceConfiguration {
	configuration.ServiceConfigurationDraft = normalizeServiceConfigurationDraft(configuration.ServiceConfigurationDraft)
	if configuration.StateHash == "" {
		configuration.StateHash = serviceConfigurationHash(configuration.ServiceConfigurationDraft)
	}
	return configuration
}
