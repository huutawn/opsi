package repository

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"unicode"
)

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

type GitHubOrigin struct {
	Owner    string
	Name     string
	FullName string
}

type LocalRepository struct {
	Root   string
	Origin GitHubOrigin
}

func Detect(ctx context.Context, runner CommandRunner, repoDir string) (LocalRepository, error) {
	if runner == nil {
		runner = ExecRunner{}
	}
	rootOutput, err := runner.Run(ctx, "git", "-C", repoDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return LocalRepository{}, fmt.Errorf("detect Git repository root: %w", err)
	}
	root, err := cleanGitOutput(rootOutput)
	if err != nil {
		return LocalRepository{}, fmt.Errorf("invalid Git repository root: %w", err)
	}
	root = filepath.Clean(root)
	originOutput, err := runner.Run(ctx, "git", "-C", root, "remote", "get-url", "origin")
	if err != nil {
		return LocalRepository{}, fmt.Errorf("read Git origin: %w", err)
	}
	originText, err := cleanGitOutput(originOutput)
	if err != nil {
		return LocalRepository{}, fmt.Errorf("invalid Git origin: %w", err)
	}
	origin, err := ParseGitHubOrigin(originText)
	if err != nil {
		return LocalRepository{}, err
	}
	return LocalRepository{Root: root, Origin: origin}, nil
}

func ParseGitHubOrigin(raw string) (GitHubOrigin, error) {
	if raw == "" || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return GitHubOrigin{}, errors.New("Git origin contains an empty value or control character")
	}
	if strings.HasPrefix(raw, "git@github.com:") {
		remainder := strings.TrimPrefix(raw, "git@github.com:")
		if strings.Contains(remainder, ":") {
			return GitHubOrigin{}, errors.New("Git origin uses malformed SCP syntax")
		}
		return originFromPath(remainder)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return GitHubOrigin{}, errors.New("Git origin is not a supported GitHub URL")
	}
	if !strings.EqualFold(u.Hostname(), "github.com") || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return GitHubOrigin{}, errors.New("only github.com origins are supported")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		if u.User != nil {
			return GitHubOrigin{}, errors.New("credential-bearing Git origins are not allowed")
		}
	case "ssh":
		if u.User == nil || u.User.Username() != "git" {
			return GitHubOrigin{}, errors.New("SSH GitHub origin must use the git user")
		}
		if _, set := u.User.Password(); set {
			return GitHubOrigin{}, errors.New("credential-bearing Git origins are not allowed")
		}
	default:
		return GitHubOrigin{}, errors.New("Git origin scheme is not supported")
	}
	return originFromPath(strings.TrimPrefix(u.Path, "/"))
}

func originFromPath(value string) (GitHubOrigin, error) {
	value = strings.TrimSuffix(value, ".git")
	if value == "" || path.Clean(value) != value {
		return GitHubOrigin{}, errors.New("GitHub origin path is malformed")
	}
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return GitHubOrigin{}, errors.New("GitHub origin must contain exactly owner/repository")
	}
	if strings.IndexFunc(parts[0]+parts[1], unicode.IsControl) >= 0 {
		return GitHubOrigin{}, errors.New("GitHub origin contains a control character")
	}
	return GitHubOrigin{Owner: parts[0], Name: parts[1], FullName: parts[0] + "/" + parts[1]}, nil
}

func cleanGitOutput(value []byte) (string, error) {
	cleaned := strings.TrimSpace(string(value))
	if cleaned == "" || strings.IndexFunc(cleaned, unicode.IsControl) >= 0 {
		return "", errors.New("command returned unsafe output")
	}
	return cleaned, nil
}

type InventoryRepository struct {
	RepositoryID   int64
	InstallationID int64
	FullName       string
	DefaultBranch  string
	Status         string
	Archived       bool
	Disabled       bool
}

var ErrRepositoryNotFound = errors.New("repository is not present in Cloud inventory")

func MatchInventory(repositories []InventoryRepository, origin GitHubOrigin, explicitID int64) (InventoryRepository, error) {
	if explicitID > 0 {
		for _, candidate := range repositories {
			if candidate.RepositoryID != explicitID {
				continue
			}
			if !strings.EqualFold(candidate.FullName, origin.FullName) {
				return InventoryRepository{}, fmt.Errorf("repository ID %d metadata %q does not match local origin %q", explicitID, candidate.FullName, origin.FullName)
			}
			if err := validateInventoryRepository(candidate); err != nil {
				return InventoryRepository{}, err
			}
			return candidate, nil
		}
		return InventoryRepository{}, fmt.Errorf("%w: repository ID %d does not exist in the project inventory", ErrRepositoryNotFound, explicitID)
	}
	var matches []InventoryRepository
	for _, candidate := range repositories {
		if strings.EqualFold(candidate.FullName, origin.FullName) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 0 {
		return InventoryRepository{}, ErrRepositoryNotFound
	}
	if len(matches) != 1 {
		return InventoryRepository{}, fmt.Errorf("Cloud inventory contains %d matches for %s", len(matches), origin.FullName)
	}
	if err := validateInventoryRepository(matches[0]); err != nil {
		return InventoryRepository{}, err
	}
	return matches[0], nil
}

func validateInventoryRepository(candidate InventoryRepository) error {
	if candidate.RepositoryID <= 0 || candidate.InstallationID <= 0 || candidate.Status != "active" || candidate.Archived || candidate.Disabled {
		return fmt.Errorf("repository %s (%d) is inactive, archived, or disabled", candidate.FullName, candidate.RepositoryID)
	}
	return nil
}
