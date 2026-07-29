package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	"github.com/opsi-dev/opsi/cli/internal/repository"
	"github.com/spf13/cobra"
)

type cdPlanOptions struct {
	RepoDir    string
	ConfigPath string
	Base       string
	Head       string
	Event      string
	JSON       bool
}

func newCDCommand(configPath *string, options Options) *cobra.Command {
	root := &cobra.Command{Use: "cd", Short: "Inspect repository CD intent without building or deploying"}
	planOptions := cdPlanOptions{RepoDir: ".", ConfigPath: defaultConfigPath, Event: string(repository.EventPush)}
	plan := &cobra.Command{Use: "plan", Short: "Resolve affected monorepo services", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return runCDPlan(cmd.OutOrStdout(), options, planOptions, cmd)
	}}
	flags := plan.Flags()
	flags.StringVar(&planOptions.RepoDir, "repo-dir", ".", "local Git repository directory")
	flags.StringVar(&planOptions.ConfigPath, "config-path", defaultConfigPath, "repository-relative Opsi CD config path")
	flags.StringVar(&planOptions.Base, "base", "", "full base commit ID")
	flags.StringVar(&planOptions.Head, "head", "", "full head commit ID")
	flags.StringVar(&planOptions.Event, "event", string(repository.EventPush), "event type: initial, push, pull_request, or merge")
	flags.BoolVar(&planOptions.JSON, "json", false, "print the versioned plan as JSON")
	root.AddCommand(plan)
	read := &cdReadOptions{}
	status := &cobra.Command{Use: "status", Short: "Show main CD delivery status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return runCDDelivery(cmd, configPath, options, read, false)
	}}
	bindCDReadFlags(status, read)
	history := &cobra.Command{Use: "history", Short: "Show main CD delivery history", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return runCDDelivery(cmd, configPath, options, read, true)
	}}
	bindCDReadFlags(history, read)
	preview := &cobra.Command{Use: "preview", Short: "Inspect isolated pull request previews"}
	previewList := &cobra.Command{Use: "list", Short: "List previews", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return runCDPreviews(cmd, configPath, options, read, "list")
	}}
	previewDetail := &cobra.Command{Use: "detail", Short: "Show one preview", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return runCDPreviews(cmd, configPath, options, read, "detail")
	}}
	previewCleanup := &cobra.Command{Use: "cleanup", Short: "Request idempotent preview cleanup", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return runCDPreviews(cmd, configPath, options, read, "cleanup")
	}}
	for _, child := range []*cobra.Command{previewList, previewDetail, previewCleanup} {
		bindCDReadFlags(child, read)
	}
	previewDetail.Flags().StringVar(&read.deploymentID, "deployment-id", "", "preview deployment ID")
	previewCleanup.Flags().StringVar(&read.deploymentID, "deployment-id", "", "preview deployment ID")
	previewCleanup.Flags().StringVar(&read.idempotencyKey, "idempotency-key", "", "cleanup idempotency key")
	previewCleanup.Flags().StringVar(&read.reason, "reason", "manual", "manual, pr_closed, or ttl_expired")
	preview.AddCommand(previewList, previewDetail, previewCleanup)
	root.AddCommand(status, history, preview)
	return root
}

type cdReadOptions struct {
	projectID, deploymentID, idempotencyKey, reason string
	jsonOutput                                      bool
}

func bindCDReadFlags(command *cobra.Command, flags *cdReadOptions) {
	command.Flags().StringVar(&flags.projectID, "project-id", "", "project id")
	command.Flags().BoolVar(&flags.jsonOutput, "json", false, "emit JSON")
}

func cdClient(cmd *cobra.Command, configPath *string, options Options, projectID string) (*cloudclient.Client, context.Context, context.CancelFunc, error) {
	if err := validateGitHubProjectID(projectID); err != nil {
		return nil, nil, nil, err
	}
	client, err := newCommandCloudClient(*configPath, options)
	if err != nil {
		return nil, nil, nil, err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	return client, ctx, cancel, nil
}

func runCDDelivery(cmd *cobra.Command, configPath *string, options Options, flags *cdReadOptions, history bool) error {
	client, ctx, cancel, err := cdClient(cmd, configPath, options, flags.projectID)
	if err != nil {
		return err
	}
	defer cancel()
	jobs, err := client.ListDeployments(ctx, flags.projectID)
	if err != nil {
		return err
	}
	if flags.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"project_id": flags.projectID, "deployments": jobs})
	}
	for _, job := range jobs {
		if job.Snapshot == nil || job.Snapshot.Preview != nil {
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", job.ID, job.Status, job.DesiredDigest, job.BaseDeploymentID)
	}
	return nil
}

