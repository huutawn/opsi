package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/agentclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"github.com/spf13/cobra"
)

type serviceFlags struct {
	projectID string
	name      string
	typ       string
	namespace string
	sets      []string
}

func newServiceCommand(configPath *string, factory func() (keychain.Store, error)) *cobra.Command {
	flags := &serviceFlags{}
	cmd := &cobra.Command{Use: "service", Short: "Manage infrastructure services"}
	cmd.AddCommand(newServiceListCatalogCommand(configPath))
	cmd.AddCommand(newServiceCreateCommand(configPath, factory, flags))
	cmd.AddCommand(newServiceStatusCommand(configPath, factory, flags))
	return cmd
}

func newServiceListCatalogCommand(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list-catalog",
		Short: "List supported service catalog entries",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			resp, err := agentclient.New(cfg).ListCatalog(ctx)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(resp)
		},
	}
}

func newServiceCreateCommand(configPath *string, factory func() (keychain.Store, error), flags *serviceFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a managed infrastructure service",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.projectID == "" || flags.name == "" || flags.typ == "" {
				return fmt.Errorf("project-id, name and type are required")
			}
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			overrides, err := parseSetFlags(flags.sets)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			if pat := optionalPAT(factory); pat != "" {
				ctx = agentclient.WithPAT(ctx, pat)
			}
			resp, err := agentclient.New(cfg).CreateManagedService(ctx, &agentv1.CreateManagedServiceRequest{ProjectID: flags.projectID, Name: flags.name, Type: flags.typ, Namespace: flags.namespace, Overrides: overrides})
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(resp)
		},
	}
	addServiceProjectNameFlags(cmd, flags)
	cmd.Flags().StringVar(&flags.typ, "type", "", "service type, e.g. postgres or redis")
	cmd.Flags().StringVar(&flags.namespace, "namespace", "", "kubernetes namespace")
	cmd.Flags().StringArrayVar(&flags.sets, "set", nil, "config override key=value; repeatable")
	return cmd
}

func newServiceStatusCommand(configPath *string, factory func() (keychain.Store, error), flags *serviceFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show managed infrastructure service status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.projectID == "" || flags.name == "" {
				return fmt.Errorf("project-id and name are required")
			}
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			if pat := optionalPAT(factory); pat != "" {
				ctx = agentclient.WithPAT(ctx, pat)
			}
			resp, err := agentclient.New(cfg).GetManagedService(ctx, &agentv1.GetManagedServiceRequest{ProjectID: flags.projectID, ID: flags.name})
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(resp)
		},
	}
	addServiceProjectNameFlags(cmd, flags)
	return cmd
}

func addServiceProjectNameFlags(cmd *cobra.Command, flags *serviceFlags) {
	cmd.Flags().StringVar(&flags.projectID, "project-id", "", "project id")
	cmd.Flags().StringVar(&flags.name, "name", "", "service name")
}

func parseSetFlags(values []string) (map[string]string, error) {
	out := map[string]string{}
	for _, value := range values {
		key, val, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("set value must be key=value: %s", value)
		}
		out[strings.TrimSpace(key)] = strings.TrimSpace(val)
	}
	return out, nil
}
