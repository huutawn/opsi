package registry

import (
	"errors"
	"path"
	"strings"
	"unicode"
)

const (
	BuildStrategyAuto       = "auto"
	BuildStrategyDockerfile = "dockerfile"
	BuildStrategyBuildpack  = "buildpack"
)

type GitHubSource struct {
	SelectedRef     string `json:"selected_ref"`
	ApplicationRoot string `json:"application_root"`
	BuildContext    string `json:"build_context"`
	BuildStrategy   string `json:"build_strategy"`
	DockerfilePath  string `json:"dockerfile_path,omitempty"`
}

func (s *Service) GetGitHubServiceBinding(projectID, bindingID string) (GitHubServiceBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	binding, ok := s.githubServiceBindings[bindingID]
	if !ok || binding.ProjectID != projectID {
		return GitHubServiceBinding{}, ErrNotFound
	}
	return binding, nil
}

func (s *Service) UpdateGitHubServiceBinding(projectID, bindingID, userID string, source GitHubSource) (GitHubServiceBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	project, ok := s.projects[projectID]
	if !ok {
		return GitHubServiceBinding{}, ErrNotFound
	}
	binding, ok := s.githubServiceBindings[bindingID]
	if !ok || binding.ProjectID != projectID || binding.Status != GitHubLinkActive {
		return GitHubServiceBinding{}, ErrNotFound
	}
	repository, ok := s.githubRepositories[binding.RepositoryID]
	if !ok {
		return GitHubServiceBinding{}, ErrNotFound
	}
	if err := normalizeGitHubSource(&source, repository.DefaultBranch); err != nil {
		return GitHubServiceBinding{}, err
	}
	if binding.GitHubSource == source {
		return binding, nil
	}
	binding.GitHubSource = source
	binding.UpdatedAt = s.clock()
	s.githubServiceBindings[bindingID] = binding
	s.appendGitHubAuditLocked(project, userID, "github.service_binding.source_updated", "github_service_binding", binding.ID, map[string]any{"installation_id": binding.InstallationID, "repository_id": binding.RepositoryID, "service_id": binding.ServiceID, "service_key": binding.ServiceKey}, binding.UpdatedAt)
	return binding, nil
}

func validateGitHubBindingIdentity(draft GitHubServiceBindingDraft) error {
	if draft.CreatedBy == "" || draft.ServiceID == "" || draft.RepositoryID <= 0 || !validGitHubServiceKey(draft.ServiceKey) {
		return githubInvalid("GITHUB_BINDING_INVALID", "service, repository, creator, and valid service_key are required")
	}
	return nil
}

func normalizeGitHubBindingDraft(draft *GitHubServiceBindingDraft, repository GitHubRepository, service ServiceRecord) error {
	if draft.ConfigPath == "" {
		draft.ConfigPath = DefaultGitHubConfigPath
	}
	if !validGitHubConfigPath(draft.ConfigPath) {
		return githubInvalid("GITHUB_CONFIG_PATH_INVALID", "config_path must be a safe relative slash-separated path")
	}
	if draft.GitHubSource == (GitHubSource{}) {
		draft.GitHubSource = legacyGitHubSource(repository, service)
	}
	return normalizeGitHubSource(&draft.GitHubSource, repository.DefaultBranch)
}

func legacyGitHubSource(repository GitHubRepository, service ServiceRecord) GitHubSource {
	// Registry owns this compatibility path until legacy service source callers are retired and every binding is canonical.
	context := service.BuildContext
	if context == "" {
		context = "."
	}
	source := GitHubSource{SelectedRef: service.Branch, ApplicationRoot: context, BuildContext: context, BuildStrategy: BuildStrategyAuto}
	if service.BuildMethod == BuildStrategyBuildpack {
		source.BuildStrategy = BuildStrategyBuildpack
	} else if service.BuildMethod == BuildStrategyDockerfile && service.Dockerfile != "" {
		source.BuildStrategy = BuildStrategyDockerfile
		source.DockerfilePath = service.Dockerfile
	}
	if err := normalizeGitHubSource(&source, repository.DefaultBranch); err != nil {
		return GitHubSource{SelectedRef: repository.DefaultBranch, ApplicationRoot: ".", BuildContext: ".", BuildStrategy: BuildStrategyAuto}
	}
	return source
}

func normalizeGitHubSource(source *GitHubSource, defaultRef string) error {
	if source.SelectedRef == "" {
		source.SelectedRef = defaultRef
	}
	if source.SelectedRef == "" {
		source.SelectedRef = "main"
	}
	if !validGitHubMetadata(source.SelectedRef, true) || strings.TrimSpace(source.SelectedRef) != source.SelectedRef {
		return githubInvalid("GITHUB_SELECTED_REF_INVALID", "selected_ref must be a non-empty Git ref")
	}
	if source.ApplicationRoot == "" {
		source.ApplicationRoot = "."
	}
	if source.BuildContext == "" {
		source.BuildContext = "."
	}
	var err error
	if source.ApplicationRoot, err = normalizeRepositoryPath(source.ApplicationRoot); err != nil {
		return githubInvalid("GITHUB_APPLICATION_ROOT_INVALID", "application_root must be a normalized repository-relative POSIX path")
	}
	if source.BuildContext, err = normalizeRepositoryPath(source.BuildContext); err != nil {
		return githubInvalid("GITHUB_BUILD_CONTEXT_INVALID", "build_context must be a normalized repository-relative POSIX path")
	}
	if source.BuildContext != "." && source.ApplicationRoot != source.BuildContext && !strings.HasPrefix(source.ApplicationRoot, source.BuildContext+"/") {
		return githubInvalid("GITHUB_SOURCE_PATH_RELATION_INVALID", "application_root must be inside build_context")
	}
	if source.BuildStrategy == "" {
		source.BuildStrategy = BuildStrategyAuto
	}
	if source.BuildStrategy != BuildStrategyAuto && source.BuildStrategy != BuildStrategyDockerfile && source.BuildStrategy != BuildStrategyBuildpack {
		return githubInvalid("GITHUB_BUILD_STRATEGY_INVALID", "build_strategy must be auto, dockerfile, or buildpack")
	}
	if source.DockerfilePath != "" {
		if source.DockerfilePath, err = normalizeRepositoryPath(source.DockerfilePath); err != nil || source.DockerfilePath == "." {
			return githubInvalid("GITHUB_DOCKERFILE_PATH_INVALID", "dockerfile_path must be a normalized repository-relative POSIX file path")
		}
	}
	if source.BuildStrategy == BuildStrategyDockerfile && source.DockerfilePath == "" {
		return githubInvalid("GITHUB_DOCKERFILE_PATH_REQUIRED", "dockerfile_path is required for dockerfile strategy")
	}
	return nil
}

func normalizeRepositoryPath(value string) (string, error) {
	if value == "" || len(value) > 1024 || path.IsAbs(value) || strings.Contains(value, "\\") || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", errors.New("invalid repository path")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return "", errors.New("repository path escapes its root")
		}
	}
	cleaned := path.Clean(value)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("repository path escapes its root")
	}
	return cleaned, nil
}
