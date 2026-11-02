import { ChartTooltip } from './ChartTooltip'
import styles from './charts.module.css'
import { fmtNum, niceMax } from './format'
import { useChartHover } from './useChartHover'
import { formatAgo, useNow } from '../components/Ago'

export interface TimeSeriesLine {
  key: string
  label: string
  color: string
  /** `null` is a gap in line and area modes (zero when stacked). */
  values: (number | null)[]
  /** Expected events are drawn grey whatever the colour. */
  expected?: boolean
  dashed?: boolean
}

export interface TimeSeriesChartProps {
  timestamps: string[]
  series: TimeSeriesLine[]
  mode: 'line' | 'area' | 'stacked'
  yMin?: number
  yMax?: number
  yFormat?: (value: number) => string
  /** Dashed horizontal line, e.g. a 5% failure budget, labelled at the right edge. */
  reference?: { value: number; label: string }
  /** Start, middle and end of the x axis. */
  xLabels: [string, string, string]
  /** Accessible name of the plot. */
  ariaLabel?: string
  /** Reference time for tooltip labels; defaults to the current time. */
  now?: number
}

const WIDTH = 600
const HEIGHT = 150
const GRID_STEPS = [0, 0.25, 0.5, 0.75, 1]
const GREY = 'var(--muted)'
const DAY_MS = 86_400_000

const round = (value: number) => +value.toFixed(2)

interface Point {
  x: number
  y: number
}

/** Consecutive non-null points; a null value ends the run so the line breaks there. */
function runsOf(values: (number | null)[], toPoint: (index: number, value: number) => Point): Point[][] {
  const runs: Point[][] = []
  let current: Point[] = []
  values.forEach((value, index) => {
    if (value === null) {
      if (current.length > 0) runs.push(current)
      current = []
      return
    }
    current.push(toPoint(index, value))
  })
  if (current.length > 0) runs.push(current)
  return runs
}

const pointText = (p: Point) => `${round(p.x)},${round(p.y)}`
const outline = (runs: Point[][]) => runs.map((run) => `M${run.map(pointText).join(' L')}`).join(' ')

/** Relative label of a bucket: "now", "3 h ago", or "today" for day-sized buckets that are still running. */
function relativeLabel(timestamps: string[], index: number, now: number): string {
  const time = Date.parse(timestamps[index])
  const age = now - time
  if (age < 60_000) return 'now'
  const spacing = timestamps.length > 1 ? Date.parse(timestamps[1]) - Date.parse(timestamps[0]) : 0
  if (spacing >= DAY_MS && age < spacing) return 'today'
  return formatAgo(time, now)
}

