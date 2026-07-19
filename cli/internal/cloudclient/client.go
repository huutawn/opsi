package cloudclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const responseLimit = 2 << 20

type Client struct {
	BaseURL    *url.URL
	HTTPClient *http.Client
	PAT        string
	UserAgent  string
}

func New(rawBaseURL, pat, version string, httpClient *http.Client) (*Client, error) {
	baseURL, err := validateBaseURL(rawBaseURL)
	if err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if httpClient.Timeout <= 0 {
		copyClient := *httpClient
		copyClient.Timeout = 30 * time.Second
		httpClient = &copyClient
	}
	if version == "" {
		version = "dev"
	}
	return &Client{BaseURL: baseURL, HTTPClient: httpClient, PAT: pat, UserAgent: "opsi-cli/" + version}, nil
}

func validateBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return nil, errors.New("cloud URL must be an absolute URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("cloud URL must not contain user info, query, or fragment")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		host := strings.ToLower(u.Hostname())
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return nil, errors.New("cloud URL must use HTTPS unless it targets loopback")
		}
	default:
		return nil, errors.New("cloud URL scheme must be HTTPS")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	return u, nil
}

func (c *Client) ListServices(ctx context.Context, projectID string) ([]Service, error) {
	var response struct {
		Services []Service `json:"services"`
	}
	err := c.do(ctx, http.MethodGet, []string{"api", "projects", projectID, "services"}, nil, "", &response)
	return response.Services, err
}

func (c *Client) ListBuildRecords(ctx context.Context, projectID string, query url.Values) (BuildRecordList, error) {
	var response BuildRecordList
	segments := []string{"api", "projects", projectID, "build-records"}
	endpoint, err := c.endpoint(segments...)
	if err != nil {
		return response, err
	}
	endpoint.RawQuery = query.Encode()
	err = c.doURL(ctx, http.MethodGet, endpoint, nil, "", &response)
	return response, err
}

func (c *Client) GetBuildRecord(ctx context.Context, projectID, recordID string) (BuildRecord, error) {
	var response BuildRecord
	err := c.do(ctx, http.MethodGet, []string{"api", "projects", projectID, "build-records", recordID}, nil, "", &response)
	return response, err
}

