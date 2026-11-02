import type { ReactNode } from 'react'
import type { AnalyticsWindow, Proposed } from '../../api/analytics'
import { bucketDayLabel } from '../../charts/format'
import { Panel } from '../../components/Panel'
import styles from './AnalyticsPanel.module.css'
import { ChartLegend, type ChartLegendItem } from './ChartLegend'

interface AnalyticsPanelProps {
  title: string
  /** Dim sub-title after the title, e.g. "deploy, restart and scale". */
  sub?: string
  legend?: ChartLegendItem[]
  /** Where the data comes from; drives the `source <table> · kept N` footer. */
  source?: { table: string; kept: string }
  /** Set for panels that need a phase 2 rollup; the API's availability decides how the panel looks. */
  proposed?: Proposed<unknown>
  /** Window of the tab; a rollup that starts after `window.from` gets the "Daily data started on" note. */
  window?: Pick<AnalyticsWindow, 'from'>
  showSources: boolean
  showProposed: boolean
  /** Remove body padding, e.g. for tables. */
  flush?: boolean
  /** Chart or table; a function receives `dim` (true while the panel is a proposal) to pass on to the chart. */
  children: ReactNode | ((dim: boolean) => ReactNode)
}

/** Panel with the Analytics anatomy: PROPOSED badge, legend, source footer and the phase 2 empty state. */
export function AnalyticsPanel({
  title,
  sub,
  legend,
  source,
  proposed,
  window,
  showSources,
  showProposed,
  flush,
  children,
}: AnalyticsPanelProps) {
  if (proposed && !showProposed) return null

  const isProposal = proposed !== undefined && !proposed.available
  const startedLate =
    proposed?.available === true &&
    proposed.data_since !== null &&
    window !== undefined &&
    Date.parse(proposed.data_since) > Date.parse(window.from)

  let footer: string | undefined
  if (showSources) {
    if (isProposal && proposed.needs) footer = `needs new data ${proposed.needs.table} · ${proposed.needs.size} · ${proposed.needs.how}`
    else if (!isProposal && source) footer = `source ${source.table} · kept ${source.kept}`
  }

  return (
    <Panel
      title={title}
      sub={sub !== undefined ? <span className={styles.sub}>{sub}</span> : undefined}
      badge={isProposal ? 'PROPOSED' : undefined}
      legend={legend && legend.length > 0 ? <ChartLegend items={legend} /> : undefined}
      footer={footer}
      variant={isProposal ? 'proposed' : 'default'}
      flush={flush}
    >
      {startedLate && proposed.data_since && <p className={styles.note}>Daily data started on {bucketDayLabel(proposed.data_since)}</p>}
      <div className={isProposal ? styles.dimmed : undefined}>{typeof children === 'function' ? children(isProposal) : children}</div>
    </Panel>
  )
}
