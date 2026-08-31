package deploymentworkflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/opsi-dev/opsi/cloud/internal/topology"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
	topologyv1 "github.com/opsi-dev/opsi/contracts/go/topologyv1"
)

type TopologyAuthority interface {
	Get(ctx context.Context, projectID string) (topologyv1.Plan, error)
	GetOperatorCapacity(ctx context.Context, projectID, runtimeID string) (topologyv1.OperatorCapacity, error)
}

type PlacementFactsAuthority interface {
	PlacementFacts(ctx context.Context, projectID string) (topologyv1.PlacementFacts, error)
}

type ResourceAuthority interface {
	List(ctx context.Context, projectID, environmentID string) ([]resourcev1.Resource, error)
}

type ResourceBudget struct {
	CPUMillicores int64 `json:"cpu_millicores"`
	MemoryBytes   int64 `json:"memory_bytes"`
}

type BudgetProjection struct {
	RealCapacity     ResourceBudget `json:"real_capacity"`
	SystemReserve    ResourceBudget `json:"system_reserve"`
	ExistingWorkload ResourceBudget `json:"existing_workloads"`
	PlannedManaged   ResourceBudget `json:"planned_managed"`
	AvailableForRun  ResourceBudget `json:"available_for_run"`
	RemainingBudget  ResourceBudget `json:"remaining_after_proposal"`
}

type ApplicationResourceValues struct {
	CPURequestMilli    int64 `json:"cpu_request_milli"`
	CPULimitMilli      int64 `json:"cpu_limit_milli"`
	MemoryRequestBytes int64 `json:"memory_request_bytes"`
	MemoryLimitBytes   int64 `json:"memory_limit_bytes"`
}

type ApplicationRecommendation struct {
	Key      string                    `json:"key"`
	Name     string                    `json:"name"`
	Replicas int32                     `json:"replicas"`
	Current  ApplicationResourceValues `json:"current"`
	Proposed ApplicationResourceValues `json:"proposed"`
}

type TargetCapacityInfo struct {
	CPUMillicores       int64  `json:"cpu_millicores"`
	MemoryBytes         int64  `json:"memory_bytes"`
	Source              string `json:"source"`
	HeartbeatAgeSeconds int64  `json:"heartbeat_age_seconds"`
	HeartbeatFresh      bool   `json:"heartbeat_fresh"`
}

type RecommendationBasis struct {
	RunRevision       uint64    `json:"run_revision"`
	PlanHash          string    `json:"plan_hash"`
	TopologyRevision  uint64    `json:"topology_revision"`
	TopologyHash      string    `json:"topology_hash"`
	CapacityStateHash string    `json:"capacity_state_hash"`
	BasisHash         string    `json:"basis_hash"`
	ObservedAt        time.Time `json:"observed_at"`
}

type Recommendation struct {
	Eligible       bool                        `json:"eligible"`
	RuntimeID      string                      `json:"runtime_id,omitempty"`
	NodeID         string                      `json:"node_id,omitempty"`
	TargetCapacity TargetCapacityInfo          `json:"target_capacity"`
	Projection     BudgetProjection            `json:"budget_projection"`
	Basis          RecommendationBasis         `json:"basis"`
	Applications   []ApplicationRecommendation `json:"applications"`
	Warnings       []string                    `json:"warnings,omitempty"`
	Reason         string                      `json:"reason,omitempty"`
}

type RecommendationEngine struct {
	Store          Store
	Topology       TopologyAuthority
	Facts          PlacementFactsAuthority
	Resources      ResourceAuthority
	Now            func() time.Time
	HeartbeatTTL   time.Duration
	ReservedCPU    int64
	ReservedMemory int64
}

func (e RecommendationEngine) clock() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

func (e RecommendationEngine) heartbeatTTL() time.Duration {
	if e.HeartbeatTTL > 0 {
		return e.HeartbeatTTL
	}
	return 2 * time.Minute
}

func (e RecommendationEngine) defaultReservedCPU() int64 {
	if e.ReservedCPU > 0 {
		return e.ReservedCPU
	}
	return 250
}

func (e RecommendationEngine) defaultReservedMemory() int64 {
	if e.ReservedMemory > 0 {
		return e.ReservedMemory
	}
	return 256 << 20
}

