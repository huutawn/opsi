package registry

import (
	"context"
	"database/sql"
	"errors"

	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
)

func (s *Service) ResolveBuildJobSource(_ context.Context, projectID, applicationID string) (buildjob.ApplicationSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	service, ok := s.services[applicationID]
	if !ok || service.ProjectID != projectID || service.Status == "deleted" {
		return buildjob.ApplicationSource{}, invalidBuildJobSourceScope()
	}
	var binding GitHubServiceBinding
	for _, candidate := range s.githubServiceBindings {
		if candidate.ProjectID == projectID && candidate.ServiceID == applicationID && candidate.Status == GitHubLinkActive {
			binding = candidate
			break
		}
	}
	if binding.ID == "" {
		return buildjob.ApplicationSource{}, invalidBuildJobSourceScope()
	}
	repository, ok := s.githubRepositories[binding.RepositoryID]
	if !ok || repository.Status != GitHubRepositoryActive || repository.Archived || repository.Disabled || repository.InstallationID != binding.InstallationID {
		return buildjob.ApplicationSource{}, buildjob.Error{Code: "GITHUB_REPOSITORY_UNAVAILABLE", Status: 409, Message: "The bound GitHub repository is unavailable.", Cause: "github_repository"}
	}
	installation, ok := s.githubInstallations[binding.InstallationID]
	if !ok || installation.Status != GitHubInstallationActive || installation.Suspended {
		return buildjob.ApplicationSource{}, buildjob.Error{Code: "GITHUB_INSTALLATION_UNAVAILABLE", Status: 409, Message: "The bound GitHub installation is unavailable.", Cause: "github_installation"}
	}
	claim, ok := s.githubRepositoryClaims[binding.RepositoryID]
	if !ok || claim.ProjectID != projectID || claim.InstallationID != binding.InstallationID || claim.Status != GitHubLinkActive {
		return buildjob.ApplicationSource{}, invalidBuildJobSourceScope()
	}
	return s.buildJobApplicationSourceWithServices(service, binding, repository), nil
}

func (s PostgresService) ResolveBuildJobSource(ctx context.Context, projectID, applicationID string) (buildjob.ApplicationSource, error) {
	if s.DB == nil {
		return buildjob.ApplicationSource{}, buildjob.Error{Code: "BUILD_JOB_UNAVAILABLE", Status: 503, Message: "BuildJob authority is unavailable.", Cause: "source_store"}
	}
	var binding GitHubServiceBinding
	var environmentID, serviceStatus, repositoryStatus, installationStatus, claimStatus, defaultBranch string
	var repositoryOwnerID int64
	var repositoryFullName string
	var archived, disabled, suspended bool
	err := s.DB.QueryRowContext(ctx, `SELECT b.id,b.project_id,b.service_id,b.repository_id,b.installation_id,b.service_key,b.config_path,b.selected_ref,b.application_root,b.build_context,b.build_strategy,COALESCE(b.dockerfile_path,''),b.status,b.created_by,b.created_at,b.updated_at,b.removed_at,
		s.environment_id,s.status,r.owner_id,r.full_name,r.default_branch,r.status,r.archived,r.disabled,i.status,i.suspended,COALESCE(c.status,'')
		FROM github_service_bindings b
		JOIN control_services s ON s.id=b.service_id AND s.project_id=b.project_id
		JOIN github_repositories r ON r.repository_id=b.repository_id AND r.installation_id=b.installation_id
		JOIN github_installations i ON i.installation_id=b.installation_id
		LEFT JOIN github_repository_claims c ON c.repository_id=b.repository_id AND c.installation_id=b.installation_id AND c.project_id=b.project_id
		WHERE b.project_id=$1 AND b.service_id=$2 AND b.status='active'`, projectID, applicationID).Scan(
		&binding.ID, &binding.ProjectID, &binding.ServiceID, &binding.RepositoryID, &binding.InstallationID, &binding.ServiceKey, &binding.ConfigPath, &binding.SelectedRef, &binding.ApplicationRoot, &binding.BuildContext, &binding.BuildStrategy, &binding.DockerfilePath, &binding.Status, &binding.CreatedBy, &binding.CreatedAt, &binding.UpdatedAt, &binding.RemovedAt,
		&environmentID, &serviceStatus, &repositoryOwnerID, &repositoryFullName, &defaultBranch, &repositoryStatus, &archived, &disabled, &installationStatus, &suspended, &claimStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return buildjob.ApplicationSource{}, invalidBuildJobSourceScope()
	}
	if err != nil {
		return buildjob.ApplicationSource{}, buildjob.Error{Code: "BUILD_JOB_UNAVAILABLE", Status: 503, Message: "BuildJob authority is unavailable.", Cause: "source_store"}
	}
	if serviceStatus == "deleted" || binding.ProjectID != projectID || binding.ServiceID != applicationID || claimStatus != GitHubLinkActive {
		return buildjob.ApplicationSource{}, invalidBuildJobSourceScope()
	}
	if installationStatus != GitHubInstallationActive || suspended {
		return buildjob.ApplicationSource{}, buildjob.Error{Code: "GITHUB_INSTALLATION_UNAVAILABLE", Status: 409, Message: "The bound GitHub installation is unavailable.", Cause: "github_installation"}
	}
	if repositoryStatus != GitHubRepositoryActive || archived || disabled {
		return buildjob.ApplicationSource{}, buildjob.Error{Code: "GITHUB_REPOSITORY_UNAVAILABLE", Status: 409, Message: "The bound GitHub repository is unavailable.", Cause: "github_repository"}
	}
	if err := normalizeGitHubSource(&binding.GitHubSource, defaultBranch); err != nil {
		return buildjob.ApplicationSource{}, buildjob.Error{Code: "BUILD_SOURCE_INVALID", Status: 409, Message: "The source binding is invalid.", Cause: "source_binding"}
	}
	service, _ := s.getService(ctx, applicationID)
	services, _ := s.ListServices(projectID)
	buildDepState := ComputeBuildDependencyState(service.Configuration, services)
	buildEnv := ComputeBuildEnvironment(service.Configuration, services)
	return buildjob.ApplicationSource{
		ProjectID: projectID, EnvironmentID: environmentID, ApplicationID: applicationID, BindingID: binding.ID, BindingUpdatedAt: binding.UpdatedAt,
		InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID, RepositoryOwnerID: repositoryOwnerID,
		RepositoryFullName: repositoryFullName, SelectedRef: binding.SelectedRef, ApplicationRoot: binding.ApplicationRoot,
		BuildContext: binding.BuildContext, BuildStrategy: binding.BuildStrategy, DockerfilePath: binding.DockerfilePath,
		BuildDependencyState: buildDepState,
		BuildEnvironment:     buildEnv,
	}, nil
}

