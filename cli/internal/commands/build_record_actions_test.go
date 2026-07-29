package commands

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type actionsRoundTripper func(*http.Request) (*http.Response, error)

func (f actionsRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestSubmitBuildRecordFromGitHubActionsUsesTokensOnlyInHeaders(t *testing.T) {
	input := validActionsBuildRecordInput()
	const requestToken = "runner-request-secret"
	oidcToken := actionsTestOIDCToken(t, input, input.WorkflowRef)
	input.RequestToken = requestToken
	var calls int
	client := &http.Client{Transport: actionsRoundTripper(func(request *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			if request.Method != http.MethodGet || request.URL.Hostname() != "pipelines.actions.githubusercontent.com" || request.URL.Query().Get("audience") != "https://opsidev.site/v1/build-records" || request.Header.Get("Authorization") != "Bearer "+requestToken {
				t.Fatalf("OIDC request=%s auth=%q", request.URL.Redacted(), request.Header.Get("Authorization"))
			}
			return actionsResponse(http.StatusOK, `{"value":"`+oidcToken+`"}`), nil
		case 2:
			if request.Method != http.MethodPost || request.URL.String() != "https://opsidev.site/v1/build-records" || request.Header.Get("Authorization") != "Bearer "+oidcToken {
				t.Fatalf("BuildRecord request=%s auth=%q", request.URL.Redacted(), request.Header.Get("Authorization"))
			}
			var submission map[string]any
			if err := json.NewDecoder(request.Body).Decode(&submission); err != nil {
				t.Fatal(err)
			}
			if submission["service_key"] != "api" || submission["status"] != "succeeded" || submission["oci_digest"] != input.OCIDigest || submission["job_workflow_ref"] != input.WorkflowRef {
				t.Fatalf("submission=%+v", submission)
			}
			return actionsResponse(http.StatusCreated, `{"record":{"id":"br-live-1"},"reused":false}`), nil
		default:
			t.Fatal("unexpected HTTP request")
			return nil, nil
		}
	})}
	result, err := submitBuildRecordFromGitHubActions(context.Background(), input, client)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || result.ID != "br-live-1" || result.Reused {
		t.Fatalf("calls=%d result=%+v", calls, result)
	}
}

func TestActionsOIDCRequestURLAcceptsGitHubOwnedActionsHostsOnly(t *testing.T) {
	for _, host := range []string{"pipelines.actions.githubusercontent.com", "vstoken.actions.githubusercontent.com", "oidc.actions.githubusercontent.com"} {
		raw := "https://" + host + "/_apis/distributedtask/hubs/build/plans/plan/jobs/job/idtoken?api-version=2.0"
		if _, err := actionsOIDCRequestURL(raw, "https://opsidev.site/v1/build-records"); err != nil {
			t.Fatalf("host %s rejected: %v", host, err)
		}
	}
	for _, host := range []string{"actions.githubusercontent.com", "actions.githubusercontent.com.evil.example", "evil.example"} {
		raw := "https://" + host + "/_apis/distributedtask/hubs/build/plans/plan/jobs/job/idtoken?api-version=2.0"
		if _, err := actionsOIDCRequestURL(raw, "https://opsidev.site/v1/build-records"); err == nil {
			t.Fatalf("host %s accepted", host)
		}
	}
}

