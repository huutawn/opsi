package webhookrelay

import (
	"bytes"
	"net/http/httptest"
	"testing"

	deploymentv1 "github.com/opsi-dev/opsi/contracts/go/deploymentv1"
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
