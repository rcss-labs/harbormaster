import type { CapacityItem, ChangeSetRequest, DeploySettingsResponse } from '../../../api/deploy'

/** The ECS limits of a strategy item. */
const BASE_MAX = 100000
const WEIGHT_MAX = 1000

/**
 * The capacity of a service as three numbers: how many tasks stay on demand (the FARGATE base), and the weights that
 * split the rest between on-demand and Spot. A Spot weight of 0 means on demand only. A null number is an empty or
 * unparsable field: it is reported, never sent.
 */
export interface CapacityDraft {
  onDemandBase: number | null
  onDemandWeight: number | null
  spotWeight: number | null
}

/** A service on a launch type reads as one on-demand provider with no base. */
export function capacityDraftFromSettings(settings: DeploySettingsResponse): CapacityDraft {
  const { strategy } = settings.capacity
  if (strategy.length === 0) return { onDemandBase: 0, onDemandWeight: 1, spotWeight: 0 }
  const onDemand = strategy.find((item) => item.provider === 'FARGATE')
  const spot = strategy.find((item) => item.provider === 'FARGATE_SPOT')
  return { onDemandBase: onDemand?.base ?? 0, onDemandWeight: onDemand?.weight ?? 0, spotWeight: spot?.weight ?? 0 }
}

/** The suggestion from the Cost tab: `base` tasks on demand, everything above them on Spot. */
export const spotCapacityDraft = (onDemandBase: number): CapacityDraft => ({ onDemandBase, onDemandWeight: 0, spotWeight: 1 })

export const capacityChanged = (settings: DeploySettingsResponse, draft: CapacityDraft): boolean =>
  JSON.stringify(draft) !== JSON.stringify(capacityDraftFromSettings(settings))

const wholeBetween = (value: number | null, min: number, max: number): value is number => value !== null && Number.isInteger(value) && value >= min && value <= max

/** Problems that keep a capacity draft from being sent; empty when it is sendable. */
export function capacityErrors(draft: CapacityDraft): string[] {
  const errors: string[] = []
  if (!wholeBetween(draft.onDemandBase, 0, BASE_MAX)) errors.push(`On-demand base must be a whole number between 0 and ${BASE_MAX}.`)
  if (!wholeBetween(draft.onDemandWeight, 0, WEIGHT_MAX)) errors.push(`On-demand weight must be a whole number between 0 and ${WEIGHT_MAX}.`)
  if (!wholeBetween(draft.spotWeight, 0, WEIGHT_MAX)) errors.push(`Spot weight must be a whole number between 0 and ${WEIGHT_MAX}.`)
  if (errors.length === 0 && draft.onDemandWeight === 0 && draft.spotWeight === 0) errors.push('At least one capacity provider needs a weight above 0.')
  return errors
}

/** The strategy to send: FARGATE unless both its base and weight are 0, FARGATE_SPOT only with a weight. Null when the draft has errors. */
export function capacityStrategy(draft: CapacityDraft): CapacityItem[] | null {
  if (capacityErrors(draft).length > 0) return null
  const items: CapacityItem[] = []
  if (draft.onDemandBase !== 0 || draft.onDemandWeight !== 0) items.push({ provider: 'FARGATE', base: draft.onDemandBase ?? 0, weight: draft.onDemandWeight ?? 0 })
  if ((draft.spotWeight ?? 0) > 0) items.push({ provider: 'FARGATE_SPOT', base: 0, weight: draft.spotWeight ?? 0 })
  return items
}

/** Reads like "FARGATE base 1, FARGATE_SPOT weight 1": the wording of the operations log. */
export const describeStrategy = (items: CapacityItem[]): string =>
  items.map((item) => [item.provider, item.base > 0 ? `base ${item.base}` : '', item.weight > 0 ? `weight ${item.weight}` : ''].filter(Boolean).join(' ')).join(', ')

/** What the service uses now: the strategy, or the launch type as "FARGATE (launch type)". */
export const describeLiveCapacity = (settings: DeploySettingsResponse): string =>
  settings.capacity.strategy.length === 0 ? `${settings.capacity.launch_type} (launch type)` : describeStrategy(settings.capacity.strategy)

