import { useId, type ReactNode } from 'react'
import styles from './Panel.module.css'

interface PanelProps {
  title: ReactNode
  /** Controls (filters, segments) shown beside the title; makes the header taller. */
  toolbar?: ReactNode
  /** Right-aligned header content such as a "Show all" link. */
  actions?: ReactNode
  children: ReactNode
  /** Remove body padding, e.g. for tables that run edge to edge. */
  flush?: boolean
  /** Shown before the title, e.g. a "PROPOSED" marker. */
  badge?: ReactNode
  /** Right-aligned header content describing the chart, such as colour keys with values. */
  legend?: ReactNode
  /** Mono strip under the body, e.g. where the data comes from. */
  footer?: ReactNode
  /** `proposed` draws a dashed violet border for panels that need data Harbormaster does not store yet. */
  variant?: 'default' | 'proposed'
}

export function Panel({ title, toolbar, actions, children, flush = false, badge, legend, footer, variant = 'default' }: PanelProps) {
  const titleId = useId()
  const hasToolbar = toolbar !== undefined
  const headClass = hasToolbar ? styles.tall : badge !== undefined || legend !== undefined ? styles.mid : ''
  return (
    <section
      className={`${styles.panel} ${variant === 'proposed' ? styles.proposed : ''}`.trim()}
      aria-labelledby={titleId}
      data-variant={variant}
    >
      <header className={`${styles.head} ${headClass}`.trim()}>
        {badge !== undefined && <span className={styles.badge}>{badge}</span>}
        <h2 id={titleId} className={styles.title}>
          {title}
        </h2>
        {hasToolbar && <div className={styles.toolbar}>{toolbar}</div>}
        {legend !== undefined && <div className={styles.legend}>{legend}</div>}
        {actions !== undefined && <div className={styles.actions}>{actions}</div>}
      </header>
      <div className={flush ? styles.flush : styles.body}>{children}</div>
      {footer !== undefined && <footer className={styles.foot}>{footer}</footer>}
    </section>
  )
}
