import type { CSSProperties } from 'react'
import styles from './charts.module.css'
import { fmtNum, niceMax } from './format'

export interface BarPart {
  v: number
  color: string
}

interface BarChartProps {
  /** One column per entry; parts stack bottom-up in the order given. */
  cols: { parts: BarPart[]; title: string }[]
  /** Panel height in px, default 100. */
  height?: number
  /** [column index, label] pairs placed under their column. */
  xLabels: [number, string][]
  unit?: string
}

const VIEW_WIDTH = 1000
const VIEW_HEIGHT = 100
/** Charts never grow past this many px, whatever the caller asks. */
const MAX_HEIGHT = 340

/** Stacked column chart in a fixed-height panel (mockup vbars()). */
export function BarChart({ cols, height = 100, xLabels, unit = '' }: BarChartProps) {
  const columnTotals = cols.map((col) => col.parts.reduce((total, part) => total + part.v, 0))
  const top = niceMax(Math.max(2, ...columnTotals))
  const columnWidth = VIEW_WIDTH / cols.length
  const yLabels = [0, 1, 2].map((step) => ({ top: `${100 - (step / 2) * 100}%`, text: `${fmtNum((top * step) / 2)}${unit}` }))

  return (
    <div className={styles.pchart} style={{ height: Math.min(MAX_HEIGHT, height) }}>
      <div className={styles.py}>
        {yLabels.map((label) => (
          <span key={label.top} style={{ top: label.top }}>
            {label.text}
          </span>
        ))}
      </div>
      <div className={styles.pplot}>
        <svg className={styles.plot} viewBox={`0 0 ${VIEW_WIDTH} ${VIEW_HEIGHT}`} preserveAspectRatio="none" aria-hidden="true">
          {[0, 1, 2].map((step) => {
            const y = VIEW_HEIGHT - (step / 2) * VIEW_HEIGHT
            return <line key={step} x1={0} x2={VIEW_WIDTH} y1={y} y2={y} className={styles.gl} />
          })}
          {cols.map((col, colIndex) => {
            let y = VIEW_HEIGHT
            return col.parts.map((part, partIndex) => {
              const partHeight = (part.v / top) * VIEW_HEIGHT
              if (partHeight <= 0) return null
              y -= partHeight
              return (
                <rect
                  key={`${colIndex}-${partIndex}`}
                  x={(colIndex * columnWidth + columnWidth * 0.18).toFixed(1)}
                  y={y.toFixed(2)}
                  width={(columnWidth * 0.64).toFixed(1)}
                  height={partHeight.toFixed(2)}
                  fill={part.color}
                >
                  <title>{col.title}</title>
                </rect>
              )
            })
          })}
        </svg>
      </div>
      <div />
      <div className={styles.px} style={{ position: 'relative' }}>
        {xLabels.map(([index, label]) => (
          <span
            key={index}
            className={styles.pxBar}
            style={{ position: 'absolute', left: `${((index + 0.5) / cols.length) * 100}%`, '--at': ((index + 0.5) / cols.length) * 100 } as CSSProperties}
          >
            {label}
          </span>
        ))}
      </div>
    </div>
  )
}
