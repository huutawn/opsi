package commands

import (
	"fmt"
	"strings"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
	"github.com/spf13/cobra"
)

func newLoginCommand(factory func() (keychain.Store, error)) *cobra.Command {
	var pat string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Store a Cloud PAT in the OS keychain",
		RunE: func(cmd *cobra.Command, _ []string) error {
			pat = strings.TrimSpace(pat)
			if pat == "" {
				return fmt.Errorf("--pat is required in Phase 1; OAuth login is added in a later phase")
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
	cmd.Flags().StringVar(&pat, "pat", "", "personal access token returned by Cloud")
	return cmd
}
