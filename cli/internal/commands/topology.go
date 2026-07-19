package commands

import (
	"fmt"
	"text/tabwriter"

	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	"github.com/spf13/cobra"
)

type topologyFlags struct {
	projectID, file, policyID, idempotencyKey, stateHash, runtimeID string
	revision                                                        uint64
	json, yes                                                       bool
}

func newTopologyCommand(configPath *string, options Options) *cobra.Command {
	flags := &topologyFlags{}
	root := &cobra.Command{Use: "topology", Short: "Plan and apply manual service placement"}
	plan := &cobra.Command{Use: "plan", Short: "Create a deterministic TopologyPlan preview", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var draft cloudclient.TopologyDraft
		if err := readPlacementJSON(flags.file, &draft); err != nil {
			return err
		}
		client, ctx, cancel, err := placementClient(cmd.Context(), *configPath, options, flags.projectID)
		if err != nil {
			return err
		}
		defer cancel()
		value, err := client.PreviewTopology(ctx, flags.projectID, draft)
		if err != nil {
			return err
		}
		if flags.json {
			return writePlacementJSON(cmd.OutOrStdout(), value)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Plan hash: %s\nCurrent state hash: %s\nAssignments: %d\n", value.PlanHash, emptyWord(value.StateHash), len(value.Draft.Assignments))
		return err
	}}
	validate := &cobra.Command{Use: "validate", Short: "Validate placement against Cloud runtime facts", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var draft cloudclient.TopologyDraft
		if err := readPlacementJSON(flags.file, &draft); err != nil {
			return err
		}
		client, ctx, cancel, err := placementClient(cmd.Context(), *configPath, options, flags.projectID)
		if err != nil {
			return err
		}
		defer cancel()
		value, err := client.ValidateTopology(ctx, flags.projectID, draft, flags.policyID)
		if err != nil {
			return err
		}
		if flags.json {
			return writePlacementJSON(cmd.OutOrStdout(), value)
		}
		return writeTopologyValidation(cmd, value)
	}}
	diff := &cobra.Command{Use: "diff", Short: "Diff a TopologyPlan against the active revision", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var draft cloudclient.TopologyDraft
		if err := readPlacementJSON(flags.file, &draft); err != nil {
			return err
		}
		client, ctx, cancel, err := placementClient(cmd.Context(), *configPath, options, flags.projectID)
		if err != nil {
			return err
		}
		defer cancel()
		value, err := client.DiffTopology(ctx, flags.projectID, draft)
		if err != nil {
			return err
		}
		if flags.json {
			return writePlacementJSON(cmd.OutOrStdout(), value)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Current revision: %d\nCurrent hash: %s\nProposed hash: %s\nChanges: %d\n", value.CurrentRevision, emptyWord(value.CurrentHash), value.ProposedHash, len(value.Changes))
		return err
	}}
	apply := &cobra.Command{Use: "apply", Short: "Apply a validated TopologyPlan revision", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requirePlacementMutation(flags.yes, flags.idempotencyKey); err != nil {
			return err
		}
		var draft cloudclient.TopologyDraft
		if err := readPlacementJSON(flags.file, &draft); err != nil {
			return err
		}
		client, ctx, cancel, err := placementClient(cmd.Context(), *configPath, options, flags.projectID)
		if err != nil {
			return err
		}
		defer cancel()
		value, err := client.ApplyTopology(ctx, flags.projectID, flags.idempotencyKey, cloudclient.TopologyApplyRequest{Draft: draft, ExpectedRevision: flags.revision, ExpectedStateHash: flags.stateHash, PolicyID: flags.policyID})
		if err != nil {
			return err
		}
		if flags.json {
			return writePlacementJSON(cmd.OutOrStdout(), value)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "TopologyPlan %s revision %d\nPlan hash: %s\nState hash: %s\nReused: %t\n", value.Plan.ID, value.Plan.Revision, value.Plan.PlanHash, value.Plan.StateHash, value.Reused)
		return err
	}}
	get := &cobra.Command{Use: "get", Short: "Get the active TopologyPlan", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client, ctx, cancel, err := placementClient(cmd.Context(), *configPath, options, flags.projectID)
		if err != nil {
			return err
		}
		defer cancel()
		value, err := client.GetTopology(ctx, flags.projectID)
		if err != nil {
			return err
		}
		if flags.json {
			return writePlacementJSON(cmd.OutOrStdout(), value)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "TopologyPlan %s revision %d\nPlan hash: %s\nState hash: %s\nAssignments: %d\n", value.ID, value.Revision, value.PlanHash, value.StateHash, len(value.Assignments))
		return err
	}}
	facts := &cobra.Command{Use: "facts", Short: "Show server-authoritative placement facts", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client, ctx, cancel, err := placementClient(cmd.Context(), *configPath, options, flags.projectID)
		if err != nil {
			return err
		}
		defer cancel()
		value, err := client.GetPlacementFacts(ctx, flags.projectID)
		if err != nil {
			return err
		}
		return writePlacementJSON(cmd.OutOrStdout(), value)
	}}
	capacity := newTopologyCapacityCommand(configPath, options, flags)
	for _, cmd := range []*cobra.Command{plan, validate, diff, apply, get, facts} {
		cmd.Flags().StringVar(&flags.projectID, "project-id", "", "project id")
		cmd.Flags().BoolVar(&flags.json, "json", false, "emit JSON")
	}
	for _, cmd := range []*cobra.Command{plan, validate, diff, apply} {
		cmd.Flags().StringVar(&flags.file, "file", "", "strict JSON TopologyPlan draft")
	}
	validate.Flags().StringVar(&flags.policyID, "policy-id", "", "active policy used only for explicit unknown-capacity override")
	apply.Flags().StringVar(&flags.policyID, "policy-id", "", "active policy used only for explicit unknown-capacity override")
	apply.Flags().Uint64Var(&flags.revision, "expected-revision", 0, "expected active topology revision")
	apply.Flags().StringVar(&flags.stateHash, "expected-state-hash", "", "expected active topology state hash")
	apply.Flags().StringVar(&flags.idempotencyKey, "idempotency-key", "", "safe idempotency key")
	apply.Flags().BoolVar(&flags.yes, "yes", false, "confirm mutation")
	root.AddCommand(plan, validate, diff, apply, get, facts, capacity)
	return root
}

