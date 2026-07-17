package commands

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/repository"
	"github.com/spf13/cobra"
)

const (
	defaultConfigPath   = ".opsi/opsi-cd.yaml"
	defaultWorkflowPath = ".github/workflows/opsi-cd.yaml"
	claimCallbackPath   = "/_opsi/github/installation-claim"
)

type initOptions struct {
	ProjectID      string
	ServiceID      string
	ServiceKey     string
	RepositoryID   int64
	InstallationID int64
	CloudURL       string
	RepoDir        string
	ConfigPath     string
	WorkflowPath   string
	BuildContext   string
	Dockerfile     string
	Platform       string
	Branch         string
	PreviewPRs     bool
	DryRun         bool
	Force          bool
	Yes            bool
	NoBrowser      bool
	JSON           bool
	Timeout        time.Duration
}

type initPlan struct {
	Repository        string                  `json:"repository"`
	RepositoryID      int64                   `json:"repository_id,omitempty"`
	ProjectID         string                  `json:"project_id"`
	ServiceID         string                  `json:"service_id"`
	ServiceKey        string                  `json:"service_key"`
	InstallationID    int64                   `json:"installation_id,omitempty"`
	InstallationClaim string                  `json:"installation_claim"`
	RepositoryClaim   string                  `json:"repository_claim"`
	Binding           string                  `json:"binding"`
	Files             []repository.FileChange `json:"files,omitempty"`
}

func newInitCommand(configPath *string, rootOptions Options) *cobra.Command {
	options := initOptions{
		RepoDir:      ".",
		ConfigPath:   defaultConfigPath,
		WorkflowPath: defaultWorkflowPath,
		BuildContext: ".",
		Dockerfile:   "Dockerfile",
		Platform:     "linux/amd64",
		Timeout:      5 * time.Minute,
	}
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Bootstrap the current GitHub repository for Opsi CD",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), *configPath, rootOptions, options)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&options.ProjectID, "project-id", "", "Cloud project ID")
	flags.StringVar(&options.ServiceID, "service-id", "", "Cloud service ID")
	flags.StringVar(&options.ServiceKey, "service-key", "", "repository service key")
	flags.Int64Var(&options.RepositoryID, "repository-id", 0, "numeric GitHub repository ID from Cloud")
	flags.Int64Var(&options.InstallationID, "installation-id", 0, "GitHub App installation ID to claim when needed")
	flags.StringVar(&options.CloudURL, "cloud-url", "", "override cloud_url from CLI config")
	flags.StringVar(&options.RepoDir, "repo-dir", ".", "local Git repository directory")
	flags.StringVar(&options.ConfigPath, "config-path", defaultConfigPath, "generated Opsi CD config path")
	flags.StringVar(&options.WorkflowPath, "workflow-path", defaultWorkflowPath, "generated GitHub workflow path")
	flags.StringVar(&options.BuildContext, "build-context", ".", "repository-relative build context")
	flags.StringVar(&options.Dockerfile, "dockerfile", "Dockerfile", "repository-relative Dockerfile path")
	flags.StringVar(&options.Platform, "platform", "linux/amd64", "build platform")
	flags.StringVar(&options.Branch, "branch", "", "production branch; defaults to Cloud repository metadata")
	flags.BoolVar(&options.PreviewPRs, "preview-prs", false, "enable preview intent for pull requests")
	flags.BoolVar(&options.DryRun, "dry-run", false, "print a JSON plan without mutations or file writes")
	flags.BoolVar(&options.Force, "force", false, "allow overwriting generated files when used with --yes")
	flags.BoolVar(&options.Yes, "yes", false, "confirm explicit overwrite")
	flags.BoolVar(&options.NoBrowser, "no-browser", false, "print authorization URL without opening a browser")
	flags.BoolVar(&options.JSON, "json", false, "print success output as JSON")
	flags.DurationVar(&options.Timeout, "timeout", 5*time.Minute, "overall init and browser callback timeout")
	return cmd
}

