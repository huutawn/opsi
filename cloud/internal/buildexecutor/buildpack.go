package buildexecutor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
)

func Buildpack(ctx context.Context, spec buildjob.BuildSpec, sourceDir, workspace, outputDir string, publisher RegistryPublisher, log io.Writer) (BuildOutput, error) {
	if err := spec.Validate(); err != nil || spec.ResolvedBuildStrategy != buildjob.StrategyBuildpack {
		return BuildOutput{}, Error{Code: "BUILD_SPEC_INVALID", Phase: "contract", Message: "canonical Build Spec is invalid"}
	}
	if spec.ApplicationRoot != spec.BuildContext {
		return BuildOutput{}, Error{Code: "BUILDPACK_MONOREPO_UNSUPPORTED", Phase: "contract", Message: "Buildpacks require an independent application root"}
	}
	if publisher == nil || within(outputDir, sourceDir) {
		return BuildOutput{}, Error{Code: "BUILDPACK_RESULT_INVALID", Phase: "contract", Message: "Buildpacks require canonical registry publication and isolated output"}
	}
	appPath, err := sourcePath(sourceDir, spec.ApplicationRoot)
	if err != nil {
		return BuildOutput{}, Error{Code: "BUILD_PATH_MISMATCH", Phase: "contract", Message: "application root leaves the materialized source"}
	}
	if info, err := os.Stat(appPath); err != nil || !info.IsDir() {
		return BuildOutput{}, Error{Code: "BUILD_CONTEXT_MISSING", Phase: "contract", Message: "application root is missing"}
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return BuildOutput{}, Error{Code: "DISK_OUTPUT_FAILURE", Phase: "infrastructure", Message: "executor output directory cannot be created"}
	}
	dockerConfig := filepath.Join(workspace, "docker-config")
	if configured := os.Getenv("DOCKER_CONFIG"); configured != "" && filepath.Clean(configured) != filepath.Clean(dockerConfig) {
		return BuildOutput{}, Error{Code: "RUNNER_ENVIRONMENT_INVALID", Phase: "infrastructure", Message: "Docker configuration is outside the executor workspace"}
	}
	if err := os.MkdirAll(dockerConfig, 0o700); err != nil {
		return BuildOutput{}, Error{Code: "RUNNER_ENVIRONMENT_INVALID", Phase: "infrastructure", Message: "isolated Docker configuration cannot be created"}
	}
	tempDir := filepath.Join(workspace, "tmp")
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		return BuildOutput{}, Error{Code: "DISK_OUTPUT_FAILURE", Phase: "infrastructure", Message: "Buildpacks temporary directory cannot be created"}
	}
	env := append(dockerEnv(dockerConfig), "TMPDIR="+tempDir)
	builder, err := inspectBuildpackToolchain(ctx, workspace, env)
	if err != nil {
		return BuildOutput{}, err
	}
	cacheNames := []string{"opsi-cnb-build-" + spec.Publication.Tag, "opsi-cnb-launch-" + spec.Publication.Tag}
	removeCaches := func() error {
		command := exec.Command("docker", append([]string{"volume", "rm", "--force"}, cacheNames...)...)
		command.Env = env
		return command.Run()
	}
	if err := removeCaches(); err != nil {
		return BuildOutput{}, Error{Code: "EXECUTOR_INFRASTRUCTURE_FAILED", Phase: "infrastructure", Message: "Buildpacks cache cannot be reset"}
	}
	defer func() { _ = removeCaches() }()
	reportPath := filepath.Join(outputDir, "report.toml")
	args := []string{
		"build", spec.Publication.TagReference(), "--path", appPath,
		"--builder", BuildpackBuilder, "--run-image", BuildpackRunImage,
		"--platform", Platform, "--pull-policy", "always", "--network", "bridge", "--creation-time", "0",
		"--report-output-dir", outputDir,
		"--cache", "type=build;format=volume;name=" + cacheNames[0] + ";type=launch;format=volume;name=" + cacheNames[1],
	}
	logFile, err := os.CreateTemp(workspace, "buildpack-log-*")
	if err != nil {
		return BuildOutput{}, Error{Code: "DISK_OUTPUT_FAILURE", Phase: "infrastructure", Message: "Buildpacks log cannot be created"}
	}
	logPath := logFile.Name()
	defer os.Remove(logPath)
	command := exec.CommandContext(ctx, "pack", args...)
	command.Dir = sourceDir
	command.Env = env
	command.Stdout = logFile
	command.Stderr = logFile
	runErr := command.Run()
	if closeErr := logFile.Close(); closeErr != nil {
		return BuildOutput{}, Error{Code: "DISK_OUTPUT_FAILURE", Phase: "infrastructure", Message: "Buildpacks log cannot be finalized"}
	}
	emitLog(log, logPath)
	if runErr != nil {
		return BuildOutput{}, classifyBuildpackFailure(readTail(logPath, 1<<20))
	}
	if info, err := os.Stat(reportPath); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return BuildOutput{}, Error{Code: "BUILDPACK_RESULT_INVALID", Phase: "build", Message: "Buildpacks report is missing"}
	}
	metadata, err := inspectBuildpackImage(ctx, workspace, env, spec.Publication.TagReference(), builder)
	if err != nil {
		return BuildOutput{}, err
	}
	if err := publisher.Prepare(ctx, spec.Publication, workspace, env); err != nil {
		return BuildOutput{}, err
	}
	defer publisher.Cleanup(ctx, spec.Publication, workspace, env)
	descriptor, err := pushImage(ctx, spec.Publication, workspace, env, log)
	if err != nil {
		return BuildOutput{}, err
	}
	remote, err := publisher.Verify(ctx, spec.Publication, descriptor, workspace, env)
	if err != nil {
		return BuildOutput{}, err
	}
	return BuildOutput{ImageDigest: descriptor.Digest, Descriptor: descriptor, Remote: remote, MetadataPath: reportPath, Builder: metadata}, nil
}

