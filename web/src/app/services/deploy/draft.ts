import { containersOf, fargateMemoryOptions, withImageTag, type Definition } from '../task-definition/mainContainer'

export interface EnvEntry { name: string; value: string }
export interface SecretEntry { name: string; valueFrom: string }
export interface HealthCheck { command: string[]; interval: number; timeout: number; retries: number; startPeriod: number }
export type ArgvKey = 'command' | 'entryPoint'

export const containerNames = (def: Definition): string[] => containersOf(def).map((entry) => (typeof entry?.name === 'string' ? entry.name : ''))

/** Copy of `def` where the container called `name` is replaced by `change(container)`; an unknown name changes nothing. */
function updateContainer(def: Definition, name: string, change: (container: Record<string, unknown>) => Record<string, unknown>): Definition {
  const containers = containersOf(def)
  if (!containers.some((entry) => entry?.name === name)) return def
  return { ...def, containerDefinitions: containers.map((entry) => (entry?.name === name ? change(entry) : entry)) }
}

/** Sets `key`, or removes it when `value` is undefined: an empty list is the same as no key to ECS. */
function withKey(container: Record<string, unknown>, key: string, value: unknown): Record<string, unknown> {
  if (value !== undefined) return { ...container, [key]: value }
  const rest = { ...container }
  delete rest[key]
  return rest
}

const listOrUndefined = <T>(items: T[]): T[] | undefined => (items.length === 0 ? undefined : items)

export const setContainerImageTag = (def: Definition, name: string, tag: string): Definition =>
  updateContainer(def, name, (entry) => ({ ...entry, image: withImageTag(entry.image, tag) }))
export const setEnvironment = (def: Definition, name: string, entries: EnvEntry[]): Definition =>
  updateContainer(def, name, (entry) => withKey(entry, 'environment', listOrUndefined(entries.map(({ name: key, value }) => ({ name: key, value })))))
export const setSecrets = (def: Definition, name: string, entries: SecretEntry[]): Definition =>
  updateContainer(def, name, (entry) => withKey(entry, 'secrets', listOrUndefined(entries.map(({ name: key, valueFrom }) => ({ name: key, valueFrom })))))
export const setCommand = (def: Definition, name: string, argv: string[]): Definition =>
  updateContainer(def, name, (entry) => withKey(entry, 'command', listOrUndefined(argv)))
export const setEntryPoint = (def: Definition, name: string, argv: string[]): Definition =>
  updateContainer(def, name, (entry) => withKey(entry, 'entryPoint', listOrUndefined(argv)))
export const setHealthCheck = (def: Definition, name: string, check: HealthCheck | null): Definition =>
  updateContainer(def, name, (entry) =>
    withKey(entry, 'healthCheck', check === null ? undefined : { command: check.command, interval: check.interval, timeout: check.timeout, retries: check.retries, startPeriod: check.startPeriod }),
  )
export const setTaskSize = (def: Definition, size: { cpu?: string; memory?: string }): Definition => ({
  ...def,
  ...(size.cpu !== undefined && { cpu: size.cpu }),
  ...(size.memory !== undefined && { memory: size.memory }),
})

const container = (def: Definition, name: string): Record<string, unknown> | undefined => containersOf(def).find((entry) => entry?.name === name)
const text = (value: unknown): string => (typeof value === 'string' ? value : typeof value === 'number' ? String(value) : '')
const list = (value: unknown): Record<string, unknown>[] => (Array.isArray(value) ? (value as Record<string, unknown>[]) : [])

export const readEnvironment = (def: Definition, name: string): EnvEntry[] =>
  list(container(def, name)?.environment).map((entry) => ({ name: text(entry.name), value: text(entry.value) }))

/** Names and references only: the `value` of a secret is never read, so it cannot reach the screen. */
export const readSecrets = (def: Definition, name: string): SecretEntry[] =>
  list(container(def, name)?.secrets).map((entry) => ({ name: text(entry.name), valueFrom: text(entry.valueFrom) }))

export const readArgv = (def: Definition, name: string, key: ArgvKey): string[] => {
  const value = container(def, name)?.[key]
  return Array.isArray(value) ? value.map(text) : []
}

