import type { NotificationRoute } from '../../../api/notifications'
import { Pill } from '../../../components/Pill'
import { destinationLabel, scopeSummary } from './notifications'
import styles from './notifications.module.css'

interface RouteListProps {
  routes: NotificationRoute[]
  selected: number | 'new'
  /** The name of the unsaved draft, listed first while it exists. */
  newName: string | null
  onSelect: (id: number | 'new') => void
}

export function RouteList({ routes, selected, newName, onSelect }: RouteListProps) {
  if (routes.length === 0 && newName === null) {
    return <p className={styles.listEmpty}>No routes yet. Add one to send alerts to Slack, Telegram, PagerDuty, a webhook or email.</p>
  }
  return (
    <ul className={styles.list} aria-label="Routes">
      {newName !== null && (
        <li className={styles.listItem}>
          <button type="button" className={styles.listButton} aria-current={selected === 'new'} onClick={() => onSelect('new')}>
            <span>
              <span className={styles.listName}>{newName}</span>
              <span className={`${styles.muted} ${styles.small} ${styles.listSub}`}>Not saved yet</span>
            </span>
            <Pill kind="mute">Off</Pill>
          </button>
        </li>
      )}
      {routes.map((route) => (
        <li key={route.id} className={styles.listItem}>
          <button type="button" className={styles.listButton} aria-current={selected === route.id} onClick={() => onSelect(route.id)}>
            <span>
              <span className={styles.listName}>{route.name}</span>
              <span className={`${styles.muted} ${styles.small} ${styles.listSub}`}>{`${destinationLabel(route.destination)}: ${route.target}`}</span>
              <span className={`${styles.muted} ${styles.small} ${styles.listSub}`}>{scopeSummary(route)}</span>
              {route.secret_unreadable && <span className={`${styles.error} ${styles.listSub}`}>Enter the destination again.</span>}
            </span>
            <span className={styles.pills}>
              <Pill kind={route.enabled ? 'ok' : 'mute'}>{route.enabled ? 'On' : 'Off'}</Pill>
              {route.fallback && <Pill kind="info">Catch-all</Pill>}
              {route.matches_nothing && <Pill kind="warn">Matches nothing</Pill>}
              {route.failing && <Pill kind="bad">Failing</Pill>}
            </span>
          </button>
        </li>
      ))}
    </ul>
  )
}
