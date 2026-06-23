package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/agentclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"github.com/spf13/cobra"
)

func newDeployCommand(configPath *string, factory func() (keychain.Store, error)) *cobra.Command {
	var req agentv1.DeployRequest
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy a service through the Agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
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
	flags.StringVar(&req.Registry, "registry", "", "Target registry prefix")
	flags.StringVar(&req.ImageTag, "image-tag", "", "Image tag override")
	flags.StringVar(&req.TriggeredBy, "triggered-by", "cli", "Deployment actor")
	return cmd
}
