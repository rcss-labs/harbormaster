import type { ReactNode } from 'react'
import type { AnalyticsWindow, RollupPanel } from '../../api/analytics'
import { bucketDayLabel } from '../../charts/format'
import { Panel } from '../../components/Panel'
import type { PanelErrorProps } from '../../components/PanelError'
import styles from './AnalyticsPanel.module.css'
import { ChartLegend, type ChartLegendItem } from './ChartLegend'
import { retentionText } from './format'

interface AnalyticsPanelProps {
  title: string
  /** Dim sub-title after the title, e.g. "deploy, restart and scale". */
  sub?: string
  legend?: ChartLegendItem[]
  /** Where the data comes from; drives the `source <table> · kept N` footer. */
  source?: { table: string; kept: string }
  /** Set for panels read from a daily rollup; lets the panel say when the daily data started. */
  rollup?: RollupPanel<unknown>
  /** Window of the tab; a rollup that starts after `window.from` gets the "Daily data started on" note. */
  window?: Pick<AnalyticsWindow, 'from'>
  showSources: boolean
  /** The panel's own data problem; replaces the body with a message and Retry. */
  error?: PanelErrorProps
  /** Remove body padding, e.g. for tables. */
  flush?: boolean
  children: ReactNode
}

/** Panel with the Analytics anatomy: legend, source footer and the "Daily data started on" note. */
export function AnalyticsPanel({
  title,
  sub,
  legend,
  source,
  rollup,
  window,
  showSources,
  error,
  flush,
  children,
}: AnalyticsPanelProps) {
  const startedLate =
    rollup?.available === true &&
    rollup.data_since !== null &&
    window !== undefined &&
    Date.parse(rollup.data_since) > Date.parse(window.from)

  const footer = showSources && source ? `source ${source.table} · kept ${retentionText(source.kept)}` : undefined

  return (
    <Panel
      title={title}
      sub={sub !== undefined ? <span className={styles.sub}>{sub}</span> : undefined}
      legend={legend && legend.length > 0 ? <ChartLegend items={legend} /> : undefined}
      footer={footer}
      flush={flush}
      error={error}
    >
      {startedLate && rollup.data_since && <p className={styles.note}>Daily data started on {bucketDayLabel(rollup.data_since)}</p>}
      {children}
    </Panel>
  )
}
