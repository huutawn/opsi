package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
	"github.com/spf13/cobra"
)

const (
	actionsBuildRecordPath       = "/v1/build-records"
	actionsRequestTimeout        = 30 * time.Second
	actionsResponseLimit   int64 = 64 << 10
)

var (
	actionsSHA     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	actionsHash    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	actionsDigest  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	actionsOCIRepo = regexp.MustCompile(`^ghcr\.io/[a-z0-9](?:[a-z0-9._-]*/?)+[a-z0-9]$`)
)

type actionsBuildRecordInput struct {
	CloudURL        string
	ServiceKey      string
	ConfigHash      string
	PlanHash        string
	Platform        string
	OCIRepository   string
	OCIDigest       string
	ProvenanceHash  string
	GitHubActions   string
	TokenRequestURL string
	RequestToken    string
	RepositoryID    string
	OwnerID         string
	Ref             string
	SHA             string
	EventName       string
	WorkflowRef     string
	JobWorkflowRef  string
	RunID           string
	RunAttempt      string
}

type actionsBuildRecordResult struct {
	ID     string `json:"id"`
	Reused bool   `json:"reused"`
}

func newInternalCommand(options Options) *cobra.Command {
	internal := &cobra.Command{Use: "internal", Hidden: true}
	buildRecord := &cobra.Command{Use: "build-record", Hidden: true}
	var input actionsBuildRecordInput
	submit := &cobra.Command{
		Use:    "submit-from-github-actions",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if input.CloudURL == "" {
				input.CloudURL = os.Getenv("OPSI_CLOUD_URL")
			}
			input.GitHubActions = os.Getenv("GITHUB_ACTIONS")
			input.TokenRequestURL = os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL")
			input.RequestToken = os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
			input.RepositoryID = os.Getenv("GITHUB_REPOSITORY_ID")
			input.OwnerID = os.Getenv("GITHUB_REPOSITORY_OWNER_ID")
			input.Ref = os.Getenv("GITHUB_REF")
			input.SHA = os.Getenv("GITHUB_SHA")
			input.EventName = os.Getenv("GITHUB_EVENT_NAME")
			input.WorkflowRef = os.Getenv("GITHUB_WORKFLOW_REF")
			input.JobWorkflowRef = os.Getenv("GITHUB_JOB_WORKFLOW_REF")
			input.RunID = os.Getenv("GITHUB_RUN_ID")
			input.RunAttempt = os.Getenv("GITHUB_RUN_ATTEMPT")
			result, err := submitBuildRecordFromGitHubActions(command.Context(), input, actionsHTTPClient(options.HTTPClient))
			if err != nil {
				return err
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(result)
		},
	}
	flags := submit.Flags()
	flags.StringVar(&input.CloudURL, "cloud-url", "", "Cloud origin; defaults to OPSI_CLOUD_URL")
	flags.StringVar(&input.ServiceKey, "service-key", "", "affected service key")
	flags.StringVar(&input.ConfigHash, "config-hash", "", "canonical Opsi config hash")
	flags.StringVar(&input.PlanHash, "plan-hash", "", "canonical Opsi plan hash")
	flags.StringVar(&input.Platform, "platform", "", "built OCI platform")
	flags.StringVar(&input.OCIRepository, "oci-repository", "", "canonical GHCR repository")
	flags.StringVar(&input.OCIDigest, "oci-digest", "", "immutable build-push digest")
	flags.StringVar(&input.ProvenanceHash, "provenance-digest", "", "optional provenance digest")
	buildRecord.AddCommand(submit)
	internal.AddCommand(buildRecord)
	return internal
}

