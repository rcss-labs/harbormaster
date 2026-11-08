package cost

import "strconv"

// TaskClass says how a task is billed, which decides whether it can be priced.
type TaskClass int

const (
	// OnDemand is a Fargate task billed at the on-demand rate.
	OnDemand TaskClass = iota
	// Spot is a Fargate Spot task. It is priced at list rates here, without the Spot discount.
	Spot
	// Unpriced is any task without a Fargate price, such as EC2 or external.
	Unpriced
)

// Classify decides the billing class of a task from its capacity provider,
// launch type and task-level CPU and memory. Only Fargate tasks with numeric
// size are priced.
func Classify(capacityProvider, launchType, cpu, memory string) TaskClass {
	if !isNumeric(cpu) || !isNumeric(memory) {
		return Unpriced
	}
	switch {
	case IsFargateOnDemand(capacityProvider, launchType):
		return OnDemand
	case capacityProvider == "FARGATE_SPOT":
		return Spot
	default:
		return Unpriced
	}
}

// IsFargateOnDemand reports whether a task runs on on-demand Fargate: its capacity provider is FARGATE, or it
// has none and its launch type is FARGATE. EC2, external and custom-provider tasks are not.
func IsFargateOnDemand(capacityProvider, launchType string) bool {
	return capacityProvider == "FARGATE" || (capacityProvider == "" && launchType == "FARGATE")
}

// isNumeric reports whether value is a task size: an integer greater than zero.
func isNumeric(value string) bool {
	number, err := strconv.Atoi(value)
	return err == nil && number > 0
}
