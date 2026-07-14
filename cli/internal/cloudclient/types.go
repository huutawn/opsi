package cloudclient

import "fmt"

type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("Cloud API returned status %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("Cloud API %s (status %d): %s", e.Code, e.Status, e.Message)
}

type Service struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
}

type GitHubInstallation struct {
	InstallationID int64  `json:"installation_id"`
	AccountLogin   string `json:"account_login"`
	Status         string `json:"status"`
	Suspended      bool   `json:"suspended"`
}

type GitHubRepository struct {
	RepositoryID   int64  `json:"repository_id"`
	InstallationID int64  `json:"installation_id"`
	OwnerLogin     string `json:"owner_login"`
	Name           string `json:"name"`
	FullName       string `json:"full_name"`
	Archived       bool   `json:"archived"`
	Disabled       bool   `json:"disabled"`
	DefaultBranch  string `json:"default_branch"`
	Status         string `json:"status"`
}

type GitHubBinding struct {
	ID             string `json:"id"`
	ProjectID      string `json:"project_id"`
	ServiceID      string `json:"service_id"`
	RepositoryID   int64  `json:"repository_id"`
	InstallationID int64  `json:"installation_id"`
	ServiceKey     string `json:"service_key"`
	ConfigPath     string `json:"config_path"`
	Status         string `json:"status"`
}

type RepositoryClaim struct {
	RepositoryID int64  `json:"repository_id"`
	ProjectID    string `json:"project_id"`
	Status       string `json:"status"`
}

type InstallationClaimStart struct {
	AuthorizationURL string `json:"authorization_url"`
}

type InstallationClaimResult struct {
	Installation       GitHubInstallation `json:"installation"`
	RepositoriesSynced int                `json:"repositories_synced"`
}
