import type { TaskDTO, TasksFilter } from '../../api/types'

/** A failed stop (non-zero exit, or never started) or an unhealthy task. Mirrors the API's `problems` rule. */
export const isProblem = (task: TaskDTO) =>
  (task.status === 'STOPPED' && ((task.exit_code ?? 0) !== 0 || task.stop_code === 'TaskFailedToStart')) || task.health === 'UNHEALTHY'

export const isStopped = (task: TaskDTO) => task.status === 'STOPPED'

const SEGMENT_TESTS: Record<TasksFilter, (task: TaskDTO) => boolean> = {
  problems: isProblem,
  running: (task) => !isStopped(task),
  stopped: isStopped,
  all: () => true,
}

export const inSegment = (task: TaskDTO, segment: TasksFilter) => SEGMENT_TESTS[segment](task)

/** Case-insensitive match on task id (short or full), service, cluster, IP, plus the family of standalone tasks. */
export function matchesQuery(task: TaskDTO, query: string): boolean {
  const needle = query.trim().toLowerCase()
  if (!needle) return true
  return [task.short_id, task.id, task.service?.name, task.cluster.name, task.private_ip, task.service ? '' : task.family].some((value) =>
    value?.toLowerCase().includes(needle),
  )
}

/** Identical failures share a key: same service and stop reason, or same service and health for running tasks. */
export function repeatKey(task: TaskDTO): string {
  const owner = task.service?.id ?? `${task.cluster.id}:${task.family}`
  const state = isStopped(task) ? `stopped:${task.stop_reason_group || task.stopped_reason || task.stop_code}` : `running:${task.health}`
  return `${owner}|${state}`
}

export type TaskTableRow =
  | { kind: 'task'; rowKey: string; task: TaskDTO; nested: boolean }
  | { kind: 'group'; rowKey: string; groupKey: string; tasks: TaskDTO[]; open: boolean }

/**
 * Flat table rows. With `group`, tasks sharing a repeat key (two or more) become one group row at the position of
 * their first member; an open group is followed by its tasks. Without it, every task is a row.
 */
export function buildRows(tasks: TaskDTO[], group: boolean, openGroups: ReadonlySet<string>): TaskTableRow[] {
  if (!group) return tasks.map((task) => ({ kind: 'task', rowKey: task.id, task, nested: false }))
  const byKey = new Map<string, TaskDTO[]>()
  for (const task of tasks) {
    const key = repeatKey(task)
    const members = byKey.get(key)
    if (members) members.push(task)
    else byKey.set(key, [task])
  }
  const rows: TaskTableRow[] = []
  for (const [groupKey, members] of byKey) {
    if (members.length < 2) {
      rows.push({ kind: 'task', rowKey: members[0].id, task: members[0], nested: false })
      continue
    }
    const open = openGroups.has(groupKey)
    rows.push({ kind: 'group', rowKey: `group:${groupKey}`, groupKey, tasks: members, open })
    if (open) for (const task of members) rows.push({ kind: 'task', rowKey: task.id, task, nested: true })
  }
  return rows
}
