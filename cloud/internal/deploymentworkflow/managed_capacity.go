package deploymentworkflow

import resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"

func PlannedManagedResourceCapacity(rawType string) (int64, int64) {
	resourceType := resourcev1.Type(rawType)
	if resourceType == "valkey" {
		resourceType = resourcev1.TypeRedis
	}
	switch resourceType {
	case resourcev1.TypePostgres:
		return 250, 256 << 20
	case resourcev1.TypeRedis:
		return 100, 256 << 20
	default:
		return 100, 128 << 20
	}
}

func plannedManagedResourceSpec(rawType string, existing resourcev1.Resource) (int64, int64, int64) {
	if existing.Managed != nil {
		replicas := int64(existing.Managed.Replicas)
		if replicas < 1 {
			replicas = 1
		}
		return existing.Managed.CPUMillicores, existing.Managed.MemoryBytes, replicas
	}
	cpu, memory := PlannedManagedResourceCapacity(rawType)
	return cpu, memory, 1
}
