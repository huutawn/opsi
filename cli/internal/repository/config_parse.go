package repository

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type v1ServiceBuildConfig struct {
	APIVersion string             `yaml:"apiVersion"`
	Kind       string             `yaml:"kind"`
	Metadata   v1ConfigMetadata   `yaml:"metadata"`
	Build      v1BuildConfig      `yaml:"build"`
	Deploy     v1DeploymentConfig `yaml:"deploy"`
}

type v1ConfigMetadata struct {
	ServiceKey string `yaml:"serviceKey"`
}
type v1BuildConfig struct {
	Context    string   `yaml:"context"`
	Dockerfile string   `yaml:"dockerfile"`
	Platforms  []string `yaml:"platforms"`
}
type v1DeploymentConfig struct {
	Production v1ProductionConfig `yaml:"production"`
	Preview    v1PreviewConfig    `yaml:"preview"`
}
type v1ProductionConfig struct {
	Branches []string `yaml:"branches"`
}
type v1PreviewConfig struct {
	PullRequests bool `yaml:"pullRequests"`
}

func validateConfigBytes(data []byte, repoRoot string) (ConfigV2, bool, error) {
	if len(data) == 0 || len(data) > 1<<20 {
		return ConfigV2{}, false, errors.New("config is empty or exceeds the 1 MiB limit")
	}
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil {
		return ConfigV2{}, false, fmt.Errorf("parse config: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err == nil {
		return ConfigV2{}, false, errors.New("config must contain exactly one YAML document")
	} else if !errors.Is(err, io.EOF) {
		return ConfigV2{}, false, fmt.Errorf("parse trailing config data: %w", err)
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return ConfigV2{}, false, errors.New("config must be a YAML mapping")
	}
	keys := map[string]bool{}
	for i := 0; i+1 < len(document.Content[0].Content); i += 2 {
		keys[document.Content[0].Content[i].Value] = true
	}
	if keys["version"] {
		var cfg ConfigV2
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil {
			return ConfigV2{}, false, fmt.Errorf("decode v2 config: %w", err)
		}
		if cfg.Version != 2 {
			return ConfigV2{}, false, fmt.Errorf("unsupported config version %d", cfg.Version)
		}
		if err := ValidateConfig(repoRoot, &cfg); err != nil {
			return ConfigV2{}, false, err
		}
		return canonicalConfig(cfg), false, nil
	}
	var legacy v1ServiceBuildConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&legacy); err != nil {
		return ConfigV2{}, false, fmt.Errorf("decode v1 config: %w", err)
	}
	if legacy.APIVersion != "cd.opsi.dev/v1alpha1" || legacy.Kind != "ServiceBuild" {
		return ConfigV2{}, false, errors.New("unknown repository config schema")
	}
	if len(legacy.Build.Platforms) != 1 {
		return ConfigV2{}, false, errors.New("v1 config must contain exactly one build platform")
	}
	cfg := ConfigV2{Version: 2, Services: []ServiceV2{{Key: legacy.Metadata.ServiceKey, Build: BuildV2{Context: legacy.Build.Context, Dockerfile: legacy.Build.Dockerfile, Platform: legacy.Build.Platforms[0]}, WatchPaths: []string{}, SharedPaths: []string{}, Dependencies: []string{}, Deploy: DeployV2{Production: ProductionV2{Enabled: len(legacy.Deploy.Production.Branches) > 0, Branches: append([]string(nil), legacy.Deploy.Production.Branches...)}, Preview: PreviewV2{Enabled: legacy.Deploy.Preview.PullRequests, PullRequests: legacy.Deploy.Preview.PullRequests}}}}}
	if err := ValidateConfig(repoRoot, &cfg); err != nil {
		return ConfigV2{}, false, fmt.Errorf("migrate v1 config: %w", err)
	}
	return canonicalConfig(cfg), true, nil
}

func LoadConfig(repoRoot, relativePath string) (ConfigV2, bool, []byte, error) {
	if err := validateSlashRelativePath(relativePath, false); err != nil {
		return ConfigV2{}, false, nil, err
	}
	root, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		return ConfigV2{}, false, nil, fmt.Errorf("resolve repository root: %w", err)
	}
	resolved := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := ensureInside(root, resolved); err != nil {
		return ConfigV2{}, false, nil, err
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return ConfigV2{}, false, nil, fmt.Errorf("inspect config: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ConfigV2{}, false, nil, errors.New("config must be a regular file, not a symlink")
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return ConfigV2{}, false, nil, fmt.Errorf("read config: %w", err)
	}
	cfg, migrated, err := validateConfigBytes(data, root)
	if err != nil {
		return ConfigV2{}, false, nil, err
	}
	rendered, err := RenderConfigV2(cfg)
	if err != nil {
		return ConfigV2{}, false, nil, err
	}
	return cfg, migrated, rendered, nil
}
