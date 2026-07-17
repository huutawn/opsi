package registry

import "testing"

func TestMarkNodeOfflineRevokesOldAgentAndUnblocksReplacement(t *testing.T) {
	service, projectID := readyRegistry(t)
	nodes, err := service.ListNodes(projectID)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("nodes err=%v nodes=%+v", err, nodes)
	}
	nodes[0].Name = nodes[0].PublicHost
	service.nodes[nodes[0].ID] = nodes[0]
	offline, err := service.MarkNodeOffline(projectID, nodes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if offline.Status != NodeOffline || offline.FailureCode != "OPERATOR_CONFIRMED_TARGET_RESET" {
		t.Fatalf("unexpected offline node: %+v", offline)
	}
	replacement, err := service.CreateBootstrapSession(projectID, "first_server", "203.0.113.10", "ubuntu", "private_key", "user-1", "replacement", 22)
	if err != nil {
		t.Fatalf("replacement bootstrap remained blocked: %v", err)
	}
	nodes, err = service.ListNodes(projectID)
	if err != nil || len(nodes) != 2 || nodes[1].ID != replacement.NodeID || nodes[1].Name == "203.0.113.10" {
		t.Fatalf("replacement node identity is not distinct: err=%v nodes=%+v", err, nodes)
	}
}

func TestDeploymentIntentCarriesServiceSpecificFields(t *testing.T) {
	service, projectID := readyRegistry(t)
	api := createRegistryService(t, service, projectID, "api", "Dockerfile.api", "deploy/api", "svc-api")
	worker := createRegistryService(t, service, projectID, "worker", "Dockerfile.worker", "deploy/worker", "svc-worker")

	apiJob, err := service.StartDeployment(projectID, api.ID, "user-1", "dep-api", "req-api")
	if err != nil {
		t.Fatal(err)
	}
	workerJob, err := service.StartDeployment(projectID, worker.ID, "user-1", "dep-worker", "req-worker")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		job      DeploymentJob
		docker   string
		manifest string
	}{
		{apiJob, "Dockerfile.api", "deploy/api"},
		{workerJob, "Dockerfile.worker", "deploy/worker"},
	} {
		if tc.job.DeploymentIntent == nil {
			t.Fatal("missing deployment intent")
		}
		intent := tc.job.DeploymentIntent
		if intent.IntentVersion != DeploymentIntentVersion || intent.Source.Dockerfile != tc.docker || intent.Source.ManifestPath != tc.manifest {
			t.Fatalf("bad intent: %+v", intent)
		}
		requests, _ := intent.Resources["requests"].(map[string]string)
		if requests["cpu"] == "" || len(intent.Bindings) != 1 || intent.Bindings[0].EnvPrefix == "" || intent.Runtime.ContainerPort != 8080 || intent.Health.Path != "/health" {
			t.Fatalf("incomplete service-specific intent: %+v", intent)
		}
		if tc.job.IntentHash == "" || intent.Review.IntentHash != tc.job.IntentHash || intent.Review.ManifestHash != tc.job.ManifestHash {
			t.Fatalf("bad intent review: job=%+v intent=%+v", tc.job, intent.Review)
		}
	}
	if apiJob.IntentHash == workerJob.IntentHash {
		t.Fatal("intent hashes should differ")
	}
}

func TestDeploymentIntentValidationRejectsMissingBuildContext(t *testing.T) {
	err := validateServiceForDeploy(ServiceRecord{Type: "application", SourceType: "git", RepoURL: "https://example.test/repo.git", GitSHA: "0123456789abcdef", Dockerfile: "Dockerfile", ManifestPath: "deploy/k8s"}, "req")
	apiErr, ok := err.(APIError)
	if !ok || apiErr.Code != "SERVICE_CONFIG_INVALID" {
		t.Fatalf("err = %#v", err)
	}
}

func TestImageSourceDeploymentRejectedBeforeJobCreation(t *testing.T) {
	service, projectID := readyRegistry(t)
	imageSvc, err := service.CreateService(projectID, ServiceDraft{Name: "image-api", Type: "application", SourceType: "image", Image: "registry.example/api:latest"}, "svc-image")
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.StartDeployment(projectID, imageSvc.ID, "user-1", "dep-image", "req-image")
	apiErr, ok := err.(APIError)
	if !ok || apiErr.Code != "IMAGE_DEPLOY_NOT_SUPPORTED" {
		t.Fatalf("err = %#v", err)
	}
	deployments, err := service.ListDeployments(projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deployments) != 0 {
		t.Fatalf("deployment persisted: %+v", deployments)
	}
}

func readyRegistry(t *testing.T) (*Service, string) {
	t.Helper()
	service := NewService()
	project, err := service.CreateProject("org-1", "Demo", "demo", "user-1", "proj")
	if err != nil {
		t.Fatal(err)
	}
	node, err := service.UpsertNode(project.ID, "vps-1", "server", NodeHealthy, "203.0.113.10", "", "node")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegisterAgent(project.ID, node.ID, "sha256:test", "hash", "v1", "agent", map[string]any{"deploy": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordAgentHeartbeat(project.ID, node.ID, AgentHeartbeat{Version: "v1", NodeReady: true, K3SStatus: "ready", Capabilities: map[string]any{"deploy": true}}); err != nil {
		t.Fatal(err)
	}
	return service, project.ID
}

func createRegistryService(t *testing.T, service *Service, projectID, name, dockerfile, manifest, key string) ServiceRecord {
	t.Helper()
	record, err := service.CreateService(projectID, ServiceDraft{Name: name, Type: "application", SourceType: "git", RepoURL: "https://example.test/repo.git", Branch: "main", GitSHA: "0123456789abcdef", BuildContext: "services/" + name, Dockerfile: dockerfile, ManifestPath: manifest, WatchPaths: []string{"services/" + name + "/**"}, ContainerPort: 8080, HealthPath: "/health", ResourceRequests: map[string]string{"cpu": name + "-cpu"}, ResourceLimits: map[string]string{"memory": "512Mi"}, Bindings: []ServiceBinding{{ServiceID: "svc-db", Alias: "primary-db", EnvPrefix: "DB", ExposeAsDefault: true, EnvKeys: []string{"DATABASE_URL"}}}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return record
}
