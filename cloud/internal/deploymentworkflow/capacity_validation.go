package deploymentworkflow

import (
	"errors"
	"fmt"
	"math"

	"github.com/opsi-dev/opsi/cloud/internal/repositoryanalysis"
)

func normalizePlanCapacity(plan *Plan) error {
	if plan.Target.CPULimitMilli == 0 && plan.Target.CPUMilli > 0 {
		plan.Target.CPULimitMilli = plan.Target.CPUMilli
	}
	if plan.Target.MemoryLimitBytes == 0 && plan.Target.MemoryBytes > 0 {
		plan.Target.MemoryLimitBytes = plan.Target.MemoryBytes
	}
	if err := validateTargetCapacity(plan.Target); err != nil {
		return err
	}
	for i := range plan.Applications {
		capacity := &plan.Applications[i].Capacity
		if capacity.CPULimitMilli == 0 && capacity.CPUMilli > 0 {
			capacity.CPULimitMilli = capacity.CPUMilli
		}
		if capacity.MemoryLimitBytes == 0 && capacity.MemoryBytes > 0 {
			capacity.MemoryLimitBytes = capacity.MemoryBytes
		}
		if err := ValidateApplicationCapacity(*capacity); err != nil {
			return fmt.Errorf("application %s capacity invalid: %w", plan.Applications[i].Key, err)
		}
	}
	return nil
}

func validateTargetCapacity(target Target) error {
	capacity := repositoryanalysis.Capacity{
		Replicas: 1, CPUMilli: target.CPUMilli, MemoryBytes: target.MemoryBytes,
		CPULimitMilli: target.CPULimitMilli, MemoryLimitBytes: target.MemoryLimitBytes,
	}
	if err := ValidateApplicationCapacity(capacity); err != nil {
		return fmt.Errorf("deployment target capacity invalid: %w", err)
	}
	if target.CPUMilli == 0 && target.CPULimitMilli != 0 {
		return errors.New("deployment target CPU limit requires a CPU request")
	}
	if target.MemoryBytes == 0 && target.MemoryLimitBytes != 0 {
		return errors.New("deployment target memory limit requires a memory request")
	}
	return nil
}

func ValidateApplicationCapacity(capacity repositoryanalysis.Capacity) error {
	if capacity.Replicas < 0 || capacity.Replicas > 100 {
		return errors.New("application replicas must be between 0 and 100")
	}
	if capacity.CPUMilli < 0 || capacity.CPUMilli > 1_000_000 {
		return errors.New("application CPU request is outside bounded values")
	}
	if capacity.MemoryBytes < 0 || capacity.MemoryBytes > 1<<50 {
		return errors.New("application memory request is outside bounded values")
	}
	if capacity.CPULimitMilli < 0 || capacity.CPULimitMilli > 1_000_000 {
		return errors.New("application CPU limit is outside bounded values")
	}
	if capacity.MemoryLimitBytes < 0 || capacity.MemoryLimitBytes > 1<<50 {
		return errors.New("application memory limit is outside bounded values")
	}
	if capacity.CPULimitMilli > 0 && capacity.CPULimitMilli < capacity.CPUMilli {
		return errors.New("application CPU limit must be greater than or equal to CPU request")
	}
	if capacity.MemoryLimitBytes > 0 && capacity.MemoryLimitBytes < capacity.MemoryBytes {
		return errors.New("application memory limit must be greater than or equal to memory request")
	}
	replicas := capacity.Replicas
	if replicas < 1 {
		replicas = 1
	}
	if capacity.CPUMilli > math.MaxInt64/int64(replicas) || capacity.MemoryBytes > math.MaxInt64/int64(replicas) || capacity.CPULimitMilli > math.MaxInt64/int64(replicas) || capacity.MemoryLimitBytes > math.MaxInt64/int64(replicas) {
		return errors.New("application capacity overflow")
	}
	return nil
}

func firstPositiveInt64(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
