import { useInRouterContext, Link } from 'react-router'
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
  /** Makes the whole cell a link, e.g. to the list behind the number. */
  href?: string
  /** State colour of the value and a matching class on the cell. */
  tone?: 'ok' | 'warn' | 'bad' | 'exp'
}

interface KpiStripProps {
  /** Accessible name of the strip. */
  label: string
  items: KpiItem[]
}

function CellBody({ item }: { item: KpiItem }) {
  return (
    <>
      <span className={styles.label}>{item.label}</span>
      <span className={styles.value} style={item.valueColor ? { color: item.valueColor } : undefined}>
        {item.value}
      </span>
      {item.spark && item.spark.length > 0 && (
        <span className={styles.spark}>
          <Sparkline values={item.spark} color={item.sparkColor} fluid />
        </span>
      )}
    </>
  )
}

export function KpiStrip({ label, items }: KpiStripProps) {
  const inRouter = useInRouterContext()
  return (
    <ul className={styles.strip} aria-label={label} style={{ gridTemplateColumns: `repeat(${items.length || 1}, minmax(0, 1fr))` }}>
      {items.map((item) => {
        const toneClass = item.tone ? styles[`tone-${item.tone}`] : ''
        // Plain cells keep the label as the <li>'s first child; only linked cells need an inner anchor.
        if (item.href === undefined) {
          return (
            <li key={item.label} className={`${styles.cell} ${styles.inner} ${toneClass}`.trim()}>
              <CellBody item={item} />
            </li>
          )
        }
        const linkClass = `${styles.inner} ${styles.link}`
        return (
          <li key={item.label} className={`${styles.cell} ${toneClass}`.trim()}>
            {inRouter ? (
              <Link to={item.href} className={linkClass}>
                <CellBody item={item} />
              </Link>
            ) : (
              <a href={item.href} className={linkClass}>
                <CellBody item={item} />
              </a>
            )}
          </li>
        )
      })}
    </ul>
  )
}