func submitBuildRecordFromGitHubActions(ctx context.Context, input actionsBuildRecordInput, client *http.Client) (actionsBuildRecordResult, error) {
	if input.GitHubActions != "true" || input.TokenRequestURL == "" || input.RequestToken == "" {
		return actionsBuildRecordResult{}, errors.New("BuildRecord submission is available only inside GitHub Actions OIDC jobs")
	}
	endpoint, audience, err := actionsBuildRecordEndpoint(input.CloudURL)
	if err != nil {
		return actionsBuildRecordResult{}, err
	}
	tokenURL, err := actionsOIDCRequestURL(input.TokenRequestURL, audience)
	if err != nil {
		return actionsBuildRecordResult{}, err
	}
	submission, err := actionsBuildRecordSubmission(input)
	if err != nil {
		return actionsBuildRecordResult{}, err
	}
	if client == nil {
		client = actionsHTTPClient(nil)
	}
	oidcToken, err := requestActionsOIDCToken(ctx, client, tokenURL, input.RequestToken)
	if err != nil {
		return actionsBuildRecordResult{}, err
	}
	return postActionsBuildRecord(ctx, client, endpoint, oidcToken, submission)
}

func actionsBuildRecordEndpoint(raw string) (*url.URL, string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, "", errors.New("OPSI_CLOUD_URL must be an exact HTTPS origin")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" || strings.HasSuffix(hostname, ".") {
		return nil, "", errors.New("OPSI_CLOUD_URL host is invalid")
	}
	port := parsed.Port()
	if port == "443" {
		port = ""
	}
	parsed.Host = hostname
	if strings.Contains(hostname, ":") {
		parsed.Host = "[" + hostname + "]"
	}
	if port != "" {
		parsed.Host += ":" + port
	}
	parsed.Path = actionsBuildRecordPath
	audience := parsed.String()
	return parsed, audience, nil
}

func actionsOIDCRequestURL(raw, audience string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" || parsed.Path == "" || parsed.RawPath != "" {
		return nil, errors.New("GitHub Actions OIDC request URL is invalid")
	}
	host := strings.ToLower(parsed.Hostname())
	if (host != "pipelines.actions.githubusercontent.com" && host != "vstoken.actions.githubusercontent.com") || (parsed.Port() != "" && parsed.Port() != "443") {
		return nil, errors.New("GitHub Actions OIDC request URL is outside the expected token boundary")
	}
	if !strings.HasPrefix(parsed.Path, "/_apis/distributedtask/hubs/") || !strings.HasSuffix(parsed.Path, "/idtoken") {
		return nil, errors.New("GitHub Actions OIDC request URL is outside the expected token path")
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == ".." || strings.ContainsAny(segment, "\\\r\n") {
			return nil, errors.New("GitHub Actions OIDC request URL path is invalid")
		}
	}
	query := parsed.Query()
	if len(query) != 1 || len(query["api-version"]) != 1 || query.Get("api-version") != "2.0" {
		return nil, errors.New("GitHub Actions OIDC request URL query is invalid")
	}
	query.Set("audience", audience)
	parsed.RawQuery = query.Encode()
	return parsed, nil
}

