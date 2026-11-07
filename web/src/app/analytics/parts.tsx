import { useState, type ReactNode } from 'react'
import type { AnalyticsDonut, AnalyticsSeries, AnalyticsSource, AnalyticsWindow, RollupPanel, TopTable } from '../../api/analytics'
import { TimeSeriesChart } from '../../charts/TimeSeriesChart'
import { bucketDayLabel } from '../../charts/format'
import { DataTable, type DataTableColumn, type RowStatus } from '../../components/DataTable'
import { EmptyState } from '../../components/EmptyState'
import { StackBar, type StackBarPart } from '../../components/StackBar'
import { AnalyticsPanel } from './AnalyticsPanel'
import type { ChartLegendItem } from './ChartLegend'
import { linesOf, retentionText, xLabelsFor } from './format'
import styles from './analytics.module.css'

/** What every tab body needs besides its data. */
export interface TabProps<D> {
  data: D
  showSources: boolean
}

interface PanelBase {
  title: string
  sub?: string
  span?: 'half' | 'full'
  source?: { table: string; kept: string }
  rollup?: RollupPanel<unknown>
  window: Pick<AnalyticsWindow, 'from'>
  showSources: boolean
}

/** Dim line under a KPI value. */
export function KpiNote({ children }: { children: ReactNode }) {
  return <small className={styles.kpiNote}>{children}</small>
}

function Cell({ span, children }: { span?: 'half' | 'full'; children: ReactNode }) {
  return <div className={span === 'full' ? styles.full : styles.cell}>{children}</div>
}

/** Empty-state text of a panel read from a daily rollup table. */
export function dailyEmpty(rollup: RollupPanel<unknown>): string {
  return rollup.data_since ? `Collecting daily data since ${bucketDayLabel(rollup.data_since)}` : 'Daily data appears after the first rollup'
}

/** Empty-state text of the failure-reason panels. */
export const FAILURES_EMPTY = 'No failed rollouts with a recorded reason in this range'

interface SeriesPanelProps extends PanelBase {
  /** Null while the panel has no data yet; `empty` then says why. */
  series: AnalyticsSeries | null | undefined
  empty?: string
  mode: 'line' | 'area' | 'stacked'
  colors?: Record<string, string>
  yMin?: number
  yMax?: number
  yFormat?: (value: number) => string
  reference?: { value: number; label: string }
  /** Panel header legend from the coloured lines. */
  legend?: (lines: ReturnType<typeof linesOf>) => ChartLegendItem[]
  lineLabel?: (line: AnalyticsSeries['series'][number]) => string
}

/** A panel with a time series chart. */
export function SeriesPanel({ series, empty = 'Nothing to show.', mode, colors = {}, yMin, yMax, yFormat, reference, legend, lineLabel, span, ...base }: SeriesPanelProps) {
  const lines = series ? linesOf(series, colors, lineLabel) : []
  return (
    <Cell span={span}>
      <AnalyticsPanel
        title={base.title}
        sub={base.sub}
        source={base.source}
        rollup={base.rollup}
        window={base.window}
        showSources={base.showSources}
        legend={legend && series ? legend(lines) : undefined}
      >
        {series ? (
          <TimeSeriesChart
            timestamps={series.timestamps}
            series={lines}
            mode={mode}
            yMin={yMin}
            yMax={yMax}
            yFormat={yFormat}
            reference={reference}
            xLabels={xLabelsFor(series.timestamps)}
            ariaLabel={`${base.title} chart: ${lines.map((line) => line.label).join(', ')}`}
          />
        ) : (
          <EmptyState>{empty}</EmptyState>
        )}
      </AnalyticsPanel>
    </Cell>
  )
}

interface StackPanelProps extends PanelBase {
  donut: AnalyticsDonut
  /** Unit after the total in the panel header, e.g. "vCPU". */
  unit: string
  colors: string[]
}

