import type { ChangeSetRequest, ChangeSetResponse, DeploySettingsResponse } from '../../../api/deploy'
import type { ScanGateRequest } from '../../../api/security'
import { containersOf, imageTagOf, type Definition } from '../task-definition/mainContainer'
import { containerNames, readArgv, readEnvironment, readHealthCheck, readSecrets, type HealthCheck } from './draft'
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
export function buildChangeSetRequest(input: { baseRevision: number; baseVersion: string; reason: string; sections: ChangeSections; scanGate?: ScanGateRequest }): ChangeSetRequest {
  const { sections } = input
  const body: ChangeSetRequest = {
    base_revision: input.baseRevision,
    base_version: sections.service || sections.scaling ? input.baseVersion : '',
    reason: input.reason.trim(),
  }
  if (sections.target) body.task_definition = sections.target.kind === 'definition' ? { definition: sections.target.definition } : { revision: sections.target.revision }
  if (sections.service) body.service = sections.service
  if (sections.scaling) body.scaling = sections.scaling
  if (input.scanGate) body.scan_gate = input.scanGate
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

  const registered = response.steps.find((step) => step.name === 'register')
  const serviceStep = response.steps.find((step) => step.name === 'service')
  const serviceDone = serviceStep?.status === 'done'
  const retry: ChangeSetRequest = {
    // base_revision guards the definition; once the service step deployed, the live revision is the new one.
    base_revision: serviceDone ? (response.to_revision ?? response.registered_revision ?? request.base_revision) : request.base_revision,
    base_version: response.settings_version || request.base_version,
    reason: request.reason,
  }
  // The definition goes again only while the rollout is still pending: a done service step already deployed it.
  if (request.task_definition && registered && !serviceDone) {
    if (registered.status === 'done' && response.registered_revision !== null) retry.task_definition = { revision: response.registered_revision }
    else if (registered.status !== 'done') {
      retry.task_definition = request.task_definition
      // Only a definition is gated: a retry by registered revision needs no acknowledgement.
      if (request.scan_gate) retry.scan_gate = request.scan_gate
    }
  }
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

/** One edited field of the Pending changes aside: where it lives, its name, the live value and the draft value. */
export interface PendingRow {
  group: string
  label: string
  before: string
  after: string
}

/** What the aside compares: the sections that will be sent, plus the live and edited task definitions. */
export interface PendingDraft {
  sections: ChangeSections
  liveDefinition?: Definition
  definitionAfter?: Definition
}

const NOT_SET = 'Not set'
const REMOVED = 'Removed'
const METRIC_TEXT: Record<string, string> = { cpu: 'CPU %', memory: 'Memory %', alb: 'ALB requests per target' }

const argvText = (argv: string[]) => (argv.length === 0 ? 'Image default' : argv.join(' '))
const healthText = (check: HealthCheck | null) =>
  check ? `${check.command.join(' ')} every ${check.interval} s, timeout ${check.timeout} s, ${check.retries} retries, start ${check.startPeriod} s` : 'None'

const without = (source: Record<string, unknown>, keys: string[]) => Object.fromEntries(Object.entries(source).filter(([key]) => !keys.includes(key)))

/** Everything the dedicated rows already cover is removed, so what is left differing is a change the aside has no row for. */
function uncoveredDefinition(definition: Definition): unknown {
  return {
    ...without(definition, ['cpu', 'memory']),
    containerDefinitions: containersOf(definition).map((entry) => {
      const text = typeof entry.image === 'string' ? entry.image : ''
      return { ...without(entry, ['image', 'environment', 'secrets', 'command', 'entryPoint', 'healthCheck']), image: text.slice(0, text.length - imageTagOf(text).length) }
    }),
  }
}

function definitionRows(before: Definition, after: Definition): PendingRow[] {
  const rows: PendingRow[] = []
  const task = (label: string, was: unknown, now: unknown, unit: string) => {
    if (String(was) !== String(now)) rows.push({ group: 'Task size', label, before: `${was}${unit}`, after: `${now}${unit}` })
  }
  task('Task CPU', before.cpu ?? '', after.cpu ?? '', ' units')
  task('Task memory', before.memory ?? '', after.memory ?? '', ' MiB')

  const wasNames = containerNames(before)
  const nowNames = containerNames(after)
  for (const name of [...wasNames.filter((candidate) => !nowNames.includes(candidate)), ...nowNames.filter((candidate) => !wasNames.includes(candidate))]) {
    const added = nowNames.includes(name)
    rows.push({ group: 'Containers', label: `Container ${name}`, before: added ? NOT_SET : 'Present', after: added ? 'Added' : REMOVED })
  }
  for (const name of nowNames.filter((candidate) => wasNames.includes(candidate))) {
    const group = nowNames.length > 1 ? `Container ${name}` : 'Container'
    const wasContainer = containersOf(before).find((entry) => entry?.name === name)
    const nowContainer = containersOf(after).find((entry) => entry?.name === name)
    const wasTag = imageTagOf(wasContainer?.image)
    const nowTag = imageTagOf(nowContainer?.image)
    if (wasTag !== nowTag) rows.push({ group, label: 'Image tag', before: wasTag || NOT_SET, after: nowTag || NOT_SET })

    const compareByName = <T extends { name: string }>(label: string, was: T[], now: T[], show: (item: T) => string) => {
      const wasByName = new Map(was.map((item) => [item.name, item]))
      const nowByName = new Map(now.map((item) => [item.name, item]))
      for (const key of new Set([...wasByName.keys(), ...nowByName.keys()])) {
        const previous = wasByName.get(key)
        const next = nowByName.get(key)
        const shownBefore = previous ? show(previous) : NOT_SET
        const shownAfter = next ? show(next) : REMOVED
        if (shownBefore !== shownAfter || (previous === undefined) !== (next === undefined)) rows.push({ group, label: `${label} ${key}`, before: shownBefore, after: shownAfter })
      }
    }
    compareByName('Environment', readEnvironment(before, name), readEnvironment(after, name), (item) => item.value)
    compareByName('Secret', readSecrets(before, name), readSecrets(after, name), (item) => item.valueFrom)
    for (const key of ['command', 'entryPoint'] as const) {
      const was = argvText(readArgv(before, name, key))
      const now = argvText(readArgv(after, name, key))
      if (was !== now) rows.push({ group, label: key === 'command' ? 'Command' : 'Entry point', before: was, after: now })
    }
    const wasCheck = healthText(readHealthCheck(before, name))
    const nowCheck = healthText(readHealthCheck(after, name))
    if (wasCheck !== nowCheck) rows.push({ group, label: 'Health check', before: wasCheck, after: nowCheck })
  }
  if (JSON.stringify(uncoveredDefinition(before)) !== JSON.stringify(uncoveredDefinition(after))) {
    rows.push({ group: 'Task definition', label: 'Other definition fields', before: 'As deployed', after: 'Edited (the review shows the diff)' })
  }
  return rows
}

const policyText = (policy: { metric: string; metric_raw?: string; target_value: number; scale_in_cooldown: number; scale_out_cooldown: number; disable_scale_in: boolean }) =>
  `${METRIC_TEXT[policy.metric] ?? policy.metric_raw ?? policy.metric} at ${policy.target_value}, in ${policy.scale_in_cooldown} s, out ${policy.scale_out_cooldown} s${policy.disable_scale_in ? ', scale-in off' : ''}`

const actionText = (action: { schedule: string; timezone: string; min_capacity: number | null; max_capacity: number | null }) =>
  `${action.schedule} ${action.timezone}, min ${action.min_capacity ?? 'unchanged'}, max ${action.max_capacity ?? 'unchanged'}`

function scalingRows(settings: DeploySettingsResponse, change: ScalingChange): PendingRow[] {
  const rows: PendingRow[] = []
  const group = 'Auto scaling'
  const { scaling } = settings
  if (change.target) rows.push({ group, label: 'Capacity', before: scaling.has_target ? `${scaling.min}–${scaling.max}` : 'None', after: `${change.target.min}–${change.target.max}` })
  if (change.remove_target) rows.push({ group, label: 'Scaling target', before: 'Registered', after: REMOVED })
  const policies = new Map(scaling.policies.map((policy) => [policy.name, policy]))
  const touchedPolicies = new Set([...(change.put_policies ?? []).map((put) => put.name), ...(change.delete_policies ?? [])])
  for (const name of touchedPolicies) {
    const put = change.put_policies?.find((candidate) => candidate.name === name)
    const previous = policies.get(name)
    rows.push({ group, label: `Policy ${name}`, before: previous ? policyText(previous) : NOT_SET, after: put ? policyText(put) : REMOVED })
  }
  const actions = new Map(scaling.scheduled_actions.map((action) => [action.name, action]))
  const touchedActions = new Set([...(change.put_scheduled_actions ?? []).map((put) => put.name), ...(change.delete_scheduled_actions ?? [])])
  for (const name of touchedActions) {
    const put = change.put_scheduled_actions?.find((candidate) => candidate.name === name)
    const previous = actions.get(name)
    rows.push({ group, label: `Scheduled action ${name}`, before: previous ? actionText(previous) : NOT_SET, after: put ? actionText(put) : REMOVED })
  }
  return rows
}

/** Every edited field as a row with its live value and its draft value, for the Pending changes aside. */
export function pendingRows({ sections, liveDefinition, definitionAfter }: PendingDraft, settings: DeploySettingsResponse | undefined): PendingRow[] {
  const rows: PendingRow[] = []
  if (liveDefinition && definitionAfter) rows.push(...definitionRows(liveDefinition, definitionAfter))
  if (settings && sections.service) rows.push(...serviceRows(settings, sections.service).map((row) => ({ group: 'Service', ...row })))
  if (settings && sections.scaling) rows.push(...scalingRows(settings, sections.scaling))
  return rows
}

export const changesLabel = (count: number) => plural(count, 'change')
