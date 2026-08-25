// Package repositoryexport renders the optional, reviewable repository
// documentation for a canonical Cloud deployment plan. It never performs a
// repository mutation; the GitHub boundary owns that audited operation.
package repositoryexport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/opsi-dev/opsi/cloud/internal/deploymentworkflow"
	"github.com/opsi-dev/opsi/cloud/internal/repositoryanalysis"
	resourcecompiler "github.com/opsi-dev/opsi/cloud/internal/resource/connection"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	"gopkg.in/yaml.v3"
)

const Path = ".opsi/opsi-cd.yaml"

type Preview struct {
	RunID        string `json:"run_id"`
	RunRevision  uint64 `json:"run_revision"`
	PlanHash     string `json:"plan_hash"`
	SourceSHA    string `json:"source_sha"`
	RepositoryID int64  `json:"repository_id"`
	TargetBranch string `json:"target_branch"`
	Path         string `json:"path"`
	YAML         string `json:"yaml"`
	Diff         string `json:"diff"`
	PreviewHash  string `json:"preview_hash"`
}

type config struct {
	Version   int        `yaml:"version"`
	Resources []resource `yaml:"resources,omitempty"`
	Services  []service  `yaml:"services"`
}
type resource struct {
	LogicalName string                          `yaml:"logicalName"`
	Type        string                          `yaml:"type"`
	Managed     bool                            `yaml:"managed"`
	Required    bool                            `yaml:"required"`
	Persistence *repositoryanalysis.Persistence `yaml:"persistence,omitempty"`
	Settings    map[string]string               `yaml:"settings,omitempty"`
}
type service struct {
	Key          string   `yaml:"key"`
	Build        build    `yaml:"build"`
	WatchPaths   []string `yaml:"watchPaths"`
	SharedPaths  []string `yaml:"sharedPaths"`
	Dependencies []string `yaml:"dependencies"`
	Runtime      *runtime `yaml:"runtime,omitempty"`
	Deploy       deploy   `yaml:"deploy"`
}
type build struct {
	Context    string `yaml:"context"`
	Dockerfile string `yaml:"dockerfile"`
	Platform   string `yaml:"platform"`
}
type runtime struct {
	Port         int                          `yaml:"port,omitempty"`
	Environment  map[string]string            `yaml:"environment,omitempty"`
	Capacity     *repositoryanalysis.Capacity `yaml:"capacity,omitempty"`
	Exposure     *repositoryanalysis.Exposure `yaml:"exposure,omitempty"`
	Secrets      []secret                     `yaml:"secrets,omitempty"`
	Bindings     []binding                    `yaml:"bindings,omitempty"`
	Dependencies []dependency                 `yaml:"dependencies,omitempty"`
}
type secret struct {
	LogicalName     string `yaml:"logicalName"`
	EnvironmentName string `yaml:"environmentName"`
	Source          string `yaml:"source"`
	Reference       string `yaml:"reference,omitempty"`
}
type binding struct {
	Kind   string `yaml:"kind"`
	Target string `yaml:"target"`
	Path   string `yaml:"path,omitempty"`
}
type injection struct {
	EnvironmentName string `yaml:"environmentName"`
	SymbolicSource  string `yaml:"symbolicSource"`
	Template        string `yaml:"template,omitempty"`
}
type dependency struct {
	Target       string                                   `yaml:"target"`
	Protocol     string                                   `yaml:"protocol"`
	Strategy     string                                   `yaml:"strategy,omitempty"`
	Path         string                                   `yaml:"path,omitempty"`
	Required     bool                                     `yaml:"required"`
	Injections   []injection                              `yaml:"injections,omitempty"`
	Verification *repositoryanalysis.VerificationContract `yaml:"verification,omitempty"`
}
type deploy struct {
	Production production    `yaml:"production"`
	Preview    previewIntent `yaml:"preview"`
}
type production struct {
	Enabled  bool     `yaml:"enabled"`
	Branches []string `yaml:"branches"`
}
type previewIntent struct {
	Enabled      bool `yaml:"enabled"`
	PullRequests bool `yaml:"pullRequests"`
}

