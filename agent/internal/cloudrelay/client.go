package cloudrelay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
)

type Client struct {
	BaseURL      string
	ProjectID    string
	AgentToken   string
	SignRequests bool
	HTTPClient   *http.Client
}

type DeploymentLease struct {
	Kind       string                     `json:"kind"`
	Action     string                     `json:"action"`
	LeaseToken string                     `json:"lease_token,omitempty"`
	Deployment DeploymentJobEnvelope      `json:"deployment"`
	Command    *deploymentv1.AgentCommand `json:"command,omitempty"`
}

type JobLease struct {
	Kind          string              `json:"kind"`
	Deployment    *DeploymentLease    `json:"deployment_lease,omitempty"`
	NodeLifecycle *NodeLifecycleLease `json:"node_lifecycle_lease,omitempty"`
}

type DeploymentJobEnvelope struct {
	ID                  string `json:"id"`
	DeploymentPlanHash  string `json:"deployment_plan_hash"`
	ManifestHash        string `json:"manifest_hash"`
	IntentHash          string `json:"intent_hash"`
	PreviousRevisionRef string `json:"previous_revision_ref"`
	// Kept wire-inert so historical result formatting can compile without
	// accepting legacy deployment intent data from Cloud.
	DeploymentIntent *deploymentIntentEnvelope `json:"-"`
}

type deploymentIntentEnvelope struct {
	Review struct {
		IntentHash string
	} `json:"review"`
}

type DeploymentResult struct {
	SchemaVersion          string                    `json:"schema_version,omitempty"`
	Status                 string                    `json:"status"`
	LeaseToken             string                    `json:"lease_token,omitempty"`
	FinalRevisionRef       string                    `json:"final_revision_ref,omitempty"`
	IntentHash             string                    `json:"intent_hash,omitempty"`
	FailureCode            string                    `json:"failure_code,omitempty"`
	FailureMessageRedacted string                    `json:"failure_message_redacted,omitempty"`
	RollbackEligible       bool                      `json:"rollback_eligible"`
	RollbackBlockedReason  string                    `json:"rollback_blocked_reason,omitempty"`
	SpecHash               string                    `json:"spec_hash,omitempty"`
	ApplicationImage       string                    `json:"application_image,omitempty"`
	ApplicationImageID     string                    `json:"application_image_id,omitempty"`
	Namespace              string                    `json:"namespace,omitempty"`
	DeploymentName         string                    `json:"deployment_name,omitempty"`
	ServiceName            string                    `json:"service_name,omitempty"`
	AvailableReplicas      int32                     `json:"available_replicas,omitempty"`
	RolloutResult          *deploymentv1.AgentResult `json:"rollout_result,omitempty"`
}

type NodeLifecycleLease struct {
	Kind          string `json:"kind"`
	ID            string `json:"id"`
	Action        string `json:"action"`
	ProjectID     string `json:"project_id"`
	TargetNodeID  string `json:"target_node_id"`
	TargetName    string `json:"target_node_name"`
	ConfirmRemove bool   `json:"confirm_remove"`
	LeaseToken    string `json:"lease_token,omitempty"`
}

type NodeLifecycleResult struct {
	Status                 string `json:"status"`
	LeaseToken             string `json:"lease_token,omitempty"`
	FailureCode            string `json:"failure_code,omitempty"`
	FailureMessageRedacted string `json:"failure_message_redacted,omitempty"`
	Verified               bool   `json:"verified"`
}

type Heartbeat struct {
	Version      string         `json:"version"`
	Capabilities map[string]any `json:"capabilities,omitempty"`
	K3SStatus    string         `json:"k3s_status,omitempty"`
	NodeReady    bool           `json:"node_ready"`
}

func (c Client) PollJob(ctx context.Context, nodeID string, wait time.Duration) (*JobLease, error) {
	body, status, err := c.pollNext(ctx, nodeID, wait)
	if err != nil || status == http.StatusNoContent {
		return nil, err
	}
	var kind struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(body, &kind); err != nil {
		return nil, err
	}
	switch kind.Kind {
	case "deployment":
		var lease DeploymentLease
		if err := json.Unmarshal(body, &lease); err != nil {
			return nil, err
		}
		return &JobLease{Kind: kind.Kind, Deployment: &lease}, nil
	case "node_lifecycle":
		var lease NodeLifecycleLease
		if err := json.Unmarshal(body, &lease); err != nil {
			return nil, err
		}
		return &JobLease{Kind: kind.Kind, NodeLifecycle: &lease}, nil
	default:
		return nil, nil
	}
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(data))
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

func (c Client) ProgressDeployment(ctx context.Context, nodeID, deploymentID string, progress deploymentv1.Progress) error {
	if c.BaseURL == "" {
		return fmt.Errorf("cloud base URL is required")
	}
	endpoint, err := url.Parse(c.BaseURL)
	if err != nil {
		return err
	}
	endpoint.Path = "/v1/agents/" + url.PathEscape(nodeID) + "/deployments/" + url.PathEscape(deploymentID) + "/progress"
	query := endpoint.Query()
	query.Set("project_id", c.ProjectID)
	endpoint.RawQuery = query.Encode()
	data, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	c.authorize(req)
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("deployment progress: status %d", resp.StatusCode)
	}
	return nil
}

func (c Client) CompleteNodeLifecycle(ctx context.Context, nodeID, jobID string, result NodeLifecycleResult) error {
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
	endpoint.Path = "/v1/agents/" + url.PathEscape(nodeID) + "/node-lifecycle/" + url.PathEscape(jobID) + "/result"
	query := endpoint.Query()
	if c.ProjectID != "" {
		query.Set("project_id", c.ProjectID)
	}
	endpoint.RawQuery = query.Encode()
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(data))
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
		return fmt.Errorf("complete node lifecycle: status %d", resp.StatusCode)
	}
	return nil
}

func (c Client) Heartbeat(ctx context.Context, nodeID string, heartbeat Heartbeat) error {
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
	endpoint.Path = "/v1/agents/" + url.PathEscape(nodeID) + "/heartbeat"
	query := endpoint.Query()
	if c.ProjectID != "" {
		query.Set("project_id", c.ProjectID)
	}
	endpoint.RawQuery = query.Encode()
	data, err := json.Marshal(heartbeat)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(data))
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
		return fmt.Errorf("heartbeat: status %d", resp.StatusCode)
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
	if c.SignRequests && c.AgentToken != "" {
		ts := time.Now().UTC().Format(time.RFC3339)
		mac := hmac.New(sha256.New, []byte(c.AgentToken))
		_, _ = mac.Write([]byte(req.Method + "\n" + req.URL.RequestURI() + "\n" + ts))
		req.Header.Set("X-Agent-Timestamp", ts)
		req.Header.Set("X-Agent-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
}