func (e RecommendationEngine) Recommend(ctx context.Context, projectID, runID string) (Recommendation, error) {
	if e.Store == nil || e.Topology == nil || e.Facts == nil || e.Resources == nil {
		return Recommendation{}, errors.New("recommendation engine dependencies missing")
	}

	run, err := e.Store.Get(ctx, projectID, runID)
	if err != nil {
		return Recommendation{}, err
	}

	facts, err := e.Facts.PlacementFacts(ctx, projectID)
	if err != nil {
		return Recommendation{}, fmt.Errorf("placement facts: %w", err)
	}

	now := e.clock()
	ttl := e.heartbeatTTL()

	// 1. Select exact runtime from plan; if missing, require exactly one ready K3s runtime in environment
	runtimeID := run.Plan.Target.RuntimeID
	if runtimeID == "" {
		readyCount := 0
		var candidate string
		for _, rt := range facts.Runtimes {
			if rt.Status == "ready" && rt.Type == "k3s" {
				if run.Plan.Target.EnvironmentID == "" || rt.EnvironmentID == run.Plan.Target.EnvironmentID {
					readyCount++
					candidate = rt.ID
				}
			}
		}
		if readyCount == 0 {
			return Recommendation{
				Eligible: false,
				Reason:   "No ready K3s target runtime found for this project.",
				Warnings: []string{"Connect and verify a server before requesting resource allocation."},
			}, nil
		}
		if readyCount > 1 {
			return Recommendation{
				Eligible: false,
				Reason:   "Multiple ready target runtimes found; exact runtime selection is required.",
				Warnings: []string{"Specify the target runtime in the deployment plan."},
			}, nil
		}
		runtimeID = candidate
	}

	var targetRuntime *topologyv1.RuntimeFact
	for i := range facts.Runtimes {
		if facts.Runtimes[i].ID == runtimeID {
			targetRuntime = &facts.Runtimes[i]
			break
		}
	}
	if targetRuntime == nil || targetRuntime.Status != "ready" || targetRuntime.Type != "k3s" {
		return Recommendation{
			Eligible:  false,
			RuntimeID: runtimeID,
			Reason:    "Target runtime is not in ready status.",
			Warnings:  []string{"Target runtime status must be ready."},
		}, nil
	}

	// 2. Require exactly one healthy node and one active Agent heartbeat <= 2 minutes
	matchingNodes := make([]topologyv1.NodeFact, 0, 1)
	for _, n := range facts.Nodes {
		if n.ProjectID == projectID && n.RuntimeID == runtimeID && n.Status == "healthy" {
			matchingNodes = append(matchingNodes, n)
		}
	}
	if len(matchingNodes) == 0 {
		return Recommendation{
			Eligible:  false,
			RuntimeID: runtimeID,
			Reason:    "No healthy node found for target runtime.",
			Warnings:  []string{"Runtime has no healthy node reporting to Opsi."},
		}, nil
	}
	if len(matchingNodes) > 1 {
		return Recommendation{
			Eligible:  false,
			RuntimeID: runtimeID,
			Reason:    "Ambiguous runtime topology: multiple healthy nodes found.",
			Warnings:  []string{"Multi-node runtime recommendation is unsupported in 1-node server topology."},
		}, nil
	}
	node := matchingNodes[0]

	if node.LastSeenAt == nil || node.LastSeenAt.UTC().Add(ttl).Before(now) {
		return Recommendation{
			Eligible:  false,
			RuntimeID: runtimeID,
			NodeID:    node.ID,
			Reason:    "Node heartbeat is stale.",
			Warnings:  []string{"Target node heartbeat is older than the allowed threshold. Verify agent connectivity."},
		}, nil
	}

	matchingAgents := make([]topologyv1.AgentFact, 0, 1)
	for _, a := range facts.Agents {
		if a.ProjectID == projectID && a.RuntimeID == runtimeID && a.NodeID == node.ID && a.Status == "active" {
			if a.Capabilities != nil && a.Capabilities["deploy"] == true {
				if a.LastSeenAt != nil && !a.LastSeenAt.UTC().Add(ttl).Before(now) {
					matchingAgents = append(matchingAgents, a)
				}
			}
		}
	}
	if len(matchingAgents) == 0 {
		return Recommendation{
			Eligible:  false,
			RuntimeID: runtimeID,
			NodeID:    node.ID,
			Reason:    "No active deploy Agent with fresh heartbeat found.",
			Warnings:  []string{"Deploy agent is inactive or heartbeat is stale."},
		}, nil
	}
	if len(matchingAgents) > 1 {
		return Recommendation{
			Eligible:  false,
			RuntimeID: runtimeID,
			NodeID:    node.ID,
			Reason:    "Ambiguous deploy Agent: multiple active agents found.",
			Warnings:  []string{"Node has multiple reporting deploy agents."},
		}, nil
	}

	// 3. Node capacity & System reserve
	var totalCPU, totalMemory, reservedCPU, reservedMemory int64
	source := "agent_observed"
	opCap, opErr := e.Topology.GetOperatorCapacity(ctx, projectID, runtimeID)
	if opErr == nil && opCap.CPUMillicores > 0 && opCap.MemoryBytes > 0 {
		source = opCap.Source
		totalCPU = opCap.CPUMillicores
		totalMemory = opCap.MemoryBytes
		reservedCPU = opCap.ReservedCPUMillicores
		reservedMemory = opCap.ReservedMemoryBytes
	} else {
		if opErr != nil && !errors.Is(opErr, topology.ErrNotFound) {
			return Recommendation{}, fmt.Errorf("operator capacity: %w", opErr)
		}
		if node.CPUCores <= 0 || node.MemoryMB <= 0 {
			return Recommendation{
				Eligible:  false,
				RuntimeID: runtimeID,
				NodeID:    node.ID,
				Reason:    "Target node capacity is unknown.",
				Warnings:  []string{"Target node reported zero CPU or memory capacity."},
			}, nil
		}
		totalCPU = int64(node.CPUCores) * 1000
		totalMemory = int64(node.MemoryMB) << 20
		reservedCPU = e.defaultReservedCPU()
		reservedMemory = e.defaultReservedMemory()
	}

	// 4. Current topology assignments NOT belonging to redeployed application keys
	currentTopo, topoErr := e.Topology.Get(ctx, projectID)
	if topoErr != nil && !errors.Is(topoErr, topology.ErrNotFound) {
		return Recommendation{}, fmt.Errorf("current topology: %w", topoErr)
	}
	if errors.Is(topoErr, topology.ErrNotFound) {
		currentTopo = topologyv1.Plan{}
	}
	redeployKeys := make(map[string]bool, len(run.Plan.Applications))
	for _, app := range run.Plan.Applications {
		redeployKeys[app.Key] = true
	}

	var existingWorkloadCPU, existingWorkloadMemory int64
	for _, assignment := range currentTopo.Assignments {
		if assignment.RuntimeID != runtimeID {
			continue
		}
		if redeployKeys[assignment.ServiceKey] {
			continue // Replaced by current run
		}
		reps := int64(assignment.Replicas)
		if reps < 1 {
			reps = 1
		}
		memoryCommitment := assignment.MemoryLimitBytes
		if memoryCommitment <= 0 {
			memoryCommitment = assignment.MemoryRequestBytes
		}
		existingWorkloadCPU += assignment.CPURequestMillicores * reps
		existingWorkloadMemory += memoryCommitment * reps
	}

	// 5. Planned managed resources not yet in topology; resources already in topology counted only once
	resources, resErr := e.Resources.List(ctx, projectID, run.Plan.Target.EnvironmentID)
	if resErr != nil {
		return Recommendation{}, fmt.Errorf("resource store list: %w", resErr)
	}

	resourcesByName := make(map[string]resourcev1.Resource, len(resources))
	assignedResourceRuntime := make(map[string]string, len(currentTopo.Assignments))
	for _, resource := range resources {
		resourcesByName[resource.Name] = resource
	}
	for _, assignment := range currentTopo.Assignments {
		assignedResourceRuntime[assignment.ServiceKey] = assignment.RuntimeID
	}

	var plannedManagedCPU, plannedManagedMemory int64
	for _, res := range run.Plan.Resources {
		if !res.Managed {
			continue
		}
		existingResource, exists := resourcesByName[res.LogicalName]
		requestedType := resourcev1.Type(res.Type)
		if requestedType == "valkey" {
			requestedType = resourcev1.TypeRedis
		}
		if exists && (existingResource.Kind != resourcev1.KindManagedService || existingResource.Type != requestedType || existingResource.Managed == nil) {
			return Recommendation{
				Eligible:  false,
				RuntimeID: runtimeID,
				NodeID:    node.ID,
				Reason:    "A resource name required by this run is already owned by an incompatible resource.",
				Warnings:  []string{"Rename the planned managed resource or remove the incompatible resource binding."},
			}, nil
		}
		if exists && existingResource.Managed != nil {
			if assignedRuntime, assigned := assignedResourceRuntime[existingResource.ID]; assigned && assignedRuntime != runtimeID {
				return Recommendation{
					Eligible:  false,
					RuntimeID: runtimeID,
					NodeID:    node.ID,
					Reason:    "A managed resource required by this run is assigned to another runtime.",
					Warnings:  []string{"Moving a provisioned managed resource between runtimes is unsupported."},
				}, nil
			}
			if assignedResourceRuntime[existingResource.ID] == runtimeID {
				continue // Already included once in existing workload commitments.
			}
		}
		cpuReq, memReq, replicas := plannedManagedResourceSpec(res.Type, existingResource)
		plannedManagedCPU += cpuReq * replicas
		plannedManagedMemory += memReq * replicas
	}

	if totalCPU < reservedCPU || totalMemory < reservedMemory {
		return Recommendation{
			Eligible:  false,
			RuntimeID: runtimeID,
			NodeID:    node.ID,
			Reason:    "System reserve exceeds the target node capacity.",
			Warnings:  []string{"Correct the operator capacity or reserve before applying a proposal."},
		}, nil
	}

	availableForRunCPU := totalCPU - reservedCPU - existingWorkloadCPU - plannedManagedCPU
	availableForRunMemory := totalMemory - reservedMemory - existingWorkloadMemory - plannedManagedMemory

	heartbeatAge := int64(0)
	if node.LastSeenAt != nil {
		heartbeatAge = int64(now.Sub(node.LastSeenAt.UTC()).Seconds())
	}

	capacityInfo := TargetCapacityInfo{
		CPUMillicores:       totalCPU,
		MemoryBytes:         totalMemory,
		Source:              source,
		HeartbeatAgeSeconds: heartbeatAge,
		HeartbeatFresh:      true,
	}

	projection := BudgetProjection{
		RealCapacity:     ResourceBudget{CPUMillicores: totalCPU, MemoryBytes: totalMemory},
		SystemReserve:    ResourceBudget{CPUMillicores: reservedCPU, MemoryBytes: reservedMemory},
		ExistingWorkload: ResourceBudget{CPUMillicores: existingWorkloadCPU, MemoryBytes: existingWorkloadMemory},
		PlannedManaged:   ResourceBudget{CPUMillicores: plannedManagedCPU, MemoryBytes: plannedManagedMemory},
		AvailableForRun:  ResourceBudget{CPUMillicores: availableForRunCPU, MemoryBytes: availableForRunMemory},
	}

	// Build deterministic capacity state hash (covers identity, capacity, reserve, current topology assignments, and managed resources)
	capStateHash := ComputeCapacityStateHash(*targetRuntime, node, matchingAgents[0], totalCPU, totalMemory, reservedCPU, reservedMemory, currentTopo.Assignments, resources)
	basisHash := ComputeBasisHash(run.Revision, run.Plan.Hash, currentTopo.Revision, currentTopo.PlanHash, capStateHash)

	basis := RecommendationBasis{
		RunRevision:       run.Revision,
		PlanHash:          run.Plan.Hash,
		TopologyRevision:  currentTopo.Revision,
		TopologyHash:      currentTopo.PlanHash,
		CapacityStateHash: capStateHash,
		BasisHash:         basisHash,
		ObservedAt:        now,
	}

	if availableForRunCPU <= 0 || availableForRunMemory <= 0 {
		projection.RemainingBudget = ResourceBudget{CPUMillicores: availableForRunCPU, MemoryBytes: availableForRunMemory}
		return Recommendation{
			Eligible:       false,
			RuntimeID:      runtimeID,
			NodeID:         node.ID,
			TargetCapacity: capacityInfo,
			Projection:     projection,
			Basis:          basis,
			Reason:         "Available headroom is zero or negative after accounting for system reserve, existing workloads, and managed services.",
			Warnings:       []string{"No available capacity remaining for application replicas."},
		}, nil
	}

	return recommendApplicationResources(run.Plan, runtimeID, node.ID, totalCPU, availableForRunCPU, availableForRunMemory, capacityInfo, projection, basis), nil
}
