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
	"regexp"
	"sort"
	"strings"

	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
)

type BuildOutput struct {
	ImageDigest       string
	Descriptor        buildjob.ImageDescriptor
	Remote            buildjob.RemoteRegistryEvidence
	OCIArtifactPath   string
	OCIArtifactSHA256 string
	MetadataPath      string
	Builder           buildrecordv1.BuilderMetadata
}

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func Build(ctx context.Context, spec buildjob.BuildSpec, sourceDir, workspace, outputDir string, log io.Writer) (BuildOutput, error) {
	ociPath := filepath.Join(outputDir, "image.oci.tar")
	return build(ctx, spec, sourceDir, workspace, outputDir, "type=oci,dest="+ociPath, ociPath, log)
}

func Publish(ctx context.Context, spec buildjob.BuildSpec, sourceDir, workspace, outputDir string, publisher RegistryPublisher, log io.Writer) (BuildOutput, error) {
	if publisher == nil {
		return BuildOutput{}, Error{Code: "EXECUTOR_INFRASTRUCTURE_FAILED", Phase: "infrastructure", Message: "registry publisher is unavailable"}
	}
	dockerConfig := filepath.Join(workspace, "docker-config")
	env := dockerEnv(dockerConfig)
	if err := publisher.Prepare(ctx, spec.Publication, workspace, env); err != nil {
		return BuildOutput{}, err
	}
	defer publisher.Cleanup(ctx, spec.Publication, workspace, env)
	output, err := build(ctx, spec, sourceDir, workspace, outputDir, "type=registry,name="+spec.Publication.TagReference()+",push=true", "", log)
	if err != nil {
		return BuildOutput{}, err
	}
	remote, err := publisher.Verify(ctx, spec.Publication, output.Descriptor, workspace, env)
	if err != nil {
		return BuildOutput{}, err
	}
	output.Remote = remote
	return output, nil
}

