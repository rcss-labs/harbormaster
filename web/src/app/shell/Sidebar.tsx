import { useState, type DragEvent, type KeyboardEvent } from 'react'
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
import { missingPinHint, pinStatus } from './pinStatus'
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
  onRemove: () => void
  index: number
  count: number
  /** Moves this pin to a new index. */
  onMove: (toIndex: number) => void
  /** Another pin, identified by its drag data key, was dropped on this one. */
  onDropFrom: (draggedKey: string) => void
}

const PIN_DRAG_TYPE = 'application/x-harbormaster-pin'
const pinKey = (pin: Pin) => `${pin.kind}:${pin.id}`

/**
 * Two lines: status mark and full name, then the cluster (service) or account (cluster). The mark follows the live status.
 * A missing pin gets a remove button beside the link, since its page can no longer be opened to unpin it there.
 */
function PinnedItem({ pin, clusters, onRemove, index, count, onMove, onDropFrom }: PinnedItemProps) {
  const [dragOver, setDragOver] = useState(false)
  const onKeyDown = (event: KeyboardEvent) => {
    if (!event.altKey || (event.key !== 'ArrowUp' && event.key !== 'ArrowDown')) return
    event.preventDefault()
    onMove(index + (event.key === 'ArrowUp' ? -1 : 1))
  }
  const onDrop = (event: DragEvent) => {
    event.preventDefault()
    setDragOver(false)
    const draggedKey = event.dataTransfer.getData(PIN_DRAG_TYPE)
    if (draggedKey) onDropFrom(draggedKey)
  }
  const { kind, text: statusText, missing } = pinStatus(pin, clusters)
  const detail = pin.kind === 'cluster' ? `${pin.account_name} · ${pin.account_id}` : pin.cluster
  const description = `${pin.name} · ${statusText} · ${detail}${missing ? ` · ${missingPinHint}` : ''}`
  const to = pin.kind === 'cluster' ? `/clusters/${pin.id}` : `/services/${pin.id}/overview`
  return (
    <div
      className={`${styles.pinItem} ${dragOver ? styles.pinDropTarget : ''}`}
      draggable
      onKeyDown={onKeyDown}
      onDragStart={(event) => {
        event.dataTransfer.setData(PIN_DRAG_TYPE, pinKey(pin))
        event.dataTransfer.effectAllowed = 'move'
      }}
      onDragOver={(event) => {
        event.preventDefault()
        event.dataTransfer.dropEffect = 'move'
        setDragOver(true)
      }}
      onDragLeave={() => setDragOver(false)}
      onDrop={onDrop}
      onDragEnd={() => setDragOver(false)}
    >
      <NavLink to={to} title={description} aria-label={description}>
        <span className={styles.pinTop}>
          <span className={styles.pinMark} data-kind={kind} aria-hidden="true">
            <StatusDot bare kind={kind} />
          </span>
          <span className={styles.pinName}>{pin.name}</span>
        </span>
        <span className={styles.pinDetail}>{detail}</span>
      </NavLink>
      <span className={styles.pinMoves}>
        <button type="button" className={styles.pinMove} aria-label={`Move ${pin.name} up`} title="Move up (Alt+↑)" disabled={index === 0} onClick={() => onMove(index - 1)}>
          ↑
        </button>
        <button type="button" className={styles.pinMove} aria-label={`Move ${pin.name} down`} title="Move down (Alt+↓)" disabled={index === count - 1} onClick={() => onMove(index + 1)}>
          ↓
        </button>
      </span>
      {missing && (
        <button type="button" className={styles.pinRemove} aria-label={`Remove pin ${pin.name}`} title="Remove from pinned" onClick={onRemove}>
          ×
        </button>
      )}
    </div>
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
  const { pins, remove, move } = usePins()
  const [pinAnnouncement, setPinAnnouncement] = useState('')
  // Unfiltered, so a pin outside the current scope filter still shows its status.
  const allClusters = useClusters()
  const [linking, setLinking] = useState(false)
  const [menuOpen, setMenuOpen] = useState(false)
  if (!me) return null

  const movePin = (pin: Pin, toIndex: number) => {
    if (toIndex < 0 || toIndex >= pins.length) return
    move(pin.kind, pin.id, toIndex)
    setPinAnnouncement(`${pin.name} moved to position ${toIndex + 1} of ${pins.length}`)
  }
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
        <div className={styles.srOnly} role="status" aria-live="polite">
          {pinAnnouncement}
        </div>
        {pins.map((pin, index) => (
          <PinnedItem
            key={pinKey(pin)}
            pin={pin}
            index={index}
            count={pins.length}
            clusters={allClusters.data?.items}
            onRemove={() => remove(pin.kind, pin.id)}
            onMove={(toIndex) => movePin(pin, toIndex)}
            onDropFrom={(draggedKey) => {
              const dragged = pins.find((p) => pinKey(p) === draggedKey)
              if (dragged) movePin(dragged, index)
            }}
          />
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