func runInit(parent context.Context, output, statusOutput io.Writer, configPath string, dependencies Options, options initOptions) error {
	if err := validateInitOptions(options); err != nil {
		return err
	}
	store, err := dependencies.KeychainFactory()
	if err != nil {
		return fmt.Errorf("open OS keychain: %w", err)
	}
	pat, err := store.GetPAT()
	if err != nil || strings.TrimSpace(pat) == "" {
		return errors.New("Cloud PAT not found in OS keychain; run opsi login --pat-file PATH")
	}
	cliConfig, err := config.Load(configPath)
	if err != nil {
		return err
	}
	cloudURL := cliConfig.CloudURL
	if options.CloudURL != "" {
		cloudURL = options.CloudURL
	}
	httpClient := dependencies.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	httpClientCopy := *httpClient
	httpClientCopy.Timeout = options.Timeout
	client, err := cloudclient.New(cloudURL, pat, dependencies.Version, &httpClientCopy)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, options.Timeout)
	defer cancel()
	local, err := repository.Detect(ctx, dependencies.GitRunner, options.RepoDir)
	if err != nil {
		return err
	}
	services, err := client.ListServices(ctx, options.ProjectID)
	if err != nil {
		return err
	}
	if err := validateService(services, options.ProjectID, options.ServiceID); err != nil {
		return err
	}
	repositories, err := client.ListGitHubRepositories(ctx, options.ProjectID)
	if err != nil {
		return err
	}
	bindings, err := client.ListGitHubBindings(ctx, options.ProjectID)
	if err != nil {
		return err
	}
	target, matchErr := repository.MatchInventory(toInventory(repositories), local.Origin, options.RepositoryID)
	installationClaim := "not-needed"
	if errors.Is(matchErr, repository.ErrRepositoryNotFound) {
		if options.InstallationID <= 0 {
			return fmt.Errorf("repository %s is not in Cloud inventory; rerun with --installation-id after installing the GitHub App", local.Origin.FullName)
		}
		if options.DryRun {
			plan := initPlan{Repository: local.Origin.FullName, ProjectID: options.ProjectID, ServiceID: options.ServiceID, ServiceKey: options.ServiceKey, InstallationID: options.InstallationID, InstallationClaim: "required", RepositoryClaim: "pending-inventory", Binding: "pending-inventory"}
			return writeJSON(output, plan)
		}
		if err := runInstallationClaim(ctx, statusOutput, client, dependencies, options); err != nil {
			return err
		}
		installationClaim = "claimed"
		installations, err := client.ListGitHubInstallations(ctx, options.ProjectID)
		if err != nil {
			return err
		}
		if err := validateClaimedInstallation(installations, options.InstallationID); err != nil {
			return err
		}
		repositories, err = client.ListGitHubRepositories(ctx, options.ProjectID)
		if err != nil {
			return err
		}
		target, matchErr = repository.MatchInventory(toInventory(repositories), local.Origin, options.RepositoryID)
		if errors.Is(matchErr, repository.ErrRepositoryNotFound) && options.RepositoryID > 0 {
			return matchErr
		}
		if errors.Is(matchErr, repository.ErrRepositoryNotFound) || (matchErr == nil && target.InstallationID != options.InstallationID) {
			return fmt.Errorf("GitHub App installation does not have access to %s", local.Origin.FullName)
		}
	}
	if matchErr != nil {
		return matchErr
	}
	branch := options.Branch
	if branch == "" {
		branch = target.DefaultBranch
	}
	if err := repository.ValidateBuildInputs(local.Root, options.BuildContext, options.Dockerfile, options.Platform, branch); err != nil {
		return err
	}
	configBytes, err := repository.RenderConfig(repository.ConfigOptions{ServiceKey: options.ServiceKey, Context: options.BuildContext, Dockerfile: options.Dockerfile, Platform: options.Platform, Branch: branch, PreviewPRs: options.PreviewPRs})
	if err != nil {
		return err
	}
	filePlan, err := repository.PrepareFiles(local.Root, []repository.FileSpec{{Path: options.ConfigPath, Content: configBytes}, {Path: options.WorkflowPath, Content: repository.RenderWorkflow()}}, options.Force, options.Yes)
	if err != nil {
		return err
	}
	existingBinding, bindingState, err := inspectBindings(bindings, options, target.RepositoryID)
	if err != nil {
		return err
	}
	plan := initPlan{Repository: local.Origin.FullName, RepositoryID: target.RepositoryID, ProjectID: options.ProjectID, ServiceID: options.ServiceID, ServiceKey: options.ServiceKey, InstallationID: options.InstallationID, InstallationClaim: installationClaim, RepositoryClaim: "required", Binding: bindingState, Files: filePlan.Changes}
	if existingBinding != nil {
		plan.RepositoryClaim = "already-claimed"
	}
	if options.DryRun {
		return writeJSON(output, plan)
	}
	binding := cloudclient.GitHubBinding{}
	if existingBinding != nil {
		binding = *existingBinding
	} else {
		if _, err := client.ClaimRepository(ctx, options.ProjectID, target.RepositoryID); err != nil {
			return err
		}
		binding, err = client.CreateServiceBinding(ctx, options.ProjectID, options.ServiceID, target.RepositoryID, options.ServiceKey, options.ConfigPath)
		if err != nil {
			return err
		}
	}
	if err := repository.WriteFiles(filePlan, repository.WriteOptions{}); err != nil {
		return err
	}
	return writeInitSuccess(output, options, local.Origin.FullName, target.RepositoryID, binding, filePlan)
}

