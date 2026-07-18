package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	"github.com/spf13/cobra"
)

const githubCommandTimeout = 30 * time.Second

type githubFlags struct {
	projectID    string
	repositoryID int64
	serviceID    string
	serviceKey   string
	configPath   string
	bindingID    string
}

func newGitHubCommand(configPath *string, options Options) *cobra.Command {
	command := &cobra.Command{Use: "github", Short: "Manage GitHub App repository bindings"}
	command.AddCommand(newGitHubInstallationListCommand(configPath, options))
	command.AddCommand(newGitHubRepositoryCommand(configPath, options))
	command.AddCommand(newGitHubBindingCommand(configPath, options))
	return command
}

func newGitHubInstallationListCommand(configPath *string, options Options) *cobra.Command {
	flags := &githubFlags{}
	command := &cobra.Command{Use: "installation", Short: "Inspect GitHub App installations"}
	command.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List GitHub App installations linked to a project",
		RunE: func(command *cobra.Command, _ []string) error {
			client, ctx, cancel, err := githubClient(command.Context(), *configPath, options, flags.projectID)
			if err != nil {
				return err
			}
			defer cancel()
			installations, err := client.ListGitHubInstallations(ctx, flags.projectID)
			if err != nil {
				return err
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(map[string]any{"installations": installations})
		},
	})
	command.Commands()[0].Flags().StringVar(&flags.projectID, "project-id", "", "project id")
	return command
}

func newGitHubRepositoryCommand(configPath *string, options Options) *cobra.Command {
	flags := &githubFlags{}
	command := &cobra.Command{Use: "repository", Short: "Manage claimed GitHub repositories"}
	command.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List GitHub repositories available to a project",
		RunE: func(command *cobra.Command, _ []string) error {
			client, ctx, cancel, err := githubClient(command.Context(), *configPath, options, flags.projectID)
			if err != nil {
				return err
			}
			defer cancel()
			repositories, err := client.ListGitHubRepositories(ctx, flags.projectID)
			if err != nil {
				return err
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(map[string]any{"repositories": repositories})
		},
	})
	command.AddCommand(&cobra.Command{
		Use:   "claim",
		Short: "Claim a GitHub repository for a project",
		RunE: func(command *cobra.Command, _ []string) error {
			if err := requireGitHubRepositoryID(flags.repositoryID); err != nil {
				return err
			}
			client, ctx, cancel, err := githubClient(command.Context(), *configPath, options, flags.projectID)
			if err != nil {
				return err
			}
			defer cancel()
			claim, err := client.ClaimRepository(ctx, flags.projectID, flags.repositoryID)
			if err != nil {
				return err
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(claim)
		},
	})
	command.AddCommand(&cobra.Command{
		Use:   "release",
		Short: "Release a GitHub repository from a project",
		RunE: func(command *cobra.Command, _ []string) error {
			if err := requireGitHubRepositoryID(flags.repositoryID); err != nil {
				return err
			}
			client, ctx, cancel, err := githubClient(command.Context(), *configPath, options, flags.projectID)
			if err != nil {
				return err
			}
			defer cancel()
			if err := client.ReleaseRepository(ctx, flags.projectID, flags.repositoryID); err != nil {
				return err
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(map[string]bool{"released": true})
		},
	})
	for _, child := range command.Commands() {
		child.Flags().StringVar(&flags.projectID, "project-id", "", "project id")
		if child.Name() != "list" {
			child.Flags().Int64Var(&flags.repositoryID, "repository-id", 0, "numeric GitHub repository id")
		}
	}
	return command
}

func newGitHubBindingCommand(configPath *string, options Options) *cobra.Command {
	flags := &githubFlags{configPath: defaultConfigPath}
	command := &cobra.Command{Use: "binding", Short: "Manage GitHub service bindings"}
	command.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List GitHub service bindings for a project",
		RunE: func(command *cobra.Command, _ []string) error {
			client, ctx, cancel, err := githubClient(command.Context(), *configPath, options, flags.projectID)
			if err != nil {
				return err
			}
			defer cancel()
			bindings, err := client.ListGitHubBindings(ctx, flags.projectID)
			if err != nil {
				return err
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(map[string]any{"bindings": bindings})
		},
	})
	command.AddCommand(&cobra.Command{
		Use:   "create",
		Short: "Create a GitHub repository service binding",
		RunE: func(command *cobra.Command, _ []string) error {
			if err := validateGitHubBindingInput(flags); err != nil {
				return err
			}
			client, ctx, cancel, err := githubClient(command.Context(), *configPath, options, flags.projectID)
			if err != nil {
				return err
			}
			defer cancel()
			binding, err := client.CreateServiceBinding(ctx, flags.projectID, flags.serviceID, flags.repositoryID, flags.serviceKey, flags.configPath)
			if err != nil {
				return err
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(binding)
		},
	})
	command.AddCommand(&cobra.Command{
		Use:   "remove",
		Short: "Remove a GitHub repository service binding",
		RunE: func(command *cobra.Command, _ []string) error {
			if flags.bindingID == "" || len(flags.bindingID) > 256 {
				return errors.New("binding-id is required and must be at most 256 bytes")
			}
			client, ctx, cancel, err := githubClient(command.Context(), *configPath, options, flags.projectID)
			if err != nil {
				return err
			}
			defer cancel()
			if err := client.RemoveServiceBinding(ctx, flags.projectID, flags.bindingID); err != nil {
				return err
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(map[string]bool{"removed": true})
		},
	})
	for _, child := range command.Commands() {
		child.Flags().StringVar(&flags.projectID, "project-id", "", "project id")
		switch child.Name() {
		case "create":
			child.Flags().StringVar(&flags.serviceID, "service-id", "", "project service id")
			child.Flags().Int64Var(&flags.repositoryID, "repository-id", 0, "numeric GitHub repository id")
			child.Flags().StringVar(&flags.serviceKey, "service-key", "", "repository service key")
			child.Flags().StringVar(&flags.configPath, "config-path", flags.configPath, "repository-relative Opsi config path")
		case "remove":
			child.Flags().StringVar(&flags.bindingID, "binding-id", "", "GitHub binding id")
		}
	}
	return command
}

func githubClient(parent context.Context, configPath string, options Options, projectID string) (*cloudclient.Client, context.Context, context.CancelFunc, error) {
	if projectID == "" {
		return nil, nil, nil, errors.New("project-id is required")
	}
	ctx, cancel := context.WithTimeout(parent, githubCommandTimeout)
	client, err := newCommandCloudClient(configPath, options)
	if err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("create GitHub Cloud client: %w", err)
	}
	return client, ctx, cancel, nil
}

func requireGitHubRepositoryID(repositoryID int64) error {
	if repositoryID <= 0 {
		return errors.New("repository-id must be a positive integer")
	}
	return nil
}

func validateGitHubBindingInput(flags *githubFlags) error {
	if err := requireGitHubRepositoryID(flags.repositoryID); err != nil {
		return err
	}
	if flags.serviceID == "" || flags.serviceKey == "" {
		return errors.New("service-id and service-key are required")
	}
	if len(flags.configPath) == 0 || len(flags.configPath) > 256 {
		return errors.New("config-path is required and must be at most 256 bytes")
	}
	return nil
}
