import { Link } from 'react-router'
import styles from './charts.module.css'
import { fmtNum } from './format'

export interface HBarItem {
  name: string
  /** Makes the name a link to this route. */
  href?: string
  sub?: string
  /** Single value; used when `parts` is absent. */
  v?: number
  /** Stacked segments; the track width is their sum. */
  parts?: { v: number; color: string }[]
  /** Desired-value marker position on the track. */
  ghost?: number
  /** Replaces the formatted value text on the right. */
  label?: string
  color?: string
}

interface HBarsProps {
  items: HBarItem[]
  /** Track length that 100% maps to; defaults to the largest item. */
  max?: number
  unit?: string
}

const partsTotal = (parts: { v: number }[]) => parts.reduce((total, part) => total + part.v, 0)

/** Horizontal bar list with name, track and value columns (mockup hbars()). */
export function HBars({ items, max, unit = '' }: HBarsProps) {
  if (items.length === 0) {
    return <div className={`${styles.muted} ${styles.small}`}>Nothing to show.</div>
  }
  const itemValue = (item: HBarItem) => item.v ?? (item.parts ? partsTotal(item.parts) : 0)
  const scale =
    max || Math.max(1, ...items.map((item) => Math.max(item.ghost ?? 0, item.parts ? partsTotal(item.parts) : itemValue(item))))

  return (
    <div className={styles.hbars}>
      {items.map((item, index) => {
        const segments = item.parts ?? [{ v: itemValue(item), color: item.color ?? 'var(--accent)' }]
        return (
          <div key={index} className={styles.hbar}>
            <div className={styles.hbarL}>
              {item.href ? <Link to={item.href}>{item.name}</Link> : <span>{item.name}</span>}
              {item.sub && <span className={`${styles.muted} ${styles.small}`}>{item.sub}</span>}
            </div>
            <div className={styles.hbarTrack}>
              {segments
                .filter((segment) => segment.v > 0)
                .map((segment, segmentIndex) => (
                  <span key={segmentIndex} style={{ width: `${(segment.v / scale) * 100}%`, background: segment.color }} />
                ))}
              {item.ghost !== undefined && (
                <em style={{ left: `calc(${(item.ghost / scale) * 100}% - 1px)` }} title="desired" />
              )}
            </div>
            <div className={styles.hbarV}>{item.label ?? fmtNum(itemValue(item)) + unit}</div>
          </div>
        )
      })}
    </div>
  )
}
