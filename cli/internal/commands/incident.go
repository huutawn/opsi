package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/agentclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"github.com/spf13/cobra"
)

func newIncidentCommand(configPath *string, factory func() (keychain.Store, error)) *cobra.Command {
	var projectID, incidentID, userID, role, statusFilter string
	var limit int32
	cmd := &cobra.Command{Use: "incident", Short: "Inspect and resolve incidents"}
	run := func(cmd *cobra.Command, requireIncidentID bool, fn func(context.Context, *agentclient.Client) (any, error)) error {
		if projectID == "" || userID == "" || role == "" || (requireIncidentID && incidentID == "") {
			return fmt.Errorf("project-id, user-id, role, and command-required incident-id are required")
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
		resp, err := fn(ctx, agentclient.New(cfg))
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(resp)
	}
	list := &cobra.Command{Use: "list", Short: "List incidents", RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd, false, func(ctx context.Context, client *agentclient.Client) (any, error) {
			return client.ListIncidents(ctx, &agentv1.IncidentListRequest{ProjectID: projectID, Status: statusFilter, Limit: limit, UserID: userID, Role: role})
		})
	}}
	get := &cobra.Command{Use: "get", Short: "Get incident details", RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd, true, func(ctx context.Context, client *agentclient.Client) (any, error) {
			return client.GetIncident(ctx, &agentv1.IncidentGetRequest{ProjectID: projectID, IncidentID: incidentID, UserID: userID, Role: role})
		})
	}}
	resolve := &cobra.Command{Use: "resolve", Short: "Resolve incident", RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd, true, func(ctx context.Context, client *agentclient.Client) (any, error) {
			return client.ResolveIncident(ctx, &agentv1.IncidentResolveRequest{ProjectID: projectID, IncidentID: incidentID, UserID: userID, Role: role})
		})
	}}
	for _, c := range []*cobra.Command{list, get, resolve} {
		c.Flags().StringVar(&projectID, "project-id", "", "project id")
		c.Flags().StringVar(&userID, "user-id", "", "user id")
		c.Flags().StringVar(&role, "role", "Owner", "user role")
	}
	for _, c := range []*cobra.Command{get, resolve} {
		c.Flags().StringVar(&incidentID, "incident-id", "", "incident id")
	}
	list.Flags().StringVar(&statusFilter, "status", "", "incident status")
	list.Flags().Int32Var(&limit, "limit", 0, "maximum incidents to return")
	cmd.AddCommand(list, get, resolve)
	return cmd
}
