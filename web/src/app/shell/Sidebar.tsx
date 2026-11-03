import { useEffect, useState } from 'react'
import { NavLink, useLocation, useNavigate } from 'react-router'
import { useAlerts } from '../../api/observe'
import { useAuthOptions, useClusters, usePollerStatus, useSettings, useTasks } from '../../api/hooks'
import type { ClusterCardDTO } from '../../api/types'
import { Brand } from '../../components/Brand'
import type { PillKind } from '../../components/Pill'
import { useToast } from '../../components/Toast'
import { useAuth } from '../auth/AuthProvider'
import { LinkSSODialog } from './LinkSSODialog'
import { clusterStatusKind, clusterStatusLabel, statusKind, statusLabel } from '../../components/status'
import type { Pin } from './pins'
import { usePins } from './pins'
import { useShellState } from './ShellState'
import { useFilters } from './useFilters'
import styles from './shell.module.css'

interface NavBadge {
  count: number
  kind: 'warn' | 'bad'
  /** Accessible name and tooltip. */
  label: string
}

interface NavItemProps {
  to: string
  label: string
  /** Two-letter mono glyph shown in the box before the label. */
  glyph: string
  count?: number
  bad?: boolean
  /** Coloured counts after the label; they come in addition to `count`. */
  badges?: NavBadge[]
  /** Extra path prefixes that also mark this item active. */
  alsoActiveFor?: string[]
  end?: boolean
}

function NavItem({ to, label, glyph, count, bad, badges = [], alsoActiveFor = [], end }: NavItemProps) {
  const { pathname } = useLocation()
  const extra = alsoActiveFor.some((prefix) => pathname.startsWith(prefix))
  const hasCounts = count !== undefined || badges.length > 0
  const tooltip = [label, ...(count !== undefined ? [String(count)] : []), ...badges.map((b) => b.label)].join(' · ')
  return (
    <NavLink to={to} end={end} title={tooltip} className={({ isActive }) => (isActive || extra ? styles.active : undefined)}>
      <span className={styles.glyph} aria-hidden="true">
        {glyph}
      </span>
      <span className={styles.navLabel}>{label}</span>
      {hasCounts && (
        <span className={styles.counts}>
          {count !== undefined && <span className={`${styles.count} ${bad ? styles.countBad : ''}`}>{count}</span>}
          {badges.map((badge) => (
            <span
              key={badge.kind}
              className={`${styles.count} ${badge.kind === 'bad' ? styles.countBad : styles.countWarn}`}
              aria-label={badge.label}
              title={badge.label}
            >
              {badge.count}
            </span>
          ))}
        </span>
      )}
    </NavLink>
  )
}

interface PinnedItemProps {
  pin: Pin
  clusters: ClusterCardDTO[] | undefined
}

/** Two lines: status dot and full name, then the cluster (service) or account (cluster). The dot follows the live status. */
function PinnedItem({ pin, clusters }: PinnedItemProps) {
  let kind: PillKind = 'mute'
  let statusText = 'Status unknown'
  if (pin.kind === 'cluster') {
    const cluster = clusters?.find((c) => c.id === pin.id)
    if (cluster) {
      kind = clusterStatusKind(cluster.status)
      statusText = clusterStatusLabel(cluster.status)
    }
  } else {
    const service = clusters?.flatMap((c) => c.services).find((s) => s.id === pin.id)
    if (service) {
      kind = statusKind(service.status)
      statusText = statusLabel(service.status)
    }
  }
  const detail = pin.kind === 'cluster' ? `${pin.account_name} · ${pin.account_id}` : pin.cluster
  const description = `${pin.name} · ${statusText} · ${detail}`
  const to = pin.kind === 'cluster' ? `/clusters/${pin.id}` : `/services/${pin.id}/overview`
  return (
    <NavLink to={to} title={description} aria-label={description}>
      <span className={styles.pinTop}>
        <span className={`${styles.pinDot} ${styles[`pin_${kind}`]}`} data-kind={kind} aria-hidden="true" />
        <span className={styles.pinName}>{pin.name}</span>
      </span>
      <span className={styles.pinDetail}>{detail}</span>
    </NavLink>
  )
}

const initials = (name: string) => name.slice(0, 2).toUpperCase()

