import { useId, type ReactNode } from 'react'
import styles from './Panel.module.css'

interface PanelProps {
  title: ReactNode
  /** Dim text after the title; kept outside the heading so the panel's accessible name is just the title. */
  sub?: ReactNode
  /** Controls (filters, segments) shown beside the title; makes the header taller. */
  toolbar?: ReactNode
  /** Right-aligned header content such as a "Show all" link. */
  actions?: ReactNode
  children: ReactNode
  /** Remove body padding, e.g. for tables that run edge to edge. */
  flush?: boolean
  /** Right-aligned header content describing the chart, such as colour keys with values. */
  legend?: ReactNode
  /** Mono strip under the body, e.g. where the data comes from. */
  footer?: ReactNode
}

export function Panel({ title, sub, toolbar, actions, children, flush = false, legend, footer }: PanelProps) {
  const titleId = useId()
  const hasToolbar = toolbar !== undefined
  const headClass = hasToolbar ? styles.tall : legend !== undefined ? styles.mid : ''
  return (
    <section className={styles.panel} aria-labelledby={titleId}>
      <header className={`${styles.head} ${headClass}`.trim()}>
        <h2 id={titleId} className={styles.title}>
          {title}
        </h2>
        {sub !== undefined && sub}
        {hasToolbar && <div className={styles.toolbar}>{toolbar}</div>}
        {legend !== undefined && <div className={styles.legend}>{legend}</div>}
        {actions !== undefined && <div className={styles.actions}>{actions}</div>}
      </header>
      <div className={flush ? styles.flush : styles.body}>{children}</div>
      {footer !== undefined && <footer className={styles.foot}>{footer}</footer>}
    </section>
  )
}
