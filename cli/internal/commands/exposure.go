package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
	"github.com/spf13/cobra"
)

type exposureFlags struct {
	projectID, baseDeploymentID, deploymentID, file, idempotencyKey, expectedStateHash, serviceID, environmentID string
	yes, jsonOutput                                                                                              bool
}

func newExposureCommand(configPath *string, factory func() (keychain.Store, error)) *cobra.Command {
	root := &cobra.Command{Use: "exposure", Short: "Manage canonical Traefik exposure through durable DeploymentJobs"}
	for _, operation := range []string{"create", "diff", "apply"} {
		flags := &exposureFlags{}
		operation := operation
		command := &cobra.Command{Use: operation, Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
			request, err := readExposureRequest(flags)
			if err != nil {
				return err
			}
			if operation == "apply" && (!flags.yes || strings.TrimSpace(flags.idempotencyKey) == "" || strings.TrimSpace(flags.expectedStateHash) == "") {
				return errors.New("exposure apply requires --yes, --idempotency-key, and --expected-state-hash")
			}
			client, ctx, cancel, err := deploymentClient(command.Context(), *configPath, factory, flags.projectID)
			if err != nil {
				return err
			}
			defer cancel()
			switch operation {
			case "create":
				preview, err := client.PreviewExposure(ctx, flags.projectID, request)
				if err != nil {
					return err
				}
				return writeExposurePreview(command, preview, flags.jsonOutput)
			case "diff":
				preview, err := client.DiffExposure(ctx, flags.projectID, request)
				if err != nil {
					return err
				}
				return writeExposurePreview(command, preview, flags.jsonOutput)
			default:
				job, err := client.ApplyExposure(ctx, flags.projectID, flags.idempotencyKey, request)
				if err != nil {
					return err
				}
				return writeDeploymentJob(command, job, flags.jsonOutput)
			}
		}}
		command.Flags().StringVar(&flags.projectID, "project-id", "", "project scope (required)")
		command.Flags().StringVar(&flags.baseDeploymentID, "base-deployment-id", "", "successful immutable DeploymentJob to expose")
		command.Flags().StringVar(&flags.file, "file", "", "strict ExposureSpec v1 JSON file")
		command.Flags().StringVar(&flags.expectedStateHash, "expected-state-hash", "", "preview state hash required for concurrency control")
		command.Flags().BoolVar(&flags.jsonOutput, "json", false, "write JSON")
		if operation == "apply" {
			command.Flags().StringVar(&flags.idempotencyKey, "idempotency-key", "", "bounded idempotency key")
			command.Flags().BoolVar(&flags.yes, "yes", false, "confirm the exposure mutation")
		}
		root.AddCommand(command)
	}

	statusFlags := &exposureFlags{}
	status := &cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		if statusFlags.deploymentID == "" {
			return errors.New("deployment-id is required")
		}
		client, ctx, cancel, err := deploymentClient(command.Context(), *configPath, factory, statusFlags.projectID)
		if err != nil {
			return err
		}
		defer cancel()
		job, err := client.GetDeployment(ctx, statusFlags.projectID, statusFlags.deploymentID)
		if err != nil {
			return err
		}
		return writeDeploymentJob(command, job, statusFlags.jsonOutput)
	}}
	bindExposureReadFlags(status, statusFlags, true)
	root.AddCommand(status)

	historyFlags := &exposureFlags{}
	history := &cobra.Command{Use: "history", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		client, ctx, cancel, err := deploymentClient(command.Context(), *configPath, factory, historyFlags.projectID)
		if err != nil {
			return err
		}
		defer cancel()
		jobs, err := client.ListExposures(ctx, historyFlags.projectID, historyFlags.serviceID, historyFlags.environmentID)
		if err != nil {
			return err
		}
		if historyFlags.jsonOutput {
			return json.NewEncoder(command.OutOrStdout()).Encode(map[string]any{"exposures": jobs})
		}
		writer := tabwriter.NewWriter(command.OutOrStdout(), 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(writer, "JOB\tSERVICE\tENVIRONMENT\tHOST/PATH\tDESIRED\tCURRENT\tSTATE")
		for _, job := range jobs {
			hostPath := "-"
			if job.ExposureSpec != nil {
				hostPath = job.ExposureSpec.Hostname + job.ExposureSpec.Path
			}
			_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", job.ID, job.ServiceID, job.EnvironmentID, hostPath, job.DesiredDigest, job.CurrentDigest, job.RolloutState)
		}
		return writer.Flush()
	}}
	bindExposureReadFlags(history, historyFlags, false)
	history.Flags().StringVar(&historyFlags.serviceID, "service-id", "", "filter service")
	history.Flags().StringVar(&historyFlags.environmentID, "environment-id", "", "filter environment")
	root.AddCommand(history)
	return root
}

func readExposureRequest(flags *exposureFlags) (deploymentv1.ExposureMutationRequest, error) {
	if flags.projectID == "" || flags.baseDeploymentID == "" || flags.file == "" {
		return deploymentv1.ExposureMutationRequest{}, errors.New("project-id, base-deployment-id, and file are required")
	}
	data, err := os.ReadFile(flags.file)
	if err != nil {
		return deploymentv1.ExposureMutationRequest{}, err
	}
	spec, err := exposurev1.DecodeStrictJSON(data)
	if err != nil {
		return deploymentv1.ExposureMutationRequest{}, err
	}
	if spec.ProjectID != flags.projectID {
		return deploymentv1.ExposureMutationRequest{}, errors.New("ExposureSpec project_id does not match --project-id")
	}
	return deploymentv1.ExposureMutationRequest{SchemaVersion: deploymentv1.ExposureMutationVersion, BaseDeploymentJobID: flags.baseDeploymentID, ExpectedStateHash: flags.expectedStateHash, Exposure: spec}, nil
}

func bindExposureReadFlags(command *cobra.Command, flags *exposureFlags, deployment bool) {
	command.Flags().StringVar(&flags.projectID, "project-id", "", "project scope (required)")
	if deployment {
		command.Flags().StringVar(&flags.deploymentID, "deployment-id", "", "rollout DeploymentJob ID")
	}
	command.Flags().BoolVar(&flags.jsonOutput, "json", false, "write JSON")
}

func writeExposurePreview(command *cobra.Command, preview cloudclient.ExposurePreview, jsonOutput bool) error {
	if jsonOutput {
		return json.NewEncoder(command.OutOrStdout()).Encode(preview)
	}
	_, err := fmt.Fprintf(command.OutOrStdout(), "Eligible: %t (%s)\nBase deployment: %s\nExposure: %s%s\nTLS: %s\nSpec hash: %s\nState hash: %s\nChanges: %s\n", preview.Eligible, preview.DecisionCode, preview.BaseDeploymentJobID, preview.Desired.Hostname, preview.Desired.Path, preview.Desired.TLS.Mode, preview.Desired.SpecHash, preview.StateHash, strings.Join(preview.Changes, ", "))
	return err
}
