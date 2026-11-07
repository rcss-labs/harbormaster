package cost

import (
	"encoding/json"
	"fmt"
)

// Floors is the minimum CPU and memory a task definition reserves across its containers.
type Floors struct {
	CPUUnits  int
	MemoryMiB int
}

type containerLimits struct {
	Cpu               int
	Memory            *int
	MemoryReservation *int
}

// ContainerFloors sums container CPU and memory from containerDefinitions in
// PascalCase ECS SDK JSON. Each container's memory floor is the larger of its
// hard Memory limit and its MemoryReservation; missing values count as zero.
func ContainerFloors(containerDefinitions []byte) (Floors, error) {
	var containers []containerLimits
	if err := json.Unmarshal(containerDefinitions, &containers); err != nil {
		return Floors{}, fmt.Errorf("parse containerDefinitions: %w", err)
	}
	var floors Floors
	for _, container := range containers {
		floors.CPUUnits += container.Cpu
		floors.MemoryMiB += max(valueOrZero(container.Memory), valueOrZero(container.MemoryReservation))
	}
	return floors, nil
}

func valueOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
