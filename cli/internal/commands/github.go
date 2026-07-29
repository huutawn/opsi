package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	"github.com/opsi-dev/opsi/cli/internal/repository"
	"github.com/spf13/cobra"
)

const githubCommandTimeout = 30 * time.Second

type githubFlags struct {
	projectID      string
	installationID int64
	noBrowser      bool
	timeout        time.Duration
	repositoryID   int64
	serviceID      string
	serviceKey     string
	configPath     string
	bindingID      string
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
	list := &cobra.Command{
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
	}
	list.Flags().StringVar(&flags.projectID, "project-id", "", "project id")
	command.AddCommand(list)

	claim := &cobra.Command{
		Use:   "claim",
		Short: "Claim a GitHub App installation through the browser",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := validateGitHubProjectID(flags.projectID); err != nil {
				return err
			}
			if flags.installationID <= 0 {
				return errors.New("installation-id must be a positive integer")
			}
			if flags.timeout <= 0 || flags.timeout > 5*time.Minute {
				return errors.New("timeout must be greater than zero and at most 5m")
			}
			client, err := newCommandCloudClient(*configPath, options)
			if err != nil {
				return fmt.Errorf("create GitHub Cloud client: %w", err)
			}
			ctx, cancel := context.WithTimeout(command.Context(), flags.timeout)
			defer cancel()
			claimOptions := initOptions{
				ProjectID:      flags.projectID,
				InstallationID: flags.installationID,
				NoBrowser:      flags.noBrowser,
				Timeout:        flags.timeout,
			}
			if err := runInstallationClaim(ctx, command.OutOrStdout(), client, options, claimOptions); err != nil {
				return fmt.Errorf("GitHub installation claim failed: %w", err)
			}
			return nil
		},
	}
	claim.Flags().StringVar(&flags.projectID, "project-id", "", "project id")
	claim.Flags().Int64Var(&flags.installationID, "installation-id", 0, "numeric GitHub App installation id")
	claim.Flags().BoolVar(&flags.noBrowser, "no-browser", false, "print authorization URL without opening a browser")
	claim.Flags().DurationVar(&flags.timeout, "timeout", 5*time.Minute, "overall browser callback timeout")
	command.AddCommand(claim)
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
			if err := validateGitHubOpaqueID("binding-id", flags.bindingID); err != nil {
				return err
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
	if err := validateGitHubProjectID(projectID); err != nil {
		return nil, nil, nil, err
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
	if err := validateGitHubProjectID(flags.projectID); err != nil {
		return err
	}
	if err := requireGitHubRepositoryID(flags.repositoryID); err != nil {
		return err
	}
	if err := validateGitHubOpaqueID("service-id", flags.serviceID); err != nil {
		return err
	}
	if err := repository.ValidateServiceKey(flags.serviceKey); err != nil {
		return fmt.Errorf("invalid service-key: %w", err)
	}
	if err := repository.ValidateConfigPath(flags.configPath); err != nil {
		return fmt.Errorf("invalid config-path: %w", err)
	}
	return nil
}

func validateGitHubProjectID(projectID string) error {
	return validateGitHubOpaqueID("project-id", projectID)
}

func validateGitHubOpaqueID(label, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > 256 || trimmed != value || strings.ContainsAny(value, "/\\") {
		return fmt.Errorf("%s is required and must be a non-path identifier of at most 256 bytes", label)
	}
	return nil
}
