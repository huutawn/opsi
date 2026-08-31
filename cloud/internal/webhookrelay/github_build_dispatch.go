package webhookrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
)

const githubRepositoryBaseURL = "https://api.github.com/repos/"

func (c *GitHubAppClient) DispatchWorkflow(ctx context.Context, config buildjob.ExecutorConfig, buildJobID, attemptID string) (buildjob.DispatchFacts, error) {
	if err := config.Validate(); err != nil || !validOpaqueGitHubInput(buildJobID) || !validOpaqueGitHubInput(attemptID) {
		return buildjob.DispatchFacts{}, errors.New("executor dispatch metadata is invalid")
	}
	installationID, err := c.repositoryInstallation(ctx, config.Owner, config.Repository)
	if err != nil {
		return buildjob.DispatchFacts{}, err
	}
	token, _, err := c.InstallationToken(ctx, installationID)
	if err != nil {
		return buildjob.DispatchFacts{}, errors.New("executor installation token is unavailable")
	}
	body, err := json.Marshal(map[string]any{"ref": config.DispatchRef(), "inputs": map[string]string{"build_job_id": buildJobID, "attempt_id": attemptID}})
	if err != nil {
		return buildjob.DispatchFacts{}, errors.New("executor dispatch request is unavailable")
	}
	endpoint := githubRepositoryBaseURL + url.PathEscape(config.Owner) + "/" + url.PathEscape(config.Repository) + "/actions/workflows/" + url.PathEscape(config.Workflow) + "/dispatches"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return buildjob.DispatchFacts{}, errors.New("executor dispatch request is unavailable")
	}
	setGitHubAPIHeaders(request, "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return buildjob.DispatchFacts{}, errors.New("executor dispatch request failed")
	}
	defer response.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, githubResponseMaxBytes+1))
	if readErr != nil || len(responseBody) > githubResponseMaxBytes || response.StatusCode != http.StatusNoContent {
		return buildjob.DispatchFacts{}, errors.New("executor dispatch was rejected")
	}
	return buildjob.DispatchFacts{}, nil
}

func (c *GitHubAppClient) CancelWorkflow(ctx context.Context, config buildjob.ExecutorConfig, runID uint64) error {
	if err := config.Validate(); err != nil || runID == 0 {
		return errors.New("executor cancellation metadata is invalid")
	}
	installationID, err := c.repositoryInstallation(ctx, config.Owner, config.Repository)
	if err != nil {
		return err
	}
	token, _, err := c.InstallationToken(ctx, installationID)
	if err != nil {
		return errors.New("executor installation token is unavailable")
	}
	endpoint := githubRepositoryBaseURL + url.PathEscape(config.Owner) + "/" + url.PathEscape(config.Repository) + "/actions/runs/" + strconv.FormatUint(runID, 10) + "/cancel"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return errors.New("executor cancellation request is unavailable")
	}
	setGitHubAPIHeaders(request, "Bearer "+token)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return errors.New("executor cancellation request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted && response.StatusCode != http.StatusConflict {
		return errors.New("executor cancellation was rejected")
	}
	return nil
}

func (c *GitHubAppClient) repositoryInstallation(ctx context.Context, owner, repository string) (int64, error) {
	jwt, err := c.appJWT()
	if err != nil {
		return 0, errors.New("executor GitHub App authority is unavailable")
	}
	endpoint := githubRepositoryBaseURL + url.PathEscape(owner) + "/" + url.PathEscape(repository) + "/installation"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, errors.New("executor installation lookup is unavailable")
	}
	setGitHubAPIHeaders(request, "Bearer "+jwt)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return 0, errors.New("executor installation lookup failed")
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, githubResponseMaxBytes+1))
	if readErr != nil || len(body) > githubResponseMaxBytes || response.StatusCode != http.StatusOK {
		return 0, errors.New("executor repository is outside GitHub App authority")
	}
	var payload struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.ID <= 0 {
		return 0, errors.New("executor installation identity is invalid")
	}
	return payload.ID, nil
}

func setGitHubAPIHeaders(request *http.Request, authorization string) {
	request.Header.Set("Authorization", authorization)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	request.Header.Set("User-Agent", githubUserAgent)
}

func validOpaqueGitHubInput(value string) bool {
	return value != "" && len(value) <= 128 && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}