func (c *Client) GetPlacementFacts(ctx context.Context, projectID string) (PlacementFacts, error) {
	var response PlacementFacts
	err := c.do(ctx, http.MethodGet, []string{"api", "projects", projectID, "topology", "facts"}, nil, "", &response)
	return response, err
}
func (c *Client) PreviewTopology(ctx context.Context, projectID string, draft TopologyDraft) (TopologyPreview, error) {
	var response TopologyPreview
	err := c.do(ctx, http.MethodPost, []string{"api", "projects", projectID, "topology", "plan"}, map[string]any{"draft": draft}, "", &response)
	return response, err
}
func (c *Client) ValidateTopology(ctx context.Context, projectID string, draft TopologyDraft, policyID string) (TopologyValidation, error) {
	var response TopologyValidation
	err := c.do(ctx, http.MethodPost, []string{"api", "projects", projectID, "topology", "validate"}, map[string]any{"draft": draft, "policy_id": policyID}, "", &response)
	return response, err
}
func (c *Client) DiffTopology(ctx context.Context, projectID string, draft TopologyDraft) (TopologyDiff, error) {
	var response TopologyDiff
	err := c.do(ctx, http.MethodPost, []string{"api", "projects", projectID, "topology", "diff"}, map[string]any{"draft": draft}, "", &response)
	return response, err
}
func (c *Client) ApplyTopology(ctx context.Context, projectID, key string, request TopologyApplyRequest) (TopologyApplyResult, error) {
	var response TopologyApplyResult
	err := c.do(ctx, http.MethodPost, []string{"api", "projects", projectID, "topology", "apply"}, request, key, &response)
	return response, err
}
func (c *Client) GetTopology(ctx context.Context, projectID string) (TopologyPlan, error) {
	var response TopologyPlan
	err := c.do(ctx, http.MethodGet, []string{"api", "projects", projectID, "topology"}, nil, "", &response)
	return response, err
}
func (c *Client) GetOperatorCapacity(ctx context.Context, projectID, runtimeID string) (OperatorCapacity, error) {
	var response OperatorCapacity
	err := c.do(ctx, http.MethodGet, []string{"api", "projects", projectID, "topology", "capacities", runtimeID}, nil, "", &response)
	return response, err
}
func (c *Client) ApplyOperatorCapacity(ctx context.Context, projectID, runtimeID, key string, request OperatorCapacityApplyRequest) (OperatorCapacityApplyResult, error) {
	var response OperatorCapacityApplyResult
	err := c.do(ctx, http.MethodPost, []string{"api", "projects", projectID, "topology", "capacities", runtimeID}, request, key, &response)
	return response, err
}
func (c *Client) PreviewDeploymentPolicy(ctx context.Context, projectID string, draft DeploymentPolicyDraft) (DeploymentPolicyPreview, error) {
	var response DeploymentPolicyPreview
	err := c.do(ctx, http.MethodPost, []string{"api", "projects", projectID, "deployment-policies", "preview"}, draft, "", &response)
	return response, err
}
func (c *Client) DiffDeploymentPolicy(ctx context.Context, projectID string, request DeploymentPolicyApplyRequest) (DeploymentPolicyDiff, error) {
	var response DeploymentPolicyDiff
	err := c.do(ctx, http.MethodPost, []string{"api", "projects", projectID, "deployment-policies", "diff"}, request, "", &response)
	return response, err
}
func (c *Client) ApplyDeploymentPolicy(ctx context.Context, projectID, key string, request DeploymentPolicyApplyRequest) (DeploymentPolicyApplyResult, error) {
	var response DeploymentPolicyApplyResult
	err := c.do(ctx, http.MethodPost, []string{"api", "projects", projectID, "deployment-policies", "apply"}, request, key, &response)
	return response, err
}
func (c *Client) ListDeploymentPolicies(ctx context.Context, projectID string) ([]DeploymentPolicy, error) {
	var response struct {
		Policies []DeploymentPolicy `json:"policies"`
	}
	err := c.do(ctx, http.MethodGet, []string{"api", "projects", projectID, "deployment-policies"}, nil, "", &response)
	return response.Policies, err
}
func (c *Client) GetDeploymentPolicy(ctx context.Context, projectID, policyID string) (DeploymentPolicy, error) {
	var response DeploymentPolicy
	err := c.do(ctx, http.MethodGet, []string{"api", "projects", projectID, "deployment-policies", policyID}, nil, "", &response)
	return response, err
}
func (c *Client) DisableDeploymentPolicy(ctx context.Context, projectID, policyID, key string, request DeploymentPolicyDisableRequest) (DeploymentPolicyApplyResult, error) {
	var response DeploymentPolicyApplyResult
	err := c.do(ctx, http.MethodPost, []string{"api", "projects", projectID, "deployment-policies", policyID, "disable"}, request, key, &response)
	return response, err
}
func (c *Client) RouteBuildRecord(ctx context.Context, projectID string, request RoutingRequest) (RoutingDecision, error) {
	var response RoutingDecision
	err := c.do(ctx, http.MethodPost, []string{"api", "projects", projectID, "routing-decisions"}, request, "", &response)
	return response, err
}

func (c *Client) ListNodes(ctx context.Context, projectID string) ([]Node, error) {
	var response nodeListResponse
	err := c.do(ctx, http.MethodGet, []string{"api", "projects", projectID, "nodes"}, nil, "", &response)
	return response.Nodes, err
}

func (c *Client) MarkNodeOffline(ctx context.Context, projectID, nodeID, idempotencyKey string) (Node, error) {
	var response Node
	err := c.do(ctx, http.MethodPost, []string{"api", "projects", projectID, "nodes", nodeID, "offline"}, struct {
		ConfirmTargetReset bool `json:"confirm_target_reset"`
	}{ConfirmTargetReset: true}, idempotencyKey, &response)
	return response, err
}

func (c *Client) CreateBootstrapSession(ctx context.Context, projectID string, request BootstrapRequest, idempotencyKey string) (BootstrapSession, error) {
	var response BootstrapSession
	err := c.do(ctx, http.MethodPost, []string{"api", "projects", projectID, "bootstrap-sessions"}, request, idempotencyKey, &response)
	return response, err
}

func (c *Client) ListBootstrapSessions(ctx context.Context, projectID string) ([]BootstrapSession, error) {
	var response struct {
		Sessions []BootstrapSession `json:"sessions"`
	}
	err := c.do(ctx, http.MethodGet, []string{"api", "projects", projectID, "bootstrap-sessions"}, nil, "", &response)
	return response.Sessions, err
}

