package cloudrunner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	"github.com/opsi-dev/opsi/agent/internal/config"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
	"github.com/opsi-dev/opsi/agent/internal/nodelifecycle"
)

type fakeClient struct {
	leases      []cloudrelay.DeploymentLease
	nodeLeases  []cloudrelay.NodeLifecycleLease
	results     []cloudrelay.DeploymentResult
	nodeResults []cloudrelay.NodeLifecycleResult
	heartbeats  int
	cancel      context.CancelFunc
}

func (f *fakeClient) PollJob(context.Context, string, time.Duration) (*cloudrelay.JobLease, error) {
	if len(f.leases) > 0 {
		lease := f.leases[0]
		f.leases = f.leases[1:]
		return &cloudrelay.JobLease{Kind: "deployment", Deployment: &lease}, nil
	}
	if len(f.nodeLeases) > 0 {
		lease := f.nodeLeases[0]
		f.nodeLeases = f.nodeLeases[1:]
		return &cloudrelay.JobLease{Kind: "node_lifecycle", NodeLifecycle: &lease}, nil
	}
	return nil, context.Canceled
}

func (f *fakeClient) CompleteDeployment(_ context.Context, _ string, _ string, result cloudrelay.DeploymentResult) error {
	f.results = append(f.results, result)
	if f.cancel != nil {
		f.cancel()
	}
	return nil
}

func (f *fakeClient) CompleteNodeLifecycle(_ context.Context, _ string, _ string, result cloudrelay.NodeLifecycleResult) error {
	f.nodeResults = append(f.nodeResults, result)
	if f.cancel != nil {
		f.cancel()
	}
	return nil
}

func (f *fakeClient) Heartbeat(context.Context, string, cloudrelay.Heartbeat) error {
	f.heartbeats++
	return nil
}

type fakeLifecycle struct{}

func (fakeLifecycle) Execute(context.Context, nodelifecycle.Request) nodelifecycle.Result {
	return nodelifecycle.Result{Status: nodelifecycle.StatusCompleted, Verified: true}
}

type fakeEngine struct{}

func (fakeEngine) Deploy(_ context.Context, req deploy.Request, progress deploy.ProgressFunc) (deploy.Record, error) {
	rec := deploy.Record{DeployID: "local-dep", ProjectID: req.ProjectID, ServiceID: req.ServiceID, ServiceName: req.ServiceName, GitSHA: req.GitSHA, ImageTag: req.ImageTag, Status: deploy.StatusSuccess}
	if progress != nil {
		_ = progress(&deploy.ProgressEvent{ProjectID: req.ProjectID, ServiceID: req.ServiceID, Phase: deploy.PhaseSuccess, Percent: 100})
	}
	return rec, nil
}

func TestRunnerExecutesDryRunLeaseAndReportsResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &fakeClient{cancel: cancel, leases: []cloudrelay.DeploymentLease{{
		Kind: "deployment", Action: "deploy",
		Deployment: cloudrelay.DeploymentJobEnvelope{ID: "dep-1", ManifestHash: "manifest"},
		Service:    cloudrelay.ServiceEnvelope{ID: "api", Name: "api", Type: "application", SourceType: "git", RepoURL: "https://example.test/repo.git", Branch: "main", GitSHA: "abcdef123456", Namespace: "default"},
	}}}
	runner := Runner{
		Client: client,
		Engine: fakeEngine{},
		NodeID: "node-1",
		DeploymentConfig: config.DeploymentConfig{
			ProjectID: "proj", ManifestPath: "k8s/deployment.yaml", BuildContext: ".", Dockerfile: "Dockerfile", Namespace: "default",
		},
		PollInterval:      time.Millisecond,
		LongPollWait:      time.Millisecond,
		HeartbeatInterval: time.Hour,
	}
	if err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("run err = %v", err)
	}
	if len(client.results) != 1 || client.results[0].Status != "succeeded" {
		t.Fatalf("result = %#v", client.results)
	}
	if client.heartbeats == 0 {
		t.Fatal("heartbeat was not sent")
	}
}

func TestRunnerExecutesNodeLifecycleLeaseAndReportsResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &fakeClient{cancel: cancel, nodeLeases: []cloudrelay.NodeLifecycleLease{{
		Kind: "node_lifecycle", ID: "nlj-1", Action: "drain", TargetNodeID: "node-target", TargetName: "node-a", LeaseToken: "lease-1",
	}}}
	runner := Runner{
		Client:            client,
		Engine:            fakeEngine{},
		NodeLifecycle:     fakeLifecycle{},
		NodeID:            "node-1",
		PollInterval:      time.Millisecond,
		LongPollWait:      time.Millisecond,
		HeartbeatInterval: time.Hour,
	}
	if err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("run err = %v", err)
	}
	if len(client.nodeResults) != 1 || client.nodeResults[0].Status != "completed" || !client.nodeResults[0].Verified || client.nodeResults[0].LeaseToken != "lease-1" {
		t.Fatalf("node result = %#v", client.nodeResults)
	}
}

