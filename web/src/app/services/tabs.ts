import type { ActionKey } from '../../api/types'
import type { LockAction } from '../../components/LockPanel'

export type TabKey = 'overview' | 'metrics' | 'task-definition' | 'deploy' | 'tasks' | 'timeline' | 'scaling' | 'logs' | 'terminal' | 'operations'

/** `configure` tabs sit after a gap and a "Configure" label in the tab bar. */
export type TabGroup = 'configure'

export const TABS: [TabKey, string, TabGroup?][] = [
  ['overview', 'Overview'],
  ['tasks', 'Tasks'],
  ['timeline', 'Timeline'],
  ['logs', 'Logs'],
  ['metrics', 'Metrics'],
  ['terminal', 'Terminal'],
  ['deploy', 'Deploy', 'configure'],
  ['task-definition', 'Task definition', 'configure'],
  ['scaling', 'Scaling', 'configure'],
  ['operations', 'History'],
]

export const TAB_GROUP_LABELS: Record<TabGroup, string> = { configure: 'Configure' }

/** Tabs whose label is bold: the Deploy tab owns every edit. */
export const BOLD_TABS: ReadonlySet<TabKey> = new Set<TabKey>(['deploy'])

export const DEFAULT_TAB: TabKey = 'overview'

export const isTabKey = (value: string): value is TabKey => TABS.some(([key]) => key === value)

export interface TabLock {
  /** One permission, or several of which any one unlocks the tab. */
  can: ActionKey | ActionKey[]
  action: LockAction
}

/** True when `can` grants the lock's permission (any one of them for an any-of lock). */
export const isUnlocked = (lock: TabLock, can: Record<ActionKey, boolean>): boolean =>
  (Array.isArray(lock.can) ? lock.can : [lock.can]).some((key) => can[key])

/** Tabs that need a permission beyond View, with the permission and its display name. */
export const TAB_LOCKS: Partial<Record<TabKey, TabLock>> = {
  deploy: { can: ['deploy', 'edit', 'operate'], action: 'Edit, Deploy or Operate' },
  logs: { can: 'logs', action: 'Logs' },
  terminal: { can: 'terminal', action: 'Terminal' },
}