func runCDPreviews(cmd *cobra.Command, configPath *string, options Options, flags *cdReadOptions, operation string) error {
	client, ctx, cancel, err := cdClient(cmd, configPath, options, flags.projectID)
	if err != nil {
		return err
	}
	defer cancel()
	if operation == "detail" || operation == "cleanup" {
		if flags.deploymentID == "" {
			return errors.New("deployment-id is required")
		}
		job, err := client.GetDeployment(ctx, flags.projectID, flags.deploymentID)
		if err != nil {
			return err
		}
		if operation == "detail" {
			return writeCDPreview(cmd, job, flags.jsonOutput)
		}
		if flags.idempotencyKey == "" {
			return errors.New("idempotency-key is required")
		}
		return requestCDPreviewCleanup(ctx, client, cmd, flags, job)
	}
	jobs, err := client.ListDeployments(ctx, flags.projectID)
	if err != nil {
		return err
	}
	previews := make([]cloudclient.DeploymentJob, 0)
	for _, job := range jobs {
		if job.Snapshot != nil && job.Snapshot.Preview != nil {
			previews = append(previews, job)
		}
	}
	if flags.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"project_id": flags.projectID, "previews": previews})
	}
	for _, job := range previews {
		hostname := ""
		if job.ExposureSpec != nil {
			hostname = job.ExposureSpec.Hostname
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", job.ID, job.Status, hostname, job.Snapshot.Preview.ExpiresAt.Format(time.RFC3339))
	}
	return nil
}

func writeCDPreview(cmd *cobra.Command, job cloudclient.DeploymentJob, jsonOutput bool) error {
	if job.Snapshot == nil || job.Snapshot.Preview == nil {
		return errors.New("deployment is not a preview")
	}
	if jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(job)
	}
	url := ""
	if job.ExposureSpec != nil {
		url = "http://" + job.ExposureSpec.Hostname + job.ExposureSpec.Path
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "Preview %s\nStatus: %s\nURL: %s\nSHA: %s\nDigest: %s\nExpires: %s\n", job.ID, job.Status, url, job.Snapshot.Preview.HeadSHA, job.DesiredDigest, job.Snapshot.Preview.ExpiresAt.Format(time.RFC3339))
	return err
}

func requestCDPreviewCleanup(ctx context.Context, client *cloudclient.Client, cmd *cobra.Command, flags *cdReadOptions, job cloudclient.DeploymentJob) error {
	body, _ := json.Marshal(map[string]string{"deployment_id": job.ID, "reason": flags.reason})
	endpoint := *client.BaseURL
	endpoint.Path = path.Join(endpoint.Path, "api", "projects", flags.projectID, "deployments", job.ID, "cleanup")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", flags.idempotencyKey)
	if client.PAT != "" {
		req.Header.Set("Authorization", "Bearer "+client.PAT)
	}
	response, err := client.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("preview cleanup failed with HTTP %d", response.StatusCode)
	}
	var result cloudclient.DeploymentJob
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return err
	}
	return writeDeploymentJob(cmd, result, flags.jsonOutput)
}

func runCDPlan(output io.Writer, options Options, input cdPlanOptions, cmd *cobra.Command) error {
	root, err := repository.Root(cmd.Context(), options.GitRunner, input.RepoDir)
	if err != nil {
		return err
	}
	cfg, _, _, err := repository.LoadConfig(root, input.ConfigPath)
	if err != nil {
		return err
	}
	plan, err := (repository.CDService{Runner: options.GitRunner}).Plan(cmd.Context(), repository.PlanRequest{Event: repository.EventType(input.Event), Base: strings.TrimSpace(input.Base), Head: strings.TrimSpace(input.Head), Repository: root, Config: cfg})
	if err != nil {
		return err
	}
	if input.JSON {
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(plan)
	}
	return writeHumanPlan(output, plan)
}

func writeHumanPlan(output io.Writer, plan repository.ChangedServicePlan) error {
	if _, err := fmt.Fprintf(output, "Opsi CD plan %s\nEvent: %s\nBase: %s\nHead: %s\nFull build: %t\nAffected services: %s\n", plan.SchemaVersion, plan.Event, displayRevision(plan.Base), displayRevision(plan.Head), plan.FullBuild, displayServices(plan.AffectedServiceKeys)); err != nil {
		return err
	}
	for _, service := range plan.Services {
		for _, reason := range service.Reasons {
			if _, err := fmt.Fprintf(output, "- %s [%s]: %s\n", service.Key, reason.Code, reason.Explanation); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintf(output, "Explanation: %s\nConfig hash: %s\nPlan hash: %s\n", plan.Explanation, plan.ConfigHash, plan.PlanHash)
	return err
}

func displayRevision(value string) string {
	if value == "" {
		return "(none)"
	}
	return value
}
func displayServices(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	return strings.Join(values, ", ")
}
