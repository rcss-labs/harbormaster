import { Fragment } from 'react'
import styles from './charts.module.css'
import { fmtNum } from './format'

export interface LineSeries {
  name: string
  color: string
  /** `null` is a gap: the line breaks there. */
  values: (number | null)[]
}

interface LineChartProps {
  series: LineSeries[]
  /** Suffix for y labels, default "%". */
  unit?: string
  /** Value at the top of the plot, default 100. */
  max?: number
  xLabels: string[]
  /** Panel height in px, clamped to 80–220. */
  height?: number
  /** Vertical dashed markers; `at` is 0–1 across the plot. */
  marks?: { at: number; label: string }[]
}

const VIEW_WIDTH = 1000
const VIEW_HEIGHT = 100
const MAX_SERIES = 8
const KEPT_SERIES = 7

const lastValue = (series: LineSeries) => series.values[series.values.length - 1] ?? 0

type Run = { x: number; y: number }[]

/** Splits a series into runs of consecutive non-null points; each run becomes its own path segment. */
function contiguousRuns(values: (number | null)[], max: number): Run[] {
  const runs: Run[] = []
  let current: Run = []
  values.forEach((v, i) => {
    if (v === null) {
      if (current.length > 0) runs.push(current)
      current = []
      return
    }
    current.push({
      x: values.length > 1 ? (i / (values.length - 1)) * VIEW_WIDTH : 0,
      y: VIEW_HEIGHT - (Math.min(v, max) / max) * VIEW_HEIGHT,
    })
  })
  if (current.length > 0) runs.push(current)
  return runs
}

/** Keeps the 7 series with the highest last value and folds the rest into one "others" series of per-index means. */
function collapseSeries(series: LineSeries[]): LineSeries[] {
  if (series.length <= MAX_SERIES) return series
  const byLastValue = [...series].sort((a, b) => lastValue(b) - lastValue(a))
  const kept = byLastValue.slice(0, KEPT_SERIES)
  const rest = byLastValue.slice(KEPT_SERIES)
  const pointCount = Math.max(...rest.map((s) => s.values.length))
  const others: LineSeries = {
    name: 'others',
    color: 'var(--border-strong)',
    values: Array.from({ length: pointCount }, (_, i) => {
      const samples = rest.map((s) => s.values[i]).filter((v): v is number => v !== undefined && v !== null)
      if (samples.length === 0) return null
      return samples.reduce((total, v) => total + v, 0) / samples.length
    }),
  }
  return [...series.filter((s) => kept.includes(s)), others]
}

/** Hand-written SVG line chart in a fixed-height panel; labels are HTML so text never stretches (mockup chart()). */
export function LineChart({ series, unit = '%', max = 100, xLabels, height = 130, marks = [] }: LineChartProps) {
  const panelHeight = Math.min(220, Math.max(80, height))
  const shown = collapseSeries(series)
  const fillOpacity = shown.length > 2 ? 0.03 : 0.08
  const yLabels = [0, 2, 4].map((step) => ({ top: `${100 - (step / 4) * 100}%`, text: `${fmtNum((max * step) / 4)}${unit}` }))

  return (
    <div className={styles.pchart} style={{ height: panelHeight }}>
      <div className={styles.py}>
        {yLabels.map((label) => (
          <span key={label.top} style={{ top: label.top }}>
            {label.text}
          </span>
        ))}
      </div>
      <div className={styles.pplot}>
        <svg className={styles.plot} viewBox={`0 0 ${VIEW_WIDTH} ${VIEW_HEIGHT}`} preserveAspectRatio="none" aria-hidden="true">
          {[0, 1, 2, 3, 4].map((step) => {
            const y = VIEW_HEIGHT - (step / 4) * VIEW_HEIGHT
            return <line key={step} x1={0} x2={VIEW_WIDTH} y1={y} y2={y} className={styles.gl} />
          })}
          {marks.map((mark) => (
            <line key={mark.at} x1={mark.at * VIEW_WIDTH} x2={mark.at * VIEW_WIDTH} y1={0} y2={VIEW_HEIGHT} className={styles.mk} />
          ))}
          {shown.map((line) => {
            const runs = contiguousRuns(line.values, max)
            if (runs.length === 0) return null
            const point = (p: { x: number; y: number }) => `${p.x.toFixed(1)} ${p.y.toFixed(2)}`
            const outline = runs.map((run) => `M${run.map(point).join(' L')}`).join(' ')
            const fill = runs
              .map((run) => `M${run.map(point).join(' L')} L${run[run.length - 1].x.toFixed(1)} ${VIEW_HEIGHT} L${run[0].x.toFixed(1)} ${VIEW_HEIGHT} Z`)
              .join(' ')
            return (
              <Fragment key={line.name}>
                <path d={fill} fill={line.color} opacity={fillOpacity} />
                <path d={outline} fill="none" stroke={line.color} strokeWidth={1.4} vectorEffect="non-scaling-stroke" className={styles.line} />
              </Fragment>
            )
          })}
        </svg>
        {marks.map((mark) => (
          <span key={mark.at} className={styles.mkl} style={{ left: `${mark.at * 100}%` }}>
            {mark.label}
          </span>
        ))}
      </div>
      <div />
      <div className={styles.px}>
        {xLabels.map((label, i) => (
          <span key={i}>{label}</span>
        ))}
      </div>
    </div>
  )
}
