import type { ReactNode } from 'react'
import { Sparkline } from '../charts/Sparkline'
import styles from './KpiStrip.module.css'

export interface KpiItem {
  label: string
  value: ReactNode
  /** Optional trend; drawn as a sparkline under the value. */
  spark?: number[]
  sparkColor?: string
  /** Colour of the value, e.g. `var(--danger)` for a failing count. */
  valueColor?: string
}

interface KpiStripProps {
  /** Accessible name of the strip. */
  label: string
  items: KpiItem[]
}

export function KpiStrip({ label, items }: KpiStripProps) {
  return (
    <ul className={styles.strip} aria-label={label} style={{ gridTemplateColumns: `repeat(${items.length || 1}, minmax(0, 1fr))` }}>
      {items.map((item) => (
        <li key={item.label} className={styles.cell}>
          <span className={styles.label}>{item.label}</span>
          <span className={styles.value} style={item.valueColor ? { color: item.valueColor } : undefined}>
            {item.value}
          </span>
          {item.spark && item.spark.length > 0 && (
            <span className={styles.spark}>
              <Sparkline values={item.spark} color={item.sparkColor} fluid />
            </span>
          )}
        </li>
      ))}
    </ul>
  )
}
