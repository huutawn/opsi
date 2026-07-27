package commands

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	"github.com/spf13/cobra"
)

func writeManualOutput(command *cobra.Command, value any, jsonOutput bool) error {
	encoder := json.NewEncoder(command.OutOrStdout())
	if !jsonOutput {
		encoder.SetIndent("", "  ")
	}
	return encoder.Encode(value)
}

func newProjectCommand(configPath *string, options Options) *cobra.Command {
	var orgID string
	cmd := &cobra.Command{Use: "project", Short: "List and create Cloud projects"}
	list := &cobra.Command{Use: "list", Short: "List projects in an organization"}
	var listJSON bool
	list.RunE = func(command *cobra.Command, _ []string) error {
		if orgID == "" {
			return errors.New("org-id is required")
		}
		client, ctx, cancel, err := newManualCloudRequest(command.Context(), *configPath, options)
		if err != nil {
			return err
		}
		defer cancel()
		projects, err := client.ListProjects(ctx, orgID)
		if err != nil {
			return err
		}
		return writeManualOutput(command, map[string]any{"projects": projects}, listJSON)
	}
	list.Flags().StringVar(&orgID, "org-id", "", "organization id")
	list.Flags().BoolVar(&listJSON, "json", false, "print JSON output")

	var name, slug, key string
	var createJSON bool
	create := &cobra.Command{Use: "create", Short: "Create a project"}
	create.RunE = func(command *cobra.Command, _ []string) error {
		if orgID == "" || name == "" || slug == "" || key == "" {
			return errors.New("org-id, name, slug and idempotency-key are required")
		}
		client, ctx, cancel, err := newManualCloudRequest(command.Context(), *configPath, options)
		if err != nil {
			return err
		}
		defer cancel()
		project, err := client.CreateProject(ctx, orgID, name, slug, key)
		if err != nil {
			return err
		}
		return writeManualOutput(command, project, createJSON)
	}
	create.Flags().StringVar(&orgID, "org-id", "", "organization id")
	create.Flags().StringVar(&name, "name", "", "project name")
	create.Flags().StringVar(&slug, "slug", "", "project slug")
	create.Flags().StringVar(&key, "idempotency-key", "", "mutation idempotency key")
	create.Flags().BoolVar(&createJSON, "json", false, "print JSON output")

	cmd.AddCommand(list, create)
	return cmd
}

func newManualCloudRequest(parent context.Context, configPath string, options Options) (*cloudclient.Client, context.Context, context.CancelFunc, error) {
	client, err := newCommandCloudClient(configPath, options)
	if err != nil {
		return nil, nil, nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	return client, ctx, cancel, nil
}
