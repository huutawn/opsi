package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
)

func writeLocalJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeLocalError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	requestID := r.Header.Get("X-Request-ID")
	w.Header().Set("content-type", "application/json")
	if requestID != "" {
		w.Header().Set("X-Request-ID", requestID)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": message, "retryable": status >= 500, "request_id": requestID}})
}

func proxyLocalRegistry(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error), localSession string, authFlow *localAuthFlow) {
	if isMutation(r.Method) && !isPlacementPreview(r.URL.Path) {
		if r.Header.Get("X-Local-Session") != localSession {
			writeLocalError(w, r, http.StatusUnauthorized, "LOCAL_SESSION_REQUIRED", "mutating local requests require X-Local-Session")
			return
		}
		if !requireLocalIdempotencyKey(w, r) {
			return
		}
	}
	if startLocalInstallationClaim(w, r, cfg, factory, authFlow) {
		return
	}
	if localTelemetry(w, r, cfg, factory) || localSecretOperation(w, r, cfg, factory) || localIncidentOperation(w, r, cfg, factory) {
		return
	}
	cloud, err := url.Parse(cfg.CloudURL)
	if err != nil || cloud.Scheme == "" || cloud.Host == "" {
		writeLocalError(w, r, http.StatusBadGateway, "INVALID_CLOUD_URL", "local cloud_url is invalid")
		return
	}
	path, rawQuery, err := localToCloudPath(r.URL)
	if err != nil {
		writeLocalError(w, r, http.StatusNotFound, "LOCAL_ROUTE_NOT_FOUND", err.Error())
		return
	}
	target := *cloud
	target.Path = path
	target.RawQuery = rawQuery

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if projectID, serviceID, ok := localDeploymentIDs(r.URL.Path, r.Method); ok {
		sourceType, found, err := fetchCloudServiceSourceType(ctx, *cloud, projectID, serviceID, r.Header, factory)
		if err != nil {
			writeLocalError(w, r, http.StatusBadGateway, "LOCAL_DEPLOY_VALIDATION_FAILED", "could not validate service deployment source")
			return
		}
		if found && sourceType == "image" {
			writeLocalError(w, r, http.StatusBadRequest, "IMAGE_DEPLOY_NOT_SUPPORTED", "Image-source deploy is not supported by the current Agent runner. Use Git source or enable the image deploy capability.")
			return
		}
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, target.String(), r.Body)
	if err != nil {
		writeLocalError(w, r, http.StatusBadGateway, "LOCAL_PROXY_REQUEST_FAILED", "could not create Cloud request")
		return
	}
	copyProxyHeaders(req.Header, r.Header)
	req.Header.Del("Authorization")
	if pat := optionalPAT(factory); pat != "" {
		req.Header.Set("Authorization", "Bearer "+pat)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeLocalError(w, r, http.StatusBadGateway, "CLOUD_UNAVAILABLE", "Cloud registry is unavailable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		writeLocalError(w, r, resp.StatusCode, "CLOUD_AUTH_REQUIRED", "Cloud rejected the saved credential; use Login to authenticate again")
		return
	}
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	if requestID := r.Header.Get("X-Request-ID"); requestID != "" {
		w.Header().Set("X-Request-ID", requestID)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func startLocalInstallationClaim(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error), flow *localAuthFlow) bool {
	projectID, installationID, ok := localInstallationClaimIDs(r.URL.Path)
	if !ok {
		return false
	}
	if r.Method != http.MethodPost {
		writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
		return true
	}
	if err := validateGitHubProjectID(projectID); err != nil {
		writeLocalError(w, r, http.StatusBadRequest, "GITHUB_PROJECT_ID_INVALID", err.Error())
		return true
	}
	if installationID <= 0 {
		writeLocalError(w, r, http.StatusBadRequest, "GITHUB_INSTALLATION_ID_INVALID", "installation_id must be a positive integer")
		return true
	}
	pat := optionalPAT(factory)
	if pat == "" {
		writeLocalError(w, r, http.StatusUnauthorized, "CLOUD_PAT_REQUIRED", "Cloud PAT is missing from the OS keychain")
		return true
	}
	callbackURL, err := localInstallationCallbackURL(r.Host)
	if err != nil {
		writeLocalError(w, r, http.StatusBadRequest, "LOCAL_CALLBACK_INVALID", err.Error())
		return true
	}
	client, err := cloudclient.New(cfg.CloudURL, pat, "opsi-local-ui", http.DefaultClient)
	if err != nil {
		writeLocalError(w, r, http.StatusBadGateway, "INVALID_CLOUD_URL", "local cloud_url is invalid")
		return true
	}
	state, err := randomState(nil)
	if err != nil {
		writeLocalError(w, r, http.StatusServiceUnavailable, "GITHUB_CLAIM_STATE_UNAVAILABLE", "installation claim state generation failed")
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), githubCommandTimeout)
	defer cancel()
	started, err := client.StartInstallationClaim(ctx, projectID, installationID, callbackURL, state)
	if err != nil {
		writeLocalError(w, r, localCloudStatus(err), "GITHUB_INSTALLATION_CLAIM_START_FAILED", "GitHub installation authorization could not be started; retry after checking project access and Cloud connectivity")
		return true
	}
	if err := validateGitHubAuthorizationURL(started.AuthorizationURL); err != nil {
		writeLocalError(w, r, http.StatusBadGateway, "GITHUB_AUTHORIZATION_URL_INVALID", err.Error())
		return true
	}
	expiresAt := time.Now().UTC().Add(5 * time.Minute)
	flow.mu.Lock()
	for pendingState, expiry := range flow.installationClaims {
		if time.Now().UTC().After(expiry) {
			delete(flow.installationClaims, pendingState)
		}
	}
	flow.installationClaims[state] = expiresAt
	flow.mu.Unlock()
	writeLocalJSON(w, http.StatusOK, map[string]any{"authorization_url": started.AuthorizationURL, "status": "started", "expires_at": expiresAt})
	return true
}

func completeLocalInstallationClaim(w http.ResponseWriter, r *http.Request, cfg config.Config, factory func() (keychain.Store, error), flow *localAuthFlow) {
	if r.Method != http.MethodGet {
		writeLocalError(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method is not allowed")
		return
	}
	grant, state := r.URL.Query().Get("grant"), r.URL.Query().Get("state")
	flow.mu.Lock()
	expiresAt, ok := flow.installationClaims[state]
	if ok {
		delete(flow.installationClaims, state)
	}
	flow.mu.Unlock()
	if !ok || grant == "" || time.Now().UTC().After(expiresAt) {
		writeLocalError(w, r, http.StatusUnauthorized, "GITHUB_INSTALLATION_CALLBACK_INVALID", "installation authorization callback expired, was reused, or is invalid")
		return
	}
	pat := optionalPAT(factory)
	if pat == "" {
		writeLocalError(w, r, http.StatusUnauthorized, "CLOUD_PAT_REQUIRED", "Cloud PAT is missing from the OS keychain")
		return
	}
	client, err := cloudclient.New(cfg.CloudURL, pat, "opsi-local-ui", http.DefaultClient)
	if err != nil {
		writeLocalError(w, r, http.StatusBadGateway, "INVALID_CLOUD_URL", "local cloud_url is invalid")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), githubCommandTimeout)
	defer cancel()
	result, err := client.RedeemInstallationClaim(ctx, grant, state)
	if err != nil {
		writeLocalError(w, r, localCloudStatus(err), "GITHUB_INSTALLATION_CLAIM_REDEEM_FAILED", "Cloud rejected the one-time installation grant; restart the connection flow")
		return
	}
	location := "/?github=claimed&installation_id=" + strconv.FormatInt(result.Installation.InstallationID, 10)
	http.Redirect(w, r, location, http.StatusFound)
}

func localInstallationClaimIDs(path string) (string, int64, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 9 || parts[0] != "api" || parts[1] != "local" || parts[2] != "projects" || parts[4] != "github" || parts[5] != "installations" || parts[7] != "claim" || parts[8] != "start" {
		return "", 0, false
	}
	projectID, err := url.PathUnescape(parts[3])
	if err != nil {
		return "", 0, true
	}
	installationID, err := strconv.ParseInt(parts[6], 10, 64)
	if err != nil || installationID <= 0 {
		return projectID, 0, true
	}
	return projectID, installationID, true
}

func localInstallationCallbackURL(hostport string) (string, error) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil || port == "" {
		return "", fmt.Errorf("local callback host must include the loopback listener port")
	}
	if host != "localhost" {
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if ip == nil || !ip.Equal(net.ParseIP("127.0.0.1")) {
			return "", fmt.Errorf("local callback host must be 127.0.0.1 or localhost")
		}
	}
	return "http://" + net.JoinHostPort(host, port) + "/api/local/github/installations/claim/callback", nil
}

