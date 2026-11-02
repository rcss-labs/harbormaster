import { Fragment } from 'react'
import { Link } from 'react-router'
import { useOverview } from '../../api/hooks'
import { useAlerts } from '../../api/observe'
import { formatAgo, useNow } from '../../components/Ago'
import { useCrumbsValue } from './crumbs'
import { Filters } from './Filters'
import { useShellState } from './ShellState'
import { ThemeToggle } from './ThemeToggle'
import { useFilters } from './useFilters'
import styles from './shell.module.css'

export const isMac = () => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
export const shortcutLabel = () => (isMac() ? '⌘K' : 'Ctrl K')

/** "live · polled 6s ago", from when the overview query last got fresh data. */
function LivePulse() {
  const { filters } = useFilters()
  const { dataUpdatedAt, isError } = useOverview(filters)
  const now = useNow(1000)
  if (!dataUpdatedAt) return null
  return (
    <span className={`${styles.pulse} ${isError ? styles.pulseStale : ''}`}>
      <span className={styles.pulseDot} aria-hidden="true" />
      {isError ? 'stale' : 'live'} · polled {formatAgo(dataUpdatedAt, now)}
    </span>
  )
}

export function TopBar() {
  const crumbs = useCrumbsValue()
  const { railOpen, toggleRail, openPalette, showPulse } = useShellState()
  const { filters } = useFilters()
  const alerts = useAlerts(filters)
  const problems = alerts.data?.counts.danger ?? 0
  return (
    <header className={styles.topbar}>
      <div className={styles.scope} role="group" aria-label="Scope">
        <span className={styles.scopeLabel} aria-hidden="true">
          SCOPE
        </span>
        <Filters />
      </div>
      <nav className={styles.crumbs} aria-label="Breadcrumb">
        {crumbs.map(([label, href], index) =>
          index === crumbs.length - 1 || !href ? (
            <span key={index} className={styles.here}>
              {label}
            </span>
          ) : (
            <Fragment key={index}>
              <Link to={href}>{label}</Link>
              <span className={styles.sep}>/</span>
            </Fragment>
          ),
        )}
      </nav>
      <div className={styles.topTools}>
        {showPulse && <LivePulse />}
        <button type="button" className={styles.searchBtn} aria-haspopup="dialog" onClick={openPalette}>
          <span className={styles.searchText}>Search or run a command…</span>
          <kbd className={styles.kbd}>{shortcutLabel()}</kbd>
        </button>
        <button type="button" className={`${styles.btn} ${railOpen ? styles.btnOn : ''}`} aria-expanded={railOpen} onClick={toggleRail}>
          Activity
          {problems > 0 && <span className={styles.activityCount}>{problems}</span>}
        </button>
        <ThemeToggle />
      </div>
    </header>
  )
}
