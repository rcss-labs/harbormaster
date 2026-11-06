import { useState } from 'react'
import { Link } from 'react-router'
import { usePollerStatus } from '../../api/hooks'
import { useDataProblems } from '../../api/observe'
import { Icon } from '../../components/Icon'
import { StatusDot } from '../../components/StatusDot'
import { useAuth } from '../auth/AuthProvider'
import { useIsDark } from '../theme'
import { useCrumbsValue } from './crumbs'
import { worstKind } from './FreshnessControl'
import { FreshnessSheet, ScopeSheet } from './MobileSheets'
import { scopeLabel, scopeShort } from './ScopeControl'
import { useShellState } from './ShellState'
import { useFilters } from './useFilters'
import styles from './shell.module.css'

/**
 * The phone top bar: one row that never wraps. Left: back to the previous crumb, or the logo on a top-level page.
 * Middle: the page title (up to two lines) with the cluster or section above it. Right: scope, freshness, search.
 * Everything else the desktop bar carries (activity, theme, account) lives on the More page.
 */
export function MobileTopBar() {
  const crumbs = useCrumbsValue()
  const { me } = useAuth()
  const { filters } = useFilters()
  const { openPalette } = useShellState()
  const poller = usePollerStatus()
  const { current, total } = useDataProblems()
  const dark = useIsDark()
  const [scopeOpen, setScopeOpen] = useState(false)
  const [freshOpen, setFreshOpen] = useState(false)

  const title = crumbs.length > 0 ? crumbs[crumbs.length - 1][0] : ''
  const sub = crumbs
    .slice(0, -1)
    .map(([label]) => label)
    .join(' · ')
  const previous = crumbs.length >= 2 ? crumbs[crumbs.length - 2] : null
  const freshness = `${current} of ${total} regions current`

  return (
    <header className={styles.mtop}>
      {previous?.[1] ? (
        <Link to={previous[1]} className={styles.mIcon} aria-label={`Back to ${previous[0]}`}>
          <Icon name="back" size={18} />
        </Link>
      ) : (
        <Link to="/" className={styles.mIcon} aria-label="Harbormaster, go to Overview">
          <img className={styles.mLogo} src={dark ? '/brand/harbormaster-symbol-dark.svg' : '/brand/harbormaster-symbol.svg'} alt="" />
        </Link>
      )}
      <div className={styles.mTitle} data-hm-clip-ok="">
        <span className={styles.mTitleText}>{title}</span>
        {sub && <span className={styles.mSub}>{sub}</span>}
      </div>
      {me && (
        <button
          type="button"
          className={styles.mScope}
          data-hm-hit="36"
          data-hm-clip-ok=""
          aria-label={`Scope: ${scopeLabel(me, filters)}`}
          aria-haspopup="dialog"
          aria-expanded={scopeOpen}
          onClick={() => setScopeOpen(true)}
        >
          <Icon name="filter" size={14} />
          <span className={styles.mScopeText}>{scopeShort(me, filters)}</span>
        </button>
      )}
      {poller.data && (
        <button type="button" className={`${styles.mIcon} ${styles.mFresh}`} aria-label={freshness} aria-haspopup="dialog" aria-expanded={freshOpen} onClick={() => setFreshOpen(true)}>
          <StatusDot bare kind={worstKind(poller.data.items)} />
          <span>{`${current}/${total}`}</span>
        </button>
      )}
      <button type="button" className={styles.mIcon} aria-label="Search or run a command" aria-haspopup="dialog" onClick={openPalette}>
        <Icon name="search" size={18} />
      </button>
      <ScopeSheet open={scopeOpen} onClose={() => setScopeOpen(false)} />
      <FreshnessSheet open={freshOpen} onClose={() => setFreshOpen(false)} />
    </header>
  )
}
