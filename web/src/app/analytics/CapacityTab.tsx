import { Link } from 'react-router'
import type { AnalyticsCapacity, AtMaxRow, CapacityClusterRow, OverReservedRow, SubnetRow } from '../../api/analytics'
import { fmtNum } from '../../charts/format'
import type { DataTableColumn } from '../../components/DataTable'
import { KpiStrip } from '../../components/KpiStrip'
import { useAuth } from '../auth/AuthProvider'
import { BarCell, MonoCell, ServiceLink, TagCell } from './cells'
import { COLOR, daysBefore, memoryText, minutesText, oneDecimal, percent, sourceFor, TAG_COLOR, whole } from './format'
import { StackPanel, dailyEmpty, KpiNote, rollupRows, SeriesPanel, SourcesPanel, TablePanel, type TabProps } from './parts'
import styles from './analytics.module.css'

const ACCOUNT_COLORS = [COLOR.accent, COLOR.info, COLOR.ok, COLOR.orange, COLOR.warn, COLOR.gray]
const PROVIDER_COLORS = [COLOR.accent, COLOR.info, COLOR.orange, COLOR.warn, COLOR.ok, COLOR.gray]
const USAGE_COLORS = { reserved: COLOR.accent, used_p95: COLOR.orange, used_avg: COLOR.ok }
const DAY_MINUTES = 24 * 60

/** The most recent non-null value of a line, for legends that show "now". */
const latest = (values: (number | null)[]) => [...values].reverse().find((value): value is number => value !== null)

/** cluster_capacity_daily is per cluster, so these totals ignore service tags (owner decision). */
const WHOLE_CLUSTER_NOTE = 'whole cluster, all tags'

