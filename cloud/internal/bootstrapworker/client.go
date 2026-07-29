package bootstrapworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
)

type httpCloudClient struct {
	client *http.Client
	cfg    Config
}

func (h httpCloudClient) LeaseNext(ctx context.Context) (Lease, bool, error) {
	body, _ := json.Marshal(map[string]string{"worker_id": h.cfg.WorkerID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(h.cfg.CloudURL, "/")+"/internal/bootstrap/sessions/lease", bytes.NewReader(body))
	if err != nil {
		return Lease{}, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Bootstrap-Worker-Token", h.cfg.BootstrapWorkerToken)
	resp, err := h.client.Do(req)
	if err != nil {
		return Lease{}, false, fmt.Errorf("lease bootstrap session: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return Lease{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Lease{}, false, cloudResponseError(resp.StatusCode, "lease bootstrap session", safeBody(resp.Body))
	}
	var lease Lease
	if err := json.NewDecoder(resp.Body).Decode(&lease); err != nil {
		return Lease{}, false, fmt.Errorf("invalid bootstrap lease protocol response: %w", err)
	}
	return lease, true, nil
}

func (h httpCloudClient) Progress(ctx context.Context, lease Lease, status, message string) error {
	return h.postState(ctx, lease, "progress", finishRequest{Status: status, Message: message})
}

func (h httpCloudClient) Checkpoint(ctx context.Context, lease Lease, checkpoint registry.BootstrapCheckpoint) (registry.BootstrapCheckpoint, error) {
	body, _ := json.Marshal(struct {
		ProjectID string `json:"project_id"`
		registry.BootstrapCheckpoint
	}{ProjectID: lease.Bundle.ProjectID, BootstrapCheckpoint: checkpoint})
	endpoint := strings.TrimRight(h.cfg.CloudURL, "/") + "/internal/bootstrap/sessions/" + url.PathEscape(lease.Bundle.SessionID) + "/checkpoint"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return registry.BootstrapCheckpoint{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Bootstrap-Worker-Token", h.cfg.BootstrapWorkerToken)
	setLeaseHeaders(req, h.cfg, lease)
	resp, err := h.client.Do(req)
	if err != nil {
		return registry.BootstrapCheckpoint{}, fmt.Errorf("checkpoint bootstrap session: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return registry.BootstrapCheckpoint{}, cloudResponseError(resp.StatusCode, "checkpoint bootstrap session", safeBody(resp.Body))
	}
	var saved registry.BootstrapCheckpoint
	if err := json.NewDecoder(resp.Body).Decode(&saved); err != nil {
		return registry.BootstrapCheckpoint{}, fmt.Errorf("invalid bootstrap checkpoint protocol response: %w", err)
	}
	return saved, nil
}

func (h httpCloudClient) Finish(ctx context.Context, lease Lease, result FinishResult) error {
	req := finishRequest{Status: result.Status, Message: result.Message}
	if result.Failure != nil {
		req.FailureCode = result.Failure.Code
		req.Message = result.Failure.Message
		req.Retryable = result.Failure.Retryable
	}
	return h.postState(ctx, lease, "finish", req)
}

type finishRequest struct {
	ProjectID   string `json:"project_id"`
	Status      string `json:"status"`
	Message     string `json:"message"`
	FailureCode string `json:"failure_code,omitempty"`
	Retryable   bool   `json:"retryable,omitempty"`
}

func (h httpCloudClient) postState(ctx context.Context, lease Lease, action string, state finishRequest) error {
	state.ProjectID = lease.Bundle.ProjectID
	state.Message = registry.RedactString(state.Message)
	body, _ := json.Marshal(state)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(h.cfg.CloudURL, "/")+"/internal/bootstrap/sessions/"+url.PathEscape(lease.Bundle.SessionID)+"/"+action, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Bootstrap-Worker-Token", h.cfg.BootstrapWorkerToken)
	setLeaseHeaders(req, h.cfg, lease)
	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s bootstrap session: %w", action, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return cloudResponseError(resp.StatusCode, action+" bootstrap session", safeBody(resp.Body))
	}
	return nil
}

func (h httpCloudClient) HeartbeatLease(ctx context.Context, lease Lease) (time.Time, error) {
	body, _ := json.Marshal(map[string]string{"project_id": lease.Bundle.ProjectID})
	endpoint := strings.TrimRight(h.cfg.CloudURL, "/") + "/internal/bootstrap/sessions/" + url.PathEscape(lease.Bundle.SessionID) + "/lease-heartbeat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Bootstrap-Worker-Token", h.cfg.BootstrapWorkerToken)
	setLeaseHeaders(req, h.cfg, lease)
	resp, err := h.client.Do(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("heartbeat bootstrap lease: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return time.Time{}, cloudResponseError(resp.StatusCode, "heartbeat bootstrap lease", safeBody(resp.Body))
	}
	var out struct {
		LeaseExpiresAt time.Time `json:"lease_expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.LeaseExpiresAt.IsZero() {
		return time.Time{}, fmt.Errorf("invalid bootstrap heartbeat protocol response")
	}
	return out.LeaseExpiresAt, nil
}

func (h httpCloudClient) WaitForHeartbeat(ctx context.Context, lease Lease) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		status, err := h.bootstrapStatus(ctx, lease)
		if err != nil {
			return err
		}
		switch status {
		case "verifying":
			return nil
		case "failed", "cancelled", "expired":
			return fmt.Errorf("bootstrap session became %s before heartbeat verification", status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (h httpCloudClient) bootstrapStatus(ctx context.Context, lease Lease) (string, error) {
	endpoint := strings.TrimRight(h.cfg.CloudURL, "/") + "/internal/bootstrap/sessions/" + url.PathEscape(lease.Bundle.SessionID) + "/status?project_id=" + url.QueryEscape(lease.Bundle.ProjectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Bootstrap-Worker-Token", h.cfg.BootstrapWorkerToken)
	setLeaseHeaders(req, h.cfg, lease)
	resp, err := h.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("get bootstrap session: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", cloudResponseError(resp.StatusCode, "get bootstrap session", safeBody(resp.Body))
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Status, nil
}

func setLeaseHeaders(req *http.Request, cfg Config, lease Lease) {
	req.Header.Set("X-Bootstrap-Worker-ID", cfg.WorkerID)
	req.Header.Set("X-Bootstrap-Lease-Token", lease.LeaseToken)
}

type cloudError struct {
	status    int
	operation string
	code      string
	msg       string
}

func (e cloudError) Error() string { return e.msg }

func cloudResponseError(status int, operation, body string) error {
	var payload struct {
		Code string `json:"error_code"`
	}
	_ = json.Unmarshal([]byte(body), &payload)
	return cloudError{status: status, operation: operation, code: payload.Code, msg: operation + ": " + body}
}

func cloudErrorCode(err error) string {
	var responseErr cloudError
	if errors.As(err, &responseErr) {
		return responseErr.code
	}
	return ""
}

func isFatalCloudError(err error) bool {
	if err == nil {
		return false
	}
	var responseErr cloudError
	if errors.As(err, &responseErr) {
		switch responseErr.status {
		case http.StatusUnauthorized, http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusUnprocessableEntity:
			return true
		}
		return false
	}
	return strings.Contains(err.Error(), "invalid bootstrap") && strings.Contains(err.Error(), "protocol response")
}

func isDaemonFatalCloudError(err error) bool {
	if isFatalCloudError(err) {
		return true
	}
	var responseErr cloudError
	return errors.As(err, &responseErr) && responseErr.operation == "lease bootstrap session" && responseErr.status == http.StatusForbidden
}

func isLeaseLossError(err error) bool {
	var responseErr cloudError
	if !errors.As(err, &responseErr) || responseErr.operation == "lease bootstrap session" {
		return false
	}
	if responseErr.status == http.StatusForbidden || responseErr.status == http.StatusGone {
		return true
	}
	if responseErr.status != http.StatusConflict {
		return false
	}
	if responseErr.code == "" {
		return responseErr.operation != "checkpoint bootstrap session"
	}
	return strings.HasPrefix(responseErr.code, "BOOTSTRAP_LEASE_") || responseErr.code == "BOOTSTRAP_SESSION_EXPIRED"
}

func safeBody(r io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(r, 4096))
	return registry.RedactString(string(data))
}

func redactForBundle(bundle Bundle, value string) string {
	value = registry.RedactString(value)
	for _, secret := range []string{bundle.SSH.Password, bundle.SSH.PrivateKey, bundle.AgentRegistrationToken} {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}

func redactForConfig(cfg Config, leaseToken, value string) string {
	value = registry.RedactString(value)
	for _, secret := range []string{cfg.BootstrapWorkerToken, leaseToken} {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}

func redactForLease(cfg Config, lease Lease, value string) string {
	return redactForConfig(cfg, lease.LeaseToken, redactForBundle(lease.Bundle, value))
}