func localCloudStatus(err error) int {
	var apiErr *cloudclient.APIError
	if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status <= 599 {
		return apiErr.Status
	}
	return http.StatusBadGateway
}

func localToCloudPath(u *url.URL) (string, string, error) {
	path := strings.TrimPrefix(u.Path, "/api/local")
	if path == "/projects" || path == "/projects/" {
		q := u.Query()
		orgID := q.Get("org_id")
		if orgID == "" {
			orgID = "org-1"
		}
		q.Del("org_id")
		return "/api/orgs/" + url.PathEscape(orgID) + "/projects", q.Encode(), nil
	}
	if strings.HasPrefix(path, "/projects/") && strings.HasSuffix(path, "/nodes/bootstrap") {
		return "/api" + strings.TrimSuffix(path, "/nodes/bootstrap") + "/bootstrap-sessions", u.RawQuery, nil
	}
	if strings.HasPrefix(path, "/projects/") && strings.Contains(path, "/github/") {
		return "/v1" + path, u.RawQuery, nil
	}
	if strings.HasPrefix(path, "/projects/") {
		return "/api" + path, u.RawQuery, nil
	}
	return "", "", fmt.Errorf("local route %s is not implemented", u.Path)
}

func isMutation(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

func isPlacementPreview(path string) bool {
	for _, suffix := range []string{"/topology/plan", "/topology/validate", "/topology/diff", "/deployment-policies/preview", "/deployment-policies/diff", "/routing-decisions"} {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

func copyProxyHeaders(dst, src http.Header) {
	for _, key := range []string{"Content-Type", "X-Request-ID", "Idempotency-Key"} {
		if value := src.Get(key); value != "" {
			dst.Set(key, value)
		}
	}
}
