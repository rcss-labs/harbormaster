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
  /** Relative time of the hovered bucket, e.g. "3 h ago". */
  title: string
  rows: TooltipRow[]
  /** Shown as a separated last row (stacked charts). */
  total?: string
  /** Measured plot width in px; when known the tooltip is kept inside it on both sides. */
  plotWidth?: number | null
}

/** Past this share of the plot width the tooltip moves to the left of the crosshair. */
const FLIP_AFTER_PERCENT = 60
/** The tooltip's 170 px minimum width plus its 10 px side margins. */
const TIP_FOOTPRINT_PX = 190
const TIP_MARGIN_PX = 20

/** Offsets stay in percent (as on a wide plot) until the tooltip would leave the plot; then they are bounded in px. */
function tooltipPosition(leftPercent: number, flip: boolean, plotWidth: number | null | undefined): CSSProperties {
  const offsetPercent = flip ? 100 - leftPercent : leftPercent
  const edge = flip ? 'right' : 'left'
  if (!plotWidth) return { [edge]: `${offsetPercent}%` }
  const latest = Math.max(0, plotWidth - TIP_FOOTPRINT_PX)
  const style: CSSProperties = (offsetPercent / 100) * plotWidth > latest ? { [edge]: `${latest}px` } : { [edge]: `${offsetPercent}%` }
  // A plot narrower than the tooltip shrinks it rather than letting it overflow.
  if (plotWidth < TIP_FOOTPRINT_PX) Object.assign(style, { minWidth: 0, maxWidth: Math.max(0, plotWidth - TIP_MARGIN_PX) })
  return style
}

export function ChartTooltip({ leftPercent, title, rows, total, plotWidth }: ChartTooltipProps) {
  const flip = leftPercent > FLIP_AFTER_PERCENT
  const position = tooltipPosition(leftPercent, flip, plotWidth)
  return (
    <div role="tooltip" className={styles.tip} style={position}>
      <div className={styles.tipTime}>{title}</div>
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
