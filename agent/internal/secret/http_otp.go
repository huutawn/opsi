package secret

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type HTTPOTPClient struct {
	Endpoint string
	Client   *http.Client
}

func (c HTTPOTPClient) RequestOTP(ctx context.Context, auth AuthContext, purpose string, ref SecretRef) (string, error) {
	var resp struct {
		RequestID string `json:"request_id"`
	}
	err := c.post(ctx, "/v1/otp/request", map[string]string{"project_id": auth.ProjectID, "user_id": auth.UserID, "purpose": purpose, "service_id": ref.ServiceID, "secret_name": ref.Name}, &resp)
	if err != nil {
		return "", err
	}
	if resp.RequestID == "" {
		return "", fmt.Errorf("cloud otp request_id is empty")
	}
	return resp.RequestID, nil
}

func (c HTTPOTPClient) VerifyOTP(ctx context.Context, auth AuthContext, requestID, purpose, code string) error {
	return c.post(ctx, "/v1/otp/verify", map[string]string{"request_id": requestID, "project_id": auth.ProjectID, "user_id": auth.UserID, "purpose": purpose, "code": code}, nil)
}

func (c HTTPOTPClient) post(ctx context.Context, path string, payload any, out any) error {
	endpoint := strings.TrimRight(c.Endpoint, "/")
	if endpoint == "" {
		return fmt.Errorf("cloud otp endpoint is required")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cloud otp status %d", resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
