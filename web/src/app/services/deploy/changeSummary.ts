import type { ChangeSetRequest, ChangeSetResponse, DeploySettingsResponse } from '../../../api/deploy'
import type { Definition } from '../task-definition/mainContainer'
import type { ScalingChange } from './scalingDraft'
import type { ServiceChange } from './serviceDraft'
import { plural } from './format'

/** What the change set does to the task definition: register the edited draft, or roll out an already registered revision. */
export type DefinitionTarget = { kind: 'definition'; definition: Definition } | { kind: 'revision'; revision: number }

export interface ChangeSections {
  target: DefinitionTarget | null
  service: ServiceChange | null
  scaling: ScalingChange | null
}

export const REASON_MAX_LENGTH = 500

export const countSections = ({ target, service, scaling }: ChangeSections): number => [target, service, scaling].filter(Boolean).length

/**
 * The request body: only sections that changed are present. `base_version` guards the service and scaling writes, so a
 * definition-only change set sends an empty one and cannot trip over unrelated settings drift.
 */
export function buildChangeSetRequest(input: { baseRevision: number; baseVersion: string; reason: string; sections: ChangeSections }): ChangeSetRequest {
  const { sections } = input
  const body: ChangeSetRequest = {
    base_revision: input.baseRevision,
    base_version: sections.service || sections.scaling ? input.baseVersion : '',
    reason: input.reason.trim(),
  }
  if (sections.target) body.task_definition = sections.target.kind === 'definition' ? { definition: sections.target.definition } : { revision: sections.target.revision }
  if (sections.service) body.service = sections.service
  if (sections.scaling) body.scaling = sections.scaling
  return body
}

/**
 * The follow-up request after a `partial` result: failed and skipped sections only, the registered revision instead of the
 * definition when registering succeeded, and the settings version the server reported. Null when no step is left to run.
 */
export function buildRetryRequest(request: ChangeSetRequest, response: ChangeSetResponse): ChangeSetRequest | null {
  const unfinished = (name: string) => {
    const step = response.steps.find((candidate) => candidate.name === name)
    return step !== undefined && step.status !== 'done'
  }
  if (!response.steps.some((step) => step.status !== 'done')) return null

  const retry: ChangeSetRequest = {
    base_revision: request.base_revision,
    base_version: response.settings_version || request.base_version,
    reason: request.reason,
  }
  const registered = response.steps.find((step) => step.name === 'register')
  if (registered?.status === 'done' && response.registered_revision !== null) retry.task_definition = { revision: response.registered_revision }
  else if (request.task_definition && registered) retry.task_definition = request.task_definition
  if (request.service && unfinished('service')) retry.service = request.service
  if (request.scaling && unfinished('scaling')) retry.scaling = request.scaling
  return retry
}

export interface ServiceRow {
  label: string
  before: string
  after: string
}

const onOff = (value: boolean) => (value ? 'on' : 'off')

/** One row per service setting the change set touches, with the live value on the left. */
export function serviceRows(settings: DeploySettingsResponse, change: ServiceChange): ServiceRow[] {
  const rows: ServiceRow[] = []
  if (change.desired_count !== undefined) rows.push({ label: 'Desired count', before: String(settings.desired_count), after: String(change.desired_count) })
  if (change.enable_execute_command !== undefined) rows.push({ label: 'ECS Exec', before: onOff(settings.enable_execute_command), after: onOff(change.enable_execute_command) })
  const now = settings.deployment_configuration
  const next = change.deployment_configuration
  if (next) {
    if (next.circuit_breaker.enable !== now.circuit_breaker.enable) rows.push({ label: 'Circuit breaker', before: onOff(now.circuit_breaker.enable), after: onOff(next.circuit_breaker.enable) })
    if (next.circuit_breaker.rollback !== now.circuit_breaker.rollback) rows.push({ label: 'Roll back on failure', before: onOff(now.circuit_breaker.rollback), after: onOff(next.circuit_breaker.rollback) })
    if (next.minimum_healthy_percent !== now.minimum_healthy_percent) rows.push({ label: 'Minimum healthy percent', before: `${now.minimum_healthy_percent}%`, after: `${next.minimum_healthy_percent}%` })
    if (next.maximum_percent !== now.maximum_percent) rows.push({ label: 'Maximum percent', before: `${now.maximum_percent}%`, after: `${next.maximum_percent}%` })
  }
  if (change.health_check_grace_period_seconds !== undefined) {
    const before = settings.health_check_grace_period_seconds
    rows.push({ label: 'Health check grace period', before: before === null ? 'none' : `${before} s`, after: `${change.health_check_grace_period_seconds} s` })
  }
  if (change.force_new_deployment) rows.push({ label: 'Restart tasks', before: 'no', after: 'yes' })
  return rows
}

/** One line per scaling write: capacity, then policies, then scheduled actions. */
export function scalingLines(settings: DeploySettingsResponse, change: ScalingChange): string[] {
  const lines: string[] = []
  const { scaling } = settings
  if (change.target) {
    const before = scaling.has_target ? `${scaling.min}–${scaling.max}` : 'none'
    lines.push(`Capacity ${before} → ${change.target.min}–${change.target.max}`)
  }
  if (change.remove_target) lines.push('Remove the scaling target (its policies and scheduled actions go with it)')
  const knownPolicies = new Set(scaling.policies.map((policy) => policy.name))
  for (const put of change.put_policies ?? []) {
    lines.push(`${knownPolicies.has(put.name) ? 'Update' : 'Add'} policy ${put.name}: ${put.metric} target ${put.target_value}`)
  }
  for (const name of change.delete_policies ?? []) lines.push(`Remove policy ${name}`)
  const knownActions = new Set(scaling.scheduled_actions.map((action) => action.name))
  for (const put of change.put_scheduled_actions ?? []) {
    lines.push(`${knownActions.has(put.name) ? 'Update' : 'Add'} scheduled action ${put.name}: ${put.schedule} (${put.timezone})`)
  }
  for (const name of change.delete_scheduled_actions ?? []) lines.push(`Remove scheduled action ${name}`)
  return lines
}

interface LabelInput {
  sections: ChangeSections
  predictedRevision: number
  canDeploy: boolean
}

/** The primary button of the review dialog, named for what it will do. */
export function primaryLabel({ sections, predictedRevision, canDeploy }: LabelInput): string {
  const { target } = sections
  if (target?.kind === 'revision') return `Deploy :${target.revision}`
  if (target?.kind === 'definition') {
    if (canDeploy) return `Register and deploy :${predictedRevision}`
    if (!sections.service && !sections.scaling) return `Register :${predictedRevision}`
  }
  return 'Apply changes'
}

/** A sentence for the top of the review dialog. */
export function summarySentence({ serviceName, sections, predictedRevision, canDeploy }: LabelInput & { serviceName: string }): string {
  const parts: string[] = []
  const { target } = sections
  if (target?.kind === 'revision') parts.push(`deploys revision :${target.revision} to ${serviceName}`)
  else if (target) parts.push(canDeploy ? `registers revision :${predictedRevision} and deploys it to ${serviceName}` : `registers revision :${predictedRevision}`)
  if (sections.service) parts.push('changes the service settings')
  if (sections.scaling) parts.push('changes auto scaling')
  if (parts.length === 0) return 'Nothing to change.'
  const head = parts.slice(0, -1).join(', ')
  const text = parts.length > 1 ? `${head} and ${parts[parts.length - 1]}` : parts[0]
  return `${text.charAt(0).toUpperCase()}${text.slice(1)}.`
}

export const changesLabel = (count: number) => plural(count, 'change')
