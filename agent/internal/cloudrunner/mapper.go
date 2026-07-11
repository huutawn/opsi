package cloudrunner

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	"github.com/opsi-dev/opsi/agent/internal/config"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
)

var errImageSourceUnsupported = errors.New("image source deploy is not supported by the Agent runner yet")

func RequestFromLease(lease cloudrelay.DeploymentLease, cfg config.DeploymentConfig) (deploy.Request, error) {
	sourceType := lease.Service.SourceType
	if lease.Deployment.DeploymentIntent != nil && lease.Deployment.DeploymentIntent.Source.Type != "" {
		sourceType = lease.Deployment.DeploymentIntent.Source.Type
	}
	if sourceType == "image" {
		return deploy.Request{}, errImageSourceUnsupported
	}
	intent := cloudrelay.DeploymentIntent{}
	if lease.Deployment.DeploymentIntent != nil {
		intent = *lease.Deployment.DeploymentIntent
	}
	req := deploy.Request{
		ProjectID:                     firstNonEmpty(intent.ProjectID, cfg.ProjectID),
		ServiceID:                     firstNonEmpty(lease.Service.ID, cfg.ServiceID),
		ServiceName:                   firstNonEmpty(lease.Service.Name, cfg.ServiceName),
		ServiceType:                   firstNonEmpty(lease.Service.Type, cfg.ServiceType, "application"),
		RepoURL:                       firstNonEmpty(intent.Source.RepoURL, lease.Service.RepoURL, cfg.RepoURL),
		Branch:                        firstNonEmpty(intent.Source.Branch, lease.Service.Branch, cfg.Branch),
		GitSHA:                        firstNonEmpty(intent.Source.GitSHA, lease.Service.GitSHA),
		Namespace:                     firstNonEmpty(lease.Service.Namespace, cfg.Namespace),
		BuildContext:                  firstNonEmpty(intent.Source.BuildContext, lease.Service.BuildContext, cfg.BuildContext),
		Dockerfile:                    firstNonEmpty(intent.Source.Dockerfile, lease.Service.Dockerfile, cfg.Dockerfile),
		ManifestPath:                  firstNonEmpty(intent.Source.ManifestPath, lease.Service.ManifestPath, cfg.ManifestPath),
		WatchPaths:                    firstNonEmptySlice(intent.Source.WatchPaths, lease.Service.WatchPaths, cfg.WatchPaths),
		ContainerPort:                 intent.Runtime.ContainerPort,
		HealthPath:                    firstNonEmpty(intent.Health.Path, lease.Service.HealthPath),
		Replicas:                      firstNonZeroInt(intent.Runtime.Replicas, lease.Service.Replicas),
		TerminationGracePeriodSeconds: firstNonZeroInt(cfg.TerminationGracePeriodSeconds, deploy.DefaultTerminationGracePeriodSeconds),
		ResourceRequestsJSON:          resourceSectionJSON(intent.Resources, "requests", cfg.ResourceRequests, deploy.DefaultResourceRequestsJSON),
		ResourceLimitsJSON:            resourceSectionJSON(intent.Resources, "limits", cfg.ResourceLimits, deploy.DefaultResourceLimitsJSON),
		Registry:                      cfg.Registry,
		TriggeredBy:                   "cloud",
		DependsOn:                     dependenciesFromIntent(intent.Bindings),
	}
	req.ProjectID = firstNonEmpty(intent.ProjectID, cfg.ProjectID, req.ProjectID)
	req.Service = req.ServiceName
	if req.ImageTag == "" && req.ProjectID != "" && req.ServiceName != "" && req.GitSHA != "" {
		req.ImageTag = imageTag(req.Registry, req.ProjectID, req.ServiceName, req.GitSHA)
	}
	req = req.WithDefaults()
	return req, req.Validate()
}

func resourceSectionJSON(resources map[string]any, section string, cfg map[string]string, fallback string) string {
	if raw, ok := resources[section]; ok {
		if data, err := json.Marshal(raw); err == nil && string(data) != "{}" && string(data) != "null" {
			return string(data)
		}
	}
	if len(cfg) > 0 {
		if data, err := json.Marshal(cfg); err == nil {
			return string(data)
		}
	}
	return fallback
}

func dependenciesFromIntent(bindings []cloudrelay.DeploymentIntentBinding) []deploy.ServiceDependency {
	if len(bindings) == 0 {
		return nil
	}
	out := make([]deploy.ServiceDependency, 0, len(bindings))
	for _, binding := range bindings {
		name := firstNonEmpty(binding.Alias, binding.ServiceID)
		if name == "" {
			continue
		}
		out = append(out, deploy.ServiceDependency{
			Name:            name,
			EnvPrefix:       binding.EnvPrefix,
			ExposeAsDefault: binding.ExposeAsDefault,
			EnvKeys:         append([]string(nil), binding.EnvKeys...),
		})
	}
	return out
}

func firstNonEmptySlice(values ...[]string) []string {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstNonZeroInt(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func imageTag(registry, projectID, service, sha string) string {
	short := sha
	if len(short) > 12 {
		short = short[:12]
	}
	name := projectID + "/" + service + ":" + short
	if registry == "" {
		return name
	}
	return strings.TrimRight(registry, "/") + "/" + name
}
