package svcatalog

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type ManifestApplier interface {
	Apply(ctx context.Context, namespace string, manifest []byte) error
}

type KubectlApplier struct {
	KubectlPath string
	Runner      ApplyRunner
}

type ApplyRunner interface {
	Run(ctx context.Context, input []byte, name string, args ...string) ([]byte, error)
}

type ExecApplyRunner struct{}

func (ExecApplyRunner) Run(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(input)
	return cmd.CombinedOutput()
}

func (a KubectlApplier) Apply(ctx context.Context, namespace string, manifest []byte) error {
	runner := a.Runner
	if runner == nil {
		runner = ExecApplyRunner{}
	}
	out, err := runner.Run(ctx, manifest, defaultString(a.KubectlPath, "kubectl"), "apply", "-n", defaultString(namespace, "default"), "-f", "-")
	if err != nil {
		return fmt.Errorf("kubectl apply service catalog manifest: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

type DryRunApplier struct{}

func (DryRunApplier) Apply(context.Context, string, []byte) error { return nil }
