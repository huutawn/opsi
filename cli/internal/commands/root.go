package commands

import (
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	"github.com/spf13/cobra"
)

type Options struct {
	Version         string
	KeychainFactory func() (keychain.Store, error)
}

func NewRootCommand(options Options) *cobra.Command {
	if options.Version == "" {
		options.Version = "dev"
	}
	if options.KeychainFactory == nil {
		options.KeychainFactory = func() (keychain.Store, error) { return keychain.NewOSStore() }
	}

	var configPath string
	root := &cobra.Command{
		Use:           "opsi",
		Short:         "Local-first SRE tooling",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStart(cmd.Context(), "127.0.0.1:9780", cmd.OutOrStdout())
		},
	}
	root.PersistentFlags().StringVar(&configPath, "config", "", "path to CLI YAML config")

	root.AddCommand(newStatusCommand(&configPath))
	root.AddCommand(newDeployCommand(&configPath))
	root.AddCommand(newSyncCommand(&configPath))
	root.AddCommand(newSecretCommand(&configPath, options.KeychainFactory))
	root.AddCommand(newLoginCommand(options.KeychainFactory))
	root.AddCommand(newStartCommand())
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print CLI version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := cmd.OutOrStdout().Write([]byte(options.Version + "\n"))
			return err
		},
	})

	return root
}
