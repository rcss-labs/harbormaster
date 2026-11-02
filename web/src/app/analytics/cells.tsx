import type { ReactNode } from 'react'
import { Link } from 'react-router'
import type { TagDTO } from '../../api/types'
import { TagChip } from '../../components/TagChip'
import styles from './cells.module.css'

/** Right-aligned-friendly mono figure for table cells. */
export function MonoCell({ children }: { children: ReactNode }) {
  return <span className={styles.mono}>{children}</span>
}

interface BarCellProps {
  /** Fill of the 56x6 track, 0–100; values outside are clamped. */
  pct: number
  /** Mono text after the bar. */
  label: string
  /** Fill colour; defaults to the accent. */
  color?: string
}

/** In-cell mini bar (56x6) followed by its value. */
export function BarCell({ pct, label, color = 'var(--accent)' }: BarCellProps) {
  const width = Math.max(0, Math.min(100, pct))
  return (
    <span className={styles.bar}>
      <span className={styles.track} aria-hidden="true">
        <i data-bar style={{ width: `${width}%`, background: color }} />
      </span>
      <span className={styles.mono}>{label}</span>
    </span>
  )
}

/** Service name linking to its page, with the cluster dimmed underneath. */
export function ServiceLink({ id, name, cluster }: { id: string; name: string; cluster?: string }) {
  return (
    <span className={styles.service}>
      <Link to={`/services/${id}`}>{name}</Link>
      {cluster !== undefined && <small>{cluster}</small>}
    </span>
  )
}

export function TagCell({ tag }: { tag: TagDTO }) {
  return <TagChip tag={tag} />
}
