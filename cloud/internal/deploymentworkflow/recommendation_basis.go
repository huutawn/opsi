package deploymentworkflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

func ComputeCapacityStateHash(runtime topologyv1.RuntimeFact, node topologyv1.NodeFact, agent topologyv1.AgentFact, totalCPU, totalMem, resCPU, resMem int64, assignments []topologyv1.Assignment, resources []resourcev1.Resource) string {
	type topologyEntry struct {
		Key, RuntimeID                      string
		CPURequest, CPULimit, MemoryRequest int64
		MemoryLimit                         int64
		Replicas                            int32
	}
	type resourceEntry struct {
		ID, Environment, Name, Type string
		Lifecycle                   resourcev1.LifecycleState
		Spec                        resourcev1.ManagedSpec
	}

	topologyEntries := make([]topologyEntry, 0, len(assignments))
	for _, assignment := range assignments {
		topologyEntries = append(topologyEntries, topologyEntry{
			Key: assignment.ServiceKey, RuntimeID: assignment.RuntimeID,
			CPURequest: assignment.CPURequestMillicores, CPULimit: assignment.CPULimitMillicores,
			MemoryRequest: assignment.MemoryRequestBytes, MemoryLimit: assignment.MemoryLimitBytes,
			Replicas: assignment.Replicas,
		})
	}
	sort.Slice(topologyEntries, func(i, j int) bool {
		if topologyEntries[i].RuntimeID != topologyEntries[j].RuntimeID {
			return topologyEntries[i].RuntimeID < topologyEntries[j].RuntimeID
		}
		return topologyEntries[i].Key < topologyEntries[j].Key
	})

	resourceEntries := make([]resourceEntry, 0, len(resources))
	for _, resource := range resources {
		if resource.Managed != nil {
			resourceEntries = append(resourceEntries, resourceEntry{
				ID: resource.ID, Environment: resource.EnvironmentID, Name: resource.Name, Type: string(resource.Type),
				Lifecycle: resource.Lifecycle, Spec: *resource.Managed,
			})
		}
	}
	sort.Slice(resourceEntries, func(i, j int) bool {
		left := resourceEntries[i].Environment + "\x00" + resourceEntries[i].Name + "\x00" + resourceEntries[i].ID
		right := resourceEntries[j].Environment + "\x00" + resourceEntries[j].Name + "\x00" + resourceEntries[j].ID
		return left < right
	})

	payload, _ := json.Marshal(struct {
		Runtime                                            topologyv1.RuntimeFact
		NodeID                                             string
		NodeStatus                                         string
		AgentID                                            string
		AgentStatus                                        string
		TotalCPU, TotalMemory, ReservedCPU, ReservedMemory int64
		Assignments                                        []topologyEntry
		Resources                                          []resourceEntry
	}{
		Runtime: runtime, NodeID: node.ID, NodeStatus: node.Status, AgentID: agent.ID, AgentStatus: agent.Status,
		TotalCPU: totalCPU, TotalMemory: totalMem, ReservedCPU: resCPU, ReservedMemory: resMem,
		Assignments: topologyEntries, Resources: resourceEntries,
	})
	return sha256Hex(payload)
}

func ComputeBasisHash(runRevision uint64, planHash string, topologyRevision uint64, topologyHash, capacityStateHash string) string {
	payload, _ := json.Marshal(struct {
		RunRevision, TopologyRevision             uint64
		PlanHash, TopologyHash, CapacityStateHash string
	}{runRevision, topologyRevision, planHash, topologyHash, capacityStateHash})
	return sha256Hex(payload)
}

func sha256Hex(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
