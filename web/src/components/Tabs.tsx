import type { ReactNode } from 'react'
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
}

interface TabsProps {
  items: TabItem[]
  active: string
  hrefFor: (key: string) => string
}

export function Tabs({ items, active, hrefFor }: TabsProps) {
  return (
    <nav className={styles.tabs}>
      {items.map((item) => {
        const className = [item.key === active ? styles.active : '', item.locked ? styles.locked : ''].join(' ').trim()
        return (
          <Link
            key={item.key}
            to={hrefFor(item.key)}
            className={className}
            title={item.locked ? `You need ${item.locked.action} on ${item.locked.tag}` : undefined}
          >
            {item.bold ? <strong>{item.label}</strong> : item.label}
            {item.locked && ' ⊘'}
            {item.badge !== undefined && <span className={styles.badge}>{item.badge}</span>}
          </Link>
        )
      })}
    </nav>
  )
}