func (s *Service) buildJobApplicationSourceWithServices(service ServiceRecord, binding GitHubServiceBinding, repository GitHubRepository) buildjob.ApplicationSource {
	services := make([]ServiceRecord, 0, len(s.services))
	for _, s := range s.services {
		services = append(services, s)
	}
	buildDepState := ComputeBuildDependencyState(service.Configuration, services)
	buildEnv := ComputeBuildEnvironment(service.Configuration, services)
	return buildjob.ApplicationSource{
		ProjectID: binding.ProjectID, EnvironmentID: service.EnvironmentID, ApplicationID: binding.ServiceID, BindingID: binding.ID, BindingUpdatedAt: binding.UpdatedAt,
		InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID, RepositoryOwnerID: repository.OwnerID,
		RepositoryFullName: repository.FullName, SelectedRef: binding.SelectedRef, ApplicationRoot: binding.ApplicationRoot,
		BuildContext: binding.BuildContext, BuildStrategy: binding.BuildStrategy, DockerfilePath: binding.DockerfilePath,
		BuildDependencyState: buildDepState,
		BuildEnvironment:     buildEnv,
	}
}

func buildJobApplicationSource(service ServiceRecord, binding GitHubServiceBinding, repository GitHubRepository) buildjob.ApplicationSource {
	buildDepState := ComputeBuildDependencyState(service.Configuration, nil)
	buildEnv := ComputeBuildEnvironment(service.Configuration, nil)
	return buildjob.ApplicationSource{
		ProjectID: binding.ProjectID, EnvironmentID: service.EnvironmentID, ApplicationID: binding.ServiceID, BindingID: binding.ID, BindingUpdatedAt: binding.UpdatedAt,
		InstallationID: binding.InstallationID, RepositoryID: binding.RepositoryID, RepositoryOwnerID: repository.OwnerID,
		RepositoryFullName: repository.FullName, SelectedRef: binding.SelectedRef, ApplicationRoot: binding.ApplicationRoot,
		BuildContext: binding.BuildContext, BuildStrategy: binding.BuildStrategy, DockerfilePath: binding.DockerfilePath,
		BuildDependencyState: buildDepState,
		BuildEnvironment:     buildEnv,
	}
}

func invalidBuildJobSourceScope() error {
	return buildjob.Error{Code: "BUILD_SOURCE_INVALID_SCOPE", Status: 409, Message: "The active source binding does not belong to the requested Application.", Cause: "source_scope"}
}