func TestSubmitBuildRecordFromGitHubActionsRejectsUnsafeEnvironmentAndURLs(t *testing.T) {
	base := validActionsBuildRecordInput()
	for name, mutate := range map[string]func(*actionsBuildRecordInput){
		"not actions": func(input *actionsBuildRecordInput) { input.GitHubActions = "false" },
		"cloud path":  func(input *actionsBuildRecordInput) { input.CloudURL = "https://opsidev.site/other" },
		"cloud query": func(input *actionsBuildRecordInput) { input.CloudURL = "https://opsidev.site?x=1" },
		"token http": func(input *actionsBuildRecordInput) {
			input.TokenRequestURL = "http://pipelines.actions.githubusercontent.com/token"
		},
		"token host": func(input *actionsBuildRecordInput) { input.TokenRequestURL = "https://evil.example/token" },
		"token path": func(input *actionsBuildRecordInput) {
			input.TokenRequestURL = "https://pipelines.actions.githubusercontent.com/?api-version=2.0"
		},
		"token query": func(input *actionsBuildRecordInput) {
			input.TokenRequestURL = "https://pipelines.actions.githubusercontent.com/_apis/distributedtask/hubs/build/plans/plan/jobs/job/idtoken?api-version=1.0"
		},
		"tag digest":     func(input *actionsBuildRecordInput) { input.OCIDigest = "latest" },
		"wrong platform": func(input *actionsBuildRecordInput) { input.Platform = "linux/arm64" },
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			mutate(&input)
			client := &http.Client{Transport: actionsRoundTripper(func(*http.Request) (*http.Response, error) {
				t.Fatal("HTTP must not run for invalid input")
				return nil, nil
			})}
			if _, err := submitBuildRecordFromGitHubActions(context.Background(), input, client); err == nil {
				t.Fatal("invalid GitHub Actions input was accepted")
			}
		})
	}
}

func TestSubmitBuildRecordErrorsDoNotLeakTokens(t *testing.T) {
	input := validActionsBuildRecordInput()
	input.RequestToken = "request-token-secret-marker"
	client := &http.Client{Transport: actionsRoundTripper(func(*http.Request) (*http.Response, error) {
		return actionsResponse(http.StatusUnauthorized, `{"error":"request-token-secret-marker"}`), nil
	})}
	_, err := submitBuildRecordFromGitHubActions(context.Background(), input, client)
	if err == nil || strings.Contains(err.Error(), input.RequestToken) {
		t.Fatalf("error=%v", err)
	}
}

func TestSubmitBuildRecordReturnsOnlyTypedCloudErrorCode(t *testing.T) {
	input := validActionsBuildRecordInput()
	var calls int
	client := &http.Client{Transport: actionsRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return actionsResponse(http.StatusOK, `{"value":"`+actionsTestOIDCToken(t, input, "")+`"}`), nil
		}
		return actionsResponse(http.StatusForbidden, `{"error_code":"BUILD_WORKLOAD_FORBIDDEN","message":"secret-marker"}`), nil
	})}
	_, err := submitBuildRecordFromGitHubActions(context.Background(), input, client)
	if err == nil || !strings.Contains(err.Error(), "BUILD_WORKLOAD_FORBIDDEN") || strings.Contains(err.Error(), "secret-marker") {
		t.Fatalf("error=%v", err)
	}
}

func validActionsBuildRecordInput() actionsBuildRecordInput {
	return actionsBuildRecordInput{
		CloudURL: "https://opsidev.site", ServiceKey: "api",
		ConfigHash: strings.Repeat("a", 64), PlanHash: strings.Repeat("b", 64),
		Platform: "linux/amd64", OCIRepository: "ghcr.io/huutawn/opsi-r5-005-fixture/api",
		OCIDigest: "sha256:" + strings.Repeat("c", 64), GitHubActions: "true",
		TokenRequestURL: "https://pipelines.actions.githubusercontent.com/_apis/distributedtask/hubs/build/plans/plan/jobs/job/idtoken?api-version=2.0",
		RequestToken:    "runner-token", RepositoryID: "1304594095", OwnerID: "143307746",
		Ref: "refs/heads/main", SHA: strings.Repeat("d", 40), EventName: "push",
		WorkflowRef: "huutawn/opsi-r5-005-fixture/.github/workflows/opsi-cd.yaml@refs/heads/main",
		RunID:       "123", RunAttempt: "1",
	}
}

func actionsResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(body))}
}

func actionsTestOIDCToken(t *testing.T, input actionsBuildRecordInput, jobWorkflowRef string) string {
	t.Helper()
	claims := map[string]string{
		"aud": "https://opsidev.site/v1/build-records", "repository_id": input.RepositoryID,
		"repository_owner_id": input.OwnerID, "ref": input.Ref, "sha": input.SHA,
		"event_name": input.EventName, "workflow_ref": input.WorkflowRef,
		"run_id": input.RunID, "run_attempt": input.RunAttempt,
	}
	if jobWorkflowRef != "" {
		claims["job_workflow_ref"] = jobWorkflowRef
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
