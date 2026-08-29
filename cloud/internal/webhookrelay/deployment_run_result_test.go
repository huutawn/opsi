package webhookrelay

import (
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/registry"
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
