import type { ReactNode } from 'react'
import { NavLink, useLocation, useNavigate } from 'react-router'
import { useAlerts } from '../../api/observe'
import { useAccounts, useClusters, useTags, useTasks, useUsers } from '../../api/hooks'
import { Brand } from '../../components/Brand'
import { useToast } from '../../components/Toast'
import { useAuth } from '../auth/AuthProvider'
import { useFilters } from './useFilters'
import styles from './shell.module.css'

interface NavItemProps {
  to: string
  label: string
  count?: number
  bad?: boolean
  /** Extra path prefixes that also mark this item active. */
  alsoActiveFor?: string[]
  end?: boolean
}

function NavItem({ to, label, count, bad, alsoActiveFor = [], end }: NavItemProps) {
  const { pathname } = useLocation()
  const extra = alsoActiveFor.some((prefix) => pathname.startsWith(prefix))
  return (
    <NavLink to={to} end={end} className={({ isActive }) => (isActive || extra ? styles.active : undefined)}>
      {label}
      {count !== undefined && <span className={`${styles.count} ${bad ? styles.countBad : ''}`}>{count}</span>}
    </NavLink>
  )
}

function DisabledNavItem({ label }: { label: string }) {
  return (
    <a className={styles.disabled} aria-disabled="true" title="Phase 5">
      {label}
    </a>
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
  const users = useUsers({ enabled: isAdmin })
  const tags = useTags({ enabled: isAdmin })
  const accounts = useAccounts({ enabled: isAdmin })
  if (!me) return null

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
        <NavItem to="/tasks" label="Tasks" count={running.data?.counts.running} />
        <DisabledNavItem label="Networks" />
        <DisabledNavItem label="Storage" />
        {isAdmin ? (
          <>
            <Section>Admin</Section>
            <NavItem to="/admin/users" label="Users and permissions" count={users.data?.items.length} />
            <NavItem to="/admin/tags" label="Tags" count={tags.data?.items.length} />
            <NavItem to="/admin/accounts" label="AWS accounts" count={accounts.data?.items.length} />
            <NavItem to="/admin/audit" label="Audit log" />
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
          <button type="button" className={styles.btn} onClick={() => navigate('/password')}>
            Change password
          </button>
          <button type="button" className={styles.btn} onClick={signOut}>
            Sign out
          </button>
        </div>
        <div>
          Running on ECS · <code>harbormaster/harbormaster-server</code>
        </div>
      </div>
    </aside>
  )
}
