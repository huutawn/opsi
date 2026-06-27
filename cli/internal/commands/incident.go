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
	var projectID, incidentID, actionID, userID, role string
	cmd := &cobra.Command{Use: "incident", Short: "Analyze and mitigate incidents"}
	run := func(cmd *cobra.Command, fn func(context.Context, *agentclient.Client) (*agentv1.IncidentResponse, error)) error {
		if projectID == "" || incidentID == "" || userID == "" || role == "" {
			return fmt.Errorf("project-id, incident-id, user-id, and role are required")
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
	analyze := &cobra.Command{Use: "analyze", Short: "Analyze incident RCA", RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd, func(ctx context.Context, client *agentclient.Client) (*agentv1.IncidentResponse, error) {
			return client.AnalyzeIncident(ctx, &agentv1.IncidentAnalyzeRequest{ProjectID: projectID, IncidentID: incidentID, UserID: userID, Role: role})
		})
	}}
	approve := &cobra.Command{Use: "approve", Short: "Approve and execute incident action", RunE: func(cmd *cobra.Command, _ []string) error {
		if actionID == "" {
			return fmt.Errorf("action-id is required")
		}
		return run(cmd, func(ctx context.Context, client *agentclient.Client) (*agentv1.IncidentResponse, error) {
			return client.ApproveIncidentAction(ctx, &agentv1.IncidentActionRequest{ProjectID: projectID, IncidentID: incidentID, ActionID: actionID, UserID: userID, Role: role})
		})
	}}
	resolve := &cobra.Command{Use: "resolve", Short: "Resolve incident", RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd, func(ctx context.Context, client *agentclient.Client) (*agentv1.IncidentResponse, error) {
			return client.ResolveIncident(ctx, &agentv1.IncidentActionRequest{ProjectID: projectID, IncidentID: incidentID, UserID: userID, Role: role})
		})
	}}
	for _, c := range []*cobra.Command{analyze, approve, resolve} {
		c.Flags().StringVar(&projectID, "project-id", "", "project id")
		c.Flags().StringVar(&incidentID, "incident-id", "", "incident id")
		c.Flags().StringVar(&userID, "user-id", "", "user id")
		c.Flags().StringVar(&role, "role", "Owner", "user role")
	}
	approve.Flags().StringVar(&actionID, "action-id", "", "recommended action id")
	cmd.AddCommand(analyze, approve, resolve)
	return cmd
}
