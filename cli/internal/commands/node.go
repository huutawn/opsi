package commands

import (
	"errors"

	"github.com/spf13/cobra"
)

func newNodeCommand(configPath *string, options Options) *cobra.Command {
	var projectID, nodeID, key string
	cmd := &cobra.Command{Use: "node", Short: "Inspect and manage Cloud nodes"}

	list := &cobra.Command{Use: "list", Short: "List project nodes"}
	var listJSON bool
	list.RunE = func(command *cobra.Command, _ []string) error {
		if projectID == "" {
			return errors.New("project-id is required")
		}
		client, ctx, cancel, err := newManualCloudRequest(command.Context(), *configPath, options)
		if err != nil {
			return err
		}
		defer cancel()
		nodes, err := client.ListNodes(ctx, projectID)
		if err != nil {
			return err
		}
		return writeManualOutput(command, map[string]any{"nodes": nodes}, listJSON)
	}
	list.Flags().BoolVar(&listJSON, "json", false, "print JSON output")

	get := &cobra.Command{Use: "get", Short: "Show node diagnostics"}
	var getJSON bool
	get.RunE = func(command *cobra.Command, _ []string) error {
		if projectID == "" || nodeID == "" {
			return errors.New("project-id and node-id are required")
		}
		client, ctx, cancel, err := newManualCloudRequest(command.Context(), *configPath, options)
		if err != nil {
			return err
		}
		defer cancel()
		value, err := client.GetNode(ctx, projectID, nodeID)
		if err != nil {
			return err
		}
		return writeManualOutput(command, value, getJSON)
	}
	get.Flags().StringVar(&nodeID, "node-id", "", "node id")
	get.Flags().BoolVar(&getJSON, "json", false, "print JSON output")

	for _, action := range []string{"offline", "drain", "remove"} {
		action := action
		var yes, force, jsonOutput bool
		mutation := &cobra.Command{Use: action, Short: action + " a project node"}
		mutation.RunE = func(command *cobra.Command, _ []string) error {
			if projectID == "" || nodeID == "" || key == "" || !yes {
				return errors.New("project-id, node-id, idempotency-key and --yes are required")
			}
			client, ctx, cancel, err := newManualCloudRequest(command.Context(), *configPath, options)
			if err != nil {
				return err
			}
			defer cancel()
			var value any
			if action == "offline" {
				value, err = client.MarkNodeOffline(ctx, projectID, nodeID, key)
			} else {
				value, err = client.NodeLifecycle(ctx, projectID, nodeID, action, key, action == "remove", force)
			}
			if err != nil {
				return err
			}
			return writeManualOutput(command, value, jsonOutput)
		}
		mutation.Flags().StringVar(&nodeID, "node-id", "", "node id")
		mutation.Flags().StringVar(&key, "idempotency-key", "", "mutation idempotency key")
		mutation.Flags().BoolVar(&yes, "yes", false, "confirm the node mutation")
		mutation.Flags().BoolVar(&jsonOutput, "json", false, "print JSON output")
		if action == "remove" {
			mutation.Flags().BoolVar(&force, "force", false, "request forced removal")
		}
		cmd.AddCommand(mutation)
	}
	for _, command := range []*cobra.Command{list, get} {
		command.Flags().StringVar(&projectID, "project-id", "", "project id")
	}
	for _, command := range cmd.Commands() {
		if command != list && command != get {
			command.Flags().StringVar(&projectID, "project-id", "", "project id")
		}
	}
	cmd.AddCommand(list, get)
	return cmd
}
