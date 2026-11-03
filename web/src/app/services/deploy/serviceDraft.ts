import type { ChangeSetRequest, DeploySettingsResponse } from '../../../api/deploy'

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
  if (draft.forceNewDeployment) change.force_new_deployment = true
  return Object.keys(change).length === 0 ? null : change
}