export const HEALTH_CHECK_DEFAULTS = { interval: 30, timeout: 5, retries: 3, startPeriod: 0 }

export function readHealthCheck(def: Definition, name: string): HealthCheck | null {
  const raw = container(def, name)?.healthCheck as Record<string, unknown> | undefined
  if (!raw || typeof raw !== 'object') return null
  const number = (value: unknown, fallback: number) => (typeof value === 'number' ? value : fallback)
  return {
    command: Array.isArray(raw.command) ? raw.command.map(text) : [],
    interval: number(raw.interval, HEALTH_CHECK_DEFAULTS.interval),
    timeout: number(raw.timeout, HEALTH_CHECK_DEFAULTS.timeout),
    retries: number(raw.retries, HEALTH_CHECK_DEFAULTS.retries),
    startPeriod: number(raw.startPeriod, HEALTH_CHECK_DEFAULTS.startPeriod),
  }
}

// ---- Validation, shared by the editors (inline messages) and the review step (blocking) ----

const SECRETS_MANAGER_ARN = /^arn:aws[a-z-]*:secretsmanager:[a-z0-9-]+:\d{12}:secret:[\w/+=.@-]+(:[\w/+=.@-]*)*$/
const SSM_PARAMETER_ARN = /^arn:aws[a-z-]*:ssm:[a-z0-9-]+:\d{12}:parameter\/[\w./-]+$/
const SSM_PARAMETER_NAME = /^\/?[\w.-]+(\/[\w.-]+)*$/

export const VALUE_FROM_HINT = 'Use a Secrets Manager ARN, an SSM parameter ARN or an SSM parameter name.'

export const isValidValueFrom = (valueFrom: string): boolean =>
  SECRETS_MANAGER_ARN.test(valueFrom) || SSM_PARAMETER_ARN.test(valueFrom) || SSM_PARAMETER_NAME.test(valueFrom)

export const HEALTH_CHECK_RANGES = {
  interval: { min: 5, max: 300 },
  timeout: { min: 2, max: 60 },
  retries: { min: 1, max: 10 },
  startPeriod: { min: 0, max: 300 },
} as const

/** Problems with a container's variables, as sentences; empty when they are fine. */
export function envProblems(entries: EnvEntry[]): string[] {
  const problems: string[] = []
  const seen = new Set<string>()
  for (const entry of entries) {
    if (entry.name.trim() === '') problems.push('An environment variable has no name.')
    else if (seen.has(entry.name)) problems.push(`Environment variable ${entry.name} is listed twice.`)
    seen.add(entry.name)
  }
  return problems
}

/** Problems that make the draft unsendable, as short messages naming the container; empty when the draft is fine. */
export function definitionErrors(def: Definition): string[] {
  const errors: string[] = []
  const cpu = String(def.cpu ?? '')
  const memory = String(def.memory ?? '')
  const allowedMemory = fargateMemoryOptions(cpu)
  if (allowedMemory.length > 0 && !allowedMemory.map(String).includes(memory)) errors.push(`Task memory ${memory} MiB is not available with ${cpu} CPU units.`)
  for (const name of containerNames(def)) {
    for (const problem of envProblems(readEnvironment(def, name))) errors.push(`${name}: ${problem}`)
    for (const entry of readSecrets(def, name)) {
      if (entry.name.trim() === '') errors.push(`${name}: a secret has no name.`)
      if (!isValidValueFrom(entry.valueFrom)) errors.push(`${name}: secret ${entry.name || '(unnamed)'} has an invalid valueFrom.`)
    }
    for (const key of ['command', 'entryPoint'] as const) {
      if (readArgv(def, name, key).some((argument) => argument === '')) errors.push(`${name}: ${key === 'command' ? 'command' : 'entry point'} has an empty argument.`)
    }
    const check = readHealthCheck(def, name)
    if (check && (check.command.length < 2 || check.command.slice(1).every((part) => part.trim() === ''))) errors.push(`${name}: the health check has no command.`)
  }
  return errors
}

/** True when the draft would register a different revision than the deployed one. */
export const isDefinitionChanged = (draft: Definition, deployed: Definition): boolean => JSON.stringify(draft) !== JSON.stringify(deployed)
