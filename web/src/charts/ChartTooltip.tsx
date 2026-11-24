import type { CSSProperties } from 'react'
import styles from './charts.module.css'

export interface TooltipRow {
  label: string
  color: string
  value: string
}

interface ChartTooltipProps {
  /** Position of the hovered point across the plot, 0–100. */
  leftPercent: number
  /** Date of the hovered bucket, e.g. "Sat, Nov 21". */
  title: string
  /** Relative time of the hovered bucket, e.g. "3 h ago". */
  age?: string
  rows: TooltipRow[]
  /** Shown as a separated last row (stacked charts). */
  total?: string
  /** Measured plot width in px; when known the tooltip is kept inside it on both sides. */
  plotWidth?: number | null
}

/** Past this share of the plot width the tooltip prefers the left of the crosshair. */
const FLIP_AFTER_PERCENT = 60
/** The tooltip's 236 px minimum width plus its 12 px side margins. */
const TIP_FOOTPRINT_PX = 260
const TIP_MARGIN_PX = 24
/** With less room than this beside the point, the tooltip stays inside the plot even though it covers the point. */
const MIN_BESIDE_PX = 160

/** Beside the hovered point on the side with room, shrunk when neither side has enough, and never outside the plot. */
function tooltipPosition(leftPercent: number, plotWidth: number | null | undefined): CSSProperties {
  const at = (flip: boolean): CSSProperties => (flip ? { right: `${100 - leftPercent}%` } : { left: `${leftPercent}%` })
  const preferLeft = leftPercent > FLIP_AFTER_PERCENT
  if (!plotWidth) return at(preferLeft)
  const left = (leftPercent / 100) * plotWidth
  const right = plotWidth - left
  const fits = (flip: boolean) => (flip ? left : right) >= TIP_FOOTPRINT_PX
  if (fits(preferLeft)) return at(preferLeft)
  if (fits(!preferLeft)) return at(!preferLeft)
  const room = Math.max(left, right)
  if (room > MIN_BESIDE_PX) return { ...at(left > right), minWidth: 0, maxWidth: room - TIP_MARGIN_PX }
  const style: CSSProperties = left > right ? { right: '0px' } : { left: '0px' }
  return plotWidth < TIP_FOOTPRINT_PX ? { ...style, minWidth: 0, maxWidth: Math.max(0, plotWidth - TIP_MARGIN_PX) } : style
}

export function ChartTooltip({ leftPercent, title, age, rows, total, plotWidth }: ChartTooltipProps) {
  const position = tooltipPosition(leftPercent, plotWidth)
  return (
    <div role="tooltip" className={styles.tip} style={position}>
      <div className={styles.tipTime}>
        {title}
        {age !== undefined && <span className={styles.tipAge}>{age}</span>}
      </div>
      {rows.map((row) => (
        <div key={row.label} className={styles.tipRow}>
          <i style={{ background: row.color }} />
          <span>{row.label}</span>
          <b>{row.value}</b>
        </div>
      ))}
      {total !== undefined && (
        <div className={`${styles.tipRow} ${styles.tipTotal}`}>
          <span>Total</span>
          <b>{total}</b>
        </div>
      )}
    </div>
  )
}