func actionsBuildRecordSubmission(input actionsBuildRecordInput) (buildrecordv1.Submission, error) {
	repositoryID, err := positiveActionsID("GITHUB_REPOSITORY_ID", input.RepositoryID)
	if err != nil {
		return buildrecordv1.Submission{}, err
	}
	ownerID, err := positiveActionsID("GITHUB_REPOSITORY_OWNER_ID", input.OwnerID)
	if err != nil {
		return buildrecordv1.Submission{}, err
	}
	runID, err := positiveActionsID("GITHUB_RUN_ID", input.RunID)
	if err != nil {
		return buildrecordv1.Submission{}, err
	}
	attempt, err := positiveActionsID("GITHUB_RUN_ATTEMPT", input.RunAttempt)
	if err != nil || attempt > 1<<31-1 {
		return buildrecordv1.Submission{}, errors.New("GITHUB_RUN_ATTEMPT must be a bounded positive integer")
	}
	if !actionsSHA.MatchString(input.SHA) || !actionsHash.MatchString(input.ConfigHash) || !actionsHash.MatchString(input.PlanHash) {
		return buildrecordv1.Submission{}, errors.New("GitHub SHA, config hash, and plan hash must be canonical lowercase hashes")
	}
	if input.ProvenanceHash != "" && !actionsDigest.MatchString(input.ProvenanceHash) {
		return buildrecordv1.Submission{}, errors.New("provenance digest must be an immutable sha256 digest")
	}
	if input.Platform != "linux/amd64" || !actionsDigest.MatchString(input.OCIDigest) || !actionsOCIRepo.MatchString(input.OCIRepository) {
		return buildrecordv1.Submission{}, errors.New("platform, OCI repository, or immutable digest is invalid")
	}
	for name, value := range map[string]string{"service key": input.ServiceKey, "ref": input.Ref, "event": input.EventName, "workflow ref": input.WorkflowRef} {
		if value == "" || len(value) > 512 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
			return buildrecordv1.Submission{}, fmt.Errorf("%s is invalid", name)
		}
	}
	return buildrecordv1.Submission{
		SchemaVersion: buildrecordv1.SchemaVersion, ServiceKey: input.ServiceKey,
		RepositoryID: repositoryID, RepositoryOwnerID: ownerID, Ref: input.Ref, SHA: input.SHA,
		EventName: input.EventName, WorkflowRef: input.WorkflowRef, JobWorkflowRef: input.JobWorkflowRef,
		RunID: runID, RunAttempt: uint32(attempt), ConfigHash: input.ConfigHash, PlanHash: input.PlanHash,
		Platform: input.Platform, OCIRepository: input.OCIRepository, OCIDigest: input.OCIDigest,
		ProvenanceDigest: input.ProvenanceHash, Status: "succeeded",
	}, nil
}

func requestActionsOIDCToken(ctx context.Context, client *http.Client, endpoint *url.URL, requestToken string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", errors.New("create GitHub Actions OIDC request")
	}
	request.Header.Set("Authorization", "Bearer "+requestToken)
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return "", errors.New("request GitHub Actions OIDC token")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub Actions OIDC token request failed with HTTP %d", response.StatusCode)
	}
	var payload struct {
		Value string `json:"value"`
	}
	if err := decodeActionsJSON(response.Body, &payload); err != nil || payload.Value == "" || len(payload.Value) > 64<<10 || strings.ContainsAny(payload.Value, " \t\r\n") {
		return "", errors.New("GitHub Actions OIDC token response is invalid")
	}
	return payload.Value, nil
}

func postActionsBuildRecord(ctx context.Context, client *http.Client, endpoint *url.URL, oidcToken string, submission buildrecordv1.Submission) (actionsBuildRecordResult, error) {
	body, err := json.Marshal(submission)
	if err != nil {
		return actionsBuildRecordResult{}, errors.New("encode BuildRecord submission")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return actionsBuildRecordResult{}, errors.New("create BuildRecord submission")
	}
	request.Header.Set("Authorization", "Bearer "+oidcToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return actionsBuildRecordResult{}, errors.New("submit BuildRecord to Cloud")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return actionsBuildRecordResult{}, fmt.Errorf("BuildRecord submission failed with HTTP %d", response.StatusCode)
	}
	var payload struct {
		Record struct {
			ID string `json:"id"`
		} `json:"record"`
		Reused bool `json:"reused"`
	}
	if err := decodeActionsJSON(response.Body, &payload); err != nil || !validBuildRecordID(payload.Record.ID) {
		return actionsBuildRecordResult{}, errors.New("BuildRecord response is invalid")
	}
	return actionsBuildRecordResult{ID: payload.Record.ID, Reused: payload.Reused}, nil
}

func decodeActionsJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, actionsResponseLimit+1))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("response contains trailing data")
	}
	return nil
}

func positiveActionsID(name, raw string) (uint64, error) {
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 || value > uint64(1<<63-1) {
		return 0, fmt.Errorf("%s must be a positive decimal identifier", name)
	}
	return value, nil
}

func actionsHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		return &http.Client{Timeout: actionsRequestTimeout}
	}
	copyClient := *client
	if copyClient.Timeout <= 0 || copyClient.Timeout > actionsRequestTimeout {
		copyClient.Timeout = actionsRequestTimeout
	}
	return &copyClient
}
