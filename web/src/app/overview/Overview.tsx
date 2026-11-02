import type { ReactNode } from 'react'
import { useOverview } from '../../api/hooks'
import type { OverviewDTO } from '../../api/types'
import { Ago } from '../../components/Ago'
import { EmptyState } from '../../components/EmptyState'
import { KpiStrip, type KpiItem } from '../../components/KpiStrip'
import { Panel } from '../../components/Panel'
import { SegmentedFilter } from '../../components/Seg'
import { REASON_COLOR } from '../../components/status'
import { BarChart } from '../../charts/BarChart'
import { HBars } from '../../charts/HBars'
import { Legend } from '../../charts/Legend'
import { BusiestClusters, useUsageKpis } from './ClusterUsagePanels'
import { EstateMap } from './EstateMap'
import { EstateView } from './EstateView'
import { NeedsAttention } from './NeedsAttention'
import { useOverviewLayout, type OverviewLayout } from './useOverviewState'
import { useCrumbs } from '../shell/crumbs'
import { useFilters } from '../shell/useFilters'
import styles from './overview.module.css'

const STOP_NORMAL_COLOR = 'var(--border-strong)'
const STOP_ERROR_COLOR = 'var(--danger)'

const accountsText = (count: number) => `${count} ${count === 1 ? 'account' : 'accounts'}`
const percentText = (value: number | null) => (value === null ? '–' : `${Math.round(value)}%`)

function withNote(value: ReactNode, note: ReactNode, color?: string) {
  return (
    <>
      {value}
      <small className={styles.kpiNote} style={color ? { color } : undefined}>
        {note}
      </small>
    </>
  )
}

/** Six cells; a sparkline appears only where the API provides history for that figure. */
function buildKpis(data: OverviewDTO, usage: ReturnType<typeof useUsageKpis>): KpiItem[] {
  const statusCount = (status: keyof typeof data.services.by_status) => data.services.by_status[status] ?? 0
  const healthyClusters = data.clusters.filter((cluster) => cluster.status === 'healthy').length
  const needAttention = statusCount('failing') + statusCount('degraded') + statusCount('unhealthy_task')
  const errorStops = data.stops_per_hour.reduce((total, hour) => total + hour.error, 0)
  const { tasks } = data
  return [
    { label: 'Clusters', value: withNote(data.clusters.length, `${healthyClusters} healthy`) },
    {
      label: 'Services',
      value: withNote(data.services.total, needAttention > 0 ? `${needAttention} need attention` : 'all steady', needAttention > 0 ? 'var(--danger)' : undefined),
    },
    {
      label: 'Tasks',
      value: withNote(tasks.running, `of ${tasks.desired} · ${tasks.unhealthy} unhealthy`, tasks.unhealthy > 0 ? 'var(--warning)' : undefined),
      spark: tasks.history_72h.map((point) => point.running),
    },
    { label: 'Avg CPU', value: percentText(usage.cpu), spark: usage.cpuSpark },
    { label: 'Avg memory', value: percentText(usage.mem), spark: usage.memSpark },
    {
      label: 'Error stops, 24 h',
      value: errorStops,
      valueColor: errorStops > 0 ? 'var(--danger)' : undefined,
      spark: data.stops_per_hour.map((hour) => hour.error),
      sparkColor: 'var(--danger)',
    },
  ]
}

function StopsPerHour({ data }: { data: OverviewDTO }) {
  const columns = data.stops_per_hour.map((hour) => ({
    parts: [
      { v: hour.normal, color: STOP_NORMAL_COLOR },
      { v: hour.error, color: STOP_ERROR_COLOR },
    ],
    title: `${new Date(hour.hour).toLocaleString()}: ${hour.normal} normal, ${hour.error} with an error`,
  }))
  return (
    <Panel
      title="Task stops per hour, 24 h"
      actions={
        <Legend
          items={[
            { name: 'Normal', color: STOP_NORMAL_COLOR },
            { name: 'Error exit', color: STOP_ERROR_COLOR },
          ]}
        />
      }
    >
      <BarChart cols={columns} height={90} xLabels={[[0, '24 h ago'], [12, '12 h'], [23, 'now']]} />
    </Panel>
  )
}

function StopReasons({ data }: { data: OverviewDTO }) {
  return (
    <Panel title="Why tasks stopped, 3 days">
      <HBars items={data.stop_reasons.map((reason) => ({ name: reason.reason, v: reason.count, color: REASON_COLOR[reason.reason] }))} />
    </Panel>
  )
}

export function Overview() {
  useCrumbs([['Overview']])
  const { filters } = useFilters()
  const overview = useOverview(filters)
  const usage = useUsageKpis()
  const [layout, setLayout] = useOverviewLayout()
  const data = overview.data

  if (overview.isError) {
    const message = overview.error instanceof Error ? overview.error.message : 'The overview could not be loaded.'
    return (
      <PageHead>
        <EmptyState>{message}</EmptyState>
      </PageHead>
    )
  }
  if (!data) return <PageHead />

  return (
    <PageHead
      meta={
        <>
          {`${accountsText(data.accounts)} · ${data.regions} regions · polled from AWS every 5 s · last poll `}
          <Ago ts={data.polled_at} />
        </>
      }
      layout={layout}
      onLayout={setLayout}
    >
      <KpiStrip label="Estate key figures" items={buildKpis(data, usage)} />
      {layout === 'triage' ? (
        <>
          <NeedsAttention />
          <div className={styles.twoCol}>
            <EstateMap />
            <div className={styles.stack}>
              <BusiestClusters />
              <StopsPerHour data={data} />
              <StopReasons data={data} />
            </div>
          </div>
        </>
      ) : (
        <EstateView />
      )}
    </PageHead>
  )
}

interface PageHeadProps {
  meta?: ReactNode
  layout?: OverviewLayout
  onLayout?: (layout: OverviewLayout) => void
  children?: ReactNode
}

/** Title block with the layout toggle; the page body is passed as children once the overview data has loaded. */
function PageHead({ meta, layout, onLayout, children }: PageHeadProps) {
  return (
    <div className={styles.page}>
      <div className={styles.pageHead}>
        <h1>Overview</h1>
        {meta !== undefined && <div className={styles.meta}>{meta}</div>}
        {layout !== undefined && onLayout !== undefined && (
          <div className={styles.layoutToggle}>
            <SegmentedFilter
              label="Layout"
              value={layout}
              onChange={onLayout}
              options={[
                { value: 'triage', label: 'Triage' },
                { value: 'estate', label: 'Estate' },
              ]}
            />
          </div>
        )}
      </div>
      {children}
    </div>
  )
}
