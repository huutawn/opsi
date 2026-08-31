package webhookrelay

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type GitHubRepositoryExport struct {
	Branch            string `json:"branch"`
	CommitSHA         string `json:"commit_sha"`
	PullRequestNumber int    `json:"pull_request_number"`
	PullRequestURL    string `json:"pull_request_url"`
	Reused            bool   `json:"reused"`
}

func (c *GitHubAppClient) ExportRepositoryConfig(ctx context.Context, installationID, repositoryID int64, repository, sourceSHA, targetBranch, exportBranch string, content []byte) (GitHubRepositoryExport, error) {
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || !validGitHubPathPart(parts[0]) || !validGitHubPathPart(parts[1]) || !validSHA40(sourceSHA) || targetBranch == "" || exportBranch == "" || len(content) == 0 {
		return GitHubRepositoryExport{}, errors.New("repository export identity is invalid")
	}
	token, _, err := c.RepositoryWriteToken(ctx, installationID, repositoryID)
	if err != nil {
		return GitHubRepositoryExport{}, err
	}
	if existing, ok, findErr := c.findExportPullRequest(ctx, token, parts, targetBranch, exportBranch); findErr != nil {
		return GitHubRepositoryExport{}, findErr
	} else if ok {
		existing.Reused = true
		return existing, nil
	}
	refPath := "/git/ref/heads/" + escapeGitHubPath(exportBranch)
	refBody, status, err := c.repositoryAPIWithToken(ctx, token, parts, http.MethodGet, refPath, nil)
	if err != nil {
		return GitHubRepositoryExport{}, err
	}
	switch status {
	case http.StatusNotFound:
		payload, _ := json.Marshal(map[string]string{"ref": "refs/heads/" + exportBranch, "sha": sourceSHA})
		_, status, err = c.repositoryAPIWithToken(ctx, token, parts, http.MethodPost, "/git/refs", payload)
		if err != nil {
			return GitHubRepositoryExport{}, err
		}
		if status != http.StatusCreated {
			return GitHubRepositoryExport{}, githubExportStatusError(status)
		}
	case http.StatusOK:
		var ref struct {
			Object struct {
				SHA string `json:"sha"`
			} `json:"object"`
		}
		if json.Unmarshal(refBody, &ref) != nil || !validSHA40(ref.Object.SHA) {
			return GitHubRepositoryExport{}, errors.New("repository export branch metadata is invalid")
		}
		if !strings.EqualFold(ref.Object.SHA, sourceSHA) {
			return GitHubRepositoryExport{}, errors.New("repository export branch no longer points at the approved source commit")
		}
	default:
		return GitHubRepositoryExport{}, githubExportStatusError(status)
	}
	filePath := "/contents/.opsi/opsi-cd.yaml?ref=" + url.QueryEscape(exportBranch)
	fileBody, fileStatus, err := c.repositoryAPIWithToken(ctx, token, parts, http.MethodGet, filePath, nil)
	if err != nil {
		return GitHubRepositoryExport{}, err
	}
	fileSHA, current := "", []byte(nil)
	if fileStatus == http.StatusOK {
		var file struct{ SHA, Content, Encoding string }
		if json.Unmarshal(fileBody, &file) != nil || file.Encoding != "base64" {
			return GitHubRepositoryExport{}, errors.New("repository export file metadata is invalid")
		}
		fileSHA = file.SHA
		current, _ = base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	} else if fileStatus != http.StatusNotFound {
		return GitHubRepositoryExport{}, githubExportStatusError(fileStatus)
	}
	commitSHA := sourceSHA
	if !bytes.Equal(current, content) {
		payload := map[string]any{"message": "docs(opsi): export deployment configuration", "content": base64.StdEncoding.EncodeToString(content), "branch": exportBranch}
		if fileSHA != "" {
			payload["sha"] = fileSHA
		}
		encoded, _ := json.Marshal(payload)
		response, status, requestErr := c.repositoryAPIWithToken(ctx, token, parts, http.MethodPut, "/contents/.opsi/opsi-cd.yaml", encoded)
		if requestErr != nil {
			return GitHubRepositoryExport{}, requestErr
		}
		if status != http.StatusCreated && status != http.StatusOK {
			return GitHubRepositoryExport{}, githubExportStatusError(status)
		}
		var written struct {
			Commit struct {
				SHA string `json:"sha"`
			} `json:"commit"`
		}
		if json.Unmarshal(response, &written) != nil || !validSHA40(written.Commit.SHA) {
			return GitHubRepositoryExport{}, errors.New("repository export commit response is invalid")
		}
		commitSHA = written.Commit.SHA
	}
	payload, _ := json.Marshal(map[string]any{"title": "Export Opsi deployment configuration", "head": exportBranch, "base": targetBranch, "body": "Documents the reviewed Opsi deployment plan for future repository analyses. This pull request does not change the current immutable deployment run."})
	response, status, err := c.repositoryAPIWithToken(ctx, token, parts, http.MethodPost, "/pulls", payload)
	if err != nil {
		return GitHubRepositoryExport{}, err
	}
	if status == http.StatusUnprocessableEntity {
		if existing, ok, findErr := c.findExportPullRequest(ctx, token, parts, targetBranch, exportBranch); findErr == nil && ok {
			existing.Reused = true
			return existing, nil
		}
	}
	if status != http.StatusCreated {
		return GitHubRepositoryExport{}, githubExportStatusError(status)
	}
	var pull struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if json.Unmarshal(response, &pull) != nil || pull.Number < 1 || pull.HTMLURL == "" {
		return GitHubRepositoryExport{}, errors.New("repository export pull request response is invalid")
	}
	return GitHubRepositoryExport{Branch: exportBranch, CommitSHA: commitSHA, PullRequestNumber: pull.Number, PullRequestURL: pull.HTMLURL}, nil
}

