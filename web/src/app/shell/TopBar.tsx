import { Fragment } from 'react'
import { Link } from 'react-router'
import { useAlerts } from '../../api/observe'
import { Icon } from '../../components/Icon'
import { toggleDensity, useDensity } from '../density'
import { toggleTheme } from '../theme'
import { useCrumbsValue } from './crumbs'
import { FreshnessControl } from './FreshnessControl'
import { shortcutLabel } from './platform'
import { ScopeControl } from './ScopeControl'
import { useShellState } from './ShellState'
import { useFilters } from './useFilters'
import styles from './shell.module.css'

export { isMac, shortcutLabel } from './platform'

export function TopBar() {
  const crumbs = useCrumbsValue()
  const { railOpen, toggleRail, openPalette, openHelp } = useShellState()
  const { filters } = useFilters()
  const alerts = useAlerts(filters)
  const density = useDensity()
  const problems = alerts.data?.counts.danger ?? 0
  return (
    <header className={styles.topbar}>
      <ScopeControl />
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
      <button type="button" className={styles.searchBtn} aria-haspopup="dialog" onClick={openPalette}>
        <Icon name="search" />
        <span className={styles.searchText}>Search or run a command…</span>
        <kbd className={styles.kbd}>{shortcutLabel()}</kbd>
      </button>
      <FreshnessControl />
      <button
        type="button"
        className={`${styles.iconBtn} ${railOpen ? styles.btnOn : ''}`}
        aria-label={problems > 0 ? `Activity, ${problems} ${problems === 1 ? 'problem' : 'problems'}` : 'Activity'}
        aria-haspopup="dialog"
        aria-expanded={railOpen}
        title="Activity"
        onClick={toggleRail}
      >
        <Icon name="activity" />
        {problems > 0 && <span className={styles.activityCount}>{problems}</span>}
      </button>
      <button type="button" className={styles.iconBtn} aria-label="Toggle row density" title={`Row density: ${density}`} onClick={toggleDensity}>
        <Icon name="density" />
      </button>
      <button type="button" className={styles.iconBtn} aria-label="Toggle theme" title="Theme" onClick={toggleTheme}>
        <Icon name="sun" />
      </button>
      <button type="button" className={styles.iconBtn} aria-label="Keyboard shortcuts" title="Keyboard shortcuts" onClick={openHelp}>
        <Icon name="help" />
      </button>
    </header>
  )
}
