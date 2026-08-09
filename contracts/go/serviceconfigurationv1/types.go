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

type ServiceConfigurationDraft struct {
	SchemaVersion string                             `json:"schema_version"`
	Environment   []deploymentv1.EnvironmentVariable `json:"environment,omitempty"`
	PublicRoute   *PublicRouteIntent                 `json:"public_route,omitempty"`
	Bindings      []Binding                          `json:"bindings,omitempty"`
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
	draft.Bindings = append([]Binding(nil), draft.Bindings...)
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
	return draft
}

func StateHash(draft ServiceConfigurationDraft) string {
	data, _ := json.Marshal(Normalize(draft))
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
