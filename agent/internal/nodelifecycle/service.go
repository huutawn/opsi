package nodelifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const (
	ActionDrain  = "drain"
	ActionRemove = "remove"

	StatusCompleted   = "completed"
	StatusFailed      = "failed"
	StatusUnsupported = "unsupported"
)

var safeNodeName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_.-]{0,251}[A-Za-z0-9])?$`)

type Request struct {
	Action         string
	TargetNodeID   string
	TargetNodeName string
	ConfirmRemove  bool
	Timeout        time.Duration
}

type Result struct {
	Status                 string
	FailureCode            string
	FailureMessageRedacted string
	Verified               bool
}

type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type Service struct {
	KubectlPath string
	Runner      Runner
}

func (s Service) Execute(ctx context.Context, req Request) Result {
	if req.Action != ActionDrain && req.Action != ActionRemove {
		return fail(StatusUnsupported, "NODE_LIFECYCLE_UNSUPPORTED", "node lifecycle action is not supported")
	}
	if req.TargetNodeID == "" || !safeNodeName.MatchString(req.TargetNodeName) {
		return fail(StatusFailed, "INVALID_NODE_TARGET", "node target is invalid")
	}
	if req.Action == ActionRemove && !req.ConfirmRemove {
		return fail(StatusFailed, "REMOVE_INTENT_REQUIRED", "remove requires explicit intent")
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := s.verifyPresent(ctx, req.TargetNodeName); err != nil {
		return fail(StatusFailed, "NODE_IDENTITY_UNVERIFIED", err.Error())
	}
	if err := s.run(ctx, "cordon", req.TargetNodeName); err != nil {
		return fail(StatusFailed, "K3S_CORDON_FAILED", err.Error())
	}
	if err := s.run(ctx, "drain", req.TargetNodeName, "--ignore-daemonsets", "--delete-emptydir-data", "--timeout", timeout.String()); err != nil {
		return fail(StatusFailed, "K3S_DRAIN_FAILED", err.Error())
	}
	if req.Action == ActionDrain {
		if err := s.verifyUnschedulable(ctx, req.TargetNodeName); err != nil {
			return fail(StatusFailed, "K3S_VERIFY_FAILED", err.Error())
		}
		return Result{Status: StatusCompleted, Verified: true}
	}
	if err := s.run(ctx, "delete", "node", req.TargetNodeName); err != nil {
		return fail(StatusFailed, "K3S_REMOVE_FAILED", err.Error())
	}
	if err := s.verifyRemoved(ctx, req.TargetNodeName); err != nil {
		return fail(StatusFailed, "K3S_VERIFY_FAILED", err.Error())
	}
	return Result{Status: StatusCompleted, Verified: true}
}

func (s Service) verifyPresent(ctx context.Context, name string) error {
	node, err := s.getNode(ctx, name)
	if err != nil {
		return err
	}
	if node.Metadata.Name != name {
		return errors.New("node identity mismatch")
	}
	return nil
}

func (s Service) verifyUnschedulable(ctx context.Context, name string) error {
	node, err := s.getNode(ctx, name)
	if err != nil {
		return err
	}
	if !node.Spec.Unschedulable {
		return errors.New("node is not cordoned")
	}
	return nil
}

func (s Service) verifyRemoved(ctx context.Context, name string) error {
	_, err := s.getNode(ctx, name)
	if err == nil {
		return errors.New("node still exists")
	}
	if strings.Contains(strings.ToLower(err.Error()), "notfound") || strings.Contains(strings.ToLower(err.Error()), "not found") {
		return nil
	}
	return err
}

func (s Service) getNode(ctx context.Context, name string) (kubeNode, error) {
	out, err := s.runOut(ctx, "get", "node", name, "-o", "json")
	if err != nil {
		return kubeNode{}, err
	}
	var node kubeNode
	if err := json.Unmarshal(out, &node); err != nil {
		return kubeNode{}, errors.New("node response is invalid")
	}
	return node, nil
}

func (s Service) run(ctx context.Context, args ...string) error {
	_, err := s.runOut(ctx, args...)
	return err
}

func (s Service) runOut(ctx context.Context, args ...string) ([]byte, error) {
	runner := s.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	out, err := runner.Run(ctx, firstNonEmpty(s.KubectlPath, "kubectl"), args...)
	if err != nil {
		return nil, fmt.Errorf("kubectl failed: %s", redact(strings.TrimSpace(string(out))))
	}
	return out, nil
}

type kubeNode struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Unschedulable bool `json:"unschedulable"`
	} `json:"spec"`
}

func fail(status, code, msg string) Result {
	return Result{Status: status, FailureCode: code, FailureMessageRedacted: redact(msg)}
}

func redact(s string) string {
	for _, token := range []string{"token", "password", "secret", "kubeconfig", "private key"} {
		if strings.Contains(strings.ToLower(s), token) {
			return "runtime error redacted"
		}
	}
	if s == "" {
		return "runtime operation failed"
	}
	return s
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
