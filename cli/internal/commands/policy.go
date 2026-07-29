package commands

import (
	"fmt"
	"text/tabwriter"

	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	"github.com/spf13/cobra"
)

type policyFlags struct {
	projectID, policyID, file, idempotencyKey, stateHash, buildRecordID, environmentID string
	revision                                                                           uint64
	json, yes                                                                          bool
}

func newPolicyCommand(configPath *string, options Options) *cobra.Command {
	flags := &policyFlags{}
	root := &cobra.Command{Use: "policy", Short: "Create and manage exact DeploymentPolicy revisions"}
	create := &cobra.Command{Use: "create", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var draft cloudclient.DeploymentPolicyDraft
		if err := readPlacementJSON(flags.file, &draft); err != nil {
			return err
		}
		client, ctx, cancel, err := placementClient(cmd.Context(), *configPath, options, flags.projectID)
		if err != nil {
			return err
		}
		defer cancel()
		value, err := client.PreviewDeploymentPolicy(ctx, flags.projectID, draft)
		if err != nil {
			return err
		}
		if flags.json {
			return writePlacementJSON(cmd.OutOrStdout(), value)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Policy hash: %s\nEnabled: %t\nServices: %d\nRuntimes: %d\nUnknown capacity override: %t\n", value.PolicyHash, value.Draft.Enabled, len(value.Draft.ServiceKeys), len(value.Draft.AllowedRuntimeIDs), value.Draft.AllowUnknownCapacity)
		return err
	}}
	diff := &cobra.Command{Use: "diff", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var draft cloudclient.DeploymentPolicyDraft
		if err := readPlacementJSON(flags.file, &draft); err != nil {
			return err
		}
		client, ctx, cancel, err := placementClient(cmd.Context(), *configPath, options, flags.projectID)
		if err != nil {
			return err
		}
		defer cancel()
		value, err := client.DiffDeploymentPolicy(ctx, flags.projectID, cloudclient.DeploymentPolicyApplyRequest{PolicyID: flags.policyID, Draft: draft})
		if err != nil {
			return err
		}
		if flags.json {
			return writePlacementJSON(cmd.OutOrStdout(), value)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Current revision: %d\nCurrent hash: %s\nProposed hash: %s\nChanges: %d\n", value.CurrentRevision, emptyWord(value.CurrentHash), value.ProposedHash, len(value.Changes))
		return err
	}}
	apply := &cobra.Command{Use: "apply", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requirePlacementMutation(flags.yes, flags.idempotencyKey); err != nil {
			return err
		}
		var draft cloudclient.DeploymentPolicyDraft
		if err := readPlacementJSON(flags.file, &draft); err != nil {
			return err
		}
		client, ctx, cancel, err := placementClient(cmd.Context(), *configPath, options, flags.projectID)
		if err != nil {
			return err
		}
		defer cancel()
		value, err := client.ApplyDeploymentPolicy(ctx, flags.projectID, flags.idempotencyKey, cloudclient.DeploymentPolicyApplyRequest{PolicyID: flags.policyID, Draft: draft, ExpectedRevision: flags.revision, ExpectedStateHash: flags.stateHash})
		if err != nil {
			return err
		}
		if flags.json {
			return writePlacementJSON(cmd.OutOrStdout(), value)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "DeploymentPolicy %s revision %d\nPolicy hash: %s\nState hash: %s\nEnabled: %t\nUnknown capacity override: %t\nReused: %t\n", value.Policy.ID, value.Policy.Revision, value.Policy.PolicyHash, value.Policy.StateHash, value.Policy.Draft.Enabled, value.Policy.Draft.AllowUnknownCapacity, value.Reused)
		return err
	}}
	disable := &cobra.Command{Use: "disable", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requirePlacementMutation(flags.yes, flags.idempotencyKey); err != nil {
			return err
		}
		client, ctx, cancel, err := placementClient(cmd.Context(), *configPath, options, flags.projectID)
		if err != nil {
			return err
		}
		defer cancel()
		value, err := client.DisableDeploymentPolicy(ctx, flags.projectID, flags.policyID, flags.idempotencyKey, cloudclient.DeploymentPolicyDisableRequest{ExpectedRevision: flags.revision, ExpectedStateHash: flags.stateHash})
		if err != nil {
			return err
		}
		if flags.json {
			return writePlacementJSON(cmd.OutOrStdout(), value)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "DeploymentPolicy %s disabled at revision %d\nPolicy hash: %s\nState hash: %s\nReused: %t\n", value.Policy.ID, value.Policy.Revision, value.Policy.PolicyHash, value.Policy.StateHash, value.Reused)
		return err
	}}
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client, ctx, cancel, err := placementClient(cmd.Context(), *configPath, options, flags.projectID)
		if err != nil {
			return err
		}
		defer cancel()
		value, err := client.ListDeploymentPolicies(ctx, flags.projectID)
		if err != nil {
			return err
		}
		if flags.json {
			return writePlacementJSON(cmd.OutOrStdout(), map[string]any{"policies": value})
		}
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(writer, "ID\tREVISION\tENABLED\tENVIRONMENT\tRUNTIMES\tHASH")
		for _, policy := range value {
			_, _ = fmt.Fprintf(writer, "%s\t%d\t%t\t%s\t%d\t%s\n", policy.ID, policy.Revision, policy.Draft.Enabled, policy.Draft.EnvironmentID, len(policy.Draft.AllowedRuntimeIDs), policy.PolicyHash)
		}
		return writer.Flush()
	}}
	get := &cobra.Command{Use: "get", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client, ctx, cancel, err := placementClient(cmd.Context(), *configPath, options, flags.projectID)
		if err != nil {
			return err
		}
		defer cancel()
		value, err := client.GetDeploymentPolicy(ctx, flags.projectID, flags.policyID)
		if err != nil {
			return err
		}
		if flags.json {
			return writePlacementJSON(cmd.OutOrStdout(), value)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "DeploymentPolicy %s revision %d\nProject: %s\nRepository: %d\nEnvironment: %s\nEnabled: %t\nRuntimes: %d\nPolicy hash: %s\nState hash: %s\n", value.ID, value.Revision, value.Draft.ProjectID, value.Draft.RepositoryID, value.Draft.EnvironmentID, value.Draft.Enabled, len(value.Draft.AllowedRuntimeIDs), value.PolicyHash, value.StateHash)
		return err
	}}
	route := &cobra.Command{Use: "route", Short: "Return a deterministic BuildRecord routing preflight", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client, ctx, cancel, err := placementClient(cmd.Context(), *configPath, options, flags.projectID)
		if err != nil {
			return err
		}
		defer cancel()
		value, err := client.RouteBuildRecord(ctx, flags.projectID, cloudclient.RoutingRequest{BuildRecordID: flags.buildRecordID, EnvironmentID: flags.environmentID})
		if err != nil {
			return err
		}
		if flags.json {
			return writePlacementJSON(cmd.OutOrStdout(), value)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Decision: %s\nEligible: %t\nBuildRecord: %s\nService: %s\nRuntime: %s\nNode: %s\nAgent: %s\nUnknown capacity override: %t\nDecision hash: %s\n", value.DecisionCode, value.Eligible, value.BuildRecordID, value.ServiceKey, emptyWord(value.RuntimeID), emptyWord(value.NodeID), emptyWord(value.AgentID), value.UnknownCapacityOverride, value.DecisionHash)
		return err
	}}
	for _, cmd := range []*cobra.Command{create, diff, apply, disable, list, get, route} {
		cmd.Flags().StringVar(&flags.projectID, "project-id", "", "project id")
		cmd.Flags().BoolVar(&flags.json, "json", false, "emit JSON")
	}
	for _, cmd := range []*cobra.Command{create, diff, apply} {
		cmd.Flags().StringVar(&flags.file, "file", "", "strict JSON DeploymentPolicy draft")
	}
	for _, cmd := range []*cobra.Command{diff, apply, disable, get} {
		cmd.Flags().StringVar(&flags.policyID, "policy-id", "", "DeploymentPolicy ID")
	}
	for _, cmd := range []*cobra.Command{apply, disable} {
		cmd.Flags().Uint64Var(&flags.revision, "expected-revision", 0, "expected policy revision")
		cmd.Flags().StringVar(&flags.stateHash, "expected-state-hash", "", "expected policy state hash")
		cmd.Flags().StringVar(&flags.idempotencyKey, "idempotency-key", "", "safe idempotency key")
		cmd.Flags().BoolVar(&flags.yes, "yes", false, "confirm mutation")
	}
	route.Flags().StringVar(&flags.buildRecordID, "build-record-id", "", "accepted BuildRecord ID")
	route.Flags().StringVar(&flags.environmentID, "environment-id", "", "target environment ID")
	root.AddCommand(create, diff, apply, disable, list, get, route)
	return root
}
