package commands

import (
	"encoding/json"

	"github.com/spf13/cobra"
)

func newVersionCommand(options Options) *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{Use: "version", Short: "Print CLI version", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		value := map[string]string{"version": options.Version, "revision": options.Revision}
		if jsonOutput {
			return json.NewEncoder(command.OutOrStdout()).Encode(value)
		}
		_, err := command.OutOrStdout().Write([]byte(options.Version + " (" + options.Revision + ")\n"))
		return err
	}}
	command.Flags().BoolVar(&jsonOutput, "json", false, "print JSON output")
	return command
}
