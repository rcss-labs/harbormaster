import { Fragment, useLayoutEffect, useRef, useState, type CSSProperties } from 'react'
import { usePhone } from '../components/useNarrow'
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

const EDGE_ZONE = 0.15

/** Rough plot width and glyph width used to estimate where labels collide; jsdom and SSR have no layout to measure. */
const ASSUMED_PLOT_PX = 640
const LABEL_CHAR_PX = 7.4
const LABEL_PAD_PX = 8
const LABEL_ROWS = 2

export interface PlacedMark {
  at: number
  label: string
  /** Row 0 is the top line, row 1 sits below it; `null` means no room, so only a hover target is drawn. */
  row: number | null
}

/**
 * Labels centre on their marker, except where a centred label would stick out of the plot (near an edge, or a long label
 * a little way in), where they anchor inward so the plot never clips them. The width is estimated, as in placement.
 */
function labelAnchor(at: number, label: string, plotPx: number): 'start' | 'end' | 'centre' {
  const width = label.length * LABEL_CHAR_PX + LABEL_PAD_PX
  const x = at * plotPx
  if (at >= 1 - EDGE_ZONE || x + width / 2 > plotPx) return 'end'
  if (at <= EDGE_ZONE || x - width / 2 < 0) return 'start'
  return 'centre'
}

function markLabelStyle(at: number, label: string, plotPx: number): CSSProperties {
  const left = `${at * 100}%`
  const anchor = labelAnchor(at, label, plotPx)
  if (anchor === 'end') return { left, transform: 'translateX(-100%)' }
  if (anchor === 'start') return { left, transform: 'translateX(0)' }
  return { left }
}

/** Spans [start, end] in px that a label covers given its anchoring. */
function labelSpan(at: number, label: string, plotPx: number): [number, number] {
  const width = label.length * LABEL_CHAR_PX + LABEL_PAD_PX
  const x = at * plotPx
  const anchor = labelAnchor(at, label, plotPx)
  if (anchor === 'end') return [x - width, x]
  if (anchor === 'start') return [x, x + width]
  return [x - width / 2, x + width / 2]
}

/** Assigns each label a row left to right: the first free row, else none. Overlap is estimated, not measured. */
export function placeMarkLabels(marks: { at: number; label: string }[], plotPx: number = ASSUMED_PLOT_PX): PlacedMark[] {
  const rowEnds: number[] = Array(LABEL_ROWS).fill(-Infinity)
  const placed = new Map<{ at: number; label: string }, number | null>()
  const ordered = [...marks].sort((a, b) => a.at - b.at)
  for (const mark of ordered) {
    const [start, end] = labelSpan(mark.at, mark.label, plotPx)
    const row = rowEnds.findIndex((rowEnd) => start >= rowEnd)
    if (row === -1) {
      placed.set(mark, null)
      continue
    }
    rowEnds[row] = end
    placed.set(mark, row)
  }
  return marks.map((mark) => ({ ...mark, row: placed.get(mark) ?? null }))
}

/**
 * Width of the plot in px, measured and kept current on phones, where the container is much narrower than the
 * assumed 640 px. Elsewhere it is `undefined`, so label placement on wider screens keeps its fixed estimate.
 */
function useMeasuredWidth(ref: React.RefObject<HTMLElement | null>, enabled: boolean): number | undefined {
  const [width, setWidth] = useState<number>()
  useLayoutEffect(() => {
    const element = ref.current
    if (!enabled || !element) return
    const measure = () => {
      const measured = element.getBoundingClientRect().width
      if (measured > 0) setWidth(measured)
    }
    measure()
    if (typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(measure)
    observer.observe(element)
    return () => observer.disconnect()
  }, [ref, enabled])
  return enabled ? width : undefined
}

/** Hand-written SVG line chart in a fixed-height panel; labels are HTML so text never stretches (mockup chart()). */
export function LineChart({ series, unit = '%', max = 100, xLabels, height = 130, marks = [] }: LineChartProps) {
  const panelHeight = Math.min(220, Math.max(80, height))
  const shown = collapseSeries(series)
  const fillOpacity = shown.length > 2 ? 0.03 : 0.08
  const plotRef = useRef<HTMLDivElement>(null)
  const measuredWidth = useMeasuredWidth(plotRef, usePhone())
  const placedMarks = placeMarkLabels(marks, measuredWidth)
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
      <div className={styles.pplot} ref={plotRef}>
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
        {placedMarks.map((mark) =>
          mark.row === null ? (
            <span key={mark.at} className={styles.mkdot} style={{ left: `${mark.at * 100}%` }} title={mark.label} aria-label={mark.label} />
          ) : (
            <span key={mark.at} className={styles.mkl} style={{ ...markLabelStyle(mark.at, mark.label, measuredWidth ?? ASSUMED_PLOT_PX), top: mark.row === 0 ? -1 : 17 }} title={mark.label}>
              {mark.label}
            </span>
          ),
        )}
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
