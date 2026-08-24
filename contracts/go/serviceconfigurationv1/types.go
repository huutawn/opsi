package serviceconfigurationv1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
)

const (
	SchemaVersion       = "opsi.service_configuration/v1"
	BindingInternalHTTP = "internal_http"
	BindingBrowserHTTP  = "browser_http"
)

type PublicRouteIntent struct {
	Hostname string `json:"hostname"`
	Path     string `json:"path"`
}

type Binding struct {
	Kind             string `json:"kind"`
	TargetServiceID  string `json:"target_service_id"`
	TargetServiceKey string `json:"target_service_key"`
	EnvPrefix        string `json:"env_prefix,omitempty"`
	EnvName          string `json:"env_name,omitempty"`
	Path             string `json:"path,omitempty"`
}

type ResourceBinding struct {
	LogicalName string `json:"logical_name"`
	BindingID   string `json:"binding_id"`
}

type DependencyInjectionMapping struct {
	EnvName        string `json:"env_name"`
	SymbolicSource string `json:"symbolic_source"`
}

// DependencyVerificationContract is optional user-declared consumer assertion intent.
// When set, the verification runner will perform an HTTP probe against the consumer
// service after deployment and compare the response code to ExpectedStatus.
// A missing or non-matching assertion results in PARTIALLY_VERIFIED, never VERIFIED.
type DependencyVerificationContract struct {
	Type           string `json:"type"`            // "consumer_http"
	Path           string `json:"path"`            // relative path on the consumer service
	ExpectedStatus int    `json:"expected_status"` // e.g. 200
}

type ApplicationDependency struct {
	LogicalName          string                          `json:"logical_name"`
	TargetKind           string                          `json:"target_kind"`
	TargetIdentity       string                          `json:"target_identity"`
	Protocol             string                          `json:"protocol"`
	Strategy             string                          `json:"strategy,omitempty"`
	AccessContext        string                          `json:"access_context,omitempty"`
	Path                 string                          `json:"path,omitempty"`
	Required             bool                            `json:"required"`
	InjectionPhase       string                          `json:"injection_phase"`
	InjectionMappings    []DependencyInjectionMapping    `json:"injection_mappings,omitempty"`
	VerificationContract *DependencyVerificationContract `json:"verification_contract,omitempty"`
}

type ServiceConfigurationDraft struct {
	SchemaVersion    string                             `json:"schema_version"`
	Environment      []deploymentv1.EnvironmentVariable `json:"environment,omitempty"`
	SecretReferences []deploymentv1.SecretReference     `json:"secret_references,omitempty"`
	PublicRoute      *PublicRouteIntent                 `json:"public_route,omitempty"`
	Bindings         []Binding                          `json:"bindings,omitempty"`
	ResourceBindings []ResourceBinding                  `json:"resource_bindings,omitempty"`
	Dependencies     []ApplicationDependency            `json:"dependencies,omitempty"`
}

type Configuration struct {
	ServiceConfigurationDraft
	Revision  uint64     `json:"revision"`
	StateHash string     `json:"state_hash"`
	AppliedBy string     `json:"applied_by,omitempty"`
	AppliedAt *time.Time `json:"applied_at,omitempty"`
}

type Draft = ServiceConfigurationDraft

func Normalize(draft ServiceConfigurationDraft) ServiceConfigurationDraft {
	draft.SchemaVersion = SchemaVersion
	draft.Environment = append([]deploymentv1.EnvironmentVariable(nil), draft.Environment...)
	draft.SecretReferences = append([]deploymentv1.SecretReference(nil), draft.SecretReferences...)
	draft.Bindings = append([]Binding(nil), draft.Bindings...)
	draft.ResourceBindings = append([]ResourceBinding(nil), draft.ResourceBindings...)
	sort.Slice(draft.Environment, func(i, j int) bool { return draft.Environment[i].Name < draft.Environment[j].Name })
	sort.Slice(draft.SecretReferences, func(i, j int) bool { return draft.SecretReferences[i].EnvName < draft.SecretReferences[j].EnvName })
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
		if binding.Kind == BindingBrowserHTTP && binding.Path == "" {
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
	for i := range draft.ResourceBindings {
		draft.ResourceBindings[i].LogicalName = strings.TrimSpace(draft.ResourceBindings[i].LogicalName)
		draft.ResourceBindings[i].BindingID = strings.TrimSpace(draft.ResourceBindings[i].BindingID)
	}
	sort.Slice(draft.ResourceBindings, func(i, j int) bool {
		first, second := draft.ResourceBindings[i], draft.ResourceBindings[j]
		return first.LogicalName+"\x00"+first.BindingID < second.LogicalName+"\x00"+second.BindingID
	})

	draft.Dependencies = append([]ApplicationDependency(nil), draft.Dependencies...)
	for i := range draft.Dependencies {
		draft.Dependencies[i].LogicalName = strings.TrimSpace(draft.Dependencies[i].LogicalName)
		draft.Dependencies[i].TargetKind = strings.TrimSpace(draft.Dependencies[i].TargetKind)
		draft.Dependencies[i].TargetIdentity = strings.TrimSpace(draft.Dependencies[i].TargetIdentity)
		draft.Dependencies[i].Protocol = strings.TrimSpace(draft.Dependencies[i].Protocol)
		draft.Dependencies[i].Strategy = strings.TrimSpace(draft.Dependencies[i].Strategy)
		draft.Dependencies[i].AccessContext = strings.TrimSpace(draft.Dependencies[i].AccessContext)
		draft.Dependencies[i].Path = strings.TrimSpace(draft.Dependencies[i].Path)
		if draft.Dependencies[i].Path != "" {
			if normalizedPath, err := exposurev1.NormalizePath(draft.Dependencies[i].Path); err == nil {
				draft.Dependencies[i].Path = normalizedPath
			}
		}
		draft.Dependencies[i].InjectionPhase = strings.TrimSpace(draft.Dependencies[i].InjectionPhase)

		draft.Dependencies[i].InjectionMappings = append([]DependencyInjectionMapping(nil), draft.Dependencies[i].InjectionMappings...)
		for j := range draft.Dependencies[i].InjectionMappings {
			draft.Dependencies[i].InjectionMappings[j].EnvName = strings.TrimSpace(draft.Dependencies[i].InjectionMappings[j].EnvName)
			draft.Dependencies[i].InjectionMappings[j].SymbolicSource = strings.TrimSpace(draft.Dependencies[i].InjectionMappings[j].SymbolicSource)
		}
		sort.Slice(draft.Dependencies[i].InjectionMappings, func(k, l int) bool {
			return draft.Dependencies[i].InjectionMappings[k].EnvName < draft.Dependencies[i].InjectionMappings[l].EnvName
		})
	}
	sort.Slice(draft.Dependencies, func(i, j int) bool {
		return draft.Dependencies[i].LogicalName < draft.Dependencies[j].LogicalName
	})

	return draft
}

func StateHash(draft ServiceConfigurationDraft) string {
	data, _ := json.Marshal(Normalize(draft))
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
