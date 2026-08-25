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

const defaultConfigPath = ".opsi/opsi-cd.yaml"
const claimCallbackPath = "/_opsi/github/installation-claim"

type initOptions struct {
	ProjectID      string
	RepositoryID   int64
	InstallationID int64
	CloudURL       string
	RepoDir        string
	SelectedRef    string
	DryRun         bool
	NoBrowser      bool
	JSON           bool
	Timeout        time.Duration
}
type initPlan struct {
	Repository        string                            `json:"repository"`
	RepositoryID      int64                             `json:"repository_id,omitempty"`
	ProjectID         string                            `json:"project_id"`
	InstallationID    int64                             `json:"installation_id,omitempty"`
	InstallationClaim string                            `json:"installation_claim"`
	RepositoryClaim   string                            `json:"repository_claim"`
	SelectedRef       string                            `json:"selected_ref,omitempty"`
	DeploymentRun     *cloudclient.DeploymentRunPreview `json:"deployment_run,omitempty"`
}

func newInitCommand(configPath *string, rootOptions Options) *cobra.Command {
	options := initOptions{RepoDir: ".", Timeout: 5 * time.Minute}
	cmd := &cobra.Command{Use: "init", Short: "Claim and analyze the current repository without writing source files", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return runInit(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), *configPath, rootOptions, options)
	}}
	flags := cmd.Flags()
	flags.StringVar(&options.ProjectID, "project-id", "", "Cloud project ID")
	flags.Int64Var(&options.RepositoryID, "repository-id", 0, "numeric GitHub repository ID from Cloud")
	flags.Int64Var(&options.InstallationID, "installation-id", 0, "GitHub App installation ID to claim when needed")
	flags.StringVar(&options.CloudURL, "cloud-url", "", "override cloud_url from CLI config")
	flags.StringVar(&options.RepoDir, "repo-dir", ".", "local Git repository directory")
	flags.StringVar(&options.SelectedRef, "ref", "", "branch, tag, or commit to analyze; defaults to the repository default branch")
	flags.BoolVar(&options.DryRun, "dry-run", false, "show required claims without mutating Cloud or repository source")
	flags.BoolVar(&options.NoBrowser, "no-browser", false, "print authorization URL without opening a browser")
	flags.BoolVar(&options.JSON, "json", false, "print output as JSON")
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
	cliConfig := config.Config{}
	if configPath == "" {
		if options.CloudURL == "" {
			return errors.New("--config or --cloud-url is required for Cloud init")
		}
		cliConfig = config.Default()
	} else {
		cliConfig, err = config.LoadSelected(configPath)
		if err != nil {
			return err
		}
	}
	cloudURL := cliConfig.CloudURL
	if options.CloudURL != "" {
		cloudURL = options.CloudURL
	}
	httpClient := dependencies.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	clientCopy := *httpClient
	clientCopy.Timeout = options.Timeout
	client, err := cloudclient.New(cloudURL, pat, dependencies.Version, &clientCopy)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, options.Timeout)
	defer cancel()
	local, err := repository.Detect(ctx, dependencies.GitRunner, options.RepoDir)
	if err != nil {
		return err
	}
	repositories, err := client.ListGitHubRepositories(ctx, options.ProjectID)
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
			return writeJSON(output, initPlan{Repository: local.Origin.FullName, ProjectID: options.ProjectID, InstallationID: options.InstallationID, InstallationClaim: "required", RepositoryClaim: "pending-inventory"})
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
		if errors.Is(matchErr, repository.ErrRepositoryNotFound) || (matchErr == nil && target.InstallationID != options.InstallationID) {
			return fmt.Errorf("GitHub App installation does not have access to %s", local.Origin.FullName)
		}
	}
	if matchErr != nil {
		return matchErr
	}
	selectedRef := options.SelectedRef
	if selectedRef == "" {
		selectedRef = target.DefaultBranch
	}
	if selectedRef == "" {
		return errors.New("repository has no default ref; provide --ref")
	}
	claimState := "already-claimed"
	switch target.ClaimStatus {
	case "conflict":
		return fmt.Errorf("repository %s is claimed by another project", target.FullName)
	case "available":
		claimState = "required"
	}
	plan := initPlan{Repository: local.Origin.FullName, RepositoryID: target.RepositoryID, ProjectID: options.ProjectID, InstallationID: target.InstallationID, InstallationClaim: installationClaim, RepositoryClaim: claimState, SelectedRef: selectedRef}
	if options.DryRun {
		return writeJSON(output, plan)
	}
	if claimState == "required" {
		if _, err := client.ClaimRepository(ctx, options.ProjectID, target.RepositoryID); err != nil {
			return err
		}
		plan.RepositoryClaim = "claimed"
	}
	idempotency, err := randomState(dependencies.Random)
	if err != nil {
		return err
	}
	run, err := client.CreateDeploymentRun(ctx, options.ProjectID, target.RepositoryID, selectedRef, "init:"+idempotency)
	if err != nil {
		return err
	}
	plan.DeploymentRun = &run
	if options.JSON {
		return writeJSON(output, plan)
	}
	if _, err = fmt.Fprintf(output, "Repository %s at %s analyzed as exact commit %s.\nDraft run: %s (%s)\n", plan.Repository, selectedRef, run.Plan.Source.CommitSHA, run.ID, run.State); err != nil {
		return err
	}
	for _, application := range run.Plan.Applications {
		if _, err = fmt.Fprintf(output, "- %s: %s (port %d)\n", application.Key, application.Root, application.Port); err != nil {
			return err
		}
	}
	for _, issue := range run.Plan.Issues {
		if _, err = fmt.Fprintf(output, "- %s: %s\n", issue.Code, issue.Message); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(output, "No repository files were written. Review and approve this draft in Opsi.")
	return err
}

