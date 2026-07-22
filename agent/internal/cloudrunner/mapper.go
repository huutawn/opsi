package cloudrunner

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	"github.com/opsi-dev/opsi/agent/internal/config"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

var errImageSourceUnsupported = errors.New("image source deploy is not supported by the Agent runner yet")

func RolloutIntentFromLease(lease cloudrelay.DeploymentLease, nodeID string) (deploymentv1.RolloutIntent, error) {
	if lease.Command == nil || lease.Command.Rollout == nil {
		return deploymentv1.RolloutIntent{}, errors.New("rollout command is required")
	}
	command := lease.Command
	intent, err := command.Rollout.Canonicalize()
	if err != nil {
		return deploymentv1.RolloutIntent{}, fmt.Errorf("invalid rollout intent: %w", err)
	}
	if command.SchemaVersion != deploymentv1.CommandSchemaVersion || command.JobID != lease.Deployment.ID || command.JobID != intent.Desired.DeploymentJobID ||
		command.LeaseToken == "" || command.LeaseToken != lease.LeaseToken || lease.Action != intent.Operation ||
		command.ProjectID != intent.Target.ProjectID || command.EnvironmentID != intent.Target.EnvironmentID ||
		command.RuntimeID != intent.Target.RuntimeID || command.NodeID != nodeID || command.NodeID != intent.Target.NodeID ||
		command.AgentID != intent.Target.AgentID || command.Attempt < intent.Attempt || command.Attempt < 1 {
		return deploymentv1.RolloutIntent{}, errors.New("rollout command target or attempt does not match its immutable intent")
	}
	commandHash, hashErr := command.Workload.Hash()
	if hashErr != nil || command.Image != intent.Desired.Image || command.SpecHash != intent.Desired.WorkloadSpecHash || commandHash != intent.Desired.WorkloadSpecHash {
		return deploymentv1.RolloutIntent{}, errors.New("rollout command workload or image does not match its immutable intent")
	}
	return intent, nil
}

func RequestFromLease(lease cloudrelay.DeploymentLease, cfg config.DeploymentConfig) (deploy.Request, error) {
	if lease.Command != nil {
		request := deploy.Request{Production: lease.Command}
		return request, request.Validate()
	}
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