func build(ctx context.Context, spec buildjob.BuildSpec, sourceDir, workspace, outputDir, exporter, ociPath string, log io.Writer) (BuildOutput, error) {
	if err := spec.Validate(); err != nil {
		return BuildOutput{}, Error{Code: "BUILD_SPEC_INVALID", Phase: "contract", Message: "canonical Build Spec is invalid"}
	}
	contextCandidate := filepath.Join(sourceDir, filepath.FromSlash(spec.BuildContext))
	if _, err := os.Lstat(contextCandidate); err != nil {
		return BuildOutput{}, Error{Code: "BUILD_CONTEXT_MISSING", Phase: "contract", Message: "canonical build context is missing"}
	}
	contextPath, err := sourcePath(sourceDir, spec.BuildContext)
	if err != nil {
		return BuildOutput{}, Error{Code: "BUILD_PATH_MISMATCH", Phase: "contract", Message: "canonical build context leaves the materialized source"}
	}
	dockerfileCandidate := filepath.Join(sourceDir, filepath.FromSlash(spec.DockerfilePath))
	if _, err := os.Lstat(dockerfileCandidate); err != nil {
		return BuildOutput{}, Error{Code: "DOCKERFILE_MISSING", Phase: "contract", Message: "canonical Dockerfile is missing"}
	}
	dockerfilePath, err := sourcePath(sourceDir, spec.DockerfilePath)
	if err != nil {
		return BuildOutput{}, Error{Code: "BUILD_PATH_MISMATCH", Phase: "contract", Message: "canonical Dockerfile leaves the materialized source"}
	}
	if info, err := os.Stat(contextPath); err != nil || !info.IsDir() {
		return BuildOutput{}, Error{Code: "BUILD_CONTEXT_MISSING", Phase: "contract", Message: "canonical build context is missing"}
	}
	if info, err := os.Stat(dockerfilePath); err != nil || !info.Mode().IsRegular() {
		return BuildOutput{}, Error{Code: "DOCKERFILE_MISSING", Phase: "contract", Message: "canonical Dockerfile is missing"}
	}
	if within(outputDir, sourceDir) {
		return BuildOutput{}, Error{Code: "BUILD_PATH_MISMATCH", Phase: "contract", Message: "executor output must be outside the build source"}
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
	env := dockerEnv(dockerConfig)
	if err := verifyToolchain(ctx, workspace, env); err != nil {
		return BuildOutput{}, err
	}
	metadataPath := filepath.Join(outputDir, "buildkit-metadata.json")
	args := canonicalBuildArgs(dockerfilePath, metadataPath, exporter, contextPath, spec.BuildEnvironment)
	logFile, err := os.CreateTemp(workspace, "buildkit-log-*")
	if err != nil {
		return BuildOutput{}, Error{Code: "DISK_OUTPUT_FAILURE", Phase: "infrastructure", Message: "BuildKit log cannot be created"}
	}
	logPath := logFile.Name()
	defer os.Remove(logPath)
	command := exec.CommandContext(ctx, "docker", args...)
	command.Dir = sourceDir
	command.Env = env
	command.Stdout = logFile
	command.Stderr = logFile
	runErr := command.Run()
	if closeErr := logFile.Close(); closeErr != nil {
		return BuildOutput{}, Error{Code: "DISK_OUTPUT_FAILURE", Phase: "infrastructure", Message: "BuildKit log cannot be finalized"}
	}
	emitLog(log, logPath)
	if runErr != nil {
		return BuildOutput{}, classifyBuildFailure(readTail(logPath, 1<<20))
	}
	descriptor, err := imageDescriptor(metadataPath)
	if err != nil {
		return BuildOutput{}, Error{Code: "EXECUTOR_INFRASTRUCTURE_FAILED", Phase: "infrastructure", Message: "BuildKit metadata does not contain a valid image descriptor"}
	}
	output := BuildOutput{ImageDigest: descriptor.Digest, Descriptor: descriptor, MetadataPath: metadataPath}
	if ociPath != "" {
		artifactHash, err := fileSHA256(ociPath)
		if err != nil {
			return BuildOutput{}, Error{Code: "DISK_OUTPUT_FAILURE", Phase: "infrastructure", Message: "OCI artifact cannot be hashed"}
		}
		output.OCIArtifactPath = ociPath
		output.OCIArtifactSHA256 = artifactHash
	}
	return output, nil
}

func canonicalBuildArgs(dockerfilePath, metadataPath, exporter, contextPath string, buildEnv ...map[string]string) []string {
	args := []string{
		"buildx", "build", "--builder", BuilderName, "--progress=plain", "--platform", Platform,
	}
	if len(buildEnv) > 0 && len(buildEnv[0]) > 0 {
		keys := make([]string, 0, len(buildEnv[0]))
		for k := range buildEnv[0] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			args = append(args, "--build-arg", k+"="+buildEnv[0][k])
		}
	}
	args = append(args,
		"--file", dockerfilePath, "--metadata-file", metadataPath, "--provenance=false",
		"--output", exporter, contextPath,
	)
	return args
}

func sourcePath(root, relative string) (string, error) {
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return "", err
	}
	if !within(resolved, root) {
		return "", os.ErrPermission
	}
	return resolved, nil
}

func within(path, root string) bool {
	absolutePath, pathErr := filepath.Abs(path)
	absoluteRoot, rootErr := filepath.Abs(root)
	if pathErr != nil || rootErr != nil {
		return false
	}
	relative, err := filepath.Rel(absoluteRoot, absolutePath)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func emitLog(target io.Writer, path string) {
	if target == nil {
		return
	}
	file, err := os.Open(path)
	if err == nil {
		_, _ = io.Copy(target, file)
		file.Close()
	}
}

func readTail(path string, limit int64) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	if info.Size() > limit {
		_, _ = file.Seek(info.Size()-limit, io.SeekStart)
	}
	data, _ := io.ReadAll(io.LimitReader(file, limit))
	return string(data)
}

