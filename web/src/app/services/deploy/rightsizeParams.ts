import { fargateMemoryOptions } from '../task-definition/mainContainer'

const HEADROOM_MIN = 10
const HEADROOM_MAX = 100

export const RIGHTSIZE_PARAMS = ['task_cpu', 'task_memory', 'headroom'] as const

/** The Deploy tab link that prefills a suggested task size; the size itself comes from the server's recommendation. */
export function rightsizeHref(serviceId: string, size: { cpuUnits: number; memoryMib: number }, headroomPct: number): string {
  const query = new URLSearchParams({ task_cpu: String(size.cpuUnits), task_memory: String(size.memoryMib), headroom: String(headroomPct) })
  return `/services/${encodeURIComponent(serviceId)}/deploy?${query}`
}

/** The suggestion in the URL, or null unless it is a valid Fargate size pair with a whole-number headroom of 10 to 100. */
export function parseRightsizeParams(params: URLSearchParams): { cpu: string; memory: string; headroomPct: number } | null {
  const cpu = params.get('task_cpu')
  const memory = params.get('task_memory')
  const headroom = params.get('headroom')
  if (cpu === null || memory === null || headroom === null) return null
  if (!fargateMemoryOptions(cpu).map(String).includes(memory)) return null
  if (!/^\d+$/.test(headroom)) return null
  const headroomPct = Number(headroom)
  if (headroomPct < HEADROOM_MIN || headroomPct > HEADROOM_MAX) return null
  return { cpu, memory, headroomPct }
}

export const rightsizeReason = (headroomPct: number): string => `Rightsizing at ${headroomPct}% headroom`