func validateInitOptions(options initOptions) error {
	if options.ProjectID == "" || options.ServiceID == "" || options.ServiceKey == "" {
		return errors.New("--project-id, --service-id, and --service-key are required")
	}
	if options.RepositoryID < 0 || options.InstallationID < 0 {
		return errors.New("repository and installation IDs must be positive integers")
	}
	if options.Timeout <= 0 || options.Timeout > 5*time.Minute {
		return errors.New("--timeout must be greater than zero and at most 5m")
	}
	if options.Force && !options.Yes {
		return errors.New("--force requires --yes")
	}
	if err := repository.ValidateServiceKey(options.ServiceKey); err != nil {
		return err
	}
	if err := repository.ValidateConfigPath(options.ConfigPath); err != nil {
		return fmt.Errorf("invalid --config-path: %w", err)
	}
	if err := repository.ValidateWorkflowPath(options.WorkflowPath); err != nil {
		return fmt.Errorf("invalid --workflow-path: %w", err)
	}
	return nil
}

func validateService(services []cloudclient.Service, projectID, serviceID string) error {
	for _, service := range services {
		if service.ID != serviceID {
			continue
		}
		if service.ProjectID != "" && service.ProjectID != projectID {
			return fmt.Errorf("service %s does not belong to project %s", serviceID, projectID)
		}
		if strings.EqualFold(service.Status, "deleted") {
			return fmt.Errorf("service %s is deleted", serviceID)
		}
		return nil
	}
	return fmt.Errorf("service %s does not exist in project %s", serviceID, projectID)
}

func toInventory(values []cloudclient.GitHubRepository) []repository.InventoryRepository {
	result := make([]repository.InventoryRepository, 0, len(values))
	for _, value := range values {
		result = append(result, repository.InventoryRepository{RepositoryID: value.RepositoryID, InstallationID: value.InstallationID, FullName: value.FullName, DefaultBranch: value.DefaultBranch, Status: value.Status, Archived: value.Archived, Disabled: value.Disabled})
	}
	return result
}

func inspectBindings(bindings []cloudclient.GitHubBinding, options initOptions, repositoryID int64) (*cloudclient.GitHubBinding, string, error) {
	for index := range bindings {
		binding := &bindings[index]
		if strings.EqualFold(binding.Status, "removed") {
			continue
		}
		exact := binding.ServiceID == options.ServiceID && binding.RepositoryID == repositoryID && binding.ServiceKey == options.ServiceKey && binding.ConfigPath == options.ConfigPath
		if exact {
			return binding, "existing", nil
		}
		if binding.ServiceID == options.ServiceID {
			return nil, "", fmt.Errorf("service %s already has a different repository binding", options.ServiceID)
		}
		if binding.RepositoryID == repositoryID && binding.ServiceKey == options.ServiceKey {
			return nil, "", fmt.Errorf("repository %d service key %s is bound to service %s", repositoryID, options.ServiceKey, binding.ServiceID)
		}
	}
	return nil, "create", nil
}

func validateClaimedInstallation(installations []cloudclient.GitHubInstallation, installationID int64) error {
	for _, installation := range installations {
		if installation.InstallationID == installationID {
			if installation.Status != "active" || installation.Suspended {
				return fmt.Errorf("GitHub App installation %d is not active", installationID)
			}
			return nil
		}
	}
	return fmt.Errorf("GitHub App installation %d was not linked to the project", installationID)
}