func TestRequestFromLeaseRejectsImageSource(t *testing.T) {
	_, err := RequestFromLease(cloudrelay.DeploymentLease{Service: cloudrelay.ServiceEnvelope{SourceType: "image", Image: "example/api:latest"}}, config.DeploymentConfig{ProjectID: "proj"})
	if !errors.Is(err, errImageSourceUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestRequestFromLeaseUsesDeploymentIntentBeforeConfig(t *testing.T) {
	req, err := RequestFromLease(cloudrelay.DeploymentLease{
		Deployment: cloudrelay.DeploymentJobEnvelope{DeploymentIntent: &cloudrelay.DeploymentIntent{
			ProjectID: "intent-proj",
			Source: cloudrelay.DeploymentIntentSource{
				Type:         "git",
				RepoURL:      "https://example.test/intent.git",
				Branch:       "intent-main",
				GitSHA:       "0123456789abcdef",
				BuildContext: "services/api",
				Dockerfile:   "Dockerfile.intent",
				ManifestPath: "deploy/intent",
				WatchPaths:   []string{"services/api/**"},
			},
			Runtime:   cloudrelay.DeploymentIntentRuntime{ContainerPort: 9090, Replicas: 3},
			Health:    cloudrelay.DeploymentIntentHealth{Path: "/ready"},
			Resources: map[string]any{"requests": map[string]string{"cpu": "250m"}, "limits": map[string]string{"memory": "768Mi"}},
			Bindings:  []cloudrelay.DeploymentIntentBinding{{ServiceID: "svc-db", Alias: "primary-db", EnvPrefix: "DB", ExposeAsDefault: true, EnvKeys: []string{"DATABASE_URL"}}},
		}},
		Service: cloudrelay.ServiceEnvelope{ID: "api", Name: "api", Type: "application", SourceType: "git", RepoURL: "https://example.test/service.git", Branch: "service-main", GitSHA: "service-sha", BuildContext: "service", Dockerfile: "Dockerfile.service", ManifestPath: "deploy/service", WatchPaths: []string{"service/**"}, Namespace: "default"},
	}, config.DeploymentConfig{ProjectID: "cfg-proj", RepoURL: "https://example.test/cfg.git", Branch: "cfg-main", BuildContext: "cfg", Dockerfile: "Dockerfile.cfg", ManifestPath: "deploy/cfg", WatchPaths: []string{"cfg/**"}, Namespace: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if req.ProjectID != "intent-proj" || req.RepoURL != "https://example.test/intent.git" || req.Branch != "intent-main" || req.GitSHA != "0123456789abcdef" || req.BuildContext != "services/api" || req.Dockerfile != "Dockerfile.intent" || req.ManifestPath != "deploy/intent" {
		t.Fatalf("request used fallback: %+v", req)
	}
	if len(req.WatchPaths) != 1 || req.WatchPaths[0] != "services/api/**" {
		t.Fatalf("watch paths = %#v", req.WatchPaths)
	}
	if req.ContainerPort != 9090 || req.HealthPath != "/ready" || req.Replicas != 3 || req.ResourceRequestsJSON != `{"cpu":"250m"}` || req.ResourceLimitsJSON != `{"memory":"768Mi"}` {
		t.Fatalf("runtime/resources = %+v", req)
	}
	if len(req.DependsOn) != 1 || req.DependsOn[0].Name != "primary-db" || req.DependsOn[0].EnvPrefix != "DB" || len(req.DependsOn[0].EnvKeys) != 1 {
		t.Fatalf("depends_on = %#v", req.DependsOn)
	}
}

func TestResultFromRecordIncludesIntentHash(t *testing.T) {
	result := ResultFromRecord(deploy.Record{Status: deploy.StatusSuccess}, nil, cloudrelay.DeploymentLease{Deployment: cloudrelay.DeploymentJobEnvelope{DeploymentIntent: &cloudrelay.DeploymentIntent{Review: cloudrelay.DeploymentIntentReview{IntentHash: "sha256:intent"}}}})
	if result.IntentHash != "sha256:intent" {
		t.Fatalf("intent hash = %q", result.IntentHash)
	}
}

func TestResultFromRecordRedactsFailureMessage(t *testing.T) {
	secret := "top-secret-token"
	result := ResultFromRecord(deploy.Record{Status: deploy.StatusFailed}, errors.New("kubectl failed password="+secret+" https://user:"+secret+"@example.test/repo.git"), cloudrelay.DeploymentLease{})
	if strings.Contains(result.FailureMessageRedacted, secret) {
		t.Fatalf("failure message leaked secret: %q", result.FailureMessageRedacted)
	}
}
