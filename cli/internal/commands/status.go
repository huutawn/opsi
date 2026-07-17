package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/agentclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	"github.com/spf13/cobra"
)

func newStatusCommand(configPath *string, factory func() (keychain.Store, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print Agent status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 200*time.Millisecond)
			defer cancel()
			ctx = agentclient.WithPAT(ctx, optionalPAT(factory))

			status, err := agentclient.New(cfg).Status(ctx)
			if err != nil {
				return fmt.Errorf("status: %w", err)
			}
			data, err := json.MarshalIndent(status, "", "  ")
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return err
		},
	}
}
