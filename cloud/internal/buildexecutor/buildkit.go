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
	"strings"

	"github.com/opsi-dev/opsi/cloud/internal/buildjob"
)

type BuildOutput struct {
	ImageDigest       string
	OCIArtifactPath   string
	OCIArtifactSHA256 string
	MetadataPath      string
}

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func Build(ctx context.Context, spec buildjob.BuildSpec, sourceDir, workspace, outputDir string, log io.Writer) (BuildOutput, error) {
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
	if err := os.Mkdir(dockerConfig, 0o700); err != nil && !os.IsExist(err) {
		return BuildOutput{}, Error{Code: "RUNNER_ENVIRONMENT_INVALID", Phase: "infrastructure", Message: "isolated Docker configuration cannot be created"}
	}
	env := dockerEnv(dockerConfig)
	if err := verifyToolchain(ctx, workspace, env); err != nil {
		return BuildOutput{}, err
	}
	metadataPath := filepath.Join(outputDir, "buildkit-metadata.json")
	ociPath := filepath.Join(outputDir, "image.oci.tar")
	args := []string{
		"buildx", "build", "--builder", "default", "--progress=plain", "--platform", Platform,
		"--file", dockerfilePath, "--metadata-file", metadataPath, "--provenance=false",
		"--output", "type=oci,dest=" + ociPath, contextPath,
	}
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
	digest, err := imageDigest(metadataPath)
	if err != nil {
		return BuildOutput{}, Error{Code: "BUILDKIT_EXECUTION_FAILED", Phase: "build", Message: "BuildKit metadata does not contain a valid image digest"}
	}
	artifactHash, err := fileSHA256(ociPath)
	if err != nil {
		return BuildOutput{}, Error{Code: "DISK_OUTPUT_FAILURE", Phase: "infrastructure", Message: "OCI artifact cannot be hashed"}
	}
	return BuildOutput{ImageDigest: digest, OCIArtifactPath: ociPath, OCIArtifactSHA256: artifactHash, MetadataPath: metadataPath}, nil
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
		return Error{Code: "BUILDKIT_UNAVAILABLE", Phase: "infrastructure", Message: "Docker Buildx is unavailable"}
	}
	fields := strings.Fields(string(buildx))
	if len(fields) < 2 || strings.TrimPrefix(fields[1], "v") != BuildxVersion {
		return Error{Code: "RUNNER_ENVIRONMENT_INVALID", Phase: "infrastructure", Message: "Docker Buildx version does not match the executor pin"}
	}
	inspect, err := run(ctx, directory, env, "docker", "buildx", "inspect", "default", "--bootstrap")
	if err != nil {
		return Error{Code: "BUILDKIT_UNAVAILABLE", Phase: "infrastructure", Message: "BuildKit builder is unavailable"}
	}
	if !strings.Contains(string(inspect), "BuildKit version: "+BuildKitVersion) {
		return Error{Code: "RUNNER_ENVIRONMENT_INVALID", Phase: "infrastructure", Message: "BuildKit version does not match the executor pin"}
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
	case strings.Contains(lower, "dockerfile parse error") || strings.Contains(lower, "failed to parse dockerfile"):
		return Error{Code: "DOCKERFILE_PARSE_FAILED", Phase: "build", Message: "Dockerfile parsing failed"}
	case strings.Contains(lower, "did not complete successfully") || strings.Contains(lower, "process \""):
		return Error{Code: "BUILD_COMMAND_FAILED", Phase: "build", Message: "Dockerfile build command failed"}
	default:
		return Error{Code: "BUILDKIT_EXECUTION_FAILED", Phase: "build", Message: "BuildKit execution failed"}
	}
}

func imageDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(data, &metadata); err != nil {
		return "", err
	}
	for _, key := range []string{"containerimage.digest", "containerimage.descriptor"} {
		value := metadata[key]
		if key == "containerimage.descriptor" {
			var descriptor struct {
				Digest string `json:"digest"`
			}
			if json.Unmarshal(value, &descriptor) == nil && digestPattern.MatchString(descriptor.Digest) {
				return descriptor.Digest, nil
			}
			continue
		}
		var digest string
		if json.Unmarshal(value, &digest) == nil && digestPattern.MatchString(digest) {
			return digest, nil
		}
	}
	return "", os.ErrInvalid
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