func (c *Client) GetBootstrapSession(ctx context.Context, projectID, sessionID string) (BootstrapSession, error) {
	var response BootstrapSession
	err := c.do(ctx, http.MethodGet, []string{"api", "projects", projectID, "bootstrap-sessions", sessionID}, nil, "", &response)
	return response, err
}

func (c *Client) BootstrapEvents(ctx context.Context, projectID, sessionID string) ([]BootstrapEvent, error) {
	var response []BootstrapEvent
	err := c.do(ctx, http.MethodGet, []string{"api", "projects", projectID, "bootstrap-sessions", sessionID, "events"}, nil, "", &response)
	return response, err
}

func (c *Client) ListGitHubInstallations(ctx context.Context, projectID string) ([]GitHubInstallation, error) {
	var response struct {
		Installations []GitHubInstallation `json:"installations"`
	}
	err := c.do(ctx, http.MethodGet, []string{"v1", "projects", projectID, "github", "installations"}, nil, "", &response)
	return response.Installations, err
}

func (c *Client) ListGitHubRepositories(ctx context.Context, projectID string) ([]GitHubRepository, error) {
	var response struct {
		Repositories []GitHubRepository `json:"repositories"`
	}
	err := c.do(ctx, http.MethodGet, []string{"v1", "projects", projectID, "github", "repositories"}, nil, "", &response)
	return response.Repositories, err
}

func (c *Client) ListGitHubBindings(ctx context.Context, projectID string) ([]GitHubBinding, error) {
	var response struct {
		Bindings []GitHubBinding `json:"bindings"`
	}
	err := c.do(ctx, http.MethodGet, []string{"v1", "projects", projectID, "github", "bindings"}, nil, "", &response)
	return response.Bindings, err
}

func (c *Client) StartInstallationClaim(ctx context.Context, projectID string, installationID int64, localCallback, localState string) (InstallationClaimStart, error) {
	request := struct {
		LocalCallback string `json:"local_callback"`
		LocalState    string `json:"local_state"`
	}{localCallback, localState}
	var response InstallationClaimStart
	err := c.do(ctx, http.MethodPost, []string{"v1", "projects", projectID, "github", "installations", strconv.FormatInt(installationID, 10), "claim", "start"}, request, "installation-claim-start", &response)
	return response, err
}

func (c *Client) RedeemInstallationClaim(ctx context.Context, grant, localState string) (InstallationClaimResult, error) {
	request := struct {
		Grant string `json:"grant"`
		State string `json:"state"`
	}{grant, localState}
	var response InstallationClaimResult
	err := c.do(ctx, http.MethodPost, []string{"v1", "github", "installations", "claim", "redeem"}, request, "installation-claim-redeem", &response)
	return response, err
}

func (c *Client) ClaimRepository(ctx context.Context, projectID string, repositoryID int64) (RepositoryClaim, error) {
	var response RepositoryClaim
	err := c.do(ctx, http.MethodPost, []string{"v1", "projects", projectID, "github", "repositories", strconv.FormatInt(repositoryID, 10), "claim"}, struct{}{}, fmt.Sprintf("repository-claim:%s:%d", projectID, repositoryID), &response)
	return response, err
}

func (c *Client) ReleaseRepository(ctx context.Context, projectID string, repositoryID int64) error {
	return c.do(ctx, http.MethodDelete, []string{"v1", "projects", projectID, "github", "repositories", strconv.FormatInt(repositoryID, 10), "claim"}, nil, fmt.Sprintf("repository-release:%s:%d", projectID, repositoryID), nil)
}

func (c *Client) CreateServiceBinding(ctx context.Context, projectID, serviceID string, repositoryID int64, serviceKey, configPath string) (GitHubBinding, error) {
	request := struct {
		ServiceID    string `json:"service_id"`
		RepositoryID int64  `json:"repository_id"`
		ServiceKey   string `json:"service_key"`
		ConfigPath   string `json:"config_path"`
	}{serviceID, repositoryID, serviceKey, configPath}
	var response GitHubBinding
	key := fmt.Sprintf("binding:%s:%s:%d:%s:%s", projectID, serviceID, repositoryID, serviceKey, configPath)
	err := c.do(ctx, http.MethodPost, []string{"v1", "projects", projectID, "github", "bindings"}, request, key, &response)
	return response, err
}