func inspectBuildpackToolchain(ctx context.Context, workspace string, env []string) (buildrecordv1.BuilderMetadata, error) {
	version, err := run(ctx, workspace, env, "pack", "version")
	versionText := strings.TrimSpace(string(version))
	if err != nil || versionText != PackVersion && !strings.HasPrefix(versionText, PackVersion+"+") {
		return buildrecordv1.BuilderMetadata{}, Error{Code: "BUILDPACK_BUILDER_UNAVAILABLE", Phase: "infrastructure", Message: "pack version does not match the executor pin"}
	}
	if err := verifyPinnedImage(ctx, workspace, env, BuildpackBuilder, BuildpackBuilderDigest); err != nil {
		return buildrecordv1.BuilderMetadata{}, Error{Code: "BUILDPACK_BUILDER_UNAVAILABLE", Phase: "infrastructure", Message: "pinned Buildpacks builder is unavailable"}
	}
	if err := verifyPinnedImage(ctx, workspace, env, BuildpackRunImage, BuildpackRunImageDigest); err != nil {
		return buildrecordv1.BuilderMetadata{}, Error{Code: "BUILDPACK_RUN_IMAGE_UNAVAILABLE", Phase: "infrastructure", Message: "pinned Buildpacks run image is unavailable"}
	}
	raw, err := run(ctx, workspace, env, "pack", "builder", "inspect", BuildpackBuilder, "--output", "json")
	if err != nil {
		return buildrecordv1.BuilderMetadata{}, Error{Code: "BUILDPACK_BUILDER_UNAVAILABLE", Phase: "infrastructure", Message: "pinned Buildpacks builder cannot be inspected"}
	}
	var inspection struct {
		Remote struct {
			Lifecycle struct {
				Version string `json:"version"`
			} `json:"lifecycle"`
		} `json:"remote_info"`
	}
	if json.Unmarshal(raw, &inspection) != nil || inspection.Remote.Lifecycle.Version != BuildpackLifecycleVersion {
		return buildrecordv1.BuilderMetadata{}, Error{Code: "BUILDPACK_RESULT_INVALID", Phase: "infrastructure", Message: "Buildpacks lifecycle does not match the executor pin"}
	}
	return buildrecordv1.BuilderMetadata{PackVersion: PackVersion, BuilderImage: BuildpackBuilder, BuilderImageDigest: BuildpackBuilderDigest, RunImage: BuildpackRunImage, RunImageDigest: BuildpackRunImageDigest, LifecycleVersion: BuildpackLifecycleVersion}, nil
}

