package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/agentclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	agentv1 "github.com/opsi-dev/opsi/contracts/go/agentv1"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"
)

const incidentEvidenceOperationTimeout = 30 * time.Second

func newIncidentCommand(configPath *string, factory func() (keychain.Store, error)) *cobra.Command {
	var projectID, incidentID, statusFilter string
	var limit int32
	var jsonOutput bool
	cmd := &cobra.Command{Use: "incident", Short: "Inspect and resolve incidents"}
	run := func(cmd *cobra.Command, requireIncidentID bool, fn func(context.Context, *agentclient.Client) (any, error)) error {
		if projectID == "" || (requireIncidentID && incidentID == "") {
			return fmt.Errorf("project-id and command-required incident-id are required")
		}
		cfg, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		pat := optionalPAT(factory)
		if pat != "" {
			ctx = agentclient.WithPAT(ctx, pat)
		}
		resp, err := fn(ctx, agentclient.New(cfg))
		if err != nil {
			return redactPATError(err, pat)
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(resp)
	}
	list := &cobra.Command{Use: "list", Short: "List incidents", RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd, false, func(ctx context.Context, client *agentclient.Client) (any, error) {
			return client.ListIncidents(ctx, &agentv1.IncidentListRequest{ProjectID: projectID, Status: statusFilter, Limit: limit})
		})
	}}
	get := &cobra.Command{Use: "get", Short: "Get incident details", RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd, true, func(ctx context.Context, client *agentclient.Client) (any, error) {
			return client.GetIncident(ctx, &agentv1.IncidentGetRequest{ProjectID: projectID, IncidentID: incidentID})
		})
	}}
	resolve := &cobra.Command{Use: "resolve", Short: "Resolve incident", RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd, true, func(ctx context.Context, client *agentclient.Client) (any, error) {
			return client.ResolveIncident(ctx, &agentv1.IncidentResolveRequest{ProjectID: projectID, IncidentID: incidentID})
		})
	}}
	evidence := &cobra.Command{Use: "evidence", Short: "Get bounded factual incident evidence", RunE: func(cmd *cobra.Command, _ []string) error {
		if projectID == "" || incidentID == "" {
			return fmt.Errorf("project-id and incident-id are required")
		}
		cfg, err := config.LoadSelected(*configPath)
		if err != nil {
			return err
		}
		if err := requireSelectedAgentAddress(*configPath); err != nil {
			return err
		}
		pat, err := resolvePAT("", factory)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), incidentEvidenceOperationTimeout)
		defer cancel()
		response, err := agentclient.New(cfg).GetIncidentEvidence(agentclient.WithPAT(ctx, pat), &agentv1.IncidentGetRequest{ProjectID: projectID, IncidentID: incidentID})
		if err != nil {
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
	}
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

func requireSelectedAgentAddress(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read selected Agent config: %w", err)
	}
	var selected struct {
		AgentAddr string `yaml:"agent_addr"`
	}
	if err := yaml.Unmarshal(data, &selected); err != nil {
		return errors.New("selected Agent config is invalid")
	}
	if strings.TrimSpace(selected.AgentAddr) == "" {
		return errors.New("selected Agent config must explicitly set agent_addr")
	}
	return nil
}
