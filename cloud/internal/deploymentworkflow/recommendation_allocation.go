package deploymentworkflow

import (
	"sort"

	"github.com/opsi-dev/opsi/cloud/internal/repositoryanalysis"
)

const (
	minimumApplicationCPURequest    = int64(100)
	minimumApplicationMemoryRequest = int64(128 << 20)
)

type perReplicaProposal struct {
	CPURequest    int64
	CPULimit      int64
	MemoryRequest int64
	MemoryLimit   int64
}

func recommendApplicationResources(plan Plan, runtimeID, nodeID string, totalCPU, availableCPU, availableMemory int64, target TargetCapacityInfo, projection BudgetProjection, basis RecommendationBasis) Recommendation {
	applications := append([]repositoryanalysis.Application(nil), plan.Applications...)
	sort.Slice(applications, func(i, j int) bool { return applications[i].Key < applications[j].Key })
	if len(applications) == 0 {
		projection.RemainingBudget = projection.AvailableForRun
		return eligibleRecommendation(runtimeID, nodeID, target, projection, basis, []ApplicationRecommendation{})
	}

	totalReplicas := applicationReplicaCount(applications)
	maxMemoryLimit := availableMemory * 90 / 100
	if availableCPU < minimumApplicationCPURequest*totalReplicas || maxMemoryLimit < minimumApplicationMemoryRequest*totalReplicas {
		projection.RemainingBudget = projection.AvailableForRun
		return Recommendation{
			Eligible:       false,
			RuntimeID:      runtimeID,
			NodeID:         nodeID,
			TargetCapacity: target,
			Projection:     projection,
			Basis:          basis,
			Reason:         "Available capacity cannot satisfy the minimum required 100m CPU and 128 MiB RAM per replica.",
			Warnings:       []string{"Insufficient headroom for safe baseline allocation."},
		}
	}

	proposal := calculatePerReplicaProposal(totalCPU, availableCPU, availableMemory, maxMemoryLimit, totalReplicas)
	recommendations, allocatedCPU, allocatedMemory := applicationRecommendations(plan, applications, proposal)
	projection.RemainingBudget = ResourceBudget{
		CPUMillicores: availableCPU - allocatedCPU,
		MemoryBytes:   availableMemory - allocatedMemory,
	}
	return eligibleRecommendation(runtimeID, nodeID, target, projection, basis, recommendations)
}

func applicationReplicaCount(applications []repositoryanalysis.Application) int64 {
	var total int64
	for _, application := range applications {
		replicas := int64(application.Capacity.Replicas)
		if replicas < 1 {
			replicas = 1
		}
		total += replicas
	}
	return total
}

func calculatePerReplicaProposal(totalCPU, availableCPU, availableMemory, maxMemoryLimit, replicas int64) perReplicaProposal {
	cpuRequest := availableCPU * 60 / 100 / replicas
	if cpuRequest < minimumApplicationCPURequest {
		cpuRequest = minimumApplicationCPURequest
	}
	if cpuRequest > 500 {
		cpuRequest = 500
	}

	memoryRequest := roundDownMemory32MiB(availableMemory * 70 / 100 / replicas)
	if memoryRequest < minimumApplicationMemoryRequest {
		memoryRequest = minimumApplicationMemoryRequest
	}
	memoryLimit := memoryRequest * 2
	if memoryLimit*replicas > maxMemoryLimit {
		memoryLimit = roundDownMemory32MiB(maxMemoryLimit / replicas)
	}
	if memoryLimit < memoryRequest {
		memoryLimit = memoryRequest
	}

	cpuLimit := totalCPU
	if replicas > 1 {
		cpuLimit = availableCPU
	}
	if cpuLimit < cpuRequest {
		cpuLimit = cpuRequest
	}
	if cpuLimit > 1500 && totalCPU <= 2000 {
		cpuLimit = 1500
	}
	return perReplicaProposal{CPURequest: cpuRequest, CPULimit: cpuLimit, MemoryRequest: memoryRequest, MemoryLimit: memoryLimit}
}

func applicationRecommendations(plan Plan, applications []repositoryanalysis.Application, proposal perReplicaProposal) ([]ApplicationRecommendation, int64, int64) {
	recommendations := make([]ApplicationRecommendation, 0, len(applications))
	var allocatedCPU, allocatedMemory int64
	for _, application := range applications {
		replicas := application.Capacity.Replicas
		if replicas < 1 {
			replicas = 1
		}
		current := currentApplicationResources(plan.Target, application.Capacity)
		recommendations = append(recommendations, ApplicationRecommendation{
			Key:      application.Key,
			Name:     application.Name,
			Replicas: replicas,
			Current:  current,
			Proposed: ApplicationResourceValues{
				CPURequestMilli:    proposal.CPURequest,
				CPULimitMilli:      proposal.CPULimit,
				MemoryRequestBytes: proposal.MemoryRequest,
				MemoryLimitBytes:   proposal.MemoryLimit,
			},
		})
		allocatedCPU += proposal.CPURequest * int64(replicas)
		allocatedMemory += proposal.MemoryLimit * int64(replicas)
	}
	return recommendations, allocatedCPU, allocatedMemory
}

func currentApplicationResources(target Target, capacity repositoryanalysis.Capacity) ApplicationResourceValues {
	cpuRequest := firstPositiveInt64(capacity.CPUMilli, target.CPUMilli, minimumApplicationCPURequest)
	cpuLimit := firstPositiveInt64(capacity.CPULimitMilli, target.CPULimitMilli, cpuRequest)
	memoryRequest := firstPositiveInt64(capacity.MemoryBytes, target.MemoryBytes, 256<<20)
	memoryLimit := firstPositiveInt64(capacity.MemoryLimitBytes, target.MemoryLimitBytes, memoryRequest)
	return ApplicationResourceValues{
		CPURequestMilli:    cpuRequest,
		CPULimitMilli:      cpuLimit,
		MemoryRequestBytes: memoryRequest,
		MemoryLimitBytes:   memoryLimit,
	}
}

func eligibleRecommendation(runtimeID, nodeID string, target TargetCapacityInfo, projection BudgetProjection, basis RecommendationBasis, applications []ApplicationRecommendation) Recommendation {
	return Recommendation{
		Eligible:       true,
		RuntimeID:      runtimeID,
		NodeID:         nodeID,
		TargetCapacity: target,
		Projection:     projection,
		Basis:          basis,
		Applications:   applications,
	}
}

func roundDownMemory32MiB(value int64) int64 {
	return ((value >> 20) / 32 * 32) << 20
}
