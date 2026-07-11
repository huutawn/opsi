package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/agentclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"github.com/spf13/cobra"
)

func newDeployCommand(configPath *string, factory func() (keychain.Store, error)) *cobra.Command {
	var req agentv1.DeployRequest
	var resourceRequests []string
	var resourceLimits []string
	var dependsOn []string
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy a service through the Agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(resourceRequests) > 0 {
				encoded, err := encodeResourceFlags(resourceRequests)
				if err != nil {
					return err
				}
				req.ResourceRequestsJSON = encoded
			}
			if len(resourceLimits) > 0 {
				encoded, err := encodeResourceFlags(resourceLimits)
				if err != nil {
					return err
				}
				req.ResourceLimitsJSON = encoded
			}
			req.DependsOn = deployDependencies(dependsOn)
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Minute)
			defer cancel()
			if pat := optionalPAT(factory); pat != "" {
				ctx = agentclient.WithPAT(ctx, pat)
			}

			enc := json.NewEncoder(cmd.OutOrStdout())
			return agentclient.New(cfg).Deploy(ctx, &req, func(event *agentv1.ProgressEvent) error {
				if err := enc.Encode(event); err != nil {
					return fmt.Errorf("write deploy progress: %w", err)
				}
				return nil
			})
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&req.ProjectID, "project-id", "", "Project scope for the deployment")
	flags.StringVar(&req.ServiceID, "service-id", "", "Project service identifier")
	flags.StringVar(&req.ServiceName, "service-name", "", "Kubernetes deployment/service name")
	flags.StringVar(&req.ServiceName, "service", "", "Alias for --service-name")
	flags.StringVar(&req.ServiceType, "service-type", "", "Service type: backend, frontend, database, storage, message_queue, worker, or custom")
	flags.StringVar(&req.RepoURL, "repo-url", "", "Git repository URL")
	flags.StringVar(&req.Branch, "branch", "", "Git branch")
	flags.StringVar(&req.GitSHA, "git-sha", "", "Git commit SHA to deploy")
	flags.StringVar(&req.Namespace, "namespace", "", "Kubernetes namespace")
	flags.StringVar(&req.BuildContext, "build-context", "", "Docker build context path")
	flags.StringVar(&req.Dockerfile, "dockerfile", "", "Dockerfile path")
	flags.StringVar(&req.ManifestPath, "manifest-path", "", "Kubernetes manifest path")
	flags.StringArrayVar(&req.WatchPaths, "watch-path", nil, "Glob path that triggers rebuild; repeatable")
	flags.StringArrayVar(&dependsOn, "depends-on", nil, "Managed service dependency name; repeatable")
	flags.Int32Var(&req.TerminationGracePeriodSeconds, "termination-grace-period-seconds", 0, "Kubernetes terminationGracePeriodSeconds override")
	flags.StringArrayVar(&resourceRequests, "resource-request", nil, "Resource request key=value, e.g. cpu=100m; repeatable")
	flags.StringArrayVar(&resourceLimits, "resource-limit", nil, "Resource limit key=value, e.g. memory=512Mi; repeatable")
	flags.StringVar(&req.ResourceRequestsJSON, "resource-requests-json", "", "Resource requests JSON override")
	flags.StringVar(&req.ResourceLimitsJSON, "resource-limits-json", "", "Resource limits JSON override")
	flags.StringVar(&req.Registry, "registry", "", "Target registry prefix")
	flags.StringVar(&req.ImageTag, "image-tag", "", "Image tag override")
	flags.StringVar(&req.TriggeredBy, "triggered-by", "cli", "Deployment actor")
	return cmd
}

func deployDependencies(values []string) []agentv1.ServiceDependency {
	if len(values) == 0 {
		return nil
	}
	deps := make([]agentv1.ServiceDependency, 0, len(values))
	for _, value := range values {
		name := strings.TrimSpace(value)
		if name == "" {
			continue
		}
		deps = append(deps, agentv1.ServiceDependency{Name: name})
	}
	return deps
}

func encodeResourceFlags(values []string) (string, error) {
	resources := map[string]string{}
	for _, value := range values {
		key, val, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(key) == "" || strings.TrimSpace(val) == "" {
			return "", fmt.Errorf("resource value must be key=value: %s", value)
		}
		resources[strings.TrimSpace(key)] = strings.TrimSpace(val)
	}
	data, err := json.Marshal(resources)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
