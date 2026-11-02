import styles from './charts.module.css'
import { pct } from './format'

export interface DonutSegment {
  name: string
  v: number
  color: string
}

interface DonutProps {
  segments: DonutSegment[]
  /** Big centre text. */
  label: string
  /** Small text under the label. */
  sub: string
  /** Rendered size in px; default 88. */
  size?: number
  /** Ring thickness in viewBox units (132 wide), so px at size 132; default 14. */
  ring?: number
  /** Replaces `label` as the big centre text. */
  centerLabel?: string
  /** Legend value column: the count (default) or the count and its share of the total. */
  legend?: 'count' | 'pct'
}

const RADIUS = 52
const CENTRE = 66
const CIRCUMFERENCE = 2 * Math.PI * RADIUS

/** Ring of arcs with a count legend beside it (mockup donut()). Zero-count segments get no arc but keep their legend row. */
export function Donut({ segments, label, sub, size = 88, ring = 14, centerLabel, legend = 'count' }: DonutProps) {
  const total = segments.reduce((sum, segment) => sum + segment.v, 0) || 1
  const drawnSegments = segments.filter((segment) => segment.v > 0)
  const lengths = drawnSegments.map((segment) => (segment.v / total) * CIRCUMFERENCE)
  // Each arc starts where the previous one ends.
  const offsets = lengths.map((_, i) => lengths.slice(0, i).reduce((sum, length) => sum + length, 0))
  const arcs = drawnSegments.map((segment, i) => (
    <circle
      key={segment.name}
      r={RADIUS}
      cx={CENTRE}
      cy={CENTRE}
      fill="none"
      stroke={segment.color}
      strokeWidth={ring}
      strokeDasharray={`${Math.max(0, lengths[i] - 2)} ${CIRCUMFERENCE - lengths[i] + 2}`}
      strokeDashoffset={-offsets[i]}
      transform={`rotate(-90 ${CENTRE} ${CENTRE})`}
    >
      <title>{`${segment.name}: ${segment.v}`}</title>
    </circle>
  ))

  return (
    <div className={styles.donutWrap}>
      <svg width={size} height={size} viewBox="0 0 132 132" role="img">
        <circle r={RADIUS} cx={CENTRE} cy={CENTRE} fill="none" stroke="var(--muted-soft)" strokeWidth={ring} />
        {arcs}
        <text x={CENTRE} y={68} textAnchor="middle" className={styles.donutV}>
          {centerLabel ?? label}
        </text>
        <text x={CENTRE} y={86} textAnchor="middle" className={styles.donutS}>
          {sub}
        </text>
      </svg>
      <div className={`${styles.donutLegend} ${legend === 'pct' ? styles.donutPct : ''}`.trim()}>
        {segments.map((segment) => (
          <div key={segment.name}>
            <i style={{ background: segment.color }} />
            <span>{segment.name}</span>
            <b>{segment.v}</b>
            {legend === 'pct' && <em>{pct(segment.v, total)}%</em>}
          </div>
        ))}
      </div>
    </div>
  )
}
