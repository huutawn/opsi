package cloudrunner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	"github.com/opsi-dev/opsi/agent/internal/config"
	"github.com/opsi-dev/opsi/agent/internal/deploy"
)

type fakeClient struct {
	leases     []cloudrelay.DeploymentLease
	results    []cloudrelay.DeploymentResult
	heartbeats int
	cancel     context.CancelFunc
}

func (f *fakeClient) PollDeployment(context.Context, string, time.Duration) (*cloudrelay.DeploymentLease, error) {
	if len(f.leases) == 0 {
		return nil, context.Canceled
	}
	lease := f.leases[0]
	f.leases = f.leases[1:]
	return &lease, nil
}

func (f *fakeClient) CompleteDeployment(_ context.Context, _ string, _ string, result cloudrelay.DeploymentResult) error {
	f.results = append(f.results, result)
	if f.cancel != nil {
		f.cancel()
	}
	return nil
}

func (f *fakeClient) Heartbeat(context.Context, string, cloudrelay.Heartbeat) error {
	f.heartbeats++
	return nil
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

func TestRequestFromLeaseRejectsImageSource(t *testing.T) {
	_, err := RequestFromLease(cloudrelay.DeploymentLease{Service: cloudrelay.ServiceEnvelope{SourceType: "image", Image: "example/api:latest"}}, config.DeploymentConfig{ProjectID: "proj"})
	if !errors.Is(err, errImageSourceUnsupported) {
		t.Fatalf("err = %v", err)
	}
}
