package cloudrelay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/deploy"
)

type Client struct {
	BaseURL    string
	ProjectID  string
	AgentToken string
	HTTPClient *http.Client
}

type WebhookEnvelope struct {
	ProjectID   string   `json:"project_id"`
	ServiceID   string   `json:"service_id"`
	ServiceName string   `json:"service_name"`
	ServiceType string   `json:"service_type"`
	RepoURL     string   `json:"repo_url"`
	Ref         string   `json:"ref"`
	After       string   `json:"after"`
	Branch      string   `json:"branch"`
	TriggeredBy string   `json:"triggered_by"`
	Body        string   `json:"body"`
	Signature   string   `json:"signature"`
	Modified    []string `json:"modified"`
}

type DeploymentLease struct {
	Kind       string                `json:"kind"`
	Action     string                `json:"action"`
	Deployment DeploymentJobEnvelope `json:"deployment"`
	Service    ServiceEnvelope       `json:"service"`
}

type DeploymentJobEnvelope struct {
	ID                  string `json:"id"`
	DeploymentPlanHash  string `json:"deployment_plan_hash"`
	ManifestHash        string `json:"manifest_hash"`
	PreviousRevisionRef string `json:"previous_revision_ref"`
}

type ServiceEnvelope struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	SourceType string `json:"source_type"`
	RepoURL    string `json:"repo_url"`
	Image      string `json:"image"`
	Branch     string `json:"branch"`
	GitSHA     string `json:"git_sha"`
	Namespace  string `json:"namespace"`
	HealthPath string `json:"health_path"`
	Replicas   int    `json:"replicas"`
}

type DeploymentResult struct {
	Status                 string `json:"status"`
	FinalRevisionRef       string `json:"final_revision_ref,omitempty"`
	FailureCode            string `json:"failure_code,omitempty"`
	FailureMessageRedacted string `json:"failure_message_redacted,omitempty"`
	RollbackEligible       bool   `json:"rollback_eligible"`
	RollbackBlockedReason  string `json:"rollback_blocked_reason,omitempty"`
}

func (c Client) PollWebhook(ctx context.Context, agentID string, wait time.Duration) (*deploy.WebhookEvent, error) {
	body, status, err := c.pollNext(ctx, agentID, wait)
	if err != nil || status == http.StatusNoContent {
		return nil, err
	}
	var kind struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(body, &kind); err == nil && kind.Kind == "deployment" {
		return nil, nil
	}
	var envelope WebhookEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	return &deploy.WebhookEvent{
		ProjectID:   envelope.ProjectID,
		ServiceID:   envelope.ServiceID,
		ServiceName: envelope.ServiceName,
		ServiceType: envelope.ServiceType,
		RepoURL:     envelope.RepoURL,
		Ref:         envelope.Ref,
		After:       envelope.After,
		Branch:      envelope.Branch,
		TriggeredBy: envelope.TriggeredBy,
		Body:        []byte(envelope.Body),
		Signature:   envelope.Signature,
		Modified:    envelope.Modified,
	}, nil
}

func (c Client) PollDeployment(ctx context.Context, nodeID string, wait time.Duration) (*DeploymentLease, error) {
	body, status, err := c.pollNext(ctx, nodeID, wait)
	if err != nil || status == http.StatusNoContent {
		return nil, err
	}
	var lease DeploymentLease
	if err := json.Unmarshal(body, &lease); err != nil {
		return nil, err
	}
	if lease.Kind != "deployment" {
		return nil, nil
	}
	return &lease, nil
}

func (c Client) CompleteDeployment(ctx context.Context, nodeID, deploymentID string, result DeploymentResult) error {
	if c.BaseURL == "" {
		return fmt.Errorf("cloud base URL is required")
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	endpoint, err := url.Parse(c.BaseURL)
	if err != nil {
		return err
	}
	endpoint.Path = "/v1/agents/" + url.PathEscape(nodeID) + "/deployments/" + url.PathEscape(deploymentID) + "/result"
	query := endpoint.Query()
	if c.ProjectID != "" {
		query.Set("project_id", c.ProjectID)
	}
	endpoint.RawQuery = query.Encode()
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	c.authorize(req)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("complete deployment: status %d", resp.StatusCode)
	}
	return nil
}

func (c Client) pollNext(ctx context.Context, nodeID string, wait time.Duration) ([]byte, int, error) {
	if c.BaseURL == "" {
		return nil, 0, fmt.Errorf("cloud base URL is required")
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	endpoint, err := url.Parse(c.BaseURL)
	if err != nil {
		return nil, 0, err
	}
	endpoint.Path = "/v1/agents/" + url.PathEscape(nodeID) + "/webhooks/next"
	query := endpoint.Query()
	query.Set("wait", wait.String())
	if c.ProjectID != "" {
		query.Set("project_id", c.ProjectID)
	}
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	c.authorize(req)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil, resp.StatusCode, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("poll deployment: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	return data, resp.StatusCode, err
}

func (c Client) authorize(req *http.Request) {
	if c.AgentToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AgentToken)
	}
}
