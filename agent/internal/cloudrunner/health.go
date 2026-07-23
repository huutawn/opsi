package cloudrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	MaxHealthProbeTimeout     = 5 * time.Second
	MaxHealthCommandOutputLen = 256 * 1024
)

var errHealthCommandOutputExceeded = errors.New("health command output exceeded the allowed bound")

const (
	K3SStatusReady       = "ready"
	K3SStatusNotReady    = "not_ready"
	K3SStatusUnavailable = "unavailable"
)

type RuntimeHealth struct {
	NodeReady bool
	K3SStatus string
}

type HealthProbe interface {
	Probe(context.Context) (RuntimeHealth, error)
}

type HealthCommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type ExecHealthCommandRunner struct{}

func (ExecHealthCommandRunner) Run(ctx context.Context, executable string, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	output := &boundedHealthOutput{limit: MaxHealthCommandOutputLen, cancel: cancel}
	cmd := exec.CommandContext(commandCtx, executable, args...)
	cmd.Stdout = output
	cmd.Stderr = output
	err := cmd.Run()
	if output.Exceeded() {
		return nil, errHealthCommandOutputExceeded
	}
	if err != nil {
		return nil, errors.New("health command failed")
	}
	return output.Bytes(), nil
}

type boundedHealthOutput struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	exceeded bool
	cancel   context.CancelFunc
}

func (b *boundedHealthOutput) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.exceeded {
		return len(data), io.ErrShortWrite
	}
	remaining := b.limit - b.buffer.Len()
	if len(data) > remaining {
		b.exceeded = true
		if b.cancel != nil {
			b.cancel()
		}
		return len(data), io.ErrShortWrite
	}
	_, _ = b.buffer.Write(data)
	return len(data), nil
}

func (b *boundedHealthOutput) Exceeded() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exceeded
}

func (b *boundedHealthOutput) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buffer.Bytes()...)
}

type KubernetesHealthProbe struct {
	KubectlPath string
	Runner      HealthCommandRunner
}

func ProbeRuntime(ctx context.Context, probe HealthProbe) RuntimeHealth {
	unavailable := RuntimeHealth{K3SStatus: K3SStatusUnavailable}
	if probe == nil {
		return unavailable
	}
	probeCtx, cancel := context.WithTimeout(ctx, MaxHealthProbeTimeout)
	defer cancel()
	health, err := probe.Probe(probeCtx)
	if err != nil || probeCtx.Err() != nil {
		return unavailable
	}
	switch health.K3SStatus {
	case K3SStatusReady:
		if !health.NodeReady {
			return unavailable
		}
	case K3SStatusNotReady:
		health.NodeReady = false
	case K3SStatusUnavailable:
		health.NodeReady = false
	default:
		return unavailable
	}
	return health
}

func (p KubernetesHealthProbe) Probe(ctx context.Context) (RuntimeHealth, error) {
	if p.Runner == nil {
		return RuntimeHealth{}, errors.New("health command runner is required")
	}
	kubectlPath := strings.TrimSpace(p.KubectlPath)
	if kubectlPath == "" {
		return RuntimeHealth{}, errors.New("kubectl path is required")
	}
	readyz, err := p.Runner.Run(ctx, kubectlPath, "--request-timeout=4s", "get", "--raw=/readyz")
	if err != nil || !strings.EqualFold(strings.TrimSpace(string(readyz)), "ok") {
		return RuntimeHealth{}, errors.New("k3s API is unavailable")
	}
	nodes, err := p.Runner.Run(ctx, kubectlPath, "--request-timeout=4s", "get", "nodes", "-o", "json")
	if err != nil {
		return RuntimeHealth{}, errors.New("kubernetes node query failed")
	}
	var result struct {
		Items []struct {
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(nodes, &result); err != nil || len(result.Items) == 0 {
		return RuntimeHealth{}, errors.New("kubernetes node response is malformed")
	}
	for _, item := range result.Items {
		ready := false
		for _, condition := range item.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				ready = true
				break
			}
		}
		if !ready {
			return RuntimeHealth{K3SStatus: K3SStatusNotReady}, nil
		}
	}
	return RuntimeHealth{NodeReady: true, K3SStatus: K3SStatusReady}, nil
}
