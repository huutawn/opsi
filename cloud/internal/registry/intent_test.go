package registry

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMarkNodeOfflineReplayDoesNotChurnState(t *testing.T) {
	service, projectID := readyRegistry(t)
	nodes, err := service.ListNodes(projectID)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("nodes err=%v nodes=%+v", err, nodes)
	}
	nodeID := nodes[0].ID
	other, err := service.UpsertNode(projectID, "other", "worker", NodeHealthy, "203.0.113.11", "", "other-node")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	requestID := strings.Repeat("r", 200)
	first, reused, err := service.MarkNodeOffline(projectID, nodeID, "user-1", "node-offline:"+nodeID, requestID)
	if err != nil || reused {
		t.Fatalf("first=%+v reused=%v err=%v", first, reused, err)
	}
	agentBefore := service.agents[first.AgentID]
	runtimeBefore := service.runtimes[first.RuntimeID]
	projectBefore := service.projects[projectID]
	otherBefore := service.nodes[other.ID]
	now = now.Add(time.Minute)
	replay, reused, err := service.MarkNodeOffline(projectID, nodeID, "user-1", "node-offline:"+nodeID, "req-replay")
	if err != nil || !reused {
		t.Fatalf("replay=%+v reused=%v err=%v", replay, reused, err)
	}
	if replay != first {
		t.Fatalf("replay changed node: first=%+v replay=%+v", first, replay)
	}
	if service.agents[first.AgentID].UpdatedAt != agentBefore.UpdatedAt || service.runtimes[first.RuntimeID].UpdatedAt != runtimeBefore.UpdatedAt || service.projects[projectID].UpdatedAt != projectBefore.UpdatedAt {
		t.Fatalf("replay churned related timestamps: agent=%s/%s runtime=%s/%s project=%s/%s", agentBefore.UpdatedAt, service.agents[first.AgentID].UpdatedAt, runtimeBefore.UpdatedAt, service.runtimes[first.RuntimeID].UpdatedAt, projectBefore.UpdatedAt, service.projects[projectID].UpdatedAt)
	}
	if _, _, err := service.MarkNodeOffline(projectID, other.ID, "user-1", "node-offline:"+nodeID, "req-conflict"); apiCode(err) != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("conflicting key err=%v", err)
	}
	if service.nodes[nodeID] != first || service.nodes[other.ID] != otherBefore {
		t.Fatalf("conflict changed nodes: retired=%+v other=%+v", service.nodes[nodeID], service.nodes[other.ID])
	}

	now = now.Add(time.Minute)
	noOp, reused, err := service.MarkNodeOffline(projectID, nodeID, "user-1", "node-offline-again:"+nodeID, "req-new-key")
	if err != nil || reused || noOp != first {
		t.Fatalf("new-key no-op=%+v reused=%v err=%v", noOp, reused, err)
	}
	if service.agents[first.AgentID].UpdatedAt != agentBefore.UpdatedAt || service.runtimes[first.RuntimeID].UpdatedAt != runtimeBefore.UpdatedAt || service.projects[projectID].UpdatedAt != projectBefore.UpdatedAt {
		t.Fatal("new key against exact retired state churned related timestamps")
	}
	marked := 0
	for _, event := range service.audit {
		if event.Action == "NODE_MARKED_OFFLINE" {
			marked++
			if event.ActorUserID != "user-1" || event.ResourceID != nodeID || event.MetadataRedacted["request_id"] != requestID[:128] {
				t.Fatalf("unexpected retirement audit: %+v", event)
			}
		}
	}
	if marked != 1 {
		t.Fatalf("NODE_MARKED_OFFLINE audit count=%d events=%+v", marked, service.audit)
	}
}

func TestMarkNodeOfflineConcurrentReplayMutatesAndAuditsOnce(t *testing.T) {
	service, projectID := readyRegistry(t)
	nodes, err := service.ListNodes(projectID)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("nodes err=%v nodes=%+v", err, nodes)
	}
	const requests = 8
	results := make(chan Node, requests)
	reused := make(chan bool, requests)
	errs := make(chan error, requests)
	var wait sync.WaitGroup
	for range requests {
		wait.Add(1)
		go func() {
			defer wait.Done()
			node, replay, err := service.MarkNodeOffline(projectID, nodes[0].ID, "user-1", "concurrent-offline", "req-concurrent")
			results <- node
			reused <- replay
			errs <- err
		}()
	}
	wait.Wait()
	close(results)
	close(reused)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first Node
	for node := range results {
		if first.ID == "" {
			first = node
		} else if node != first {
			t.Fatalf("concurrent result mismatch: first=%+v got=%+v", first, node)
		}
	}
	replays := 0
	for replay := range reused {
		if replay {
			replays++
		}
	}
	marked := 0
	for _, event := range service.audit {
		if event.Action == "NODE_MARKED_OFFLINE" {
			marked++
		}
	}
	if replays != requests-1 || marked != 1 {
		t.Fatalf("concurrent replays=%d audits=%d", replays, marked)
	}
	if _, ok := service.idempotency["node-offline:v1:"+projectID+":concurrent-offline"].(string); !ok {
		t.Fatalf("missing memory idempotency binding: %+v", service.idempotency)
	}
}

func TestMarkNodeOfflineRejectsInvalidKeysBeforeMutation(t *testing.T) {
	for _, key := range []string{"", "has space", "line\nbreak", strings.Repeat("x", 129)} {
		service, projectID := readyRegistry(t)
		nodes, err := service.ListNodes(projectID)
		if err != nil || len(nodes) != 1 {
			t.Fatalf("nodes err=%v nodes=%+v", err, nodes)
		}
		before := nodes[0]
		bindings := len(service.idempotency)
		if _, _, err := service.MarkNodeOffline(projectID, before.ID, "user-1", key, "req-invalid"); apiCode(err) != "IDEMPOTENCY_KEY_INVALID" {
			t.Fatalf("key %q err=%v", key, err)
		}
		if service.nodes[before.ID] != before || len(service.audit) != 0 || len(service.idempotency) != bindings {
			t.Fatalf("key %q changed state: node=%+v audits=%+v", key, service.nodes[before.ID], service.audit)
		}
	}
}

func TestMarkNodeOfflineRevokesOldAgentAndUnblocksReplacement(t *testing.T) {
	service, projectID := readyRegistry(t)
	nodes, err := service.ListNodes(projectID)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("nodes err=%v nodes=%+v", err, nodes)
	}
	nodes[0].Name = nodes[0].PublicHost
	service.nodes[nodes[0].ID] = nodes[0]
	offline, reused, err := service.MarkNodeOffline(projectID, nodes[0].ID, "user-1", "offline-replacement", "req-offline-replacement")
	if err != nil || reused {
		t.Fatalf("offline=%+v reused=%v err=%v", offline, reused, err)
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
	record, err := service.CreateService(projectID, ServiceDraft{Name: name, Type: "application", SourceType: "git", RepoURL: "https://example.test/repo.git", Branch: "main", GitSHA: "0123456789abcdef", BuildContext: "services/" + name, Dockerfile: dockerfile, ManifestPath: manifest, WatchPaths: []string{"services/" + name + "/**"}, ContainerPort: 8080, HealthPath: "/health", ResourceRequests: map[string]string{"cpu": name + "-cpu"}, ResourceLimits: map[string]string{"memory": "512Mi"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return record
}
