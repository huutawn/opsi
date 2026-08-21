package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
	serviceconfigurationv1 "github.com/opsi-dev/opsi/contracts/go/serviceconfigurationv1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

const (
	ServiceConfigurationSchemaVersion = serviceconfigurationv1.SchemaVersion
	ServiceBindingInternalHTTP        = serviceconfigurationv1.BindingInternalHTTP
	ServiceBindingBrowserHTTP         = serviceconfigurationv1.BindingBrowserHTTP
)

type PublicRouteIntent = serviceconfigurationv1.PublicRouteIntent
type ServiceConfigurationDraft = serviceconfigurationv1.ServiceConfigurationDraft
type ServiceConfiguration = serviceconfigurationv1.Configuration

type GeneratedEnvironment struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Binding int    `json:"binding"`
}
type DependencyTargetFacts struct {
	Exists        bool
	ProjectID     string
	EnvironmentID string
	TargetKind    string
	ResourceType  string
	Lifecycle     string
	Host          string
	Port          int32
	Database      string
	Deleted       bool
	StateHash     string
	ServiceKey    string
	ContainerPort int
	PublicRoute   *PublicRouteIntent
	Exposure      *exposurev1.ExposureSpec
	Dependencies  []serviceconfigurationv1.ApplicationDependency
}

type DependencyTargetResolver interface {
	ResolveDependencyTarget(ctx context.Context, projectID, targetIdentity string, targetKind string) (DependencyTargetFacts, error)
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
	return serviceconfigurationv1.Normalize(draft)
}

func serviceConfigurationHash(draft ServiceConfigurationDraft) string {
	return serviceconfigurationv1.StateHash(draft)
}

func isPlatformReservedEnv(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	if upper == "PORT" || upper == "HOSTNAME" || upper == "HOST_IP" || upper == "POD_NAME" || upper == "POD_NAMESPACE" || upper == "POD_IP" || strings.HasPrefix(upper, "OPSI_") || strings.HasPrefix(upper, "KUBERNETES_") {
		return true
	}
	return false
}

func validSymbolicSourceForProtocol(protocol, source string) bool {
	switch protocol {
	case "postgres":
		switch source {
		case "resource.host", "resource.port", "credential.database", "credential.username", "credential.password", "connection.url":
			return true
		}
	case "redis":
		switch source {
		case "resource.host", "resource.port", "credential.password", "connection.url":
			return true
		}
	case "nats":
		switch source {
		case "resource.host", "resource.port", "connection.url":
			return true
		}
	case "http":
		switch source {
		case "application.internal_url", "application.internal_host", "application.internal_port",
			"application.public_url", "application.public_host", "application.public_port", "application.public_scheme",
			"application.path", "application.url":
			return true
		}
	}
	return false
}

func validSymbolicSourceForStrategy(strategy, source string) bool {
	switch strategy {
	case serviceconfigurationv1.StrategyInternalHTTP:
		switch source {
		case "application.internal_url", "application.internal_host", "application.internal_port", "application.path":
			return true
		}
	case serviceconfigurationv1.StrategyPublicHTTP:
		switch source {
		case "application.public_url", "application.public_host", "application.public_port", "application.public_scheme", "application.path", "application.url":
			return true
		}
	case serviceconfigurationv1.StrategySameOrigin:
		switch source {
		case "application.path", "application.url":
			return true
		}
	}
	return false
}

type BuildDependencyMapping struct {
	EnvName string `json:"env_name"`
	Value   string `json:"value"`
}

func ComputeBuildDependencyMappings(config ServiceConfiguration, services []ServiceRecord) []BuildDependencyMapping {
	targets := make(map[string]ServiceRecord, len(services))
	for _, s := range services {
		targets[s.ID] = s
	}

	mappings := []BuildDependencyMapping{}
	for _, dep := range config.Dependencies {
		if dep.InjectionPhase != "build" || dep.TargetKind != "application" {
			continue
		}
		target, ok := targets[dep.TargetIdentity]
		if !ok {
			continue
		}

		publicHost := ""
		publicURL := ""
		targetPath := "/"
		if target.Configuration.PublicRoute != nil {
			publicHost = target.Configuration.PublicRoute.Hostname
			targetPath = target.Configuration.PublicRoute.Path
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
			val := ""
			switch m.SymbolicSource {
			case "application.public_url":
				val = publicURL
			case "application.public_host":
				val = publicHost
			case "application.public_port":
				val = "443"
			case "application.public_scheme":
				val = "https"
			case "application.path":
				if dep.Strategy == serviceconfigurationv1.StrategySameOrigin && dep.Path != "" {
					val = dep.Path
				} else {
					val = targetPath
				}
			case "application.url":
				if dep.Strategy == serviceconfigurationv1.StrategySameOrigin {
					if dep.Path != "" {
						val = dep.Path
					} else {
						val = targetPath
					}
				} else if dep.Strategy == serviceconfigurationv1.StrategyPublicHTTP {
					val = publicURL
				}
			}
			if val != "" {
				mappings = append(mappings, BuildDependencyMapping{EnvName: m.EnvName, Value: val})
			}
		}
	}

	sort.Slice(mappings, func(i, j int) bool {
		return mappings[i].EnvName < mappings[j].EnvName
	})
	return mappings
}