func (c *Client) RemoveServiceBinding(ctx context.Context, projectID, bindingID string) error {
	return c.do(ctx, http.MethodDelete, []string{"v1", "projects", projectID, "github", "bindings", bindingID}, nil, fmt.Sprintf("binding-remove:%s:%s", projectID, bindingID), nil)
}

func (c *Client) do(ctx context.Context, method string, segments []string, body any, idempotencyKey string, response any) error {
	endpoint, err := c.endpoint(segments...)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode Cloud request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	return c.doURL(ctx, method, endpoint, reader, idempotencyKey, response)
}

func (c *Client) doURL(ctx context.Context, method string, endpoint *url.URL, body io.Reader, idempotencyKey string, response any) error {
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return fmt.Errorf("create Cloud request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.PAT)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", c.UserAgent)
	if method != http.MethodGet {
		request.Header.Set("Content-Type", "application/json")
		requestID, err := randomID()
		if err != nil {
			return err
		}
		request.Header.Set("X-Request-ID", requestID)
		if idempotencyKey == "" {
			idempotencyKey, err = randomID()
			if err != nil {
				return err
			}
		}
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	client := *c.HTTPClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	result, err := client.Do(request)
	if err != nil {
		return errors.New("Cloud API request failed")
	}
	defer result.Body.Close()
	data, err := readBounded(result.Body)
	if err != nil {
		return err
	}
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return parseAPIError(result.StatusCode, data, c.PAT)
	}
	if response == nil || len(data) == 0 {
		return nil
	}
	if !json.Valid(data) {
		return newResponseDecodeError("invalid JSON", result)
	}
	if err := json.Unmarshal(data, response); err != nil {
		return newResponseDecodeError("unexpected response schema", result)
	}
	return nil
}

func newResponseDecodeError(kind string, response *http.Response) error {
	contentType := "unknown"
	if value, _, err := mime.ParseMediaType(response.Header.Get("Content-Type")); err == nil && value != "" {
		contentType = value
	}
	return fmt.Errorf("Cloud API returned %s (status %d, content-type %q)", kind, response.StatusCode, contentType)
}

func (c *Client) endpoint(segments ...string) (*url.URL, error) {
	escaped := make([]string, len(segments))
	plain := make([]string, len(segments))
	for i, segment := range segments {
		if segment == "" {
			return nil, errors.New("Cloud API path segment is empty")
		}
		escaped[i] = url.PathEscape(segment)
		plain[i] = segment
	}
	u := *c.BaseURL
	baseEscaped := strings.TrimRight(c.BaseURL.EscapedPath(), "/")
	u.RawPath = baseEscaped + "/" + strings.Join(escaped, "/")
	u.Path = strings.TrimRight(c.BaseURL.Path, "/") + "/" + strings.Join(plain, "/")
	return &u, nil
}

func readBounded(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, responseLimit+1))
	if err != nil {
		return nil, errors.New("read Cloud API response")
	}
	if len(data) > responseLimit {
		return nil, errors.New("Cloud API response exceeds 2 MiB limit")
	}
	return data, nil
}

func parseAPIError(status int, data []byte, pat string) error {
	var payload struct {
		Code    string `json:"error_code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &payload)
	if payload.Code == "" || len(payload.Code) > 128 || strings.IndexFunc(payload.Code, func(value rune) bool {
		return !((value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || (value >= '0' && value <= '9') || value == '_' || value == '-')
	}) >= 0 {
		payload.Code = fmt.Sprintf("HTTP_%d", status)
	}
	if payload.Message == "" {
		payload.Message = http.StatusText(status)
	}
	if pat != "" {
		payload.Message = strings.ReplaceAll(payload.Message, pat, "[REDACTED]")
	}
	if len(payload.Message) > 1024 || strings.IndexFunc(payload.Message, func(value rune) bool { return value != '\n' && value != '\t' && value < ' ' }) >= 0 {
		payload.Message = http.StatusText(status)
	}
	return &APIError{Status: status, Code: payload.Code, Message: payload.Message}
}

func randomID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", errors.New("generate Cloud request ID")
	}
	return hex.EncodeToString(value[:]), nil
}