export function Sidebar() {
  const { me, logout } = useAuth()
  const navigate = useNavigate()
  const toast = useToast()
  const { sidebarCollapsed, toggleSidebar } = useShellState()
  const { filters } = useFilters()
  const isAdmin = me?.admin ?? false
  const alerts = useAlerts(filters)
  const clusters = useClusters(filters)
  const running = useTasks({ ...filters, filter: 'running' })
  const settings = useSettings()
  const authOptions = useAuthOptions()
  const poller = usePollerStatus()
  const { pins } = usePins()
  // Unfiltered, so a pin outside the current scope filter still shows its status.
  const allClusters = useClusters()
  const [linking, setLinking] = useState(false)
  const [menuOpen, setMenuOpen] = useState(false)
  const menuShown = menuOpen && sidebarCollapsed
  useEffect(() => {
    if (!menuShown) return
    const onKeyDown = (event: KeyboardEvent) => event.key === 'Escape' && setMenuOpen(false)
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [menuShown])
  if (!me) return null

  const canLinkSSO = authOptions.data?.sso != null && me.has_password && !me.sso_linked
  const unhealthy = running.data?.summary.by_status.running_unhealthy ?? 0
  const stoppedErrors = running.data?.counts.stopped_error ?? 0
  const windowMinutes = settings.data?.stopped_tasks_window_minutes
  const taskBadges: NavBadge[] = []
  if (unhealthy > 0) taskBadges.push({ count: unhealthy, kind: 'warn', label: `${unhealthy} unhealthy` })
  if (stoppedErrors > 0) {
    const within = windowMinutes === undefined ? '' : ` in the last ${windowMinutes} min`
    taskBadges.push({ count: stoppedErrors, kind: 'bad', label: `${stoppedErrors} stopped with errors${within}` })
  }
  const accountIssues = (poller.data?.items ?? []).filter((entry) => entry.failing || entry.throttled).length
  const settingsBadges: NavBadge[] = accountIssues > 0 ? [{ count: accountIssues, kind: 'warn', label: `${accountIssues} account issues` }] : []
  const dangerAlerts = alerts.data?.counts.danger ?? 0
  const signOut = async () => {
    try {
      await logout()
    } catch {
      toast.show('Sign out failed on the server. You were signed out in this browser.', 'bad')
    }
  }

  const actions = (
    <>
      {me.has_password && (
        <button type="button" className={styles.btn} onClick={() => navigate('/password')}>
          Change password
        </button>
      )}
      {canLinkSSO && (
        <button type="button" className={styles.btn} onClick={() => setLinking(true)}>
          Link SSO account
        </button>
      )}
      <button type="button" className={styles.btn} onClick={signOut}>
        Sign out
      </button>
    </>
  )

  return (
    <aside className={`${styles.sidebar} ${sidebarCollapsed ? styles.sidebarCollapsed : ''}`} aria-label="Sidebar">
      <div className={styles.brand} title="Running on ECS · harbormaster/harbormaster-server">
        <Brand variant="sidebar" />
        <div className={styles.brandSub}>ECS control center</div>
      </div>
      <nav className={styles.nav} aria-label="Main">
        <NavItem to="/" end glyph="OV" label="Overview" count={dangerAlerts > 0 ? dangerAlerts : undefined} bad />
        <NavItem to="/clusters" glyph="CL" label="Clusters" count={clusters.data?.items.length} alsoActiveFor={['/clusters', '/services']} />
        <NavItem to="/tasks" glyph="TK" label="Tasks" badges={taskBadges} />
        <NavItem to="/networks" glyph="NW" label="Networks" />
        <NavItem to="/storage" glyph="ST" label="Storage" />
        <NavItem to="/analytics/delivery" glyph="AN" label="Analytics" alsoActiveFor={['/analytics']} />
        {isAdmin ? (
          <>
            <div className={styles.navSection}>Admin</div>
            <NavItem to="/settings/general" glyph="SE" label="Settings" badges={settingsBadges} alsoActiveFor={['/settings']} />
          </>
        ) : (
          <>
            <div className={styles.navSection}>Account</div>
            <NavItem to="/me/access" glyph="MA" label="My access" />
          </>
        )}
      </nav>
      <nav className={styles.pinned} aria-label="Pinned">
        <div className={styles.navSection}>Pinned</div>
        {pins.length === 0 && <div className={styles.pinnedEmpty}>Pin a service or cluster from its page.</div>}
        {pins.map((pin) => (
          <PinnedItem key={`${pin.kind}:${pin.id}`} pin={pin} clusters={allClusters.data?.items} />
        ))}
      </nav>
      <div className={styles.sideFoot}>
        <div className={styles.user}>
          {sidebarCollapsed ? (
            // The text block and the action buttons are hidden when collapsed, so the avatar opens them as a menu.
            <button
              type="button"
              className={`${styles.avatar} ${styles.avatarBtn}`}
              aria-label={`Account menu for ${me.username}`}
              aria-haspopup="true"
              aria-expanded={menuShown}
              title={me.username}
              onClick={() => setMenuOpen((open) => !open)}
            >
              {initials(me.username)}
            </button>
          ) : (
            <span className={styles.avatar} aria-hidden="true" title={me.username}>
              {initials(me.username)}
            </span>
          )}
          <div className={styles.userText}>
            Signed in as <strong>{me.username}</strong> · {me.admin ? 'Global admin' : 'User'}
          </div>
        </div>
        {sidebarCollapsed ? (
          menuShown && (
            <div className={styles.accountMenu} role="group" aria-label="Account" onClick={() => setMenuOpen(false)}>
              {actions}
            </div>
          )
        ) : (
          <div className={styles.sideActions}>{actions}</div>
        )}
        <button
          type="button"
          className={styles.collapseBtn}
          aria-expanded={!sidebarCollapsed}
          aria-label={sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'}
          title={`${sidebarCollapsed ? 'Expand' : 'Collapse'} sidebar ([)`}
          onClick={toggleSidebar}
        >
          <span className={styles.collapseGlyph} aria-hidden="true">
            {sidebarCollapsed ? '»' : '«'}
          </span>
          <span className={styles.collapseText} aria-hidden="true">
            Collapse [
          </span>
        </button>
      </div>
      {linking && <LinkSSODialog onClose={() => setLinking(false)} />}
    </aside>
  )
}