func verifyPinnedImage(ctx context.Context, workspace string, env []string, image, digest string) error {
	raw, err := run(ctx, workspace, env, "docker", "buildx", "imagetools", "inspect", image, "--format", "{{json .}}")
	if err != nil {
		return err
	}
	var inspection struct {
		Manifest struct {
			Digest string `json:"digest"`
		} `json:"manifest"`
	}
	if json.Unmarshal(raw, &inspection) != nil || inspection.Manifest.Digest != digest {
		return os.ErrInvalid
	}
	return nil
}

func inspectBuildpackImage(ctx context.Context, workspace string, env []string, image string, metadata buildrecordv1.BuilderMetadata) (buildrecordv1.BuilderMetadata, error) {
	raw, err := run(ctx, workspace, env, "docker", "image", "inspect", image)
	if err != nil {
		return buildrecordv1.BuilderMetadata{}, Error{Code: "BUILDPACK_RESULT_INVALID", Phase: "build", Message: "Buildpacks image metadata is unavailable"}
	}
	var images []struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if json.Unmarshal(raw, &images) != nil || len(images) != 1 {
		return buildrecordv1.BuilderMetadata{}, Error{Code: "BUILDPACK_RESULT_INVALID", Phase: "build", Message: "Buildpacks image metadata is invalid"}
	}
	var build struct {
		Buildpacks []buildrecordv1.Buildpack `json:"buildpacks"`
		Processes  []struct {
			Type      string   `json:"type"`
			Command   []string `json:"command"`
			Arguments []string `json:"args"`
			Direct    bool     `json:"direct"`
		} `json:"processes"`
		DefaultProcessType string `json:"buildpack-default-process-type"`
	}
	if json.Unmarshal([]byte(images[0].Config.Labels["io.buildpacks.build.metadata"]), &build) != nil || len(build.Buildpacks) == 0 {
		return buildrecordv1.BuilderMetadata{}, Error{Code: "BUILDPACK_RESULT_INVALID", Phase: "build", Message: "CNB detector metadata is invalid"}
	}
	metadata.Buildpacks = build.Buildpacks
	for _, process := range build.Processes {
		metadata.Processes = append(metadata.Processes, buildrecordv1.Process{Type: process.Type, Command: process.Command, Arguments: process.Arguments, Direct: process.Direct, Default: process.Type == build.DefaultProcessType})
	}
	return metadata, nil
}

func classifyBuildpackFailure(output string) error {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "no buildpack groups passed detection") || strings.Contains(lower, "failed to detect"):
		return Error{Code: "BUILDPACK_DETECTION_FAILED", Phase: "detection", Message: "Cloud Native Buildpacks detection failed"}
	case strings.Contains(lower, "failed to fetch run image") || strings.Contains(lower, "run image not found") || strings.Contains(lower, "run image unavailable"):
		return Error{Code: "BUILDPACK_RUN_IMAGE_UNAVAILABLE", Phase: "infrastructure", Message: "pinned Buildpacks run image is unavailable"}
	case strings.Contains(lower, "failed to fetch builder image") || strings.Contains(lower, "builder image not found") || strings.Contains(lower, "builder image unavailable"):
		return Error{Code: "BUILDPACK_BUILDER_UNAVAILABLE", Phase: "infrastructure", Message: "pinned Buildpacks builder is unavailable"}
	default:
		return Error{Code: "BUILDPACK_BUILD_FAILED", Phase: "build", Message: "Cloud Native Buildpacks build failed"}
	}
}

func manifestDescriptor(raw []byte) (buildjob.ImageDescriptor, error) {
	var manifest struct {
		MediaType string `json:"mediaType"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.MediaType == "" {
		return buildjob.ImageDescriptor{}, os.ErrInvalid
	}
	sum := sha256.Sum256(raw)
	return buildjob.ImageDescriptor{Digest: "sha256:" + hex.EncodeToString(sum[:]), MediaType: manifest.MediaType, Size: int64(len(raw))}, nil
}
