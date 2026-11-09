import { useState } from 'react'
import { Link, NavLink, useLocation, useNavigate } from 'react-router'
import { useAlerts } from '../../api/observe'
import { useAuthOptions, useClusters, usePollerStatus, useServicesList, useSettings, useTasks } from '../../api/hooks'
import type { ClusterCardDTO } from '../../api/types'
import { Brand } from '../../components/Brand'
import { Icon } from '../../components/Icon'
import type { IconName } from '../../components/icons'
import { Popover } from '../../components/Popover'
import { StatusDot } from '../../components/StatusDot'
import { useToast } from '../../components/Toast'
import { useAuth } from '../auth/AuthProvider'
import { LinkSSODialog } from './LinkSSODialog'
import type { Pin } from './pins'
import { usePins } from './pins'
import { pinStatus } from './pinStatus'
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
  icon: IconName
  count?: number
  bad?: boolean
  /** Coloured counts after the label; they come in addition to `count`. */
  badges?: NavBadge[]
  /** Extra path prefixes that also mark this item active. */
  alsoActiveFor?: string[]
  end?: boolean
}

function NavItem({ to, label, icon, count, bad, badges = [], alsoActiveFor = [], end }: NavItemProps) {
  const { pathname } = useLocation()
  const extra = alsoActiveFor.some((prefix) => pathname.startsWith(prefix))
  const hasCounts = count !== undefined || badges.length > 0
  const tooltip = [label, ...(count !== undefined ? [String(count)] : []), ...badges.map((b) => b.label)].join(' · ')
  return (
    <NavLink to={to} end={end} title={tooltip} className={({ isActive }) => (isActive || extra ? styles.active : undefined)}>
      <Icon name={icon} className={styles.navIcon} />
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

/** Two lines: status mark and full name, then the cluster (service) or account (cluster). The mark follows the live status. */
function PinnedItem({ pin, clusters }: PinnedItemProps) {
  const { kind, text: statusText } = pinStatus(pin, clusters)
  const detail = pin.kind === 'cluster' ? `${pin.account_name} · ${pin.account_id}` : pin.cluster
  const description = `${pin.name} · ${statusText} · ${detail}`
  const to = pin.kind === 'cluster' ? `/clusters/${pin.id}` : `/services/${pin.id}/overview`
  return (
    <NavLink to={to} title={description} aria-label={description}>
      <span className={styles.pinTop}>
        <span className={styles.pinMark} data-kind={kind} aria-hidden="true">
          <StatusDot bare kind={kind} />
        </span>
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
  const services = useServicesList(filters)
  const running = useTasks({ ...filters, filter: 'running' })
  const settings = useSettings()
  const authOptions = useAuthOptions()
  const poller = usePollerStatus()
  const { pins } = usePins()
  // Unfiltered, so a pin outside the current scope filter still shows its status.
  const allClusters = useClusters()
  const [linking, setLinking] = useState(false)
  const [menuOpen, setMenuOpen] = useState(false)
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
    setMenuOpen(false)
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
        <NavItem to="/" end icon="overview" label="Overview" count={dangerAlerts > 0 ? dangerAlerts : undefined} bad />
        <NavItem to="/services" icon="services" label="Services" count={services.data?.total} />
        <NavItem to="/clusters" icon="clusters" label="Clusters" count={clusters.data?.items.length} />
        <NavItem to="/tasks" icon="tasks" label="Tasks" badges={taskBadges} />
        <div className={styles.navSection}>Infrastructure</div>
        <NavItem to="/networks" icon="networks" label="Networks" />
        <NavItem to="/storage" icon="storage" label="Storage" />
        <div className={styles.navSection}>Insights</div>
        <NavItem to="/analytics/delivery" icon="analytics" label="Analytics" alsoActiveFor={['/analytics']} />
        <NavItem to="/security" icon="shield" label="Security" />
        <NavItem to="/logs" icon="logs" label="Log search" />
        {isAdmin ? (
          <>
            <div className={styles.navSection}>Admin</div>
            <NavItem to="/settings/general" icon="settings" label="Settings" badges={settingsBadges} alsoActiveFor={['/settings']} />
          </>
        ) : (
          <>
            <div className={styles.navSection}>Account</div>
            <NavItem to="/me/access" icon="user" label="My access" />
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
        <div className={styles.accWrap}>
          <button
            type="button"
            className={styles.accountBtn}
            aria-label={`Account menu for ${me.username}`}
            aria-haspopup="dialog"
            aria-expanded={menuOpen}
            title={`${me.username} · ${me.admin ? 'Global admin' : 'User'}`}
            onClick={() => setMenuOpen((open) => !open)}
          >
            <span className={styles.avatar} aria-hidden="true">
              {initials(me.username)}
            </span>
            <span className={styles.userText}>
              <strong>{me.username}</strong> · {me.admin ? 'Global admin' : 'User'}
            </span>
          </button>
          <Popover open={menuOpen} onClose={() => setMenuOpen(false)} label="Account" width={232} role="group">
            <div className={styles.accountMenu}>
              <Link to="/me/access" className={styles.menuItem} onClick={() => setMenuOpen(false)}>
                My access
              </Link>
              {me.has_password && (
                <button
                  type="button"
                  className={styles.menuItem}
                  onClick={() => {
                    setMenuOpen(false)
                    navigate('/password')
                  }}
                >
                  Change password
                </button>
              )}
              {canLinkSSO && (
                <button
                  type="button"
                  className={styles.menuItem}
                  onClick={() => {
                    setMenuOpen(false)
                    setLinking(true)
                  }}
                >
                  Link SSO account
                </button>
              )}
              <button type="button" className={`${styles.menuItem} ${styles.menuDanger}`} onClick={signOut}>
                Sign out
              </button>
            </div>
          </Popover>
        </div>
        <button
          type="button"
          className={styles.collapseBtn}
          aria-expanded={!sidebarCollapsed}
          aria-label={sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'}
          title={`${sidebarCollapsed ? 'Expand' : 'Collapse'} sidebar ([)`}
          onClick={toggleSidebar}
        >
          <Icon name="panel" className={styles.navIcon} />
          <span className={styles.collapseText} aria-hidden="true">
            Collapse
          </span>
          <kbd className={`${styles.kbd} ${styles.collapseText}`} aria-hidden="true">
            [
          </kbd>
        </button>
      </div>
      {linking && <LinkSSODialog onClose={() => setLinking(false)} />}
    </aside>
  )
}
