package actionplane

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	actionv1 "github.com/opsi-dev/opsi/contracts/go/actionv1"
)

type Device = actionv1.ActionDevice

const (
	DeviceActive  = actionv1.DeviceActive
	DeviceRevoked = actionv1.DeviceRevoked
)

type DeviceResolver interface {
	Resolve(context.Context, string, string, string) (Device, error)
}

type HTTPDeviceResolver struct {
	BaseURL string
	Token   string
	NodeID  string
	Client  *http.Client
}

func (r HTTPDeviceResolver) Resolve(ctx context.Context, projectID, deviceID, owner string) (Device, error) {
	if strings.TrimSpace(r.BaseURL) == "" || strings.TrimSpace(r.Token) == "" || strings.TrimSpace(r.NodeID) == "" {
		return Device{}, errorsDeviceUnavailable()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(r.BaseURL, "/")+"/v1/agent/projects/"+urlSegment(projectID)+"/action-devices/"+urlSegment(deviceID), nil)
	if err != nil {
		return Device{}, errorsDeviceUnavailable()
	}
	request.Header.Set("Authorization", "Bearer "+r.Token)
	request.Header.Set("X-Opsi-Node-ID", r.NodeID)
	request.Header.Set("X-Agent-Timestamp", time.Now().UTC().Format(time.RFC3339))
	request.Header.Set("X-Agent-Signature", agentSignature(request, r.Token))
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return Device{}, errorsDeviceUnavailable()
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Device{}, fmt.Errorf("action device resolver status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, actionv1.MaxJSONBytes))
	if err != nil {
		return Device{}, errorsDeviceUnavailable()
	}
	var device Device
	if err := actionv1.DecodeStrict(body, &device); err != nil {
		return Device{}, errorsDeviceUnavailable()
	}
	if device.ProjectID != projectID || device.OwnerPrincipal != owner || device.Status != DeviceActive || len(device.PublicKey) != 32 {
		return Device{}, errorsDeviceUnavailable()
	}
	sum := sha256.Sum256(device.PublicKey)
	if device.FingerprintSHA256 != hex.EncodeToString(sum[:]) {
		return Device{}, errorsDeviceUnavailable()
	}
	return device, nil
}

func errorsDeviceUnavailable() error { return fmt.Errorf("%s", actionv1.FailureDeviceUnavailable) }
func urlSegment(value string) string {
	return url.PathEscape(value)
}
func agentSignature(request *http.Request, token string) string {
	// The Cloud agent gate signs method, URI, and timestamp with the bearer token.
	return signAgentRequest(request.Method+"\n"+request.URL.RequestURI()+"\n"+request.Header.Get("X-Agent-Timestamp"), token)
}

func signAgentRequest(value, token string) string {
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(value))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
