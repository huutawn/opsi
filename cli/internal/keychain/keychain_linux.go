//go:build linux

package keychain

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const keychainOperationTimeout = 5 * time.Second

type secretToolRunner func(context.Context, string, []string, []byte) ([]byte, []byte, error)

// OSStore is a Fedora Secret Service store. It intentionally has no fallback
// backend, so unavailable or locked desktop keychains fail closed.
type OSStore struct {
	timeout time.Duration
	run     secretToolRunner
}

func NewOSStore() (*OSStore, error) {
	return &OSStore{timeout: keychainOperationTimeout, run: runSecretTool}, nil
}

func (s *OSStore) SetPAT(token string) error {
	return s.runOperation("store", []string{"store", "--label=Opsi PAT", "service", "opsi", "key", patKey}, []byte(token))
}

func (s *OSStore) GetPAT() (string, error) {
	output, err := s.runOperationOutput("lookup", []string{"lookup", "service", "opsi", "key", patKey}, nil)
	if err != nil {
		return "", err
	}
	if len(output) == 0 {
		return "", ErrPATNotFound
	}
	return strings.TrimSuffix(string(output), "\n"), nil
}

func (s *OSStore) DeletePAT() error {
	return s.runOperation("delete", []string{"clear", "service", "opsi", "key", patKey}, nil)
}

func (s *OSStore) runOperation(operation string, args []string, input []byte) error {
	_, err := s.runOperationOutput(operation, args, input)
	return err
}

func (s *OSStore) runOperationOutput(operation string, args []string, input []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	output, stderr, err := s.run(ctx, "secret-tool", args, input)
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("%s PAT: %w", operation, ErrKeychainTimeout)
	}
	if err != nil {
		return nil, fmt.Errorf("%s PAT: %w", operation, classifySecretToolError(stderr))
	}
	return output, nil
}

func classifySecretToolError(stderr []byte) error {
	message := strings.ToLower(string(stderr))
	if strings.Contains(message, "no matching secret") || strings.Contains(message, "not found") {
		return ErrPATNotFound
	}
	return ErrKeychainUnavailable
}

func runSecretTool(ctx context.Context, program string, args []string, input []byte) ([]byte, []byte, error) {
	return runExternalCommand(ctx, program, args, input)
}

func runExternalCommand(ctx context.Context, program string, args []string, input []byte) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run() // Run waits after CommandContext kills a timed-out child.
	return stdout.Bytes(), stderr.Bytes(), err
}

var _ Store = (*OSStore)(nil)
