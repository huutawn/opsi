package repository

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type MutationRequest struct {
	Repository   string
	ConfigPath   string
	WorkflowPath string
	Service      ServiceV2
	Force        bool
	Confirmed    bool
}

type MutationPreview struct {
	Config       ConfigV2     `json:"config"`
	MigratedV1   bool         `json:"migrated_v1"`
	Files        []FileChange `json:"files"`
	ConfigHash   string       `json:"config_hash"`
	ConfigYAML   string       `json:"config_yaml"`
	WorkflowYAML string       `json:"workflow_yaml"`
	ConfigDiff   string       `json:"config_diff"`
	WorkflowDiff string       `json:"workflow_diff"`
	filePlan     FilePlan
}

func (s CDService) PreviewMutation(request MutationRequest) (MutationPreview, error) {
	if err := ValidateConfigPath(request.ConfigPath); err != nil {
		return MutationPreview{}, fmt.Errorf("config path: %w", err)
	}
	if err := ValidateWorkflowPath(request.WorkflowPath); err != nil {
		return MutationPreview{}, fmt.Errorf("workflow path: %w", err)
	}
	root, err := filepath.EvalSymlinks(request.Repository)
	if err != nil {
		return MutationPreview{}, fmt.Errorf("resolve repository root: %w", err)
	}
	if err := ValidateBuildInputs(root, request.Service.Build.Context, request.Service.Build.Dockerfile, request.Service.Build.Platform, firstBranch(request.Service)); err != nil {
		return MutationPreview{}, err
	}
	cfg := ConfigV2{Version: 2, Services: []ServiceV2{}}
	migrated := false
	oldConfig := []byte{}
	oldConfig, err = readRepositoryFile(root, request.ConfigPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		oldConfig = nil
	case err != nil:
		return MutationPreview{}, fmt.Errorf("read config: %w", err)
	default:
		cfg, migrated, err = validateConfigBytes(oldConfig, root)
		if err != nil {
			return MutationPreview{}, err
		}
	}
	cfg, err = UpsertService(cfg, request.Service)
	if err != nil {
		return MutationPreview{}, err
	}
	if err := ValidateConfig(root, &cfg); err != nil {
		return MutationPreview{}, err
	}
	configBytes, err := RenderConfigV2(cfg)
	if err != nil {
		return MutationPreview{}, err
	}
	workflowBytes := RenderWorkflow(cfg)
	oldWorkflow, err := readRepositoryFile(root, request.WorkflowPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return MutationPreview{}, fmt.Errorf("read workflow: %w", err)
	}
	filePlan, err := PrepareFiles(root, []FileSpec{{Path: request.ConfigPath, Content: configBytes}, {Path: request.WorkflowPath, Content: workflowBytes}}, true, true)
	if err != nil {
		return MutationPreview{}, err
	}
	return MutationPreview{Config: cfg, MigratedV1: migrated, Files: filePlan.Changes, ConfigHash: ConfigHash(configBytes), ConfigYAML: string(configBytes), WorkflowYAML: string(workflowBytes), ConfigDiff: boundedTextDiff(request.ConfigPath, oldConfig, configBytes), WorkflowDiff: boundedTextDiff(request.WorkflowPath, oldWorkflow, workflowBytes), filePlan: filePlan}, nil
}

func (s CDService) ApplyMutation(request MutationRequest) (MutationPreview, error) {
	preview, err := s.PreviewMutation(request)
	if err != nil {
		return MutationPreview{}, err
	}
	for _, file := range preview.Files {
		if file.Action == "updated" && (!request.Force || !request.Confirmed) {
			return MutationPreview{}, errors.New("updating managed files requires --force and explicit confirmation")
		}
	}
	if err := WriteFiles(preview.filePlan, WriteOptions{}); err != nil {
		return MutationPreview{}, err
	}
	return preview, nil
}

func firstBranch(service ServiceV2) string {
	if len(service.Deploy.Production.Branches) > 0 {
		return service.Deploy.Production.Branches[0]
	}
	return "main"
}

func readRepositoryFile(root, relative string) ([]byte, error) {
	if err := validatePathSymlinks(root, relative); err != nil {
		return nil, err
	}
	target := filepath.Join(root, filepath.FromSlash(relative))
	info, err := os.Lstat(target)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file, not a symlink", relative)
	}
	return os.ReadFile(target)
}

func boundedTextDiff(name string, oldData, newData []byte) string {
	if string(oldData) == string(newData) {
		return ""
	}
	const limit = 256 << 10
	oldText, newText := string(oldData), string(newData)
	if len(oldText) > limit {
		oldText = oldText[:limit] + "\n... truncated ...\n"
	}
	if len(newText) > limit {
		newText = newText[:limit] + "\n... truncated ...\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", name, name)
	if oldText != "" {
		for _, line := range strings.Split(strings.TrimSuffix(oldText, "\n"), "\n") {
			b.WriteString("-")
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(newText, "\n"), "\n") {
		b.WriteString("+")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}
