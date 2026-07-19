package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	"github.com/spf13/cobra"
)

const buildRecordCommandTimeout = 30 * time.Second

type buildRecordFlags struct {
	projectID    string
	recordID     string
	serviceKey   string
	repositoryID uint64
	sha          string
	status       string
	limit        int
	cursor       string
	json         bool
}

func newBuildRecordCommand(configPath *string, options Options) *cobra.Command {
	flags := &buildRecordFlags{}
	command := &cobra.Command{Use: "build-record", Short: "Inspect trusted BuildRecord metadata"}
	list := &cobra.Command{Use: "list", Short: "List project BuildRecords", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		client, ctx, cancel, err := buildRecordClient(command.Context(), *configPath, options, flags.projectID)
		if err != nil {
			return err
		}
		defer cancel()
		query := url.Values{}
		if flags.serviceKey != "" {
			query.Set("service_key", flags.serviceKey)
		}
		if flags.repositoryID != 0 {
			query.Set("repository_id", strconv.FormatUint(flags.repositoryID, 10))
		}
		if flags.sha != "" {
			query.Set("sha", flags.sha)
		}
		if flags.status != "" {
			query.Set("status", flags.status)
		}
		query.Set("limit", strconv.Itoa(flags.limit))
		if flags.cursor != "" {
			query.Set("cursor", flags.cursor)
		}
		result, err := client.ListBuildRecords(ctx, flags.projectID, query)
		if err != nil {
			return err
		}
		if flags.json {
			return json.NewEncoder(command.OutOrStdout()).Encode(result)
		}
		return writeBuildRecordList(command, result)
	}}
	get := &cobra.Command{Use: "get", Short: "Get one project BuildRecord", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		if !validBuildRecordID(flags.recordID) {
			return errors.New("record-id is required and must be a safe opaque identifier")
		}
		client, ctx, cancel, err := buildRecordClient(command.Context(), *configPath, options, flags.projectID)
		if err != nil {
			return err
		}
		defer cancel()
		record, err := client.GetBuildRecord(ctx, flags.projectID, flags.recordID)
		if err != nil {
			return err
		}
		if flags.json {
			return json.NewEncoder(command.OutOrStdout()).Encode(record)
		}
		return writeBuildRecordDetail(command, record)
	}}
	for _, child := range []*cobra.Command{list, get} {
		child.Flags().StringVar(&flags.projectID, "project-id", "", "project id")
		child.Flags().BoolVar(&flags.json, "json", false, "emit JSON")
	}
	list.Flags().StringVar(&flags.serviceKey, "service-key", "", "filter by service key")
	list.Flags().Uint64Var(&flags.repositoryID, "repository-id", 0, "filter by numeric GitHub repository ID")
	list.Flags().StringVar(&flags.sha, "sha", "", "filter by full source commit SHA")
	list.Flags().StringVar(&flags.status, "status", "", "filter by BuildRecord status")
	list.Flags().IntVar(&flags.limit, "limit", 50, "maximum records (1-100)")
	list.Flags().StringVar(&flags.cursor, "cursor", "", "stable pagination cursor")
	get.Flags().StringVar(&flags.recordID, "record-id", "", "BuildRecord ID")
	command.AddCommand(list, get)
	return command
}

func buildRecordClient(parent context.Context, configPath string, options Options, projectID string) (*cloudclient.Client, context.Context, context.CancelFunc, error) {
	if err := validateGitHubProjectID(projectID); err != nil {
		return nil, nil, nil, err
	}
	client, err := newCommandCloudClient(configPath, options)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create BuildRecord Cloud client: %w", err)
	}
	ctx, cancel := context.WithTimeout(parent, buildRecordCommandTimeout)
	return client, ctx, cancel, nil
}

func writeBuildRecordList(command *cobra.Command, result cloudclient.BuildRecordList) error {
	if len(result.Records) == 0 {
		_, err := fmt.Fprintln(command.OutOrStdout(), "No BuildRecords found.")
		return err
	}
	writer := tabwriter.NewWriter(command.OutOrStdout(), 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "ID\tSERVICE\tREPOSITORY\tSHA\tDIGEST\tWORKFLOW RUN\tSTATUS")
	for _, record := range result.Records {
		_, _ = fmt.Fprintf(writer, "%s\t%s\t%d\t%s\t%s\t%s #%d/%d\t%s\n", record.ID, record.ServiceKey, record.RepositoryID, record.Workload.SHA, record.Build.OCIDigest, record.Workload.WorkflowRef, record.Workload.RunID, record.Workload.RunAttempt, record.Build.Status)
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	if result.NextCursor != "" {
		_, err := fmt.Fprintf(command.OutOrStdout(), "Next cursor: %s\n", result.NextCursor)
		return err
	}
	return nil
}
func writeBuildRecordDetail(command *cobra.Command, record cloudclient.BuildRecord) error {
	_, err := fmt.Fprintf(command.OutOrStdout(), "BuildRecord: %s\nProject: %s\nRepository: %d (owner %d)\nBinding: %s\nService: %s (%s)\nSource: %s @ %s\nWorkflow: %s\nRun: %d attempt %d\nArtifact: %s@%s\nPlatform: %s\nConfig hash: %s\nPlan hash: %s\nProvenance: %s\nStatus: %s\nCreated: %s\n", record.ID, record.ProjectID, record.RepositoryID, record.RepositoryOwnerID, record.ActiveBindingID, record.ServiceKey, record.ServiceID, record.Workload.Ref, record.Workload.SHA, record.Workload.WorkflowRef, record.Workload.RunID, record.Workload.RunAttempt, record.Build.OCIRepository, record.Build.OCIDigest, record.Build.Platform, record.Build.ConfigHash, displayOptional(record.Build.PlanHash), displayOptional(record.Build.ProvenanceDigest), record.Build.Status, record.CreatedAt.UTC().Format(time.RFC3339))
	return err
}
func validBuildRecordID(value string) bool {
	return value != "" && len(value) <= 128 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "/\\\r\n")
}

func displayOptional(value string) string {
	if value == "" {
		return "none"
	}
	return value
}
