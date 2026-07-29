package commands

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/agentclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"github.com/spf13/cobra"
)

func newTelemetryCommand(configPath *string, factory func() (keychain.Store, error)) *cobra.Command {
	var projectID, serviceID, cursor string
	var since int64
	var limit int32
	var logs, summary, services, jsonOutput bool
	cmd := &cobra.Command{Use: "telemetry", Short: "Query redacted Agent telemetry"}
	query := &cobra.Command{Use: "query", Short: "Query summaries, services, and redacted logs", RunE: func(command *cobra.Command, _ []string) error {
		if projectID == "" {
			return errors.New("project-id is required")
		}
		cfg, err := config.LoadSelected(*configPath)
		if err != nil {
			return err
		}
		pat, err := resolvePAT("", factory)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(command.Context(), 30*time.Second)
		defer cancel()
		ctx = agentclient.WithPAT(ctx, pat)
		response, err := agentclient.New(cfg).QueryTelemetry(ctx, &agentv1.TelemetryQueryRequest{ProjectID: projectID, ServiceID: serviceID, SinceUnix: since, Cursor: cursor, Limit: limit, IncludeLogs: logs, IncludeSummary: summary, IncludeServices: services})
		if err != nil {
			return redactPATError(err, pat)
		}
		encoder := json.NewEncoder(command.OutOrStdout())
		if !jsonOutput {
			encoder.SetIndent("", "  ")
		}
		return encoder.Encode(response)
	}}
	query.Flags().StringVar(&projectID, "project-id", "", "project id")
	query.Flags().StringVar(&serviceID, "service-id", "", "service id")
	query.Flags().Int64Var(&since, "since-unix", 0, "telemetry start timestamp")
	query.Flags().StringVar(&cursor, "cursor", "", "telemetry cursor")
	query.Flags().Int32Var(&limit, "limit", 0, "maximum records")
	query.Flags().BoolVar(&logs, "include-logs", false, "include redacted logs")
	query.Flags().BoolVar(&summary, "include-summary", true, "include runtime summary")
	query.Flags().BoolVar(&services, "include-services", true, "include service status")
	query.Flags().BoolVar(&jsonOutput, "json", false, "print JSON output")
	cmd.AddCommand(query)
	return cmd
}
