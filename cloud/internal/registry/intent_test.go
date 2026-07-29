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
	var replacementNode *Node
	for index := range nodes {
		if nodes[index].ID == replacement.NodeID {
			replacementNode = &nodes[index]
			break
		}
	}
	if err != nil || len(nodes) != 2 || replacementNode == nil || replacementNode.Name == "203.0.113.10" {
		t.Fatalf("replacement node identity is not distinct: err=%v nodes=%+v", err, nodes)
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
