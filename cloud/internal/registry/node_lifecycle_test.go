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
