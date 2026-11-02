import { useState, type ReactNode } from 'react'
import { NavLink, useLocation, useNavigate } from 'react-router'
import { useAlerts } from '../../api/observe'
import { useAuthOptions, useClusters, useSettings, useTasks } from '../../api/hooks'
import { Brand } from '../../components/Brand'
import { useToast } from '../../components/Toast'
import { useAuth } from '../auth/AuthProvider'
import { LinkSSODialog } from './LinkSSODialog'
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
  count?: number
  bad?: boolean
  /** Coloured counts after the label; they come in addition to `count`. */
  badges?: NavBadge[]
  /** Extra path prefixes that also mark this item active. */
  alsoActiveFor?: string[]
  end?: boolean
}

function NavItem({ to, label, count, bad, badges = [], alsoActiveFor = [], end }: NavItemProps) {
  const { pathname } = useLocation()
  const extra = alsoActiveFor.some((prefix) => pathname.startsWith(prefix))
  return (
    <NavLink to={to} end={end} className={({ isActive }) => (isActive || extra ? styles.active : undefined)}>
      {label}
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
    </NavLink>
  )
}

function Section({ children }: { children: ReactNode }) {
  return <div className={styles.navSection}>{children}</div>
}

export function Sidebar() {
  const { me, logout } = useAuth()
  const navigate = useNavigate()
  const toast = useToast()
  const { filters } = useFilters()
  const isAdmin = me?.admin ?? false
  const alerts = useAlerts(filters)
  const clusters = useClusters(filters)
  const running = useTasks({ ...filters, filter: 'running' })
  const settings = useSettings()
  const authOptions = useAuthOptions()
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
  const dangerAlerts = alerts.data?.counts.danger ?? 0
  const signOut = async () => {
    try {
      await logout()
    } catch {
      toast.show('Sign out failed on the server. You were signed out in this browser.', 'bad')
    }
  }

  return (
    <aside className={styles.sidebar}>
      <div className={styles.brand}>
        <Brand variant="sidebar" />
        <div className={styles.brandSub}>ECS control center</div>
      </div>
      <nav className={styles.nav} aria-label="Main">
        <NavItem to="/" end label="Overview" count={dangerAlerts > 0 ? dangerAlerts : undefined} bad />
        <NavItem to="/clusters" label="Clusters" count={clusters.data?.items.length} alsoActiveFor={['/clusters', '/services']} />
        <NavItem to="/tasks" label="Tasks" badges={taskBadges} />
        <NavItem to="/networks" label="Networks" />
        <NavItem to="/storage" label="Storage" />
        {isAdmin ? (
          <>
            <Section>Admin</Section>
            <NavItem to="/settings/general" label="Settings" alsoActiveFor={['/settings']} />
          </>
        ) : (
          <>
            <Section>Account</Section>
            <NavItem to="/me/access" label="My access" />
          </>
        )}
      </nav>
      <div className={styles.sideFoot}>
        <div>
          Signed in as <strong>{me.username}</strong> · {me.admin ? 'Global admin' : 'User'}
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
        <div>
          Running on ECS · <code>harbormaster/harbormaster-server</code>
        </div>
      </div>
      {linking && <LinkSSODialog onClose={() => setLinking(false)} />}
    </aside>
  )
}
