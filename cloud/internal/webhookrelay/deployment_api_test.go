package webhookrelay

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/auth"
	"github.com/opsi-dev/opsi/cloud/internal/registry"
	buildrecordv1 "github.com/opsi-dev/opsi/contracts/go/buildrecordv1"
	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
	exposurev1 "github.com/opsi-dev/opsi/contracts/go/exposurev1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

func TestDecodeStrictDeploymentJSONRejectsRawKubernetesAndUnknownFields(t *testing.T) {
	for _, body := range []string{
		`{"schema_version":"opsi.deployment_job/v1","build_record_id":"br-1","environment_id":"env-1","raw_yaml":"apiVersion: v1","workload":{}}`,
		`{"schema_version":"opsi.deployment_job/v1","build_record_id":"br-1","environment_id":"env-1","workload":{"schema_version":"opsi.workload_spec/v1","service_key":"api","replicas":1,"application_container_name":"app","container_port":8080,"resources":{"requests":{"cpu":"100m","memory":"128Mi"},"limits":{"cpu":"500m","memory":"512Mi"}},"termination_grace_period_seconds":30,"exposure":{"mode":"internal"},"hostNetwork":true}}`,
	} {
		request := httptest.NewRequest("POST", "/api/projects/proj/deployments/preview", bytes.NewBufferString(body))
		response := httptest.NewRecorder()
		var value deploymentv1.CreateRequest
		if decodeStrictDeploymentJSON(response, request, &value) {
			t.Fatalf("accepted unsafe deployment body: %s", body)
		}
		if response.Code != 400 {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

func TestWorkloadMatchesTopologyRequiresExactRequestsAndExposure(t *testing.T) {
	workload := deploymentv1.WorkloadSpec{SchemaVersion: deploymentv1.WorkloadSchemaVersion, ServiceKey: "api", Replicas: 2, ApplicationContainerName: deploymentv1.ApplicationContainer, ContainerPort: 8080, Resources: deploymentv1.Resources{Requests: deploymentv1.ResourceValues{CPU: "250m", Memory: "256Mi"}, Limits: deploymentv1.ResourceValues{CPU: "500m", Memory: "512Mi"}}, TerminationGracePeriodSecond: 30, Exposure: deploymentv1.ExposureIntent{Mode: "internal"}}
	assignment := topologyv1.Assignment{ServiceKey: "api", EnvironmentID: "env", RuntimeID: "runtime", Replicas: 2, CPURequestMillicores: 250, MemoryRequestBytes: 256 * 1024 * 1024, Exposure: topologyv1.ExposureIntent{Mode: "internal"}}
	if !workloadMatchesTopology(workload, assignment) {
		t.Fatal("exact topology-compatible workload was rejected")
	}
	weaker := workload
	weaker.Resources.Requests.CPU = "100m"
	if workloadMatchesTopology(weaker, assignment) {
		t.Fatal("weaker client CPU request was accepted")
	}
	public := assignment
	public.Exposure.Mode = "public"
	if workloadMatchesTopology(workload, public) {
		t.Fatal("R5-010 accepted an external exposure assignment")
	}
}

func TestDeploymentPayloadHashNormalizesEnvironmentOrder(t *testing.T) {
	first := deploymentv1.WorkloadSpec{SchemaVersion: deploymentv1.WorkloadSchemaVersion, ServiceKey: "api", Replicas: 1, ApplicationContainerName: deploymentv1.ApplicationContainer, ContainerPort: 8080, Resources: deploymentv1.Resources{Requests: deploymentv1.ResourceValues{CPU: "100m", Memory: "128Mi"}, Limits: deploymentv1.ResourceValues{CPU: "500m", Memory: "512Mi"}}, TerminationGracePeriodSecond: 30, Environment: []deploymentv1.EnvironmentVariable{{Name: "B", Value: "2"}, {Name: "A", Value: "1"}}, Exposure: deploymentv1.ExposureIntent{Mode: "internal"}}
	second := first
	second.Environment = []deploymentv1.EnvironmentVariable{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}}
	if hashDeploymentPayload("br-1", "env-1", first) != hashDeploymentPayload("br-1", "env-1", second) {
		t.Fatal("normalized replay payload hashes differ")
	}
	second.ContainerPort++
	if hashDeploymentPayload("br-1", "env-1", first) == hashDeploymentPayload("br-1", "env-1", second) {
		t.Fatal("conflicting replay payload hashes match")
	}
}

func TestDeploymentIdempotencyKeyIsBoundedAndWhitespaceFree(t *testing.T) {
	for _, value := range []string{"", "has space", "line\nbreak", string(make([]byte, 129))} {
		if validDeploymentIdempotencyKey(value) {
			t.Fatalf("accepted invalid idempotency key %q", value)
		}
	}
	if !validDeploymentIdempotencyKey("r5-010:api:immutable-001") {
		t.Fatal("rejected valid bounded idempotency key")
	}
}

func TestExposureAPIIsProjectScopedStrictIdempotentAndSanitized(t *testing.T) {
	server := NewServer(Config{})
	store := server.Registry.(*registry.Service)
	project, err := store.CreateProject("org-1", "Exposure", "exposure", "owner", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.UpsertNode(project.ID, "node-1", "server", registry.NodeHealthy, "203.0.113.10", "", "node-key")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.RegisterAgent(project.ID, node.ID, "sha256:test", "hash", "v1", "agent-key", map[string]any{"deploy": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordAgentHeartbeat(project.ID, node.ID, registry.AgentHeartbeat{Version: "v1", NodeReady: true, K3SStatus: "ready", Capabilities: map[string]any{"deploy": true}}); err != nil {
		t.Fatal(err)
	}
	service, err := store.CreateService(project.ID, registry.ServiceDraft{Name: "api", Type: "application", SourceType: "git", RepoURL: "https://example.test/repo.git", Branch: "main", GitSHA: strings.Repeat("a", 40), BuildContext: "services/api", Dockerfile: "Dockerfile", ManifestPath: "deploy/api.yaml"}, "service-key")
	if err != nil {
		t.Fatal(err)
	}
	workload := deploymentv1.WorkloadSpec{SchemaVersion: deploymentv1.WorkloadSchemaVersion, ServiceKey: service.Name, Replicas: 1, ApplicationContainerName: deploymentv1.ApplicationContainer, ContainerPort: 8080, Resources: deploymentv1.Resources{Requests: deploymentv1.ResourceValues{CPU: "100m", Memory: "128Mi"}, Limits: deploymentv1.ResourceValues{CPU: "500m", Memory: "512Mi"}}, TerminationGracePeriodSecond: 30, Exposure: deploymentv1.ExposureIntent{Mode: "internal"}}
	workloadHash, _ := workload.Hash()
	image, _ := deploymentv1.NewImmutableImage("ghcr.io/example/api", "sha256:"+strings.Repeat("a", 64))
	snapshot := deploymentv1.JobSnapshot{SchemaVersion: deploymentv1.JobSchemaVersion, ProjectID: project.ID, Image: image, Workload: workload, SpecHash: workloadHash, PayloadHash: "base-payload", Authority: deploymentv1.AuthoritySnapshot{BuildRecord: buildrecordv1.Record{SchemaVersion: buildrecordv1.SchemaVersion, ID: "br-1", ProjectID: project.ID, ServiceID: service.ID, ServiceKey: service.Name, ActiveBindingID: "binding-1", Build: buildrecordv1.BuildMetadata{OCIRepository: image.Repository, OCIDigest: image.Digest, Status: "succeeded"}}, TopologyPlanID: "topology-1", TopologyRevision: 1, TopologyHash: strings.Repeat("1", 64), DeploymentPolicyID: "policy-1", DeploymentPolicyRevision: 1, DeploymentPolicyHash: strings.Repeat("2", 64), RoutingDecisionHash: strings.Repeat("3", 64), EnvironmentID: service.EnvironmentID, RuntimeID: service.RuntimeID, NodeID: node.ID, AgentID: agent.ID}}
	baseJob, _, err := store.StartImmutableDeployment(snapshot, "owner", "base-key", "base-request")
	if err != nil {
		t.Fatal(err)
	}
	baseLease, ok, err := store.LeaseDeployment(project.ID, node.ID)
	if err != nil || !ok {
		t.Fatalf("base lease ok=%v err=%v", ok, err)
	}
	base, err := store.CompleteDeployment(project.ID, node.ID, baseJob.ID, "base-result", registry.DeploymentResult{SchemaVersion: deploymentv1.ResultSchemaVersion, Status: deploymentv1.StateSucceeded, LeaseToken: baseLease.LeaseToken, SpecHash: snapshot.SpecHash, ApplicationImage: image.Reference, ApplicationImageID: "containerd://" + image.Digest, AvailableReplicas: 1})
	if err != nil {
		t.Fatal(err)
	}
	exposure, err := (exposurev1.ExposureSpec{SchemaVersion: exposurev1.SchemaVersion, ProjectID: project.ID, EnvironmentID: base.EnvironmentID, RuntimeID: base.RuntimeID, ServiceKey: workload.ServiceKey, DeploymentJobID: "dep-exposure", Hostname: "api.example.com", Path: "/", ServicePort: workload.ContainerPort, TLS: exposurev1.TLSConfig{Mode: exposurev1.TLSDisabled}}).Canonicalize()
	if err != nil {
		t.Fatal(err)
	}
	mutation := deploymentv1.ExposureMutationRequest{SchemaVersion: deploymentv1.ExposureMutationVersion, BaseDeploymentJobID: base.ID, Exposure: exposure}
	body, _ := json.Marshal(mutation)
	ownerHash, _ := auth.HashPAT("owner-pat")
	viewerHash, _ := auth.HashPAT("viewer-pat")
	foreignHash, _ := auth.HashPAT("foreign-pat")
	server.Auth = &auth.Service{Store: auth.MemoryStore{Candidates: []auth.Candidate{
		{UserID: "owner", OrgID: "org-1", ProjectID: project.ID, Role: "Owner", Hash: ownerHash, ExpiresAt: time.Now().Add(time.Hour)},
		{UserID: "viewer", OrgID: "org-1", ProjectID: project.ID, Role: "Viewer", Hash: viewerHash, ExpiresAt: time.Now().Add(time.Hour)},
		{UserID: "foreign", OrgID: "org-2", ProjectID: "foreign-project", Role: "Owner", Hash: foreignHash, ExpiresAt: time.Now().Add(time.Hour)},
	}}}
	handler := server.Handler()

	call := func(method, path, token, key string, requestBody []byte) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, bytes.NewReader(requestBody))
		request.Header.Set("Authorization", "Bearer "+token)
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
			request.Header.Set("X-Request-ID", "request-1")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	preview := call(http.MethodPost, "/api/projects/"+project.ID+"/exposures/preview", "viewer-pat", "", body)
	if preview.Code != http.StatusOK || !bytes.Contains(preview.Body.Bytes(), []byte(`"eligible":true`)) {
		t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	viewerApply := call(http.MethodPost, "/api/projects/"+project.ID+"/exposures", "viewer-pat", "viewer-key", body)
	if viewerApply.Code != http.StatusForbidden {
		t.Fatalf("viewer apply status=%d body=%s", viewerApply.Code, viewerApply.Body.String())
	}
	missingKey := call(http.MethodPost, "/api/projects/"+project.ID+"/exposures", "owner-pat", "", body)
	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf("missing key status=%d body=%s", missingKey.Code, missingKey.Body.String())
	}
	created := call(http.MethodPost, "/api/projects/"+project.ID+"/exposures", "owner-pat", "exposure-key", body)
	if created.Code != http.StatusAccepted || bytes.Contains(created.Body.Bytes(), []byte("owner-pat")) || bytes.Contains(created.Body.Bytes(), []byte("lease_token")) || bytes.Contains(created.Body.Bytes(), []byte("raw_manifest")) {
		t.Fatalf("created status=%d body=%s", created.Code, created.Body.String())
	}
	replay := call(http.MethodPost, "/api/projects/"+project.ID+"/exposures", "owner-pat", "exposure-key", body)
	if replay.Code != http.StatusAccepted || !bytes.Contains(replay.Body.Bytes(), []byte(`"reused":true`)) {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	detail := call(http.MethodGet, "/api/projects/"+project.ID+"/exposures/"+exposure.DeploymentJobID, "viewer-pat", "", nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	foreign := call(http.MethodGet, "/api/projects/"+project.ID+"/exposures/"+exposure.DeploymentJobID, "foreign-pat", "", nil)
	if foreign.Code != http.StatusForbidden {
		t.Fatalf("foreign detail status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	unsafe := append(bytes.TrimSuffix(body, []byte("}")), []byte(`,"raw_manifest":"apiVersion: v1"}`)...)
	strict := call(http.MethodPost, "/api/projects/"+project.ID+"/exposures/preview", "owner-pat", "", unsafe)
	if strict.Code != http.StatusBadRequest {
		t.Fatalf("unsafe status=%d body=%s", strict.Code, strict.Body.String())
	}
}
