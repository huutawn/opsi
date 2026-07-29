package commands

import (
	"errors"

	"github.com/spf13/cobra"
)

func newAuditCommand(configPath *string, options Options) *cobra.Command {
	var projectID string
	var jsonOutput bool
	cmd := &cobra.Command{Use: "audit", Short: "List project audit events"}
	list := &cobra.Command{Use: "list", Short: "List redacted audit events", RunE: func(command *cobra.Command, _ []string) error {
		if projectID == "" {
			return errors.New("project-id is required")
		}
		client, ctx, cancel, err := newManualCloudRequest(command.Context(), *configPath, options)
		if err != nil {
			return err
		}
		defer cancel()
		events, err := client.ListAudit(ctx, projectID)
		if err != nil {
			return err
		}
		return writeManualOutput(command, map[string]any{"events": events}, jsonOutput)
	}}
	list.Flags().StringVar(&projectID, "project-id", "", "project id")
	list.Flags().BoolVar(&jsonOutput, "json", false, "print JSON output")
	cmd.AddCommand(list)
	return cmd
}