/** Hand-written SVG line, area or stacked-area chart with a hover tooltip (prototype chartEl()). */
export function TimeSeriesChart({
  timestamps,
  series,
  mode,
  yMin = 0,
  yMax,
  yFormat = fmtNum,
  reference,
  xLabels,
  ariaLabel = 'Chart',
  now,
}: TimeSeriesChartProps) {
  const count = timestamps.length
  const stacked = mode === 'stacked'
  const { index: hovered, bind } = useChartHover(count)
  const liveNow = useNow(60_000)

  const totals = Array.from({ length: count }, (_, i) => series.reduce((sum, line) => sum + (line.values[i] ?? 0), 0))
  const highest = stacked ? Math.max(0, ...totals) : Math.max(0, ...series.flatMap((line) => line.values.map((v) => v ?? 0)))
  let top = yMax ?? niceMax(highest)
  if (top <= yMin) top = yMin + 1

  const x = (i: number) => (count > 1 ? (i / (count - 1)) * WIDTH : 0)
  const y = (v: number) => HEIGHT - ((Math.max(yMin, Math.min(top, v)) - yMin) / (top - yMin)) * HEIGHT
  const colorOf = (line: TimeSeriesLine) => (line.expected ? GREY : line.color)

  const layers: React.ReactNode[] = []
  if (stacked) {
    let base: number[] = new Array<number>(count).fill(0)
    series.forEach((line) => {
      const upper = base.map((b, i) => b + (line.values[i] ?? 0))
      const upperPoints = upper.map((v, i) => ({ x: x(i), y: y(v) }))
      const lowerPoints = base.map((v, i) => ({ x: x(i), y: y(v) })).reverse()
      const area = `M${[...upperPoints, ...lowerPoints].map(pointText).join(' L')} Z`
      layers.push(
        <path key={`area-${line.key}`} d={area} fill={colorOf(line)} fillOpacity={0.42} data-area={line.key} />,
        <path
          key={`line-${line.key}`}
          d={outline([upperPoints])}
          fill="none"
          stroke={colorOf(line)}
          strokeWidth={1.25}
          vectorEffect="non-scaling-stroke"
          data-line={line.key}
        />,
      )
      base = upper
    })
  } else {
    series.forEach((line) => {
      const runs = runsOf(line.values, (i, v) => ({ x: x(i), y: y(v) }))
      if (runs.length === 0) return
      if (mode === 'area') {
        const area = runs
          .map((run) => `M${run.map(pointText).join(' L')} L${round(run[run.length - 1].x)},${HEIGHT} L${round(run[0].x)},${HEIGHT} Z`)
          .join(' ')
        layers.push(<path key={`area-${line.key}`} d={area} fill={colorOf(line)} fillOpacity={0.12} data-area={line.key} />)
      }
      layers.push(
        <path
          key={`line-${line.key}`}
          d={outline(runs)}
          fill="none"
          stroke={colorOf(line)}
          strokeWidth={1.6}
          strokeDasharray={line.dashed ? '5 4' : undefined}
          strokeLinejoin="round"
          vectorEffect="non-scaling-stroke"
          data-line={line.key}
        />,
      )
    })
  }

  const leftPercent = hovered !== null && count > 1 ? (hovered / (count - 1)) * 100 : 0
  const referenceTop = reference ? (y(reference.value) / HEIGHT) * 100 : 0

  return (
    <div className={styles.ts}>
      <div className={styles.tsPlot} role="group" aria-label={ariaLabel} {...bind}>
        <svg className={styles.tsSvg} viewBox={`0 0 ${WIDTH} ${HEIGHT}`} preserveAspectRatio="none" aria-hidden="true">
          {GRID_STEPS.map((step) => (
            <line
              key={step}
              x1={0}
              x2={WIDTH}
              y1={HEIGHT * step}
              y2={HEIGHT * step}
              stroke={step === 1 ? 'var(--border-strong)' : 'var(--row-border)'}
              strokeWidth={1}
              vectorEffect="non-scaling-stroke"
              data-gridline
            />
          ))}
          {layers}
          {reference && (
            <line
              x1={0}
              x2={WIDTH}
              y1={round(y(reference.value))}
              y2={round(y(reference.value))}
              stroke="var(--muted)"
              strokeDasharray="3 4"
              strokeWidth={1}
              vectorEffect="non-scaling-stroke"
              data-reference
            />
          )}
        </svg>
        <span className={styles.tsY} style={{ top: '0%' }}>
          {yFormat(top)}
        </span>
        <span className={styles.tsY} style={{ top: '50%' }}>
          {yFormat(yMin + (top - yMin) / 2)}
        </span>
        {reference && (
          <span className={styles.tsRef} style={{ top: `${referenceTop}%` }}>
            {reference.label}
          </span>
        )}
        {hovered !== null && (
          <>
            <div className={styles.tsCross} style={{ left: `${leftPercent}%` }} />
            {!stacked &&
              series.map((line) => {
                const value = line.values[hovered]
                if (value === null || value === undefined) return null
                return (
                  <div
                    key={line.key}
                    className={styles.tsDot}
                    style={{ left: `${leftPercent}%`, top: `${(y(value) / HEIGHT) * 100}%`, background: colorOf(line) }}
                  />
                )
              })}
            <ChartTooltip
              leftPercent={leftPercent}
              title={relativeLabel(timestamps, hovered, now ?? liveNow)}
              rows={series.map((line) => {
                const value = line.values[hovered]
                return { label: line.label, color: colorOf(line), value: value === null || value === undefined ? '–' : yFormat(value) }
              })}
              total={stacked ? yFormat(totals[hovered]) : undefined}
            />
          </>
        )}
      </div>
      <div className={styles.tsX}>
        {xLabels.map((label, i) => (
          <span key={i}>{label}</span>
        ))}
      </div>
    </div>
  )
}
