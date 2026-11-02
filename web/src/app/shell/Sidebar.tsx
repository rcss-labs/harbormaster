import { useState } from 'react'
import { NavLink, useLocation, useNavigate } from 'react-router'
import { useAlerts } from '../../api/observe'
import { useAuthOptions, useClusters, usePollerStatus, useSettings, useTasks } from '../../api/hooks'
import { Brand } from '../../components/Brand'
import { useToast } from '../../components/Toast'
import { useAuth } from '../auth/AuthProvider'
import { LinkSSODialog } from './LinkSSODialog'
import { usePinnedServices } from './pins'
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
  const { pinned } = usePinnedServices()
  const [linking, setLinking] = useState(false)
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
      <nav className={styles.pinned} aria-label="Pinned services">
        <div className={styles.navSection}>Pinned</div>
        {pinned.length === 0 && <div className={styles.pinnedEmpty}>Pin a service from its page.</div>}
        {pinned.map((service) => (
          <NavLink key={service.id} to={`/services/${service.id}/overview`} title={`${service.name} · ${service.cluster}`}>
            <span className={styles.pinName}>{service.name}</span>
            <span className={styles.pinCluster}>{service.cluster}</span>
          </NavLink>
        ))}
      </nav>
      <div className={styles.sideFoot}>
        <div className={styles.user}>
          <span className={styles.avatar} aria-hidden="true" title={me.username}>
            {initials(me.username)}
          </span>
          <div className={styles.userText}>
            Signed in as <strong>{me.username}</strong> · {me.admin ? 'Global admin' : 'User'}
          </div>
        </div>
        <div className={styles.sideActions}>
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
        </div>
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
