package registry

import "testing"

func TestNodeLifecycleMetadataMethodsFailClosed(t *testing.T) {
	service, projectID := readyRegistry(t)
	nodes, err := service.ListNodes(projectID)
	if err != nil {
		t.Fatal(err)
	}
	nodeID := nodes[0].ID

	_, err = service.DrainNode(projectID, nodeID)
	if apiErr, ok := err.(APIError); !ok || apiErr.Code != "NODE_LIFECYCLE_AGENT_REQUIRED" {
		t.Fatalf("drain err = %#v", err)
	}
	_, err = service.RemoveNode(projectID, nodeID, false)
	if apiErr, ok := err.(APIError); !ok || apiErr.Code != "ONLY_SERVER_NODE" {
		t.Fatalf("remove precondition err = %#v", err)
	}
	_, err = service.RemoveNode(projectID, nodeID, true)
	if apiErr, ok := err.(APIError); !ok || apiErr.Code != "NODE_LIFECYCLE_AGENT_REQUIRED" {
		t.Fatalf("force remove err = %#v", err)
	}

	nodes, err = service.ListNodes(projectID)
	if err != nil {
		t.Fatal(err)
	}
	if nodes[0].Status != NodeHealthy {
		t.Fatalf("node status mutated to %q", nodes[0].Status)
	}
}

func TestNodeLifecycleRequiresAgentResultForCompletion(t *testing.T) {
	service, projectID := readyRegistry(t)
	nodes, err := service.ListNodes(projectID)
	if err != nil {
		t.Fatal(err)
	}
	node := nodes[0]
	if _, err := service.RecordAgentHeartbeat(projectID, node.ID, AgentHeartbeat{Version: "v1", NodeReady: true, K3SStatus: "ready", Capabilities: map[string]any{"deploy": true, "node_lifecycle": true}}); err != nil {
		t.Fatal(err)
	}
	job, err := service.RequestNodeLifecycle(projectID, node.ID, "drain", "user-1", "drain-1", "req-1", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != NodeLifecycleRequested {
		t.Fatalf("status = %q", job.Status)
	}
	nodes, _ = service.ListNodes(projectID)
	if nodes[0].Status != NodeHealthy {
		t.Fatalf("cloud marked success before agent result: %q", nodes[0].Status)
	}
	lease, ok, err := service.LeaseNodeLifecycle(projectID, job.NodeID)
	if err != nil || !ok {
		t.Fatalf("lease ok=%v err=%v", ok, err)
	}
	job, err = service.CompleteNodeLifecycle(projectID, job.NodeID, job.ID, "req-2", NodeLifecycleResult{Status: NodeLifecycleCompleted, LeaseToken: lease.LeaseToken, Verified: false})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != NodeLifecycleFailed || job.FailureCode != "NODE_LIFECYCLE_NOT_VERIFIED" {
		t.Fatalf("unverified completion must fail, got %+v", job)
	}
	nodes, _ = service.ListNodes(projectID)
	if nodes[0].Status != NodeHealthy {
		t.Fatalf("unverified result mutated node: %q", nodes[0].Status)
	}
}

func TestNodeLifecycleVerifiedDrainUpdatesNodeAndIsIdempotent(t *testing.T) {
	service, projectID := readyRegistry(t)
	nodes, _ := service.ListNodes(projectID)
	node := nodes[0]
	_, _ = service.RecordAgentHeartbeat(projectID, node.ID, AgentHeartbeat{Version: "v1", NodeReady: true, K3SStatus: "ready", Capabilities: map[string]any{"deploy": true, "node_lifecycle": true}})
	job, err := service.RequestNodeLifecycle(projectID, node.ID, "drain", "user-1", "drain-2", "req-1", false, false)
	if err != nil {
		t.Fatal(err)
	}
	lease, ok, err := service.LeaseNodeLifecycle(projectID, job.NodeID)
	if err != nil || !ok {
		t.Fatalf("lease ok=%v err=%v", ok, err)
	}
	job, err = service.CompleteNodeLifecycle(projectID, job.NodeID, job.ID, "req-2", NodeLifecycleResult{Status: NodeLifecycleCompleted, LeaseToken: lease.LeaseToken, Verified: true})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != NodeLifecycleCompleted {
		t.Fatalf("job = %+v", job)
	}
	nodes, _ = service.ListNodes(projectID)
	if nodes[0].Status != NodeDraining {
		t.Fatalf("node status = %q", nodes[0].Status)
	}
	again, err := service.CompleteNodeLifecycle(projectID, job.NodeID, job.ID, "req-3", NodeLifecycleResult{Status: NodeLifecycleFailed, LeaseToken: "stale"})
	if err != nil || again.Status != NodeLifecycleCompleted {
		t.Fatalf("duplicate result not idempotent: %+v err=%v", again, err)
	}
}

func TestNodeLifecycleFailClosedPreconditions(t *testing.T) {
	service, projectID := readyRegistry(t)
	nodes, _ := service.ListNodes(projectID)
	node := nodes[0]
	if _, err := service.RequestNodeLifecycle(projectID, "missing", "drain", "user-1", "bad-1", "req-1", false, false); err != ErrNotFound {
		t.Fatalf("missing node err = %#v", err)
	}
	if _, err := service.RequestNodeLifecycle(projectID, node.ID, "exec", "user-1", "bad-2", "req-1", false, false); err == nil {
		t.Fatal("unsupported action succeeded")
	}
	if _, err := service.RequestNodeLifecycle(projectID, node.ID, "remove", "user-1", "bad-3", "req-1", false, true); err == nil {
		t.Fatal("remove without explicit intent succeeded")
	}
}