func ComputeBuildEnvironment(config ServiceConfiguration, services []ServiceRecord) map[string]string {
	mappings := ComputeBuildDependencyMappings(config, services)
	if len(mappings) == 0 {
		return nil
	}
	env := make(map[string]string, len(mappings))
	for _, m := range mappings {
		env[m.EnvName] = m.Value
	}
	return env
}

func ComputeBuildDependencyState(config ServiceConfiguration, services []ServiceRecord) string {
	mappings := ComputeBuildDependencyMappings(config, services)
	if len(mappings) == 0 {
		return ""
	}
	data, _ := json.Marshal(mappings)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func ComputeBuildConfigHash(sha, strategy, dockerfilePath, buildContext, repository, buildDepState string) string {
	return buildjob.ComputeBuildConfigHash(sha, strategy, dockerfilePath, buildContext, repository, buildDepState)
}

func checkDependencyCycle(sourceID, targetID string, targetDeps []serviceconfigurationv1.ApplicationDependency, targets map[string]ServiceRecord, resolver DependencyTargetResolver, ctx context.Context, projectID string) error {
	visited := map[string]bool{sourceID: true}
	var dfs func(currentID string, currentDeps []serviceconfigurationv1.ApplicationDependency) error
	dfs = func(currentID string, currentDeps []serviceconfigurationv1.ApplicationDependency) error {
		if currentID == sourceID {
			return errors.New("circular dependency detected in build phase dependency graph")
		}
		if visited[currentID] {
			return nil
		}
		visited[currentID] = true

		var depsToTraverse []serviceconfigurationv1.ApplicationDependency
		if currentDeps != nil {
			depsToTraverse = currentDeps
		} else if resolver != nil {
			facts, err := resolver.ResolveDependencyTarget(ctx, projectID, currentID, "application")
			if err == nil && facts.Exists {
				depsToTraverse = facts.Dependencies
			}
		} else if target, ok := targets[currentID]; ok {
			depsToTraverse = target.Configuration.Dependencies
		}

		for _, dep := range depsToTraverse {
			if dep.TargetKind == "application" && dep.InjectionPhase == "build" {
				if err := dfs(dep.TargetIdentity, nil); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return dfs(targetID, targetDeps)
}

func validateServiceConfiguration(ctx context.Context, resolver DependencyTargetResolver, source ServiceRecord, draft ServiceConfigurationDraft, services []ServiceRecord) (ServiceConfigurationDraft, []GeneratedEnvironment, error) {
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

	if len(draft.Dependencies) > 100 {
		return draft, nil, configurationError("DEPENDENCY_COUNT_EXCEEDED", "dependencies", "dependency count exceeds limit")
	}

	logicalNames := map[string]struct{}{}
	envKeys := map[string]string{}
	envRegex := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

	for index, dep := range draft.Dependencies {
		if dep.LogicalName == "" {
			return draft, nil, configurationError("DEPENDENCY_INVALID", fmt.Sprintf("dependencies[%d].logical_name", index), "logical name is required")
		}
		if _, ok := logicalNames[dep.LogicalName]; ok {
			return draft, nil, configurationError("DEPENDENCY_INVALID", fmt.Sprintf("dependencies[%d].logical_name", index), "logical name must be unique")
		}
		logicalNames[dep.LogicalName] = struct{}{}

		if dep.TargetKind != "application" && dep.TargetKind != "managed_resource" {
			return draft, nil, configurationError("DEPENDENCY_INVALID", fmt.Sprintf("dependencies[%d].target_kind", index), "invalid target kind")
		}
		if dep.TargetIdentity == "" {
			return draft, nil, configurationError("DEPENDENCY_TARGET_NOT_FOUND", fmt.Sprintf("dependencies[%d].target_identity", index), "missing target identity")
		}
		if dep.Protocol == "" {
			return draft, nil, configurationError("DEPENDENCY_INVALID", fmt.Sprintf("dependencies[%d].protocol", index), "protocol is required")
		}
		if dep.InjectionPhase != "runtime" && dep.InjectionPhase != "build" {
			return draft, nil, configurationError("DEPENDENCY_INVALID", fmt.Sprintf("dependencies[%d].injection_phase", index), "invalid injection phase")
		}
		if dep.TargetKind == "managed_resource" && dep.InjectionPhase == "build" {
			return draft, nil, configurationError("DEPENDENCY_BUILD_PHASE_UNSUPPORTED", fmt.Sprintf("dependencies[%d].injection_phase", index), "managed resource build phase injection is unsupported in ADC-02")
		}

		for _, currentDep := range source.Configuration.Dependencies {
			if currentDep.LogicalName == dep.LogicalName && currentDep.TargetKind == "managed_resource" && currentDep.TargetIdentity != dep.TargetIdentity {
				return draft, nil, APIError{Status: 409, Code: "DEPENDENCY_BINDING_REPLACEMENT_REQUIRES_EXPLICIT_MIGRATION", Message: "replacing an active database dependency target requires explicit PostgreSQL cutover migration", NextAction: fmt.Sprintf("dependencies[%d].target_identity", index)}
			}
		}

		var targetContainerPort int
		var targetPublicRoute *PublicRouteIntent
		var targetDeps []serviceconfigurationv1.ApplicationDependency

		if resolver != nil {
			facts, err := resolver.ResolveDependencyTarget(ctx, source.ProjectID, dep.TargetIdentity, dep.TargetKind)
			if err != nil {
				return draft, nil, configurationError("DEPENDENCY_TARGET_INVALID", fmt.Sprintf("dependencies[%d].target_identity", index), "failed to resolve target")
			}
			if !facts.Exists || facts.Deleted || facts.Lifecycle == "deleted" || facts.Lifecycle == "retiring" || facts.Lifecycle == "retired" || facts.Lifecycle == "deleting" || facts.Lifecycle == "failed" {
				return draft, nil, configurationError("DEPENDENCY_TARGET_NOT_FOUND", fmt.Sprintf("dependencies[%d].target_identity", index), "target not found or deleted")
			}
			if facts.ProjectID != source.ProjectID {
				return draft, nil, configurationError("DEPENDENCY_TARGET_FORBIDDEN", fmt.Sprintf("dependencies[%d].target_identity", index), "target belongs to another project")
			}
			if facts.EnvironmentID != source.EnvironmentID {
				return draft, nil, configurationError("DEPENDENCY_TARGET_INVALID", fmt.Sprintf("dependencies[%d].target_identity", index), "target environment does not match")
			}
			if facts.TargetKind != dep.TargetKind {
				return draft, nil, configurationError("DEPENDENCY_TARGET_INVALID", fmt.Sprintf("dependencies[%d].target_kind", index), "target kind mismatch")
			}
			if dep.TargetKind == "application" && dep.TargetIdentity == source.ID {
				return draft, nil, configurationError("DEPENDENCY_INVALID", fmt.Sprintf("dependencies[%d].target_identity", index), "a service cannot depend on itself")
			}
			if dep.TargetKind == "managed_resource" {
				if dep.Protocol == "postgres" && facts.ResourceType != "" && facts.ResourceType != "postgres" {
					return draft, nil, configurationError("DEPENDENCY_PROTOCOL_UNSUPPORTED", fmt.Sprintf("dependencies[%d].protocol", index), "target resource does not support postgres protocol")
				}
				if dep.Protocol == "redis" && facts.ResourceType != "" && facts.ResourceType != "redis" {
					return draft, nil, configurationError("DEPENDENCY_PROTOCOL_UNSUPPORTED", fmt.Sprintf("dependencies[%d].protocol", index), "target resource does not support redis protocol")
				}
			}
			if dep.TargetKind == "application" {
				targetContainerPort = facts.ContainerPort
				targetPublicRoute = facts.PublicRoute
				targetDeps = facts.Dependencies
				if facts.Exposure != nil && targetPublicRoute == nil {
					targetPublicRoute = &PublicRouteIntent{Hostname: facts.Exposure.Hostname, Path: facts.Exposure.Path}
				}
			}
		} else {
			if dep.TargetKind == "application" {
				target, ok := targets[dep.TargetIdentity]
				if !ok || target.ProjectID != source.ProjectID {
					return draft, nil, configurationError("DEPENDENCY_TARGET_FORBIDDEN", fmt.Sprintf("dependencies[%d].target_identity", index), "target application must belong to the project")
				}
				if target.ID == source.ID {
					return draft, nil, configurationError("DEPENDENCY_INVALID", fmt.Sprintf("dependencies[%d].target_identity", index), "a service cannot depend on itself")
				}
				targetContainerPort = target.ContainerPort
				targetPublicRoute = target.Configuration.PublicRoute
				targetDeps = target.Configuration.Dependencies
			}
		}

		if dep.TargetKind == "application" {
			if dep.Protocol != "http" {
				return draft, nil, configurationError("DEPENDENCY_PROTOCOL_UNSUPPORTED", fmt.Sprintf("dependencies[%d].protocol", index), "application dependencies must use protocol http")
			}
			// If strategy is not set, default to internal_http if access_context is empty/server, or same_origin if access_context is browser
			if dep.Strategy == "" {
				if dep.AccessContext == serviceconfigurationv1.AccessContextBrowser {
					dep.Strategy = serviceconfigurationv1.StrategySameOrigin
				} else {
					dep.Strategy = serviceconfigurationv1.StrategyInternalHTTP
				}
			}
			if dep.AccessContext == "" {
				if dep.Strategy == serviceconfigurationv1.StrategySameOrigin {
					dep.AccessContext = serviceconfigurationv1.AccessContextBrowser
				} else {
					dep.AccessContext = serviceconfigurationv1.AccessContextServer
				}
			}

			if dep.Strategy != serviceconfigurationv1.StrategySameOrigin && dep.Strategy != serviceconfigurationv1.StrategyInternalHTTP && dep.Strategy != serviceconfigurationv1.StrategyPublicHTTP {
				return draft, nil, configurationError("DEPENDENCY_STRATEGY_INVALID", fmt.Sprintf("dependencies[%d].strategy", index), "application dependency strategy must be same_origin, internal_http, or public_http")
			}
			if dep.AccessContext != serviceconfigurationv1.AccessContextServer && dep.AccessContext != serviceconfigurationv1.AccessContextBrowser {
				return draft, nil, configurationError("DEPENDENCY_ACCESS_CONTEXT_INVALID", fmt.Sprintf("dependencies[%d].access_context", index), "access context must be server or browser")
			}

			// Matrix validation:
			// server + internal_http -> VALID
			// server + public_http -> VALID
			// server + same_origin -> REJECT (STRATEGY_CONTEXT_MISMATCH)
			// browser + same_origin -> VALID
			// browser + public_http -> VALID
			// browser + internal_http -> REJECT (BROWSER_INTERNAL_HTTP_FORBIDDEN)
			if dep.AccessContext == serviceconfigurationv1.AccessContextBrowser && dep.Strategy == serviceconfigurationv1.StrategyInternalHTTP {
				return draft, nil, configurationError("BROWSER_INTERNAL_HTTP_FORBIDDEN", fmt.Sprintf("dependencies[%d]", index), "browser access context cannot use internal_http strategy")
			}
			if dep.AccessContext == serviceconfigurationv1.AccessContextServer && dep.Strategy == serviceconfigurationv1.StrategySameOrigin {
				return draft, nil, configurationError("STRATEGY_CONTEXT_MISMATCH", fmt.Sprintf("dependencies[%d]", index), "server access context cannot use same_origin strategy")
			}

			if dep.Strategy == serviceconfigurationv1.StrategySameOrigin {
				if dep.Path == "" {
					if targetPublicRoute != nil && targetPublicRoute.Path != "" {
						dep.Path = targetPublicRoute.Path
					} else {
						dep.Path = "/api"
					}
				}
				normPath, err := exposurev1.NormalizePath(dep.Path)
				if err != nil {
					return draft, nil, configurationError("DEPENDENCY_PATH_INVALID", fmt.Sprintf("dependencies[%d].path", index), err.Error())
				}
				dep.Path = normPath
			}

			if dep.Strategy == serviceconfigurationv1.StrategyInternalHTTP {
				if targetContainerPort < 1 || targetContainerPort > 65535 {
					return draft, nil, configurationError("TARGET_PORT_MISSING", fmt.Sprintf("dependencies[%d]", index), "internal HTTP target application must declare a container port")
				}
			}
			if dep.Strategy == serviceconfigurationv1.StrategyPublicHTTP {
				if targetPublicRoute == nil {
					return draft, nil, configurationError("TARGET_PUBLIC_ROUTE_MISSING", fmt.Sprintf("dependencies[%d]", index), "public HTTP target application must have an applied public route")
				}
			}
			if dep.Strategy == serviceconfigurationv1.StrategySameOrigin {
				if targetPublicRoute == nil {
					return draft, nil, configurationError("TARGET_PUBLIC_ROUTE_MISSING", fmt.Sprintf("dependencies[%d]", index), "same-origin target application must have an applied public route")
				}
				if draft.PublicRoute != nil {
					if draft.PublicRoute.Hostname != targetPublicRoute.Hostname {
						return draft, nil, configurationError("SAME_ORIGIN_HOSTNAME_MISMATCH", fmt.Sprintf("dependencies[%d]", index), "same-origin consumer and target must share the same public route hostname")
					}
				}
				if dep.Path != targetPublicRoute.Path {
					return draft, nil, configurationError("SAME_ORIGIN_PATH_MISMATCH", fmt.Sprintf("dependencies[%d].path", index), "same-origin dependency path must match the target public route path")
				}
			}
		}

		if len(dep.InjectionMappings) > 64 {
			return draft, nil, configurationError("DEPENDENCY_INVALID", fmt.Sprintf("dependencies[%d].injection_mappings", index), "too many injection mappings")
		}

		depEnvNames := map[string]struct{}{}
		for i, mapping := range dep.InjectionMappings {
			if !envRegex.MatchString(mapping.EnvName) {
				return draft, nil, configurationError("DEPENDENCY_INVALID", fmt.Sprintf("dependencies[%d].injection_mappings[%d].env_name", index, i), "invalid env name format")
			}
			if isPlatformReservedEnv(mapping.EnvName) {
				return draft, nil, configurationError("DEPENDENCY_ENV_CONFLICT", fmt.Sprintf("dependencies[%d].injection_mappings[%d].env_name", index, i), fmt.Sprintf("env name %s is reserved by the platform", mapping.EnvName))
			}
			if _, ok := depEnvNames[mapping.EnvName]; ok {
				return draft, nil, configurationError("DEPENDENCY_INVALID", fmt.Sprintf("dependencies[%d].injection_mappings[%d].env_name", index, i), "duplicate env name in mapping")
			}
			depEnvNames[mapping.EnvName] = struct{}{}

			if existingOwner, ok := envKeys[mapping.EnvName]; ok {
				return draft, nil, configurationError("DEPENDENCY_ENV_COLLISION", fmt.Sprintf("dependencies[%d].injection_mappings[%d].env_name", index, i), fmt.Sprintf("env name %s conflicts with dependency %s", mapping.EnvName, existingOwner))
			}
			envKeys[mapping.EnvName] = dep.LogicalName

			if mapping.SymbolicSource == "" {
				return draft, nil, configurationError("DEPENDENCY_INVALID", fmt.Sprintf("dependencies[%d].injection_mappings[%d].symbolic_source", index, i), "symbolic source is required")
			}
			if dep.TargetKind == "managed_resource" && !validSymbolicSourceForProtocol(dep.Protocol, mapping.SymbolicSource) {
				return draft, nil, configurationError("DEPENDENCY_SYMBOLIC_SOURCE_INVALID", fmt.Sprintf("dependencies[%d].injection_mappings[%d].symbolic_source", index, i), fmt.Sprintf("symbolic source %s is invalid for protocol %s", mapping.SymbolicSource, dep.Protocol))
			}
			if dep.TargetKind == "application" {
				if !validSymbolicSourceForProtocol(dep.Protocol, mapping.SymbolicSource) || !validSymbolicSourceForStrategy(dep.Strategy, mapping.SymbolicSource) {
					return draft, nil, configurationError("DEPENDENCY_SYMBOLIC_SOURCE_INVALID", fmt.Sprintf("dependencies[%d].injection_mappings[%d].symbolic_source", index, i), fmt.Sprintf("symbolic source %s is invalid for application strategy %s", mapping.SymbolicSource, dep.Strategy))
				}
			}
		}

		// Cycle detection for build phase dependencies:
		if dep.TargetKind == "application" && dep.InjectionPhase == "build" {
			if err := checkDependencyCycle(source.ID, dep.TargetIdentity, targetDeps, targets, resolver, ctx, source.ProjectID); err != nil {
				return draft, nil, configurationError("DEPENDENCY_CYCLE_DETECTED", fmt.Sprintf("dependencies[%d].target_identity", index), err.Error())
			}
		}
	}

	manual := make(map[string]struct{}, len(draft.Environment))
	for _, item := range draft.Environment {
		if depLogical, exists := envKeys[item.Name]; exists {
			return draft, nil, configurationError("DEPENDENCY_ENV_CONFLICT", item.Name, fmt.Sprintf("manual user environment %s conflicts with dependency %s mapping", item.Name, depLogical))
		}
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
			values = []deploymentv1.EnvironmentVariable{{Name: binding.EnvPrefix + "_HOST"}, {Name: binding.EnvPrefix + "_PORT", Value: strconv.Itoa(target.ContainerPort)}, {Name: binding.EnvPrefix + "_URL"}}
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
			if depLogical, exists := envKeys[item.Name]; exists {
				return draft, nil, configurationError("DEPENDENCY_ENV_CONFLICT", item.Name, fmt.Sprintf("service binding environment %s conflicts with dependency %s mapping", item.Name, depLogical))
			}
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

func CompileServiceRuntime(source ServiceRecord, assignment topologyv1.Assignment, assignments []topologyv1.Assignment, applied ServiceConfiguration, services []ServiceRecord) (CompiledServiceRuntime, error) {
	draft, generated, err := validateServiceConfiguration(context.Background(), nil, source, applied.ServiceConfigurationDraft, services)
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
			targetAssignment, ok := serviceAssignment(assignments, target.Name, assignment.EnvironmentID)
			if !ok {
				return CompiledServiceRuntime{}, configurationError("TARGET_ASSIGNMENT_MISSING", fmt.Sprintf("bindings[%d]", item.Binding), "internal HTTP target must have an assignment in the source environment")
			}
			if targetAssignment.RuntimeID != assignment.RuntimeID {
				return CompiledServiceRuntime{}, configurationError("MULTI_RUNTIME_NETWORKING_UNSUPPORTED", fmt.Sprintf("bindings[%d]", item.Binding), "manual v1 internal HTTP requires source and target on the same runtime")
			}
			namespace := deploymentv1.StableDNSName("opsi", source.ProjectID, assignment.EnvironmentID)
			resource := deploymentv1.StableDNSName("opsi", target.Name, targetAssignment.RuntimeID)
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
	for _, dep := range draft.Dependencies {
		if dep.InjectionPhase != "runtime" || dep.TargetKind != "application" {
			continue
		}
		target, ok := targets[dep.TargetIdentity]
		if !ok {
			continue
		}

		targetAssignment, hasAssignment := serviceAssignment(assignments, target.Name, assignment.EnvironmentID)
		namespace := deploymentv1.StableDNSName("opsi", source.ProjectID, assignment.EnvironmentID)
		internalHost := ""
		if hasAssignment {
			resource := deploymentv1.StableDNSName("opsi", target.Name, targetAssignment.RuntimeID)
			internalHost = resource + "." + namespace + ".svc.cluster.local"
		}
		internalPort := strconv.Itoa(target.ContainerPort)
		internalURL := ""
		if internalHost != "" {
			internalURL = "http://" + internalHost + ":" + internalPort
		}

		publicHost := ""
		publicPort := "443"
		publicScheme := "https"
		publicURL := ""
		targetPath := "/"
		if target.Configuration.PublicRoute != nil {
			publicHost = target.Configuration.PublicRoute.Hostname
			targetPath = target.Configuration.PublicRoute.Path
			if targetPath == "" {
				targetPath = "/"
			}
			if targetPath == "/" {
				publicURL = "https://" + publicHost
			} else {
				publicURL = "https://" + publicHost + targetPath
			}
		}

		for _, mapping := range dep.InjectionMappings {
			val := ""
			switch mapping.SymbolicSource {
			case "application.internal_url":
				if dep.Strategy == serviceconfigurationv1.StrategyInternalHTTP {
					if !hasAssignment {
						return CompiledServiceRuntime{}, configurationError("TARGET_ASSIGNMENT_MISSING", dep.LogicalName, "internal HTTP target must have an assignment in the source environment")
					}
					if targetAssignment.RuntimeID != assignment.RuntimeID {
						return CompiledServiceRuntime{}, configurationError("MULTI_RUNTIME_NETWORKING_UNSUPPORTED", dep.LogicalName, "manual v1 internal HTTP requires source and target on the same runtime")
					}
					val = internalURL
				}
			case "application.internal_host":
				if dep.Strategy == serviceconfigurationv1.StrategyInternalHTTP {
					if !hasAssignment {
						return CompiledServiceRuntime{}, configurationError("TARGET_ASSIGNMENT_MISSING", dep.LogicalName, "internal HTTP target must have an assignment in the source environment")
					}
					if targetAssignment.RuntimeID != assignment.RuntimeID {
						return CompiledServiceRuntime{}, configurationError("MULTI_RUNTIME_NETWORKING_UNSUPPORTED", dep.LogicalName, "manual v1 internal HTTP requires source and target on the same runtime")
					}
					val = internalHost
				}
			case "application.internal_port":
				val = internalPort
			case "application.public_url":
				val = publicURL
			case "application.public_host":
				val = publicHost
			case "application.public_port":
				val = publicPort
			case "application.public_scheme":
				val = publicScheme
			case "application.path":
				if dep.Strategy == serviceconfigurationv1.StrategySameOrigin && dep.Path != "" {
					val = dep.Path
				} else {
					val = targetPath
				}
			case "application.url":
				if dep.Strategy == serviceconfigurationv1.StrategySameOrigin {
					if dep.Path != "" {
						val = dep.Path
					} else {
						val = targetPath
					}
				} else if dep.Strategy == serviceconfigurationv1.StrategyPublicHTTP {
					val = publicURL
				} else if dep.Strategy == serviceconfigurationv1.StrategyInternalHTTP {
					val = internalURL
				}
			}
			if val != "" {
				environment = append(environment, deploymentv1.EnvironmentVariable{Name: mapping.EnvName, Value: val})
			}
		}
	}

	sort.Slice(environment, func(i, j int) bool { return environment[i].Name < environment[j].Name })
	if err := deploymentv1.ValidateEnvironment(environment, nil); err != nil {
		return CompiledServiceRuntime{}, err
	}
	return CompiledServiceRuntime{Environment: environment, PublicRoute: draft.PublicRoute}, nil
}

// CompileServiceRuntimeSpecs is the single source of truth for immutable workloads.
func CompileServiceRuntimeSpecs(source ServiceRecord, assignment topologyv1.Assignment, assignments []topologyv1.Assignment, applied ServiceConfiguration, services []ServiceRecord) (deploymentv1.WorkloadSpec, error) {
	compiled, err := CompileServiceRuntime(source, assignment, assignments, applied, services)
	if err != nil {
		return deploymentv1.WorkloadSpec{}, err
	}
	if source.ContainerPort < 1 || source.ContainerPort > 65535 {
		return deploymentv1.WorkloadSpec{}, configurationError("CONTAINER_PORT_MISSING", "container_port", "service must declare a valid container port")
	}
	if source.HealthPath == "" {
		return deploymentv1.WorkloadSpec{}, configurationError("HEALTH_PATH_MISSING", "health_path", "service must declare a health path")
	}
	workloadExposure := assignment.Exposure.Mode
	if workloadExposure == "public" {
		if compiled.PublicRoute == nil {
			return deploymentv1.WorkloadSpec{}, configurationError("PUBLIC_ROUTE_REQUIRED", "public_route", "public topology exposure requires an applied public route")
		}
		workloadExposure = "internal"
	}
	if workloadExposure != "none" && workloadExposure != "internal" {
		return deploymentv1.WorkloadSpec{}, configurationError("PUBLIC_EXPOSURE_UNSUPPORTED", "exposure.mode", "this deployment flow supports internal workloads only")
	}
	cpu := strconv.FormatInt(assignment.CPURequestMillicores, 10) + "m"
	memory := strconv.FormatInt((assignment.MemoryRequestBytes+1024*1024-1)/(1024*1024), 10) + "Mi"
	readiness := &deploymentv1.Probe{Path: source.HealthPath, Port: int32(source.ContainerPort), InitialDelaySeconds: 2, PeriodSeconds: 5, TimeoutSeconds: 2, FailureThreshold: 6}
	liveness := *readiness
	workload := deploymentv1.WorkloadSpec{SchemaVersion: deploymentv1.WorkloadSchemaVersion, ServiceKey: source.Name, Replicas: assignment.Replicas, ApplicationContainerName: deploymentv1.ApplicationContainer, ContainerPort: int32(source.ContainerPort), ReadinessProbe: readiness, LivenessProbe: &liveness, Resources: deploymentv1.Resources{Requests: deploymentv1.ResourceValues{CPU: cpu, Memory: memory}, Limits: deploymentv1.ResourceValues{CPU: cpu, Memory: memory}}, TerminationGracePeriodSecond: 30, Environment: compiled.Environment, Exposure: deploymentv1.ExposureIntent{Mode: workloadExposure}}
	if err := workload.Validate(); err != nil {
		return deploymentv1.WorkloadSpec{}, err
	}
	return workload, nil
}

func serviceAssignment(assignments []topologyv1.Assignment, serviceKey, environmentID string) (topologyv1.Assignment, bool) {
	for _, assignment := range assignments {
		if assignment.ServiceKey == serviceKey && assignment.EnvironmentID == environmentID {
			return assignment, true
		}
	}
	return topologyv1.Assignment{}, false
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
	currentResBindings := map[string]string{}
	nextResBindings := map[string]string{}
	for _, b := range current.ResourceBindings {
		currentResBindings[b.LogicalName] = b.BindingID
	}
	for _, b := range next.ResourceBindings {
		nextResBindings[b.LogicalName] = b.BindingID
	}
	for name, value := range currentResBindings {
		if nextValue, ok := nextResBindings[name]; !ok {
			changes = append(changes, ServiceConfigurationChange{Kind: "resource_binding", Action: "remove", Name: name, Before: value})
		} else if nextValue != value {
			changes = append(changes, ServiceConfigurationChange{Kind: "resource_binding", Action: "change", Name: name, Before: value, After: nextValue})
		}
	}
	for name, value := range nextResBindings {
		if _, ok := currentResBindings[name]; !ok {
			changes = append(changes, ServiceConfigurationChange{Kind: "resource_binding", Action: "add", Name: name, After: value})
		}
	}
	currentDeps := map[string]serviceconfigurationv1.ApplicationDependency{}
	nextDeps := map[string]serviceconfigurationv1.ApplicationDependency{}
	for _, d := range current.Dependencies {
		currentDeps[d.LogicalName] = d
	}
	for _, d := range next.Dependencies {
		nextDeps[d.LogicalName] = d
	}
	for name, currentDep := range currentDeps {
		nextDep, ok := nextDeps[name]
		if !ok {
			changes = append(changes, ServiceConfigurationChange{Kind: "dependency", Action: "remove", Name: name})
		} else {
			if currentDep.TargetIdentity != nextDep.TargetIdentity || currentDep.TargetKind != nextDep.TargetKind {
				changes = append(changes, ServiceConfigurationChange{Kind: "dependency_target", Action: "change", Name: name})
			}
			if currentDep.Protocol != nextDep.Protocol {
				changes = append(changes, ServiceConfigurationChange{Kind: "dependency_protocol", Action: "change", Name: name})
			}
			if currentDep.Required != nextDep.Required {
				changes = append(changes, ServiceConfigurationChange{Kind: "dependency_required", Action: "change", Name: name})
			}
			if currentDep.InjectionPhase != nextDep.InjectionPhase {
				changes = append(changes, ServiceConfigurationChange{Kind: "dependency_phase", Action: "change", Name: name})
			}
			currMappings := map[string]string{}
			for _, m := range currentDep.InjectionMappings { currMappings[m.EnvName] = m.SymbolicSource }
			nextMappings := map[string]string{}
			for _, m := range nextDep.InjectionMappings { nextMappings[m.EnvName] = m.SymbolicSource }
			for envName, sym := range currMappings {
				if nextSym, ok := nextMappings[envName]; !ok {
					changes = append(changes, ServiceConfigurationChange{Kind: "dependency_mapping", Action: "remove", Name: name + ":" + envName, Before: sym})
				} else if nextSym != sym {
					changes = append(changes, ServiceConfigurationChange{Kind: "dependency_mapping", Action: "change", Name: name + ":" + envName, Before: sym, After: nextSym})
				}
			}
			for envName, sym := range nextMappings {
				if _, ok := currMappings[envName]; !ok {
					changes = append(changes, ServiceConfigurationChange{Kind: "dependency_mapping", Action: "add", Name: name + ":" + envName, After: sym})
				}
			}
		}
	}
	for name := range nextDeps {
		if _, ok := currentDeps[name]; !ok {
			changes = append(changes, ServiceConfigurationChange{Kind: "dependency", Action: "add", Name: name})
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
	normalized, generated, err := validateServiceConfiguration(context.Background(), s.DependencyResolver, service, draft, services)
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
	normalized, _, err := validateServiceConfiguration(context.Background(), s.DependencyResolver, service, request.Draft, services)
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