func newTopologyCapacityCommand(configPath *string, options Options, flags *topologyFlags) *cobra.Command {
	root := &cobra.Command{Use: "capacity", Short: "Manage audited operator-declared runtime capacity"}
	get := &cobra.Command{Use: "get", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client, ctx, cancel, err := placementClient(cmd.Context(), *configPath, options, flags.projectID)
		if err != nil {
			return err
		}
		defer cancel()
		value, err := client.GetOperatorCapacity(ctx, flags.projectID, flags.runtimeID)
		if err != nil {
			return err
		}
		return writePlacementJSON(cmd.OutOrStdout(), value)
	}}
	apply := &cobra.Command{Use: "apply", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requirePlacementMutation(flags.yes, flags.idempotencyKey); err != nil {
			return err
		}
		var request cloudclient.OperatorCapacityApplyRequest
		if err := readPlacementJSON(flags.file, &request); err != nil {
			return err
		}
		request.ExpectedRevision = flags.revision
		request.ExpectedStateHash = flags.stateHash
		client, ctx, cancel, err := placementClient(cmd.Context(), *configPath, options, flags.projectID)
		if err != nil {
			return err
		}
		defer cancel()
		value, err := client.ApplyOperatorCapacity(ctx, flags.projectID, flags.runtimeID, flags.idempotencyKey, request)
		if err != nil {
			return err
		}
		return writePlacementJSON(cmd.OutOrStdout(), value)
	}}
	for _, cmd := range []*cobra.Command{get, apply} {
		cmd.Flags().StringVar(&flags.projectID, "project-id", "", "project id")
		cmd.Flags().StringVar(&flags.runtimeID, "runtime-id", "", "runtime id")
	}
	apply.Flags().StringVar(&flags.file, "file", "", "strict JSON capacity request")
	apply.Flags().Uint64Var(&flags.revision, "expected-revision", 0, "expected capacity revision")
	apply.Flags().StringVar(&flags.stateHash, "expected-state-hash", "", "expected capacity state hash")
	apply.Flags().StringVar(&flags.idempotencyKey, "idempotency-key", "", "safe idempotency key")
	apply.Flags().BoolVar(&flags.yes, "yes", false, "confirm mutation")
	root.AddCommand(get, apply)
	return root
}

func writeTopologyValidation(cmd *cobra.Command, value cloudclient.TopologyValidation) error {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "RUNTIME\tELIGIBLE\tHEARTBEAT\tCAPACITY SOURCE\tCPU REQUEST/AVAILABLE\tMEMORY REQUEST/AVAILABLE\tOVERRIDE")
	for _, runtime := range value.Runtimes {
		c := runtime.Capacity
		_, _ = fmt.Fprintf(writer, "%s\t%t\t%ds fresh=%t\t%s\t%d/%d m\t%d/%d bytes\t%t\n", runtime.RuntimeID, runtime.Eligible, c.HeartbeatAgeSeconds, c.HeartbeatFresh, c.Source, c.RequestedCPUMillicores, c.AvailableCPUMillicores, c.RequestedMemoryBytes, c.AvailableMemoryBytes, c.UnknownCapacityPolicyOverride)
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "Valid: %t\nPlan hash: %s\nIssues: %d\n", value.Valid, value.PlanHash, len(value.Issues))
	return err
}
func emptyWord(value string) string {
	if value == "" {
		return "none"
	}
	return value
}
