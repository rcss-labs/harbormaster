// Diffs two registered task definition revisions in the browser, for the Timeline's change detail and trigger sentence.
import type { TimelineDiffRow } from '../../../api/timeline'

export type DiffRow = TimelineDiffRow

const NONE = 'none'

type Json = Record<string, unknown>

function asObject(value: unknown): Json | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? (value as Json) : null
}

function text(value: unknown): string {
  return typeof value === 'string' || typeof value === 'number' ? String(value) : NONE
}

function containersByName(definition: Json): Map<string, Json> | null {
  const list = definition.containerDefinitions
  if (list === undefined) return new Map()
  if (!Array.isArray(list)) return null
  const byName = new Map<string, Json>()
  for (const entry of list) {
    const container = asObject(entry)
    if (container && typeof container.name === 'string') byName.set(container.name, container)
  }
  return byName
}

/** name -> value for an environment or secrets list. */
function namedValues(container: Json | undefined, listKey: string, valueKey: string): Map<string, string> {
  const values = new Map<string, string>()
  const list = container?.[listKey]
  if (!Array.isArray(list)) return values
  for (const entry of list) {
    const item = asObject(entry)
    if (item && typeof item.name === 'string') values.set(item.name, text(item[valueKey]))
  }
  return values
}

function changedNames(before: Map<string, string>, after: Map<string, string>, prefix: string): DiffRow[] {
  const rows: DiffRow[] = []
  for (const name of new Set([...before.keys(), ...after.keys()])) {
    const from = before.get(name) ?? NONE
    const to = after.get(name) ?? NONE
    if (from !== to) rows.push({ label: `${prefix} ${name}`, from, to })
  }
  return rows
}

/** Rows for CPU, memory, each container's image, then environment and secret changes. Malformed input gives no rows. */
export function revisionDiff(before: unknown, after: unknown): DiffRow[] {
  const beforeDefinition = asObject(before)
  const afterDefinition = asObject(after)
  if (!beforeDefinition || !afterDefinition) return []
  const beforeContainers = containersByName(beforeDefinition)
  const afterContainers = containersByName(afterDefinition)
  if (!beforeContainers || !afterContainers) return []

  const rows: DiffRow[] = []
  for (const field of ['cpu', 'memory'] as const) {
    const from = text(beforeDefinition[field])
    const to = text(afterDefinition[field])
    if (from !== to) rows.push({ label: field, from, to })
  }

  const names = [...new Set([...beforeContainers.keys(), ...afterContainers.keys()])]
  const several = names.length > 1
  for (const name of names) {
    const oldContainer = beforeContainers.get(name)
    const newContainer = afterContainers.get(name)
    const from = text(oldContainer?.image)
    const to = text(newContainer?.image)
    if (from !== to) rows.push({ label: several ? `image ${name}` : 'image', from, to })
    rows.push(...changedNames(namedValues(oldContainer, 'environment', 'value'), namedValues(newContainer, 'environment', 'value'), 'env'))
    rows.push(...changedNames(namedValues(oldContainer, 'secrets', 'valueFrom'), namedValues(newContainer, 'secrets', 'valueFrom'), 'secret'))
  }
  return rows
}

/** One sentence naming the first memory, CPU or image change, or null when none of those changed. */
export function triggerSentence(rows: DiffRow[]): string | null {
  const memory = rows.find((row) => row.label === 'memory')
  if (memory) return `It ${verb(memory)} memory from ${memory.from} to ${memory.to}${unit(memory, ' MiB')}.`
  const cpu = rows.find((row) => row.label === 'cpu')
  if (cpu) return `It ${verb(cpu)} CPU from ${cpu.from} to ${cpu.to}${unit(cpu, ' units')}.`
  const image = rows.find((row) => row.label === 'image' || row.label.startsWith('image '))
  if (image) return `It changed the image from ${image.from} to ${image.to}.`
  return null
}

const isNumeric = (value: string) => value.trim() !== '' && Number.isFinite(Number(value))

/** The unit is only named when both sides are plain numbers; otherwise the values carry their own. */
function unit(row: DiffRow, name: string): string {
  return isNumeric(row.from) && isNumeric(row.to) ? name : ''
}

/** Raised or lowered when both sides are numbers, else just changed. */
function verb(row: DiffRow): 'changed' | 'lowered' | 'raised' {
  if (!isNumeric(row.from) || !isNumeric(row.to)) return 'changed'
  return Number(row.to) < Number(row.from) ? 'lowered' : 'raised'
}
