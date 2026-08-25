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
	configCommand := &cobra.Command{Use: "config", Short: "Manage explicit repository configuration for advanced/manual use"}
	configOptions := cdConfigOptions{RepoDir: ".", ConfigPath: defaultConfigPath, Context: ".", Dockerfile: "Dockerfile", Platform: "linux/amd64", Branch: "main"}
	upsert := &cobra.Command{Use: "upsert", Short: "Explicitly add or update one service in .opsi/opsi-cd.yaml", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return runCDConfigUpsert(cmd, options, configOptions) }}
	configFlags := upsert.Flags()
	configFlags.StringVar(&configOptions.RepoDir, "repo-dir", ".", "local Git repository directory")
	configFlags.StringVar(&configOptions.ConfigPath, "config-path", defaultConfigPath, "repository config path")
	configFlags.StringVar(&configOptions.ServiceKey, "service-key", "", "service key")
	configFlags.StringVar(&configOptions.Context, "build-context", ".", "repository-relative build context")
	configFlags.StringVar(&configOptions.Dockerfile, "dockerfile", "Dockerfile", "repository-relative Dockerfile")
	configFlags.StringVar(&configOptions.Platform, "platform", "linux/amd64", "build platform")
	configFlags.StringVar(&configOptions.Branch, "branch", "main", "documented production branch")
	configFlags.BoolVar(&configOptions.PreviewPRs, "preview-prs", false, "document preview intent")
	configFlags.BoolVar(&configOptions.DryRun, "dry-run", false, "render and diff without writing")
	configFlags.BoolVar(&configOptions.Force, "force", false, "replace an existing config when used with --yes")
	configFlags.BoolVar(&configOptions.Yes, "yes", false, "confirm an explicit config replacement")
	configFlags.BoolVar(&configOptions.JSON, "json", false, "emit JSON")
	configCommand.AddCommand(upsert)
	exportOptions := cdExportOptions{}
	exportCommand := &cobra.Command{Use: "export", Short: "Export a canonical Cloud deployment run as a reviewable configuration pull request", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return runCDExport(cmd, configPath, options, exportOptions)
	}}
	exportFlags := exportCommand.Flags()
	exportFlags.StringVar(&exportOptions.ProjectID, "project-id", "", "project id")
	exportFlags.StringVar(&exportOptions.RunID, "run-id", "", "canonical deployment run id")
	exportFlags.StringVar(&exportOptions.TargetBranch, "target-branch", "", "pull request base branch; defaults to repository default")
	exportFlags.BoolVar(&exportOptions.Preview, "preview", false, "print canonical YAML and diff without creating a pull request")
	exportFlags.BoolVar(&exportOptions.JSON, "json", false, "emit JSON")
	root.AddCommand(configCommand, exportCommand)
	return root
}

type cdConfigOptions struct {
	RepoDir, ConfigPath, ServiceKey, Context, Dockerfile, Platform, Branch string
	PreviewPRs, DryRun, Force, Yes, JSON                                   bool
}

func runCDConfigUpsert(cmd *cobra.Command, options Options, input cdConfigOptions) error {
	if input.ServiceKey == "" {
		return errors.New("service-key is required")
	}
	if input.Force && !input.Yes {
		return errors.New("--force requires --yes")
	}
	root, err := repository.Root(cmd.Context(), options.GitRunner, input.RepoDir)
	if err != nil {
		return err
	}
	request := repository.MutationRequest{Repository: root, ConfigPath: input.ConfigPath, Force: input.Force, Confirmed: input.Yes, Service: repository.ServiceV2{Key: input.ServiceKey, Build: repository.BuildV2{Context: input.Context, Dockerfile: input.Dockerfile, Platform: input.Platform}, WatchPaths: []string{}, SharedPaths: []string{}, Dependencies: []string{}, Deploy: repository.DeployV2{Production: repository.ProductionV2{Enabled: true, Branches: []string{input.Branch}}, Preview: repository.PreviewV2{Enabled: input.PreviewPRs, PullRequests: input.PreviewPRs}}}}
	service := repository.CDService{Runner: options.GitRunner}
	result, err := service.PreviewMutation(request)
	if err != nil {
		return err
	}
	if !input.DryRun {
		result, err = service.ApplyMutation(request)
		if err != nil {
			return err
		}
	}
	if input.JSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	if input.DryRun {
		_, err = fmt.Fprint(cmd.OutOrStdout(), result.ConfigDiff)
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Updated %s (config hash %s).\n", input.ConfigPath, result.ConfigHash)
	return err
}

type cdExportOptions struct {
	ProjectID, RunID, TargetBranch string
	Preview, JSON                  bool
}

func runCDExport(cmd *cobra.Command, configPath *string, options Options, input cdExportOptions) error {
	if input.RunID == "" {
		return errors.New("run-id is required")
	}
	client, ctx, cancel, err := cdClient(cmd, configPath, options, input.ProjectID)
	if err != nil {
		return err
	}
	defer cancel()
	preview, err := client.PreviewRepositoryExport(ctx, input.ProjectID, input.RunID, input.TargetBranch)
	if err != nil {
		return err
	}
	if input.Preview {
		if input.JSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(preview)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\n%s", preview.YAML, preview.Diff)
		return err
	}
	if !preview.ExportEnabled {
		return errors.New(preview.DisabledReason)
	}
	if len(preview.PreviewHash) != 64 {
		return errors.New("Cloud returned an invalid repository export preview hash")
	}
	result, err := client.ExportRepositoryConfiguration(ctx, input.ProjectID, "export:"+input.RunID+":"+preview.PreviewHash[:16], preview)
	if err != nil {
		return err
	}
	if input.JSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Created pull request %s from %s. No merge was performed.\n", result.PullRequestURL, result.Branch)
	return err
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
