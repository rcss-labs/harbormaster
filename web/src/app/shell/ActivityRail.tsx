import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { useOverview } from '../../api/hooks'
import { useAlerts } from '../../api/observe'
import { formatAgo, useNow } from '../../components/Ago'
import { Drawer } from '../../components/Drawer'
import type { PillKind } from '../../components/Pill'
import { SegmentedFilter } from '../../components/Seg'
import { StatusDot } from '../../components/StatusDot'
import { buildActivity, collapseRepeats, filterActivity, type ActivityFilter, type ActivitySeverity } from './activity'
import { useShellState } from './ShellState'
import { useFilters } from './useFilters'
import styles from './shell.module.css'

const SEVERITY_KIND: Record<ActivitySeverity, PillKind> = {
  danger: 'bad',
  warning: 'warn',
  info: 'info',
  ok: 'ok',
}

const SEVERITY_LABEL: Record<ActivitySeverity, string> = {
  danger: 'Problem',
  warning: 'Warning',
  info: 'Info',
  ok: 'OK',
}

/** Recent problems and changes in a drawer. It opens from the top bar and never docks beside the page. */
export function ActivityRail() {
  const { railOpen, closeRail } = useShellState()
  const { filters } = useFilters()
  const alerts = useAlerts(filters)
  const overview = useOverview(filters)
  const now = useNow(30_000)
  const [filter, setFilter] = useState<ActivityFilter>('problems')

  const items = useMemo(() => buildActivity(alerts.data?.items ?? [], overview.data?.events ?? []), [alerts.data, overview.data])
  const rows = useMemo(() => collapseRepeats(filterActivity(items, filter)), [items, filter])
  const counts = useMemo(
    () => ({
      problems: collapseRepeats(filterActivity(items, 'problems')).length,
      changes: collapseRepeats(filterActivity(items, 'changes')).length,
      all: collapseRepeats(items).length,
    }),
    [items],
  )

  return (
    <Drawer open={railOpen} onClose={closeRail} title="Activity" closeLabel="Close activity">
      <div className={styles.railFilters}>
        <SegmentedFilter
          label="Activity filter"
          value={filter}
          onChange={setFilter}
          options={[
            { value: 'problems', label: 'Problems', count: counts.problems },
            { value: 'changes', label: 'Changes', count: counts.changes },
            { value: 'all', label: 'All', count: counts.all },
          ]}
        />
      </div>
      <div className={styles.railList}>
        {rows.length === 0 && <div className={styles.railEmpty}>Nothing here.</div>}
        {rows.map((row) => (
          <div key={`${row.kind}|${row.subject}|${row.text}`} className={styles.railRow}>
            <StatusDot bare kind={SEVERITY_KIND[row.severity]} title={SEVERITY_LABEL[row.severity]}>
              {SEVERITY_LABEL[row.severity]}
            </StatusDot>
            <div className={styles.railBody}>
              <div className={styles.railTop}>
                <Link to={row.href} className={styles.railSubject} onClick={closeRail}>
                  {row.subject}
                </Link>
                {row.count > 1 && <span className={styles.railCount}>×{row.count}</span>}
                <span className={styles.railWhen}>{formatAgo(row.at, now)}</span>
              </div>
              <span className={styles.railText}>{row.text}</span>
              <span className={styles.railActor}>{row.actor}</span>
            </div>
          </div>
        ))}
      </div>
    </Drawer>
  )
}
