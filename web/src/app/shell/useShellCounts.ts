import { usePollerStatus, useTasks } from '../../api/hooks'
import { useAlerts } from '../../api/observe'
import { useFilters } from './useFilters'

export interface ShellCounts {
  /** Danger alerts in the current scope: the Overview badge and the Activity "N problems". */
  problems: number
  /** Tasks stopped with errors (red) take precedence over unhealthy running tasks (amber). */
  tasks: { count: number; kind: 'bad' | 'warn' } | null
  /** Regions whose data is failing or throttled. */
  accountIssues: number
}

/** The counts the phone shell shows on tab badges and the More page; the same sources as the sidebar badges. */
export function useShellCounts(): ShellCounts {
  const { filters } = useFilters()
  const alerts = useAlerts(filters)
  const running = useTasks({ ...filters, filter: 'running' })
  const poller = usePollerStatus()
  const stoppedErrors = running.data?.counts.stopped_error ?? 0
  const unhealthy = running.data?.summary.by_status.running_unhealthy ?? 0
  const tasks = stoppedErrors > 0 ? { count: stoppedErrors, kind: 'bad' as const } : unhealthy > 0 ? { count: unhealthy, kind: 'warn' as const } : null
  return {
    problems: alerts.data?.counts.danger ?? 0,
    tasks,
    accountIssues: (poller.data?.items ?? []).filter((entry) => entry.failing || entry.throttled).length,
  }
}
