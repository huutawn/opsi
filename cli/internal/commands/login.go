package commands

import (
	"fmt"
	"strings"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
	"github.com/spf13/cobra"
)

func newLoginCommand(factory func() (keychain.Store, error)) *cobra.Command {
	var patFile string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Store a Cloud PAT in the OS keychain",
		RunE: func(cmd *cobra.Command, _ []string) error {
			value, err := readProtectedSecret(patFile, "PAT")
			if err != nil {
				return err
			}
			defer clearBytes(value)
			pat := strings.TrimSpace(string(value))
			if pat == "" {
				return fmt.Errorf("PAT file is empty")
			}
			store, err := factory()
			if err != nil {
				return err
			}
			if err := store.SetPAT(pat); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "PAT stored in OS keychain")
			return err
		},
	}
	cmd.Flags().StringVar(&patFile, "pat-file", "", "protected PAT file; use /dev/stdin for piped input")
	return cmd
}
