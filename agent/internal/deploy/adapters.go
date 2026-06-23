package deploy

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type ExecGitClient struct{}

func (ExecGitClient) Clone(ctx context.Context, repoURL, branch, gitSHA, dest string) error {
	args := []string{"clone"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, repoURL, dest)
	if err := run(ctx, "git", args...); err != nil {
		return err
	}
	return runIn(ctx, dest, "git", "checkout", gitSHA)
}

type ExecBuilder struct{}

func (ExecBuilder) Build(ctx context.Context, workDir, dockerfile, imageTag string) error {
	dockerfilePath := dockerfile
	if !filepath.IsAbs(dockerfilePath) {
		dockerfilePath = filepath.Join(workDir, dockerfile)
	}
	if err := runIn(ctx, workDir, "docker", "buildx", "build", "--load", "-f", dockerfilePath, "-t", imageTag, "."); err == nil {
		return nil
	}
	return runIn(ctx, workDir, "docker", "build", "-f", dockerfilePath, "-t", imageTag, ".")
}

func (ExecBuilder) Push(ctx context.Context, imageTag string) error {
	return run(ctx, "docker", "push", imageTag)
}

type ContainerdBuilder struct {
	NerdctlPath string
	Namespace   string
}

func (b ContainerdBuilder) Build(ctx context.Context, workDir, dockerfile, imageTag string) error {
	dockerfilePath := dockerfile
	if !filepath.IsAbs(dockerfilePath) {
		dockerfilePath = filepath.Join(workDir, dockerfile)
	}
	return runIn(ctx, workDir, firstNonEmpty(b.NerdctlPath, "nerdctl"), "--namespace", firstNonEmpty(b.Namespace, "k8s.io"), "build", "-f", dockerfilePath, "-t", imageTag, ".")
}

func (b ContainerdBuilder) Push(ctx context.Context, imageTag string) error {
	return run(ctx, firstNonEmpty(b.NerdctlPath, "nerdctl"), "--namespace", firstNonEmpty(b.Namespace, "k8s.io"), "push", imageTag)
}

type KubectlAdapter struct{}

func (KubectlAdapter) Apply(ctx context.Context, manifestPath, namespace, serviceName, imageTag string) error {
	if err := run(ctx, "kubectl", "apply", "-f", manifestPath, "-n", namespace); err != nil {
		return err
	}
	if imageTag == "" || serviceName == "" {
		return nil
	}
	return run(ctx, "kubectl", "set", "image", "deployment/"+serviceName, "*="+imageTag, "-n", namespace)
}

func (KubectlAdapter) WatchRollout(ctx context.Context, service, namespace string, timeout, _ time.Duration) error {
	return run(ctx, "kubectl", "rollout", "status", "deployment/"+service, "-n", namespace, "--timeout", timeout.String())
}

func (KubectlAdapter) Rollback(ctx context.Context, service, namespace string) error {
	return run(ctx, "kubectl", "rollout", "undo", "deployment/"+service, "-n", namespace)
}

type DryRunGitClient struct{}

func (DryRunGitClient) Clone(context.Context, string, string, string, string) error { return nil }

type DryRunBuilder struct{}

func (DryRunBuilder) Build(context.Context, string, string, string) error { return nil }
func (DryRunBuilder) Push(context.Context, string) error                  { return nil }

type DryRunK3sAdapter struct{}

func (DryRunK3sAdapter) Apply(context.Context, string, string, string, string) error { return nil }
func (DryRunK3sAdapter) WatchRollout(context.Context, string, string, time.Duration, time.Duration) error {
	return nil
}
func (DryRunK3sAdapter) Rollback(context.Context, string, string) error { return nil }

func run(ctx context.Context, name string, args ...string) error {
	return runIn(ctx, "", name, args...)
}

func runIn(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
