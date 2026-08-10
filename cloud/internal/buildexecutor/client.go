package buildexecutor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
	Lease   []byte
}

func (c Client) BuildSpec(ctx context.Context, jobID string) (buildjob.BuildSpec, error) {
	var spec buildjob.BuildSpec
	endpoint := strings.TrimSuffix(c.BaseURL, "/") + "/v1/build-runner/build-spec?build_job_id=" + url.QueryEscape(jobID)
	if err := c.do(ctx, http.MethodGet, endpoint, nil, &spec); err != nil {
		return buildjob.BuildSpec{}, err
	}
	if err := spec.Validate(); err != nil {
		return buildjob.BuildSpec{}, Error{Code: "BUILD_SPEC_INVALID", Phase: "contract", Message: "canonical Build Spec is invalid"}
	}
	return spec, nil
}

func (c Client) SourceAccess(ctx context.Context, jobID, attemptID string, runID uint64, runAttempt uint32) (SourceAccess, error) {
	body, err := json.Marshal(map[string]any{"build_job_id": jobID, "attempt_id": attemptID, "github_run_id": runID, "github_run_attempt": runAttempt})
	if err != nil {
		return SourceAccess{}, Error{Code: "SOURCE_ACCESS_DENIED", Phase: "source", Message: "source access request is unavailable"}
	}
	var access SourceAccess
	if err := c.do(ctx, http.MethodPost, strings.TrimSuffix(c.BaseURL, "/")+"/v1/build-runner/source-access", body, &access); err != nil {
		return SourceAccess{}, err
	}
	if access.BuildJobID != jobID || access.Repository == "" || access.RepositoryID <= 0 || access.GitHubInstallationID <= 0 || access.ResolvedCommitSHA == "" || len(access.AccessToken) == 0 || !access.ExpiresAt.After(time.Now().UTC()) {
		access.AccessToken.destroy()
		return SourceAccess{}, Error{Code: "SOURCE_TOKEN_UNAVAILABLE", Phase: "source", Message: "source credential response is invalid"}
	}
	return access, nil
}

func (c Client) Complete(ctx context.Context, result Result) (buildjob.CompletionResult, error) {
	request := buildjob.RunnerResult{
		BuildJobID: result.BuildJobID, AttemptID: result.AttemptID, RegistryReference: result.RegistryReference, Digest: result.ImageDigest,
		Executor: buildjob.ExecutorResult{Platform: result.Platform, BuildKitVersion: result.BuildKitVersion, BuildxVersion: result.BuildxVersion, BuilderIdentity: BuildKitImage, StartedAt: result.StartedAt, CompletedAt: result.CompletedAt, BuildDescriptor: result.BuildDescriptor, Remote: result.Remote},
	}
	body, err := json.Marshal(request)
	if err != nil {
		return buildjob.CompletionResult{}, Error{Code: "EXECUTOR_INFRASTRUCTURE_FAILED", Phase: "completion", Message: "runner result cannot be encoded"}
	}
	var response buildjob.CompletionResult
	if err := c.do(ctx, http.MethodPost, strings.TrimSuffix(c.BaseURL, "/")+"/v1/build-runner/result", body, &response); err != nil {
		return buildjob.CompletionResult{}, err
	}
	return response, nil
}

func (c Client) Fail(ctx context.Context, jobID, attemptID, code string) error {
	body, err := json.Marshal(buildjob.RunnerFailure{BuildJobID: jobID, AttemptID: attemptID, Code: code})
	if err != nil {
		return Error{Code: "EXECUTOR_INFRASTRUCTURE_FAILED", Phase: "completion", Message: "runner failure cannot be encoded"}
	}
	var response struct {
		Status string `json:"status"`
	}
	return c.do(ctx, http.MethodPost, strings.TrimSuffix(c.BaseURL, "/")+"/v1/build-runner/failure", body, &response)
}

func (c Client) do(ctx context.Context, method, endpoint string, body []byte, target any) error {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return Error{Code: "SOURCE_ACCESS_DENIED", Phase: "source", Message: "runner request is invalid"}
	}
	request.Header.Set("Authorization", "Bearer "+string(c.Lease))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return Error{Code: "SOURCE_ACCESS_DENIED", Phase: "source", Message: "Cloud runner endpoint is unavailable"}
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var failure struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&failure)
		code := failure.Code
		if code == "" {
			code = "SOURCE_ACCESS_DENIED"
		}
		return Error{Code: code, Phase: "source", Message: "Cloud denied runner access with HTTP " + strconv.Itoa(response.StatusCode)}
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
		return Error{Code: "SOURCE_TOKEN_UNAVAILABLE", Phase: "source", Message: "Cloud runner response is invalid"}
	}
	return nil
}
