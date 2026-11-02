import { useEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router'
import { useOverview } from '../../api/hooks'
import { useAlerts } from '../../api/observe'
import { formatAgo, useNow } from '../../components/Ago'
import { SegmentedFilter } from '../../components/Seg'
import { buildActivity, collapseRepeats, filterActivity, type ActivityFilter, type ActivitySeverity } from './activity'
import { useShellState } from './ShellState'
import { useFilters } from './useFilters'
import styles from './shell.module.css'

const SEVERITY_CLASS: Record<ActivitySeverity, string> = {
  danger: styles.sevDanger,
  warning: styles.sevWarning,
  info: styles.sevInfo,
  ok: styles.sevOk,
}

/** Recent problems and changes beside the page; an overlay with a backdrop on narrower screens. */
export function ActivityRail() {
  const { railOpen, railDocked, closeRail, paletteOpen } = useShellState()
  const { filters } = useFilters()
  const alerts = useAlerts(filters)
  const overview = useOverview(filters)
  const now = useNow(30_000)
  const [filter, setFilter] = useState<ActivityFilter>('problems')
  const closeRef = useRef<HTMLButtonElement>(null)
  const overlay = railOpen && !railDocked

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

  // Move focus into the overlay when it opens, unless the palette took it.
  useEffect(() => {
    if (overlay && !paletteOpen) closeRef.current?.focus()
  }, [overlay, paletteOpen])

  if (!railOpen) return null

  const panel = (
    <aside
      className={`${styles.rail} ${overlay ? styles.railOverlay : ''}`}
      aria-label="Activity"
      {...(overlay ? { role: 'dialog' } : {})}
    >
      <div className={styles.railHead}>
        <h2>Activity</h2>
        <button ref={closeRef} type="button" className={styles.railClose} aria-label="Close activity" onClick={closeRail}>
          ×
        </button>
      </div>
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
            <span className={`${styles.railDot} ${SEVERITY_CLASS[row.severity]}`} aria-hidden="true" />
            <div className={styles.railBody}>
              <div className={styles.railTop}>
                <Link to={row.href} className={styles.railSubject} onClick={overlay ? closeRail : undefined}>
                  {row.subject}
                </Link>
                {row.count > 1 && <span className={`${styles.railCount} ${SEVERITY_CLASS[row.severity]}`}>×{row.count}</span>}
                <span className={styles.railWhen}>{formatAgo(row.at, now)}</span>
              </div>
              <span className={styles.railText}>{row.text}</span>
              <span className={styles.railActor}>{row.actor}</span>
            </div>
          </div>
        ))}
      </div>
    </aside>
  )

  if (!overlay) return panel
  return (
    <>
      <div className={styles.railBackdrop} onMouseDown={closeRail} />
      {panel}
    </>
  )
}
