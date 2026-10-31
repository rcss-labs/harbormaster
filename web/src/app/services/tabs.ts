import type { ActionKey } from '../../api/types'
import type { LockAction } from '../../components/LockPanel'

export type TabKey = 'overview' | 'metrics' | 'task-definition' | 'tasks' | 'scaling' | 'logs' | 'terminal' | 'operations'

export const TABS: [TabKey, string][] = [
  ['overview', 'Overview'],
  ['metrics', 'Metrics'],
  ['task-definition', 'Task definition'],
  ['tasks', 'Tasks'],
  ['scaling', 'Scaling'],
  ['logs', 'Logs'],
  ['terminal', 'Terminal'],
  ['operations', 'Operations log'],
]

export const DEFAULT_TAB: TabKey = 'overview'

export const isTabKey = (value: string): value is TabKey => TABS.some(([key]) => key === value)

/** Tabs that need a permission beyond View, with the permission and its display name. */
export const TAB_LOCKS: Partial<Record<TabKey, { can: ActionKey; action: LockAction }>> = {
  logs: { can: 'logs', action: 'Logs' },
  terminal: { can: 'terminal', action: 'Terminal' },
}
