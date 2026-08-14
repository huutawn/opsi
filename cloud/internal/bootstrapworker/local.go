package bootstrapworker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
)

type LocalExecutor struct{}

func (LocalExecutor) Connect(context.Context, RemoteTarget) (RemoteSession, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("bootstrap command must run as root")
	}
	return localSession{}, nil
}

type localSession struct{}

func (localSession) Run(ctx context.Context, spec CommandSpec) (CommandResult, error) {
	command := exec.CommandContext(ctx, "/bin/sh", "-s")
	command.Stdin = bytes.NewBufferString(renderScript(spec))
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	code := 0
	if err != nil {
		code = 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		}
	}
	return CommandResult{ExitCode: code, Stdout: stdout.String(), Stderr: stderr.String()}, err
}

func (localSession) Close() error { return nil }
