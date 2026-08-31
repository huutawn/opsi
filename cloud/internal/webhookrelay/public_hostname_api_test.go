package webhookrelay

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/cloudflare"
	"github.com/opsi-dev/opsi/cloud/internal/publichostname"
)

type successfulCloudflare struct{}

func (successfulCloudflare) ReconcileARecord(_ context.Context, hostname, ip, allocationID string) (cloudflare.Record, error) {
	return cloudflare.Record{ID: "record-" + allocationID, Type: "A", Name: hostname, Content: ip, Proxied: true, TTL: 1, Comment: cloudflare.Marker(allocationID)}, nil
}
func (successfulCloudflare) DeleteARecord(context.Context, string, string, string) error {
	return nil
}
func (successfulCloudflare) ReconcileZoneRules(context.Context) error { return nil }

func TestDeveloperCanReleaseProjectHostnameAndQuotaReturns(t *testing.T) {
	server := NewServer(Config{DeploymentDomain: "test.example.com", PublicHostnameLimit: 3})
	project, err := server.Registry.CreateProject("org", "project", "project", "owner", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	facts, err := server.Registry.PlacementFacts(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	allocation, _, err := server.PublicHostnames.Reserve(t.Context(), publichostname.ReserveRequest{Hostname: "app.test.example.com", OwnerUserID: "owner", ProjectID: project.ID, EnvironmentID: facts.Environments[0].ID, RuntimeID: facts.Runtimes[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	ownerHash, _ := auth.HashPAT("owner-pat")
	developerHash, _ := auth.HashPAT("developer-pat")
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{
		{UserID: "owner", OrgID: "org", ProjectID: project.ID, Role: "Owner", Hash: ownerHash, ExpiresAt: time.Now().Add(time.Hour)},
		{UserID: "developer", OrgID: "org", ProjectID: project.ID, Role: "Developer", Hash: developerHash, ExpiresAt: time.Now().Add(time.Hour)},
	}}}
	handler := server.Handler()

	quotaRequest := httptest.NewRequest(http.MethodGet, "/api/projects/"+project.ID+"/public-hostnames", nil)
	quotaRequest.Header.Set("Authorization", "Bearer owner-pat")
	quotaResponse := httptest.NewRecorder()
	handler.ServeHTTP(quotaResponse, quotaRequest)
	if quotaResponse.Code != http.StatusOK || !bytes.Contains(quotaResponse.Body.Bytes(), []byte(`"used":1`)) {
		t.Fatalf("quota status=%d body=%s", quotaResponse.Code, quotaResponse.Body.String())
	}

	releaseRequest := httptest.NewRequest(http.MethodPost, "/api/projects/"+project.ID+"/public-hostnames/"+allocation.ID+"/release", bytes.NewBufferString(`{}`))
	releaseRequest.Header.Set("Authorization", "Bearer developer-pat")
	releaseRequest.Header.Set("Idempotency-Key", "release-key")
	releaseRequest.Header.Set("X-Request-ID", "release-request")
	releaseResponse := httptest.NewRecorder()
	handler.ServeHTTP(releaseResponse, releaseRequest)
	if releaseResponse.Code != http.StatusOK || !bytes.Contains(releaseResponse.Body.Bytes(), []byte(`"status":"released"`)) {
		t.Fatalf("release status=%d body=%s", releaseResponse.Code, releaseResponse.Body.String())
	}
	quota, err := server.PublicHostnames.Quota(t.Context(), "owner")
	if err != nil || quota.Used != 0 || quota.Remaining != 3 {
		t.Fatalf("quota=%+v err=%v", quota, err)
	}
}

func TestRetryPublicationReResolvesVerifiedNodeIPv4(t *testing.T) {
	server := NewServer(Config{DeploymentDomain: "test.example.com", PublicHostnameLimit: 3})
	server.Cloudflare = successfulCloudflare{}
	project, err := server.Registry.CreateProject("org", "project", "project", "owner", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	facts, err := server.Registry.PlacementFacts(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.Registry.UpsertNode(project.ID, "node-1", "server", "healthy", "203.0.113.10", "agent-1", "node-key"); err != nil {
		t.Fatal(err)
	}
	allocation, _, err := server.PublicHostnames.Reserve(t.Context(), publichostname.ReserveRequest{Hostname: "app.test.example.com", OwnerUserID: "owner", ProjectID: project.ID, EnvironmentID: facts.Environments[0].ID, RuntimeID: facts.Runtimes[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	allocation, err = server.PublicHostnames.PublicationFailed(t.Context(), allocation.ID, "", "CLOUDFLARE_DNS_FAILED", "DNS publication failed.")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := server.retryPublicHostname(t.Context(), allocation)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != publichostname.StatusActive || updated.TargetIP != "203.0.113.10" || updated.CloudflareRecordID == "" {
		t.Fatalf("updated=%+v", updated)
	}
}
