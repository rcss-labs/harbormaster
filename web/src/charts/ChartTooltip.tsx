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
}

/** Past this share of the plot width the tooltip moves to the left of the crosshair. */
const FLIP_AFTER_PERCENT = 60

export function ChartTooltip({ leftPercent, title, rows, total }: ChartTooltipProps) {
  const flip = leftPercent > FLIP_AFTER_PERCENT
  const position = flip ? { right: `${100 - leftPercent}%` } : { left: `${leftPercent}%` }
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