func validateInitOptions(options initOptions) error {
	if strings.TrimSpace(options.ProjectID) == "" {
		return errors.New("--project-id is required")
	}
	if options.RepositoryID < 0 || options.InstallationID < 0 {
		return errors.New("repository and installation IDs must be positive integers")
	}
	if options.Timeout <= 0 || options.Timeout > 5*time.Minute {
		return errors.New("--timeout must be greater than zero and at most 5m")
	}
	if strings.TrimSpace(options.SelectedRef) != options.SelectedRef || strings.IndexFunc(options.SelectedRef, unicode.IsControl) >= 0 {
		return errors.New("--ref is invalid")
	}
	return nil
}
func toInventory(values []cloudclient.GitHubRepository) []repository.InventoryRepository {
	result := make([]repository.InventoryRepository, 0, len(values))
	for _, value := range values {
		result = append(result, repository.InventoryRepository{RepositoryID: value.RepositoryID, InstallationID: value.InstallationID, FullName: value.FullName, DefaultBranch: value.DefaultBranch, Status: value.Status, Archived: value.Archived, Disabled: value.Disabled, ClaimStatus: value.ClaimStatus})
	}
	return result
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
	if err := validateGitHubAuthorizationURL(started.AuthorizationURL); err != nil {
		return err
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
func validateGitHubAuthorizationURL(raw string) error {
	authorizationURL, err := url.Parse(raw)
	if err != nil || authorizationURL.Scheme != "https" || authorizationURL.Host == "" || authorizationURL.User != nil || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return errors.New("Cloud returned an invalid installation authorization URL")
	}
	return nil
}

type claimResult struct{ grant string }
type claimCallback struct {
	host, state string
	result      chan claimResult
	mu          sync.Mutex
	accepted    bool
}

func newClaimCallback(host, state string) *claimCallback {
	return &claimCallback{host: host, state: state, result: make(chan claimResult, 1)}
}
func (c *claimCallback) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet || request.Host != c.host || request.URL.Path != claimCallbackPath || len(request.URL.RawQuery) > 8192 {
		http.Error(response, "invalid installation callback", http.StatusBadRequest)
		return
	}
	state, grant := request.URL.Query().Get("state"), request.URL.Query().Get("grant")
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
func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
