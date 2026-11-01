import type { ReactNode } from 'react'
import { Link } from 'react-router'
import styles from './networks.module.css'

export interface NetStat {
  value: number
  label: string
}

interface NetCardProps {
  title: ReactNode
  subtitle?: ReactNode
  stats: NetStat[]
  note?: ReactNode
  /** Makes the whole card a link; ignored when `off`. */
  to?: string
  /** Dims the card for a region with nothing to show; it is never a link. */
  off?: boolean
}

/** "1 cluster" or "3 clusters", chosen by the count. */
export function countLabel(count: number, singular: string, plural: string): string {
  return `${count} ${count === 1 ? singular : plural}`
}

export function NetCard({ title, subtitle, stats, note, to, off = false }: NetCardProps) {
  const body = (
    <>
      <div>
        <div className={styles.big}>{title}</div>
        {subtitle !== undefined && <div className={`${styles.mono} ${styles.muted}`}>{subtitle}</div>}
      </div>
      {stats.length > 0 && (
        <div className={styles.stats}>
          {stats.map((stat) => (
            <span key={stat.label}>
              <b>{stat.value}</b> {stat.label}
            </span>
          ))}
        </div>
      )}
      {note !== undefined && <div className={styles.notes}>{note}</div>}
    </>
  )

  if (to !== undefined && !off) {
    return (
      <Link to={to} className={styles.netCard}>
        {body}
      </Link>
    )
  }
  return <div className={off ? `${styles.netCard} ${styles.off}` : styles.netCard}>{body}</div>
}