func verifyToolchain(ctx context.Context, directory string, env []string) error {
	buildx, err := run(ctx, directory, env, "docker", "buildx", "version")
	if err != nil {
		return Error{Code: "EXECUTOR_INFRASTRUCTURE_FAILED", Phase: "infrastructure", Message: "Docker Buildx is unavailable"}
	}
	fields := strings.Fields(string(buildx))
	if len(fields) < 2 || fields[1] != BuildxVersion {
		return Error{Code: "RUNNER_ENVIRONMENT_INVALID", Phase: "infrastructure", Message: "Docker Buildx version does not match the executor pin"}
	}
	inspect, err := run(ctx, directory, env, "docker", "buildx", "inspect", BuilderName, "--bootstrap")
	if err != nil {
		return Error{Code: "EXECUTOR_INFRASTRUCTURE_FAILED", Phase: "infrastructure", Message: "BuildKit builder is unavailable"}
	}
	inspection := string(inspect)
	for _, expected := range []string{"Driver:", "docker-container", "image=\"" + BuildKitImage + "\"", "network=\"bridge\"", "BuildKit daemon flags: " + BuildKitDaemonFlag, "BuildKit version:", BuildKitVersion} {
		if !strings.Contains(inspection, expected) {
			return Error{Code: "RUNNER_ENVIRONMENT_INVALID", Phase: "infrastructure", Message: "BuildKit builder does not match the executor pin"}
		}
	}
	container, err := run(ctx, directory, env, "docker", "inspect", "buildx_buildkit_"+BuilderName+"0")
	if err != nil {
		return Error{Code: "EXECUTOR_INFRASTRUCTURE_FAILED", Phase: "infrastructure", Message: "BuildKit container is unavailable"}
	}
	var identity []struct {
		Config struct {
			Image string
			Cmd   []string
		}
		HostConfig struct {
			NetworkMode string
		}
	}
	if json.Unmarshal(container, &identity) != nil || len(identity) != 1 || identity[0].Config.Image != BuildKitImage || identity[0].HostConfig.NetworkMode != "bridge" || len(identity[0].Config.Cmd) != 1 || identity[0].Config.Cmd[0] != BuildKitDaemonFlag {
		return Error{Code: "RUNNER_ENVIRONMENT_INVALID", Phase: "infrastructure", Message: "BuildKit container identity does not match the executor pin"}
	}
	return nil
}

func dockerEnv(dockerConfig string) []string {
	extra := map[string]string{"DOCKER_CONFIG": dockerConfig, "HOME": filepath.Dir(dockerConfig)}
	for _, key := range []string{"DOCKER_HOST", "XDG_RUNTIME_DIR"} {
		if value := os.Getenv(key); value != "" {
			extra[key] = value
		}
	}
	return commandEnv(extra)
}

func classifyBuildFailure(output string) error {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "network.host is not allowed") || strings.Contains(lower, "security.insecure is not allowed") || strings.Contains(lower, "device is not allowed"):
		return Error{Code: "USER_BUILD_FAILED", Phase: "build", Message: "Dockerfile requested an entitlement not granted by Opsi"}
	case strings.Contains(lower, "dockerfile parse error") || strings.Contains(lower, "failed to parse dockerfile"):
		return Error{Code: "USER_BUILD_FAILED", Phase: "build", Message: "Dockerfile parsing failed"}
	case strings.Contains(lower, "did not complete successfully") || strings.Contains(lower, "process \""):
		return Error{Code: "USER_BUILD_FAILED", Phase: "build", Message: "Dockerfile build command failed"}
	case strings.Contains(lower, "unauthorized") || strings.Contains(lower, "denied") || strings.Contains(lower, "insufficient_scope"):
		return Error{Code: "REGISTRY_AUTH_FAILED", Phase: "publication", Message: "Registry authentication failed"}
	case strings.Contains(lower, "failed to push") || strings.Contains(lower, "error pushing"):
		return Error{Code: "REGISTRY_PUSH_FAILED", Phase: "publication", Message: "Registry publication failed"}
	default:
		return Error{Code: "EXECUTOR_INFRASTRUCTURE_FAILED", Phase: "infrastructure", Message: "BuildKit execution failed"}
	}
}

func imageDescriptor(path string) (buildjob.ImageDescriptor, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return buildjob.ImageDescriptor{}, err
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(data, &metadata); err != nil {
		return buildjob.ImageDescriptor{}, err
	}
	var descriptor struct {
		Digest    string `json:"digest"`
		MediaType string `json:"mediaType"`
		Size      int64  `json:"size"`
	}
	if json.Unmarshal(metadata["containerimage.descriptor"], &descriptor) == nil && digestPattern.MatchString(descriptor.Digest) && descriptor.MediaType != "" {
		return buildjob.ImageDescriptor{Digest: descriptor.Digest, MediaType: descriptor.MediaType, Size: descriptor.Size}, nil
	}
	return buildjob.ImageDescriptor{}, os.ErrInvalid
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
