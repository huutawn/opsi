package deploymentworkflow

import resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"

func PlannedManagedResourceCapacity(rawType string) (int64, int64) {
	resourceType := resourcev1.Type(rawType)
	if resourceType == "valkey" {
		resourceType = resourcev1.TypeRedis
	}
	definition, ok := resourcev1.Definition(resourceType)
	if ok && len(definition.Provisioning.Profiles) > 0 {
		defaults := definition.Provisioning.Profiles[0].ResourceDefaults
		if defaults != nil && defaults.CPUMillicores > 0 && defaults.MemoryBytes > 0 {
			return defaults.CPUMillicores, defaults.MemoryBytes
		}
	}
	return 100, 128 << 20
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