func Render(plan deploymentworkflow.Plan) ([]byte, error) {
	if plan.Hash == "" || len(plan.Source.CommitSHA) != 40 || len(plan.Applications) == 0 {
		return nil, errors.New("canonical deployment plan is required")
	}
	canonicalHash, err := deploymentworkflow.HashPlan(plan)
	if err != nil || canonicalHash != plan.Hash {
		return nil, errors.New("deployment plan hash is invalid")
	}
	keyByCanonical := map[string]string{}
	for _, app := range plan.Applications {
		keyByCanonical[app.Key] = app.SourceKey
	}
	cfg := config{Version: 2, Resources: make([]resource, 0, len(plan.Resources)), Services: make([]service, 0, len(plan.Applications))}
	for _, item := range plan.Resources {
		settings := map[string]string{}
		for name, value := range item.Settings {
			if !deploymentv1.IsSecretLikeEnvironmentName(name) {
				settings[name] = value
			}
		}
		if len(settings) == 0 {
			settings = nil
		}
		cfg.Resources = append(cfg.Resources, resource{LogicalName: item.LogicalName, Type: item.Type, Managed: item.Managed, Required: item.Required, Persistence: item.Persistence, Settings: settings})
	}
	for _, app := range plan.Applications {
		if app.Build.Strategy != "dockerfile" || app.Build.DockerfilePath == "" {
			return nil, errors.New("repository export requires Dockerfile-based applications")
		}
		item := service{Key: app.SourceKey, Build: build{Context: app.Build.Context, Dockerfile: app.Build.DockerfilePath, Platform: app.Build.Platform}, WatchPaths: []string{}, SharedPaths: []string{}, Dependencies: []string{}, Deploy: deploy{Production: production{Enabled: true, Branches: []string{plan.Source.SelectedRef}}, Preview: previewIntent{}}}
		rt := &runtime{Port: app.Port, Environment: copySafeEnvironment(app.Environment)}
		if app.Capacity != (repositoryanalysis.Capacity{}) {
			capacity := app.Capacity
			rt.Capacity = &capacity
		}
		if app.Exposure != (repositoryanalysis.Exposure{}) {
			exposure := app.Exposure
			rt.Exposure = &exposure
		}
		for _, value := range plan.Secrets {
			if value.ApplicationKey == app.Key {
				source, reference := "external", "secret://"+value.Name
				if value.Generated {
					source, reference = "generated", ""
				}
				rt.Secrets = append(rt.Secrets, secret{LogicalName: value.Name, EnvironmentName: value.EnvironmentName, Source: source, Reference: reference})
			}
		}
		for _, value := range plan.Bindings {
			if value.From == app.Key {
				target := keyByCanonical[value.To]
				if target == "" {
					target = value.To
				}
				rt.Bindings = append(rt.Bindings, binding{Kind: value.Kind, Target: target, Path: value.Path})
			}
		}
		for _, value := range plan.Dependencies {
			if value.From == app.Key {
				target := keyByCanonical[value.To]
				if target == "" {
					target = value.To
				}
				mapped := dependency{Target: target, Protocol: value.Protocol, Strategy: value.Strategy, Path: value.Path, Required: value.Required, Verification: value.Verification}
				for _, inject := range value.Injections {
					if value.Protocol == "http" {
						if !resourcecompiler.ValidApplicationSource(value.Strategy, inject.SymbolicSource, inject.Template) {
							return nil, errors.New("repository export contains an invalid application mapping")
						}
					} else if _, lookupErr := resourcecompiler.LookupSource(value.Protocol, inject.SymbolicSource, inject.Template); lookupErr != nil {
						return nil, errors.New("repository export contains an invalid connection mapping")
					}
					mapped.Injections = append(mapped.Injections, injection{EnvironmentName: inject.EnvironmentName, SymbolicSource: canonicalExportSource(value.Protocol, inject.SymbolicSource), Template: inject.Template})
				}
				rt.Dependencies = append(rt.Dependencies, mapped)
			}
		}
		sort.Slice(rt.Secrets, func(i, j int) bool { return rt.Secrets[i].LogicalName < rt.Secrets[j].LogicalName })
		sort.Slice(rt.Bindings, func(i, j int) bool {
			return rt.Bindings[i].Target+rt.Bindings[i].Path < rt.Bindings[j].Target+rt.Bindings[j].Path
		})
		sort.Slice(rt.Dependencies, func(i, j int) bool {
			return rt.Dependencies[i].Target+rt.Dependencies[i].Path < rt.Dependencies[j].Target+rt.Dependencies[j].Path
		})
		item.Runtime = rt
		cfg.Services = append(cfg.Services, item)
	}
	sort.Slice(cfg.Resources, func(i, j int) bool { return cfg.Resources[i].LogicalName < cfg.Resources[j].LogicalName })
	sort.Slice(cfg.Services, func(i, j int) bool { return cfg.Services[i].Key < cfg.Services[j].Key })
	var output bytes.Buffer
	output.WriteString("# Exported by Opsi from a reviewed Cloud deployment plan.\n")
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(cfg); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func canonicalExportSource(protocol, source string) string {
	if source == "connection.url" || strings.HasPrefix(source, "resource.") && strings.HasSuffix(source, ".connection_string") {
		switch protocol {
		case "postgres":
			return "connection.postgres.uri"
		case "redis":
			return "connection.redis.uri"
		case "nats":
			return "connection.nats.uri"
		}
	}
	return source
}

func NewPreview(run deploymentworkflow.Run, targetBranch string, current []byte) (Preview, error) {
	if targetBranch == "" {
		return Preview{}, errors.New("target branch is required")
	}
	rendered, err := Render(run.Plan)
	if err != nil {
		return Preview{}, err
	}
	p := Preview{RunID: run.ID, RunRevision: run.Revision, PlanHash: run.Plan.Hash, SourceSHA: run.Plan.Source.CommitSHA, RepositoryID: run.Plan.Source.RepositoryID, TargetBranch: targetBranch, Path: Path, YAML: string(rendered), Diff: textDiff(Path, current, rendered)}
	encoded, _ := json.Marshal(p)
	digest := sha256.Sum256(encoded)
	p.PreviewHash = hex.EncodeToString(digest[:])
	return p, nil
}

func copySafeEnvironment(input map[string]string) map[string]string {
	out := map[string]string{}
	for name, value := range input {
		if !deploymentv1.IsSecretLikeEnvironmentName(name) {
			out[name] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
func textDiff(name string, oldData, newData []byte) string {
	if bytes.Equal(oldData, newData) {
		return ""
	}
	var b strings.Builder
	b.WriteString("--- a/" + name + "\n+++ b/" + name + "\n")
	for _, line := range strings.Split(strings.TrimSuffix(string(oldData), "\n"), "\n") {
		if line != "" || len(oldData) > 0 {
			b.WriteString("-" + line + "\n")
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(newData), "\n"), "\n") {
		b.WriteString("+" + line + "\n")
	}
	return b.String()
}
