import { useState, type ReactNode } from 'react'
import type { AnalyticsDonut, AnalyticsSeries, AnalyticsSource, AnalyticsWindow, Proposed, TopTable } from '../../api/analytics'
import { Donut } from '../../charts/Donut'
import { TimeSeriesChart } from '../../charts/TimeSeriesChart'
import { DataTable, type DataTableColumn, type RowStatus } from '../../components/DataTable'
import { AnalyticsPanel } from './AnalyticsPanel'
import type { ChartLegendItem } from './ChartLegend'
import { linesOf, xLabelsFor } from './format'
import styles from './analytics.module.css'

/** What every tab body needs besides its data. */
export interface TabProps<D> {
  data: D
  showSources: boolean
  showProposed: boolean
}

interface PanelBase {
  title: string
  sub?: string
  span?: 'half' | 'full'
  source?: { table: string; kept: string }
  proposed?: Proposed<unknown>
  window: Pick<AnalyticsWindow, 'from'>
  showSources: boolean
  showProposed: boolean
}

/** Dim line under a KPI value. */
export function KpiNote({ children }: { children: ReactNode }) {
  return <small className={styles.kpiNote}>{children}</small>
}

/** A grid cell that disappears with its panel, so hidden proposals leave no gap. */
function Cell({ span, hidden, children }: { span?: 'half' | 'full'; hidden: boolean; children: ReactNode }) {
  if (hidden) return null
  return <div className={span === 'full' ? styles.full : styles.cell}>{children}</div>
}

const hiddenProposal = (base: PanelBase) => base.proposed !== undefined && !base.showProposed

interface SeriesPanelProps extends PanelBase {
  /** Null while a proposal has no data yet. */
  series: AnalyticsSeries | null | undefined
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

/** A panel with a time series chart; proposals without data show a quiet placeholder of the same height. */
export function SeriesPanel({ series, mode, colors = {}, yMin, yMax, yFormat, reference, legend, lineLabel, span, ...base }: SeriesPanelProps) {
  const lines = series ? linesOf(series, colors, lineLabel) : []
  return (
    <Cell span={span} hidden={hiddenProposal(base)}>
      <AnalyticsPanel
        title={base.title}
        sub={base.sub}
        source={base.source}
        proposed={base.proposed}
        window={base.window}
        showSources={base.showSources}
        showProposed={base.showProposed}
        legend={legend && series ? legend(lines) : undefined}
      >
        {(dim) =>
          series ? (
            <TimeSeriesChart
              timestamps={series.timestamps}
              series={lines}
              mode={mode}
              yMin={yMin}
              yMax={yMax}
              yFormat={yFormat}
              reference={reference}
              xLabels={xLabelsFor(series.timestamps)}
              dim={dim}
              ariaLabel={`${base.title} chart: ${lines.map((line) => line.label).join(', ')}`}
            />
          ) : (
            <div className={styles.placeholder}>Not collected yet</div>
          )
        }
      </AnalyticsPanel>
    </Cell>
  )
}

interface DonutPanelProps extends PanelBase {
  donut: AnalyticsDonut
  centerSub: string
  colors: string[]
}

export function DonutPanel({ donut, centerSub, colors, span, ...base }: DonutPanelProps) {
  const round = (value: number) => Math.round(value * 10) / 10
  const segments = donut.slices.map((slice, index) => ({ name: slice.label, v: round(slice.value), color: colors[index % colors.length] }))
  return (
    <Cell span={span} hidden={hiddenProposal(base)}>
      <AnalyticsPanel title={base.title} sub={base.sub} source={base.source} window={base.window} showSources={base.showSources} showProposed={base.showProposed}>
        <Donut segments={segments} label={String(round(donut.total))} sub={centerSub} size={132} ring={16} legend="pct" />
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
    <Cell span={span} hidden={hiddenProposal(base)}>
      <AnalyticsPanel title={base.title} sub={base.sub} source={base.source} proposed={base.proposed} window={base.window} showSources={base.showSources} showProposed={base.showProposed} flush>
        <DataTable label={base.title} columns={columns} rows={shown} rowKey={rowKey} rowStatus={rowStatus} minWidth={minWidth} empty={base.proposed && !base.proposed.available ? 'Not collected yet' : empty} />
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

/** Rows of a proposal's TopTable once it is available, otherwise none. */
export const proposedRows = <R,>(proposed: Proposed<TopTable<R>>): { rows: R[]; total: number } => ({
  rows: proposed.data?.rows ?? [],
  total: proposed.data?.total ?? 0,
})

/** "Data used on this page" from the API's source list. */
export function SourcesPanel({ sources, window, showSources, showProposed }: { sources: AnalyticsSource[]; window: Pick<AnalyticsWindow, 'from'>; showSources: boolean; showProposed: boolean }) {
  if (!showSources) return null
  const columns: DataTableColumn<AnalyticsSource>[] = [
    { key: 'table', header: 'Table', width: 'minmax(150px, 1fr)', render: (source) => <span className={styles.sourceTable}>{source.table}</span> },
    { key: 'reads', header: 'What this page reads', width: 'minmax(220px, 2fr)', render: (source) => source.reads },
    {
      key: 'kept',
      header: 'Kept',
      width: '170px',
      render: (source) => <span className={styles.mono}>{source.window_used ? `${source.kept} · reads ${source.window_used}` : source.kept}</span>,
    },
    { key: 'setting', header: 'Setting', width: 'minmax(220px, 1.3fr)', render: (source) => <span className={styles.dimMono}>{source.setting ?? '—'}</span> },
  ]
  return (
    <div className={styles.full}>
      <AnalyticsPanel title="Data used on this page" sub="existing tables only" window={window} showSources={showSources} showProposed={showProposed} flush>
        <DataTable label="Data used on this page" columns={columns} rows={sources} rowKey={(source) => source.table} minWidth={720} />
      </AnalyticsPanel>
    </div>
  )
}
