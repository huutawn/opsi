package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/agentclient"
	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

const incidentEvidenceOperationTimeout = 30 * time.Second

func newIncidentCommand(configPath *string, options Options) *cobra.Command {
	var projectID, nodeID, incidentID, statusFilter string
	var limit int32
	var jsonOutput bool
	cmd := &cobra.Command{Use: "incident", Short: "Inspect and resolve incidents"}

	resolveTargetResolver := func() (*agentclient.AgentTargetResolver, string, error) {
		cfg, err := config.LoadSelected(*configPath)
		if err != nil {
			return nil, "", err
		}
		pat := optionalPAT(options.KeychainFactory)
		client := options.HTTPClient
		if client == nil {
			client = http.DefaultClient
		}
		cloudClient, err := cloudclient.New(cfg.CloudURL, pat, options.Version, client)
		if err != nil {
			return nil, "", err
		}
		resolver := agentclient.NewAgentTargetResolver(agentclient.AgentTargetResolverOptions{
			CloudClient: cloudClient,
			BaseConfig:  cfg,
			PAT:         pat,
		})
		return resolver, pat, nil
	}

	list := &cobra.Command{Use: "list", Short: "List incidents", RunE: func(cmd *cobra.Command, _ []string) error {
		if projectID == "" {
			return errors.New("project-id is required")
		}
		resolver, pat, err := resolveTargetResolver()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		resp, _, err := resolver.ListIncidents(ctx, &agentv1.IncidentListRequest{ProjectID: projectID, Status: statusFilter, Limit: limit})
		if err != nil {
			return redactPATError(err, pat)
		}
		encoder := json.NewEncoder(cmd.OutOrStdout())
		if !jsonOutput {
			encoder.SetIndent("", "  ")
		}
		return encoder.Encode(resp)
	}}
	get := &cobra.Command{Use: "get", Short: "Get incident details", RunE: func(cmd *cobra.Command, _ []string) error {
		if projectID == "" || incidentID == "" {
			return fmt.Errorf("project-id and command-required incident-id are required")
		}
		resolver, pat, err := resolveTargetResolver()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		resp, err := resolver.GetIncident(ctx, &agentv1.IncidentGetRequest{ProjectID: projectID, IncidentID: incidentID}, nodeID)
		if err != nil {
			var ambig *agentclient.AmbiguousTargetError
			if errors.As(err, &ambig) {
				return fmt.Errorf("node-id is required when project has multiple agents: %w", err)
			}
			return redactPATError(err, pat)
		}
		encoder := json.NewEncoder(cmd.OutOrStdout())
		if !jsonOutput {
			encoder.SetIndent("", "  ")
		}
		return encoder.Encode(resp)
	}}
	resolve := &cobra.Command{Use: "resolve", Short: "Resolve incident", RunE: func(cmd *cobra.Command, _ []string) error {
		if projectID == "" || incidentID == "" {
			return errors.New("project-id and incident-id are required")
		}
		return errors.New("ACTION_APPROVAL_REQUIRED: use opsi action preflight and separate interactive approval")
	}}
	evidence := &cobra.Command{Use: "evidence", Short: "Get bounded factual incident evidence", RunE: func(cmd *cobra.Command, _ []string) error {
		if projectID == "" || incidentID == "" {
			return fmt.Errorf("project-id and incident-id are required")
		}
		resolver, _, err := resolveTargetResolver()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), incidentEvidenceOperationTimeout)
		defer cancel()
		response, err := resolver.GetIncidentEvidence(ctx, &agentv1.IncidentGetRequest{ProjectID: projectID, IncidentID: incidentID}, nodeID)
		if err != nil {
			var ambig *agentclient.AmbiguousTargetError
			if errors.As(err, &ambig) {
				return fmt.Errorf("node-id is required when project has multiple agents: %w", err)
			}
			return incidentEvidenceCLIError(err)
		}
		encoder := json.NewEncoder(cmd.OutOrStdout())
		if !jsonOutput {
			encoder.SetIndent("", "  ")
		}
		return encoder.Encode(response)
	}}
	for _, c := range []*cobra.Command{list, get, resolve, evidence} {
		c.Flags().StringVar(&projectID, "project-id", "", "project id")
	}
	for _, c := range []*cobra.Command{get, resolve, evidence} {
		c.Flags().StringVar(&incidentID, "incident-id", "", "incident id")
		c.Flags().StringVar(&nodeID, "node-id", "", "node id")
	}
	list.Flags().BoolVar(&jsonOutput, "json", false, "print compact JSON output")
	get.Flags().BoolVar(&jsonOutput, "json", false, "print compact JSON output")
	evidence.Flags().BoolVar(&jsonOutput, "json", false, "print compact JSON output")
	list.Flags().StringVar(&statusFilter, "status", "", "incident status")
	list.Flags().Int32Var(&limit, "limit", 0, "maximum incidents to return")
	cmd.AddCommand(list, get, resolve, evidence)
	return cmd
}

func incidentEvidenceCLIError(err error) error {
	code, message := "INCIDENT_EVIDENCE_AGENT_UNAVAILABLE", "Agent incident evidence request failed"
	switch grpcstatus.Code(err) {
	case codes.InvalidArgument:
		code, message = "INCIDENT_EVIDENCE_INVALID_REQUEST", "Incident evidence request is invalid"
	case codes.Unauthenticated:
		code, message = "INCIDENT_EVIDENCE_AUTH_REQUIRED", "Agent authentication is required"
	case codes.PermissionDenied:
		code, message = "INCIDENT_EVIDENCE_ACCESS_DENIED", "Incident evidence access is denied"
	case codes.NotFound:
		code, message = "INCIDENT_EVIDENCE_NOT_FOUND", "Incident was not found"
	case codes.FailedPrecondition:
		code, message = "INCIDENT_EVIDENCE_INVALID", "Stored incident evidence is invalid"
	case codes.ResourceExhausted:
		code, message = "INCIDENT_EVIDENCE_TOO_LARGE", "Incident evidence exceeds the size limit"
	case codes.DeadlineExceeded:
		code, message = "INCIDENT_EVIDENCE_TIMEOUT", "Incident evidence request timed out"
	case codes.Unimplemented:
		code, message = "INCIDENT_EVIDENCE_UNSUPPORTED", "Agent does not support incident evidence"
	}
	return fmt.Errorf("%s: %s", code, message)
}

