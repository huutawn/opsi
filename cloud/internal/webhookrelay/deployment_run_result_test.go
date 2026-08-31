package webhookrelay

import (
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/deploymentworkflow"
	"github.com/opsi-dev/opsi/cloud/internal/publichostname"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	"github.com/opsi-dev/opsi/cloud/internal/repositoryanalysis"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
)

func TestPublishedServiceURLsUsesOnlySuccessfulExposureEvidence(t *testing.T) {
	base := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	exposure := func(hostname string, tls string) *exposurev1.ExposureSpec {
		config := exposurev1.TLSConfig{Mode: tls}
		if tls == exposurev1.TLSSecretRef {
			config.SecretReference = "tls-secret"
		}
		spec, err := (exposurev1.ExposureSpec{SchemaVersion: exposurev1.SchemaVersion, ProjectID: "proj-1", EnvironmentID: "env-1", RuntimeID: "rt-1", ServiceKey: "web", DeploymentJobID: "dep-route", Hostname: hostname, Path: "/", ServicePort: 3000, TLS: config}).Canonicalize()
		if err != nil {
			t.Fatal(err)
		}
		return &spec
	}
	urls := publishedServiceURLs([]registry.DeploymentJob{
		{ID: "failed", ServiceID: "svc-web", Mode: "rollout", Status: deploymentv1.StateFailed, RolloutState: deploymentv1.RolloutStateFailed, ExposureSpec: exposure("failed.example.test", exposurev1.TLSDisabled), UpdatedAt: base.Add(2 * time.Minute)},
		{ID: "old", ServiceID: "svc-web", Mode: "rollout", Status: deploymentv1.StateSucceeded, RolloutState: deploymentv1.RolloutStateSucceeded, ExposureSpec: exposure("old.example.test", exposurev1.TLSDisabled), UpdatedAt: base},
		{ID: "new", ServiceID: "svc-web", Mode: "rollout", Status: deploymentv1.StateSucceeded, RolloutState: deploymentv1.RolloutStateSucceeded, ExposureSpec: exposure("new.example.test", exposurev1.TLSSecretRef), UpdatedAt: base.Add(time.Minute)},
	})
	if urls["svc-web"] != "https://new.example.test/" || len(urls) != 1 {
		t.Fatalf("urls=%v", urls)
	}
}

func TestAutomaticEndpointShowsPublishingReadyAndManualPreserved(t *testing.T) {
	run := deploymentworkflow.Run{Plan: deploymentworkflow.Plan{Applications: []repositoryanalysis.Application{{Key: "web", Port: 3000, Exposure: repositoryanalysis.Exposure{Mode: "public", Automatic: true, Hostname: "tcip.test.opsidev.site", Path: "/"}}}}}
	application := deploymentRunApplicationResult{ServiceKey: "web", ServiceID: "svc-web", ContainerPort: 3000}
	allocation := &publichostname.Allocation{Status: publichostname.StatusActive}
	publishing, ok := automaticEndpoint(run, application, nil, allocation)
	if !ok || publishing.Status != "publishing" || publishing.URL != "https://tcip.test.opsidev.site/" {
		t.Fatalf("publishing=%+v ok=%v", publishing, ok)
	}
	run.PublicRouteFailures = []deploymentworkflow.PublicRouteFailure{{ServiceKey: "web", Message: "The public route could not be prepared."}}
	failed, ok := automaticEndpoint(run, application, nil, allocation)
	if !ok || failed.Status != "failed" || failed.Message == "" {
		t.Fatalf("failed=%+v ok=%v", failed, ok)
	}
	run.PublicRouteFailures = nil
	automatic, err := (exposurev1.ExposureSpec{SchemaVersion: exposurev1.SchemaVersion, ProjectID: "proj-1", EnvironmentID: "env-1", RuntimeID: "rt-1", ServiceKey: "web", DeploymentJobID: "dep-1", Hostname: "tcip.test.opsidev.site", Path: "/", ServicePort: 3000, TLS: exposurev1.TLSConfig{Mode: exposurev1.TLSDisabled}, Metadata: &exposurev1.Metadata{Rationale: automaticPublicRouteRationale}}).Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	ready, ok := automaticEndpoint(run, application, &registry.DeploymentJob{ServiceID: "svc-web", Status: deploymentv1.StateSucceeded, RolloutState: deploymentv1.RolloutStateSucceeded, ExposureSpec: &automatic}, allocation)
	if !ok || ready.Status != "ready" {
		t.Fatalf("ready=%+v ok=%v", ready, ok)
	}
	manual := automatic
	manual.Metadata = nil
	preserved, ok := automaticEndpoint(run, application, &registry.DeploymentJob{ServiceID: "svc-web", ExposureSpec: &manual}, allocation)
	if !ok || preserved.Status != "manual_preserved" {
		t.Fatalf("preserved=%+v ok=%v", preserved, ok)
	}
}
