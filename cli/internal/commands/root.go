package commands

import (
	"io"
	"net"
	"net/http"

	"github.com/opsi-dev/opsi/cli/internal/keychain"
	"github.com/opsi-dev/opsi/cli/internal/repository"
	"github.com/spf13/cobra"
)

type Options struct {
	Version         string
	Revision        string
	KeychainFactory func() (keychain.Store, error)
	HTTPClient      *http.Client
	GitRunner       repository.CommandRunner
	BrowserOpener   func(string) error
	Listen          func(network, address string) (net.Listener, error)
	Random          io.Reader
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
			return runStart(cmd.Context(), "127.0.0.1:9780", "", configPath, cmd.OutOrStdout(), options.KeychainFactory)
		},
	}
	root.PersistentFlags().StringVar(&configPath, "config", "", "path to CLI YAML config")

	root.AddCommand(newStatusCommand(&configPath, options.KeychainFactory))
	root.AddCommand(newAuthCommand(&configPath, options))
	root.AddCommand(newProjectCommand(&configPath, options))
	root.AddCommand(newNodeCommand(&configPath, options))
	root.AddCommand(newRuntimeCommand(&configPath, options))
	root.AddCommand(newAuditCommand(&configPath, options))
	root.AddCommand(newTelemetryCommand(&configPath, options.KeychainFactory))
	root.AddCommand(newDeployCommand(&configPath, options.KeychainFactory))
	root.AddCommand(newExposureCommand(&configPath, options.KeychainFactory))
	root.AddCommand(newSyncCommand(&configPath, options.KeychainFactory))
	root.AddCommand(newServiceCommand(&configPath, options.KeychainFactory))
	root.AddCommand(newSecretCommand(&configPath, options.KeychainFactory))
	root.AddCommand(newIncidentCommand(&configPath, options.KeychainFactory))
	root.AddCommand(newServerCommand(&configPath, options))
	root.AddCommand(newLoginCommand(options.KeychainFactory))
	root.AddCommand(newInitCommand(&configPath, options))
	root.AddCommand(newCDCommand(&configPath, options))
	root.AddCommand(newGitHubCommand(&configPath, options))
	root.AddCommand(newBuildRecordCommand(&configPath, options))
	root.AddCommand(newTopologyCommand(&configPath, options))
	root.AddCommand(newPolicyCommand(&configPath, options))
	root.AddCommand(newInternalCommand(options))
	root.AddCommand(newStartCommand(&configPath, options.KeychainFactory))
	root.AddCommand(newVersionCommand(options))

	return root
}