func (c *GitHubAppClient) findExportPullRequest(ctx context.Context, token string, repository []string, base, branch string) (GitHubRepositoryExport, bool, error) {
	suffix := "/pulls?state=open&base=" + url.QueryEscape(base) + "&head=" + url.QueryEscape(repository[0]+":"+branch)
	body, status, err := c.repositoryAPIWithToken(ctx, token, repository, http.MethodGet, suffix, nil)
	if err != nil {
		return GitHubRepositoryExport{}, false, err
	}
	if status != http.StatusOK {
		return GitHubRepositoryExport{}, false, githubExportStatusError(status)
	}
	var pulls []struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Head    struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if json.Unmarshal(body, &pulls) != nil {
		return GitHubRepositoryExport{}, false, errors.New("repository export pull request list is invalid")
	}
	for _, pull := range pulls {
		if pull.Number > 0 && pull.HTMLURL != "" && pull.Head.Ref == branch && validSHA40(pull.Head.SHA) {
			return GitHubRepositoryExport{Branch: branch, CommitSHA: pull.Head.SHA, PullRequestNumber: pull.Number, PullRequestURL: pull.HTMLURL}, true, nil
		}
	}
	return GitHubRepositoryExport{}, false, nil
}
func (c *GitHubAppClient) repositoryAPIWithToken(ctx context.Context, token string, repository []string, method, suffix string, payload []byte) ([]byte, int, error) {
	endpoint := "https://api.github.com/repos/" + url.PathEscape(repository[0]) + "/" + url.PathEscape(repository[1]) + suffix
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, errors.New("repository export request is invalid")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	request.Header.Set("User-Agent", githubUserAgent)
	if len(payload) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, 0, errors.New("repository export GitHub request failed")
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, githubResponseMaxBytes+1))
	if readErr != nil || len(body) > githubResponseMaxBytes {
		return nil, 0, errors.New("repository export GitHub response is invalid")
	}
	return body, response.StatusCode, nil
}
func escapeGitHubPath(value string) string {
	parts := strings.Split(value, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}
func githubExportStatusError(status int) error {
	if status == http.StatusForbidden || status == http.StatusUnauthorized {
		return errGitHubRepositoryWriteDenied
	}
	return fmt.Errorf("repository export GitHub request returned status %d", status)
}
