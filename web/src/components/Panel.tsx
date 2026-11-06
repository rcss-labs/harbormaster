import { useId, type ReactNode } from 'react'
import styles from './Panel.module.css'
import { PanelError, type PanelErrorProps } from './PanelError'
import { Skeleton } from './Skeleton'

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
  /** Strip under the body, e.g. where the data comes from. */
  footer?: ReactNode
  /** Accessible name for the section when the visible title is not enough. */
  label?: string
  /** Shows a skeleton in place of the children. */
  loading?: boolean
  /** The panel's own data problem; shown in place of the children so the rest of the page stays usable. */
  error?: PanelErrorProps
}

export function Panel({ title, sub, toolbar, actions, children, flush = false, legend, footer, label, loading = false, error }: PanelProps) {
  const titleId = useId()
  const hasToolbar = toolbar !== undefined
  const headClass = hasToolbar ? styles.tall : legend !== undefined ? styles.mid : ''
  // An error wins over loading: a retry in flight should not hide why the panel was empty.
  const body = error ? <PanelError {...error} /> : loading ? <Skeleton /> : children
  return (
    <section className={styles.panel} {...(label ? { 'aria-label': label } : { 'aria-labelledby': titleId })}>
      <header className={`${styles.head} ${headClass}`.trim()}>
        <h2 id={titleId} className={styles.title}>
          {title}
        </h2>
        {sub !== undefined && sub}
        {hasToolbar && <div className={styles.toolbar}>{toolbar}</div>}
        {legend !== undefined && <div className={styles.legend}>{legend}</div>}
        {actions !== undefined && <div className={styles.actions}>{actions}</div>}
      </header>
      <div className={flush && !error && !loading ? styles.flush : styles.body}>{body}</div>
      {footer !== undefined && <footer className={styles.foot}>{footer}</footer>}
    </section>
  )
}
