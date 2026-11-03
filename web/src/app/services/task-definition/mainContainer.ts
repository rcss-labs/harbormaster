// Pure helpers over a task definition document: which container is "main", its image tag, and Fargate sizes.

export type Definition = Record<string, unknown>

/** Same tag syntax the server enforces for the free-text image tag. */
export const IMAGE_TAG_RE = /^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$/

export const FARGATE_CPU_VALUES = ['256', '512', '1024', '2048', '4096', '8192', '16384']

function stepped(min: number, max: number, step: number): number[] {
  const values: number[] = []
  for (let value = min; value <= max; value += step) values.push(value)
  return values
}

/** AWS table of task CPU units to allowed memory in MiB (mirrors internal/deploy/validate.go). */
const FARGATE_MEMORY: Record<string, number[]> = {
  '256': [512, 1024, 2048],
  '512': stepped(1024, 4096, 1024),
  '1024': stepped(2048, 8192, 1024),
  '2048': stepped(4096, 16384, 1024),
  '4096': stepped(8192, 30720, 1024),
  '8192': stepped(16384, 61440, 4096),
  '16384': stepped(32768, 122880, 8192),
}

export function fargateMemoryOptions(cpu: string): number[] {
  return FARGATE_MEMORY[cpu] ?? []
}

/** The smallest valid memory for `cpu` that is at least `minimum`, or the largest one; undefined for an unknown cpu. */
export function nextFargateMemory(cpu: string, minimum: number): number | undefined {
  const options = fargateMemoryOptions(cpu)
  return options.find((value) => value >= minimum) ?? options[options.length - 1]
}

export interface OomHint {
  memory: string
  suggested: string
}

/**
 * Suggests more memory when tasks were stopped for running out of it and the draft has not raised it yet.
 * `deployedMemory` is the live task memory (MiB); the suggestion is the next valid Fargate size at twice that.
 */
export function oomMemoryHint(args: { stoppedForOom: boolean; deployedMemory: unknown; draftMemory: unknown; draftCpu: unknown }): OomHint | undefined {
  const deployedMemory = Number(args.deployedMemory)
  const draftMemory = Number(args.draftMemory ?? deployedMemory)
  if (!args.stoppedForOom || !Number.isFinite(deployedMemory) || draftMemory > deployedMemory) return undefined
  const suggested = nextFargateMemory(String(args.draftCpu), deployedMemory * 2)
  return suggested && suggested > deployedMemory ? { memory: String(deployedMemory), suggested: String(suggested) } : undefined
}

export function containersOf(def: Definition): Record<string, unknown>[] {
  const containers = def.containerDefinitions
  return Array.isArray(containers) ? (containers as Record<string, unknown>[]) : []
}

/** Same rule as Go MainContainer: the container named like the service, else the first essential one, else the first. */
export function mainContainerIndex(def: Definition, serviceName: string): number {
  const containers = containersOf(def)
  if (containers.length === 0) return -1
  const named = containers.findIndex((container) => container?.name === serviceName)
  if (named >= 0) return named
  const essential = containers.findIndex((container) => container?.essential === undefined || container?.essential === true)
  return essential >= 0 ? essential : 0
}

/** Text after the last ':' of the image, ignoring a ':' that belongs to a registry host port. */
export function imageTagOf(image: unknown): string {
  if (typeof image !== 'string') return ''
  const colon = image.lastIndexOf(':')
  return colon > image.lastIndexOf('/') ? image.slice(colon + 1) : ''
}

export function withImageTag(image: unknown, tag: string): string {
  const text = typeof image === 'string' ? image : ''
  const colon = text.lastIndexOf(':')
  const base = colon > text.lastIndexOf('/') ? text.slice(0, colon) : text
  return `${base}:${tag}`
}

export function mainImageTag(def: Definition, serviceName: string): string {
  const index = mainContainerIndex(def, serviceName)
  return index < 0 ? '' : imageTagOf(containersOf(def)[index]?.image)
}

/** Returns a copy of `def` where the main container is replaced by `change(container)`. */
export function updateMainContainer(
  def: Definition,
  serviceName: string,
  change: (container: Record<string, unknown>) => Record<string, unknown>,
): Definition {
  const index = mainContainerIndex(def, serviceName)
  if (index < 0) return def
  const containers = containersOf(def).map((container, position) => (position === index ? change(container) : container))
  return { ...def, containerDefinitions: containers }
}
