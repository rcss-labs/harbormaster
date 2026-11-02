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
}

export function Panel({ title, toolbar, actions, children, flush = false }: PanelProps) {
  const titleId = useId()
  const hasToolbar = toolbar !== undefined
  return (
    <section className={styles.panel} aria-labelledby={titleId}>
      <header className={`${styles.head} ${hasToolbar ? styles.tall : ''}`.trim()}>
        <h2 id={titleId} className={styles.title}>
          {title}
        </h2>
        {hasToolbar && <div className={styles.toolbar}>{toolbar}</div>}
        {actions !== undefined && <div className={styles.actions}>{actions}</div>}
      </header>
      <div className={flush ? styles.flush : styles.body}>{children}</div>
    </section>
  )
}
