package commands

import (
	"errors"

	"github.com/spf13/cobra"
)

func newRuntimeCommand(configPath *string, options Options) *cobra.Command {
	var projectID, runtimeID string
	cmd := &cobra.Command{Use: "runtime", Short: "Inspect Cloud runtime placement facts"}
	for _, action := range []string{"list", "get"} {
		action := action
		var jsonOutput bool
		command := &cobra.Command{Use: action, RunE: func(command *cobra.Command, _ []string) error {
			if projectID == "" || (action == "get" && runtimeID == "") {
				return errors.New("project-id and command-required runtime-id are required")
			}
			client, ctx, cancel, err := newManualCloudRequest(command.Context(), *configPath, options)
			if err != nil {
				return err
			}
			defer cancel()
			facts, err := client.GetPlacementFacts(ctx, projectID)
			if err != nil {
				return err
			}
			if action == "list" {
				return writeManualOutput(command, map[string]any{"runtimes": facts.Runtimes}, jsonOutput)
			}
			for _, runtime := range facts.Runtimes {
				if runtime.ID == runtimeID {
					return writeManualOutput(command, runtime, jsonOutput)
				}
			}
			return errors.New("runtime was not found in the requested project")
		}}
		command.Flags().StringVar(&projectID, "project-id", "", "project id")
		if action == "get" {
			command.Flags().StringVar(&runtimeID, "runtime-id", "", "runtime id")
		}
		command.Flags().BoolVar(&jsonOutput, "json", false, "print JSON output")
		cmd.AddCommand(command)
	}
	return cmd
}