/** Editable service settings. A null number is an empty or unparsable field: it is reported, never sent. */
export interface ServiceDraft {
  desiredCount: number | null
  enableExecuteCommand: boolean
  circuitBreakerEnable: boolean
  circuitBreakerRollback: boolean
  minimumHealthyPercent: number | null
  maximumPercent: number | null
  gracePeriodSeconds: number | null
  forceNewDeployment: boolean
  capacity: CapacityDraft
}

export type ServiceChange = NonNullable<ChangeSetRequest['service']>

export function serviceDraftFromSettings(settings: DeploySettingsResponse): ServiceDraft {
  const config = settings.deployment_configuration
  return {
    desiredCount: settings.desired_count,
    enableExecuteCommand: settings.enable_execute_command,
    circuitBreakerEnable: config.circuit_breaker.enable,
    circuitBreakerRollback: config.circuit_breaker.rollback,
    minimumHealthyPercent: config.minimum_healthy_percent,
    maximumPercent: config.maximum_percent,
    gracePeriodSeconds: settings.health_check_grace_period_seconds,
    forceNewDeployment: false,
    capacity: capacityDraftFromSettings(settings),
  }
}

const inRange = (value: number | null, min: number, max: number): value is number =>
  value !== null && Number.isInteger(value) && value >= min && value <= max

export function isValidDesiredCount(settings: DeploySettingsResponse, value: number | null): value is number {
  return inRange(value, 0, settings.max_desired)
}

/** Human-readable problems with the draft, in field order; empty when every field is sendable. */
export function serviceDraftErrors(settings: DeploySettingsResponse, draft: ServiceDraft): string[] {
  const errors: string[] = []
  if (!isValidDesiredCount(settings, draft.desiredCount)) errors.push(`Desired count must be a whole number between 0 and ${settings.max_desired}.`)
  if (!inRange(draft.minimumHealthyPercent, 0, 100)) errors.push('Minimum healthy percent must be a whole number between 0 and 100.')
  if (!inRange(draft.maximumPercent, 100, 200)) errors.push('Maximum percent must be a whole number between 100 and 200.')
  if (settings.has_load_balancer && draft.gracePeriodSeconds !== null && !inRange(draft.gracePeriodSeconds, 0, Number.MAX_SAFE_INTEGER)) {
    errors.push('Health check grace period must be zero or more seconds.')
  }
  errors.push(...capacityErrors(draft.capacity))
  return errors
}

/** The `service` section of a change set: only fields that differ from `settings` (and are valid) are set. Null when nothing changed. */
export function serviceDraftToChange(settings: DeploySettingsResponse, draft: ServiceDraft): ServiceChange | null {
  const change: ServiceChange = {}
  if (isValidDesiredCount(settings, draft.desiredCount) && draft.desiredCount !== settings.desired_count) change.desired_count = draft.desiredCount
  if (draft.enableExecuteCommand !== settings.enable_execute_command) change.enable_execute_command = draft.enableExecuteCommand

  const config = settings.deployment_configuration
  const rollback = draft.circuitBreakerEnable && draft.circuitBreakerRollback
  const minimum = inRange(draft.minimumHealthyPercent, 0, 100) ? draft.minimumHealthyPercent : config.minimum_healthy_percent
  const maximum = inRange(draft.maximumPercent, 100, 200) ? draft.maximumPercent : config.maximum_percent
  if (
    draft.circuitBreakerEnable !== config.circuit_breaker.enable ||
    rollback !== config.circuit_breaker.rollback ||
    minimum !== config.minimum_healthy_percent ||
    maximum !== config.maximum_percent
  ) {
    change.deployment_configuration = {
      circuit_breaker: { enable: draft.circuitBreakerEnable, rollback },
      minimum_healthy_percent: minimum,
      maximum_percent: maximum,
    }
  }

  if (
    settings.has_load_balancer &&
    inRange(draft.gracePeriodSeconds, 0, Number.MAX_SAFE_INTEGER) &&
    draft.gracePeriodSeconds !== settings.health_check_grace_period_seconds
  ) {
    change.health_check_grace_period_seconds = draft.gracePeriodSeconds
  }
  const strategy = capacityChanged(settings, draft.capacity) ? capacityStrategy(draft.capacity) : null
  if (strategy) change.capacity_provider_strategy = strategy
  if (draft.forceNewDeployment) change.force_new_deployment = true
  return Object.keys(change).length === 0 ? null : change
}