export function CapacityTab({ data, showSources }: TabProps<AnalyticsCapacity>) {
  const { me } = useAuth()
  const { kpis, window } = data
  const common = { window, showSources }
  const polledAt = Date.parse(data.polled_at)
  const tasks = sourceFor(data.sources, 'tasks')
  const counts = sourceFor(data.sources, 'service_counts_history')
  const capacityDaily = sourceFor(data.sources, 'cluster_capacity_daily')
  const utilDaily = sourceFor(data.sources, 'service_util_daily')
  // The rollup panels look back 90 days, whatever the 7-day window of the live panels.
  const ninetyDays = { ...common, window: { from: daysBefore(window.to, 90) } }
  const tagColors = Object.fromEntries((me?.tags ?? []).map((tag) => [String(tag.id), TAG_COLOR[tag.color]]))
  const topShare = Math.max(0, ...data.clusters.rows.map((row) => row.share))

  const clusterColumns: DataTableColumn<CapacityClusterRow>[] = [
    {
      key: 'cluster',
      header: 'Cluster',
      width: 'minmax(120px, 1.2fr)',
      sortValue: (row) => row.cluster,
      render: (row) => (
        <Link to={`/clusters/${row.cluster_id}`} className={styles.clusterLink}>
          {row.cluster}
        </Link>
      ),
    },
    { key: 'account', header: 'Account', width: '110px', render: (row) => <MonoCell>{row.account_name}</MonoCell> },
    { key: 'tag', header: 'Tag', width: '90px', render: (row) => <TagCell tag={row.tag} /> },
    { key: 'tasks', header: 'Tasks', width: '70px', align: 'right', sortValue: (row) => row.tasks, render: (row) => <MonoCell>{row.tasks}</MonoCell> },
    { key: 'vcpu', header: 'vCPU', width: '80px', align: 'right', sortValue: (row) => row.vcpu, render: (row) => <MonoCell>{oneDecimal(row.vcpu)}</MonoCell> },
    { key: 'memory', header: 'Memory', width: '100px', align: 'right', sortValue: (row) => row.mem_gib, render: (row) => <MonoCell>{memoryText(row.mem_gib)}</MonoCell> },
    {
      key: 'share',
      header: 'Share of vCPU',
      width: '140px',
      sortValue: (row) => row.share,
      render: (row) => <BarCell pct={topShare > 0 ? (row.share / topShare) * 100 : 0} label={percent(row.share)} />,
    },
    { key: 'spot', header: 'Spot', width: '70px', align: 'right', sortValue: (row) => row.spot_share, render: (row) => <MonoCell>{percent(row.spot_share, 0)}</MonoCell> },
  ]

  const atMaxColumns: DataTableColumn<AtMaxRow>[] = [
    { key: 'service', header: 'Service', width: 'minmax(130px, 1.3fr)', render: (row) => <ServiceLink id={row.service_id} name={row.service} cluster={row.cluster} /> },
    { key: 'desired', header: 'Desired / max', width: '110px', align: 'right', render: (row) => <MonoCell>{`${row.desired} / ${row.max_capacity}`}</MonoCell> },
    {
      key: 'since',
      header: 'At max for',
      width: '100px',
      align: 'right',
      render: (row) => <MonoCell>{row.at_max_since === null ? '—' : minutesText((polledAt - Date.parse(row.at_max_since)) / 60_000)}</MonoCell>,
    },
  ]

  const subnetColumns: DataTableColumn<SubnetRow>[] = [
    {
      key: 'subnet',
      header: 'Subnet',
      width: 'minmax(120px, 1.3fr)',
      render: (row) => (
        <>
          <strong className={styles.subnetName}>{row.name || row.subnet_id}</strong>
          <div className={styles.dimMono}>{row.account_id}</div>
        </>
      ),
    },
    { key: 'az', header: 'Zone', width: '100px', render: (row) => <MonoCell>{row.az}</MonoCell> },
    { key: 'free', header: 'Free', width: '60px', align: 'right', render: (row) => <MonoCell>{row.available_ips}</MonoCell> },
    {
      key: 'used',
      header: 'Used',
      width: '120px',
      render: (row) => <BarCell pct={row.used_pct * 100} label={percent(row.used_pct, 0)} color={freeFraction(row) < 0.06 ? COLOR.bad : COLOR.warn} />,
    },
  ]

  const overColumns: DataTableColumn<OverReservedRow>[] = [
    { key: 'service', header: 'Service', width: 'minmax(140px, 1.4fr)', render: (row) => <ServiceLink id={row.service_id} name={row.service} /> },
    { key: 'cluster', header: 'Cluster', width: 'minmax(90px, 1fr)', render: (row) => <MonoCell>{row.cluster}</MonoCell> },
    { key: 'tag', header: 'Tag', width: '90px', render: (row) => <TagCell tag={row.tag} /> },
    { key: 'vcpu', header: 'Reserved vCPU', width: '110px', align: 'right', render: (row) => <MonoCell>{oneDecimal(row.reserved_vcpu)}</MonoCell> },
    {
      key: 'cpuUse',
      header: 'CPU p95 / reserved',
      width: '150px',
      render: (row) => <BarCell pct={ratio(row.used_p95_vcpu, row.reserved_vcpu)} label={`${Math.round(ratio(row.used_p95_vcpu, row.reserved_vcpu))}%`} color={COLOR.orange} />,
    },
    { key: 'mem', header: 'Reserved memory', width: '120px', align: 'right', render: (row) => <MonoCell>{memoryText(row.reserved_mem_gib)}</MonoCell> },
    {
      key: 'memUse',
      header: 'Memory p95 / reserved',
      width: '160px',
      render: (row) => <BarCell pct={ratio(row.used_p95_mem_gib, row.reserved_mem_gib)} label={`${Math.round(ratio(row.used_p95_mem_gib, row.reserved_mem_gib))}%`} color={COLOR.orange} />,
    },
    { key: 'wasted', header: 'Unused vCPU', width: '100px', align: 'right', render: (row) => <MonoCell>{row.wasted_vcpu.toFixed(1)}</MonoCell> },
  ]

  const over = rollupRows(data.over_reserved)

  return (
    <>
      <KpiStrip
        label="Capacity key figures"
        items={[
          { label: 'Reserved vCPU', value: <>{oneDecimal(kpis.reserved_vcpu)}<KpiNote>sum of running task cpu</KpiNote></> },
          { label: 'Reserved memory', value: <>{memoryText(kpis.reserved_mem_gib)}<KpiNote>sum of running task memory</KpiNote></> },
          { label: 'Running tasks', value: <>{whole(kpis.running_tasks)}<KpiNote>{`in ${whole(kpis.services)} services`}</KpiNote></> },
          { label: 'Spot share', value: <>{kpis.spot_share === null ? '—' : percent(kpis.spot_share, 0)}<KpiNote>tasks on FARGATE_SPOT</KpiNote></> },
          { label: 'At scaling max', value: <>{whole(kpis.at_scaling_max)}<KpiNote>desired = auto-scaling max</KpiNote></>, valueColor: kpis.at_scaling_max > 0 ? COLOR.warn : undefined },
          { label: 'Subnets low on IPs', value: <>{whole(kpis.subnets_low_ips)}<KpiNote>under 10% addresses free</KpiNote></>, valueColor: kpis.subnets_low_ips > 0 ? COLOR.warn : undefined },
        ]}
      />
      <div className={styles.grid}>
        <SeriesPanel
          {...common}
          title="Reserved vCPU, last 7 days"
          sub="estimated: task counts × current task definition"
          span="full"
          series={data.reserved_vcpu_7d}
          mode="stacked"
          colors={tagColors}
          yFormat={fmtNum}
          source={counts ? { table: 'service_counts_history × task_definitions', kept: counts.kept } : undefined}
          legend={(lines) => lines.map((line) => ({ label: line.label, color: line.color, value: fmtNum(latest(line.values) ?? 0) }))}
        />
        <StackPanel {...common} title="Reserved vCPU by account" span="half" donut={data.vcpu_by_account} unit="vCPU" colors={ACCOUNT_COLORS} source={tasks} />
        <StackPanel
          {...common}
          title="Tasks by capacity provider"
          span="half"
          donut={data.tasks_by_capacity_provider}
          unit="tasks"
          colors={PROVIDER_COLORS}
          source={tasks ? { table: 'tasks.capacity_provider', kept: tasks.kept } : undefined}
        />
        <SeriesPanel
          {...ninetyDays}
          title="Reserved vs used vCPU, last 90 days"
          sub={WHOLE_CLUSTER_NOTE}
          span="full"
          series={data.reserved_vs_used_90d.data}
          rollup={data.reserved_vs_used_90d}
          empty={dailyEmpty(data.reserved_vs_used_90d)}
          source={capacityDaily}
          mode="line"
          colors={USAGE_COLORS}
          yFormat={fmtNum}
          legend={(lines) => lines.map((line) => ({ label: line.label.toLowerCase(), color: line.color, value: fmtNum(latest(line.values) ?? 0) }))}
        />
        <SeriesPanel
          {...ninetyDays}
          title="Spot share, last 90 days"
          sub={WHOLE_CLUSTER_NOTE}
          span="half"
          series={data.spot_share_90d.data}
          rollup={data.spot_share_90d}
          empty={dailyEmpty(data.spot_share_90d)}
          source={capacityDaily}
          mode="area"
          colors={{ spot_share: COLOR.info }}
          yFormat={(value) => `${value.toFixed(0)}%`}
          legend={(lines) => lines.map((line) => ({ label: 'now', color: line.color, value: `${(latest(line.values) ?? 0).toFixed(0)}%` }))}
        />
        <SeriesPanel
          {...ninetyDays}
          title="Memory reserved vs used p95"
          sub={`GiB · reserved is ${WHOLE_CLUSTER_NOTE}`}
          span="half"
          series={data.memory_90d.data}
          rollup={data.memory_90d}
          empty={dailyEmpty(data.memory_90d)}
          source={utilDaily}
          mode="line"
          colors={USAGE_COLORS}
          yFormat={fmtNum}
          legend={(lines) => lines.map((line) => ({ label: line.label.toLowerCase(), color: line.color, value: fmtNum(latest(line.values) ?? 0) }))}
        />
        <TablePanel
          {...ninetyDays}
          title="Most over-reserved services"
          sub="90-day p95 use vs what the task definition reserves"
          rollup={data.over_reserved}
          empty={dailyEmpty(data.over_reserved)}
          source={utilDaily}
          rows={over.rows}
          columns={overColumns}
          rowKey={(row) => row.service_id}
          minWidth={1000}
        />
        <TablePanel
          {...common}
          title="Clusters by reserved vCPU"
          source={tasks}
          rows={data.clusters.rows}
          total={data.clusters.total}
          noun="clusters"
          columns={clusterColumns}
          rowKey={(row) => row.cluster_id}
          minWidth={860}
          empty="No running tasks."
        />
        <TablePanel
          {...common}
          title="Services at auto-scaling max"
          span="half"
          source={counts ? { table: 'scaling + service_counts_history', kept: counts.kept } : undefined}
          rows={data.at_scaling_max}
          columns={atMaxColumns}
          rowKey={(row) => row.service_id}
          rowStatus={(row) => (row.at_max_since !== null && (polledAt - Date.parse(row.at_max_since)) / 60_000 > DAY_MINUTES ? 'warn' : undefined)}
          minWidth={340}
          empty="No service is at its scaling max."
        />
        <TablePanel
          {...common}
          title="Subnets running low on IPs"
          span="half"
          source={sourceFor(data.sources, 'subnets', 'subnets.available_ips')}
          rows={data.subnets_low}
          columns={subnetColumns}
          rowKey={(row) => row.subnet_id}
          rowStatus={(row) => (freeFraction(row) < 0.06 ? 'bad' : 'warn')}
          minWidth={360}
          empty="No subnet is running low."
        />
        <SourcesPanel sources={data.sources} {...common} />
      </div>
    </>
  )
}

const freeFraction = (row: SubnetRow) => (row.total_ips === 0 ? 0 : row.available_ips / row.total_ips)
const ratio = (part: number, total: number) => (total === 0 ? 0 : (part / total) * 100)
