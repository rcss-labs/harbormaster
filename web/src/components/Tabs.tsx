import { Fragment, type ReactNode } from 'react'
import { Link } from 'react-router'
import styles from './Tabs.module.css'

export interface TabItem {
  key: string
  label: string
  /** Set when the user lacks the permission: shows a lock and explains why. */
  locked?: { action: string; tag: string }
  badge?: ReactNode
  /** Renders the label in <strong>. */
  bold?: boolean
  /** Items that share a group sit together after a gap; the group name labels the first one. Optional: tabs without it are unchanged. */
  group?: string
}

interface TabsProps {
  items: TabItem[]
  active: string
  hrefFor: (key: string) => string
  /** Accessible name of the tab bar. */
  ariaLabel?: string
}

export function Tabs({ items, active, hrefFor, ariaLabel }: TabsProps) {
  return (
    <nav className={styles.tabs} aria-label={ariaLabel}>
      {items.map((item, index) => {
        const previousGroup = index > 0 ? items[index - 1].group : undefined
        const groupChanged = index > 0 && item.group !== previousGroup
        const className = [item.key === active ? styles.active : '', item.locked ? styles.locked : ''].join(' ').trim()
        return (
          <Fragment key={item.key}>
            {groupChanged && <span role="separator" className={styles.gap} />}
            {groupChanged && item.group && <span className={styles.groupLabel}>{item.group}</span>}
          <Link
            to={hrefFor(item.key)}
            className={className}
            title={item.locked ? `You need ${item.locked.action} on ${item.locked.tag}` : undefined}
          >
            {item.bold ? <strong>{item.label}</strong> : item.label}
            {item.locked && ' ⊘'}
            {item.badge !== undefined && <span className={styles.badge}>{item.badge}</span>}
          </Link>
          </Fragment>
        )
      })}
    </nav>
  )
}