/** A breakdown drawn as one stacked bar with its counts listed under it. */
export function StackPanel({ donut, unit, colors, span, ...base }: StackPanelProps) {
  const round = (value: number) => Math.round(value * 10) / 10
  const parts: StackBarPart[] = donut.slices.map((slice, index) => ({ n: round(slice.value), color: colors[index % colors.length], label: slice.label }))
  return (
    <Cell span={span}>
      <AnalyticsPanel title={base.title} sub={`${round(donut.total)} ${unit}`} source={base.source} window={base.window} showSources={base.showSources}>
        <StackBar parts={parts} label={base.title} legend />
      </AnalyticsPanel>
    </Cell>
  )
}

interface TablePanelProps<R> extends PanelBase {
  /** Also the accessible name of the table. */
  rows: R[]
  /** Size of the matching population when the API capped the rows; shows the "Showing 8 of N" line. */
  total?: number
  noun?: string
  columns: DataTableColumn<R>[]
  rowKey: (row: R) => string
  rowStatus?: (row: R) => RowStatus | undefined
  /** Rows come ranked by the server; the first `limit` are shown. */
  limit?: number
  minWidth: number
  empty?: string
}

const TOP_LIMIT = 8

/** A flush table panel; with `total` it shows the first 8 rows and a "Show all" link to the rest. */
export function TablePanel<R>({ rows, total, noun = 'rows', columns, rowKey, rowStatus, limit = TOP_LIMIT, minWidth, empty = 'Nothing to show.', span = 'full', ...base }: TablePanelProps<R>) {
  const [expanded, setExpanded] = useState(false)
  const capped = total !== undefined
  const shown = capped && !expanded ? rows.slice(0, limit) : rows
  return (
    <Cell span={span}>
      <AnalyticsPanel title={base.title} sub={base.sub} source={base.source} rollup={base.rollup} window={base.window} showSources={base.showSources} flush>
        <DataTable label={base.title} columns={columns} rows={shown} rowKey={rowKey} rowStatus={rowStatus} minWidth={minWidth} empty={empty} phoneSort />
        {capped && shown.length < Math.max(total, rows.length) && (
          <div className={styles.more}>
            <span>{`Showing ${shown.length} of ${Math.max(total, rows.length)} ${noun}`}</span>
            {!expanded && rows.length > limit && (
              <button type="button" onClick={() => setExpanded(true)}>
                Show all
              </button>
            )}
          </div>
        )}
      </AnalyticsPanel>
    </Cell>
  )
}

/** Rows of a rollup panel's TopTable once it is available, otherwise none. */
export const rollupRows = <R,>(panel: RollupPanel<TopTable<R>>): { rows: R[]; total: number } => ({
  rows: panel.data?.rows ?? [],
  total: panel.data?.total ?? 0,
})

/** "Data used on this page" from the API's source list. */
export function SourcesPanel({ sources, window, showSources }: { sources: AnalyticsSource[]; window: Pick<AnalyticsWindow, 'from'>; showSources: boolean }) {
  if (!showSources) return null
  const columns: DataTableColumn<AnalyticsSource>[] = [
    { key: 'table', header: 'Table', width: 'minmax(150px, 1fr)', render: (source) => <span className={styles.sourceTable}>{source.table}</span> },
    { key: 'reads', header: 'What this page reads', width: 'minmax(220px, 2fr)', render: (source) => source.reads },
    {
      key: 'kept',
      header: 'Kept',
      width: '170px',
      render: (source) => <span className={styles.mono}>{source.window_used ? `${retentionText(source.kept)} · reads ${source.window_used}` : retentionText(source.kept)}</span>,
    },
    { key: 'setting', header: 'Setting', width: 'minmax(220px, 1.3fr)', render: (source) => <span className={styles.dimMono}>{source.setting ?? '—'}</span> },
  ]
  return (
    <div className={styles.full}>
      <AnalyticsPanel title="Data used on this page" window={window} showSources={showSources} flush>
        <DataTable label="Data used on this page" columns={columns} rows={sources} rowKey={(source) => source.table} minWidth={720} />
      </AnalyticsPanel>
    </div>
  )
}
