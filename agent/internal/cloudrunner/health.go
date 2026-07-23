package cloudrunner

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"
)

const MaxHealthProbeTimeout = 5 * time.Second

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
	return exec.CommandContext(ctx, executable, args...).Output()
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
