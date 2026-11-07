import type { ReactNode } from 'react'
import styles from './Banner.module.css'
import { Icon } from './Icon'

interface BannerProps {
  kind: 'info' | 'warn' | 'bad'
  children: ReactNode
  /** Trailing control, e.g. a link or button. */
  action?: ReactNode
  /** Single-line, tighter variant for page-level data notices. */
  compact?: boolean
}

export function Banner({ kind, children, action, compact = false }: BannerProps) {
  return (
    <div data-hm-banner={kind} className={`${styles.banner} ${styles[kind]} ${compact ? styles.compact : ''}`.trim()}>
      <Icon name={kind === 'info' ? 'info' : 'warn'} />
      <span className={styles.text}>{children}</span>
      {action !== undefined && <span className={styles.action}>{action}</span>}
    </div>
  )
}
