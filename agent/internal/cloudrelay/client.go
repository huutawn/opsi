package cloudrelay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/deploy"
)

type Client struct {
	BaseURL    string
	ProjectID  string
	HTTPClient *http.Client
}

type WebhookEnvelope struct {
	ProjectID   string `json:"project_id"`
	ServiceID   string `json:"service_id"`
	ServiceName string `json:"service_name"`
	ServiceType string `json:"service_type"`
	RepoURL     string `json:"repo_url"`
	Ref         string `json:"ref"`
	After       string `json:"after"`
	Branch      string `json:"branch"`
	TriggeredBy string `json:"triggered_by"`
	Body        string `json:"body"`
	Signature   string `json:"signature"`
}

func (c Client) PollWebhook(ctx context.Context, agentID string, wait time.Duration) (*deploy.WebhookEvent, error) {
	if c.BaseURL == "" {
		return nil, fmt.Errorf("cloud base URL is required")
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	endpoint, err := url.Parse(c.BaseURL)
	if err != nil {
		return nil, err
	}
	endpoint.Path = "/v1/agents/" + url.PathEscape(agentID) + "/webhooks/next"
	query := endpoint.Query()
	query.Set("wait", wait.String())
	if c.ProjectID != "" {
		query.Set("project_id", c.ProjectID)
	}
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("poll webhook: status %d", resp.StatusCode)
	}
	var envelope WebhookEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
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
	}, nil
}