func runInstallationClaim(ctx context.Context, output io.Writer, client *cloudclient.Client, dependencies Options, options initOptions) error {
	listen := dependencies.Listen
	if listen == nil {
		listen = net.Listen
	}
	listener, err := listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start installation callback listener: %w", err)
	}
	defer listener.Close()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.IsLoopback() || !address.IP.Equal(net.ParseIP("127.0.0.1")) {
		return errors.New("installation callback listener is not bound to 127.0.0.1")
	}
	state, err := randomState(dependencies.Random)
	if err != nil {
		return err
	}
	callbackURL := "http://" + listener.Addr().String() + claimCallbackPath
	callback := newClaimCallback(listener.Addr().String(), state)
	server := &http.Server{Handler: callback, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10}
	serverErrors := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()
	defer server.Shutdown(context.Background())
	started, err := client.StartInstallationClaim(ctx, options.ProjectID, options.InstallationID, callbackURL, state)
	if err != nil {
		return err
	}
	authorizationURL, parseErr := url.Parse(started.AuthorizationURL)
	if parseErr != nil || authorizationURL.Scheme != "https" || authorizationURL.Host == "" || authorizationURL.User != nil || strings.IndexFunc(started.AuthorizationURL, unicode.IsControl) >= 0 {
		return errors.New("Cloud returned an invalid installation authorization URL")
	}
	if _, err := fmt.Fprintf(output, "Open this URL to authorize the GitHub App installation:\n%s\n", started.AuthorizationURL); err != nil {
		return err
	}
	if !options.NoBrowser {
		opener := dependencies.BrowserOpener
		if opener == nil {
			opener = openBrowser
		}
		_ = opener(started.AuthorizationURL)
	}
	select {
	case result := <-callback.result:
		_ = server.Shutdown(context.Background())
		_, err := client.RedeemInstallationClaim(ctx, result.grant, state)
		return err
	case err := <-serverErrors:
		return fmt.Errorf("installation callback server: %w", err)
	case <-ctx.Done():
		return errors.New("timed out waiting for GitHub installation authorization")
	}
}

type claimResult struct {
	grant string
}

type claimCallback struct {
	host     string
	state    string
	result   chan claimResult
	mu       sync.Mutex
	accepted bool
}

func newClaimCallback(host, state string) *claimCallback {
	return &claimCallback{host: host, state: state, result: make(chan claimResult, 1)}
}

func (c *claimCallback) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet || request.Host != c.host || request.URL.Path != claimCallbackPath || len(request.URL.RawQuery) > 8192 {
		http.Error(response, "invalid installation callback", http.StatusBadRequest)
		return
	}
	state := request.URL.Query().Get("state")
	grant := request.URL.Query().Get("grant")
	if state == "" || grant == "" || strings.IndexFunc(state+grant, unicode.IsControl) >= 0 || subtle.ConstantTimeCompare([]byte(state), []byte(c.state)) != 1 {
		http.Error(response, "invalid installation callback", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	if c.accepted {
		c.mu.Unlock()
		http.Error(response, "installation callback already accepted", http.StatusConflict)
		return
	}
	c.accepted = true
	c.mu.Unlock()
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(response, "Opsi GitHub installation connected. You may close this window.")
	c.result <- claimResult{grant: grant}
}

func randomState(source io.Reader) (string, error) {
	if source == nil {
		source = rand.Reader
	}
	value := make([]byte, 32)
	if _, err := io.ReadFull(source, value); err != nil {
		return "", errors.New("generate installation callback state")
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func openBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	return command.Start()
}

func writeInitSuccess(output io.Writer, options initOptions, fullName string, repositoryID int64, binding cloudclient.GitHubBinding, plan repository.FilePlan) error {
	if options.JSON {
		payload := struct {
			Repository   string                  `json:"repository"`
			RepositoryID int64                   `json:"repository_id"`
			ProjectID    string                  `json:"project_id"`
			ServiceID    string                  `json:"service_id"`
			ServiceKey   string                  `json:"service_key"`
			BindingID    string                  `json:"binding_id"`
			Files        []repository.FileChange `json:"files"`
		}{fullName, repositoryID, options.ProjectID, options.ServiceID, options.ServiceKey, binding.ID, plan.Changes}
		return writeJSON(output, payload)
	}
	if _, err := fmt.Fprintf(output, "Repository: %s (%d)\nProject: %s\nService: %s\nService key: %s\nBinding: %s\n", fullName, repositoryID, options.ProjectID, options.ServiceID, options.ServiceKey, binding.ID); err != nil {
		return err
	}
	for _, change := range plan.Changes {
		label := strings.ToUpper(change.Action[:1]) + change.Action[1:]
		if _, err := fmt.Fprintf(output, "%s: %s", label, change.Path); err != nil {
			return err
		}
		if change.Action == "updated" {
			if _, err := fmt.Fprintf(output, " (sha256 %s -> %s)", change.OldSHA256, change.NewSHA256); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(output); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(output, "Next: review and commit the generated files")
	return err
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
