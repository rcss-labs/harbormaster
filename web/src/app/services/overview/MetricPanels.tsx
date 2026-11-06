import { useState, type ReactNode } from 'react'
import { Link } from 'react-router'
import { allNull, useTargetHealth } from '../../../api/observe'
import type { RunningTaskDTO, ServiceDTO, ServiceMetricsDTO } from '../../../api/types'
import { BarChart } from '../../../charts/BarChart'
import { Legend } from '../../../charts/Legend'
import { LineChart } from '../../../charts/LineChart'
import { XL24, fmtNum, niceMax } from '../../../charts/format'
import { useNow } from '../../../components/Ago'
import { Banner } from '../../../components/Banner'
import { DataTable, type DataTableColumn } from '../../../components/DataTable'
import { Panel } from '../../../components/Panel'
import type { PanelErrorProps } from '../../../components/PanelError'
import { StackBar } from '../../../components/StackBar'
import { queryPanelError } from '../panelError'
import { StopTaskDialog } from '../../../components/StopTaskDialog'
import { Meter } from '../../../components/Meter'
import { KpiStrip, type KpiItem } from '../../../components/KpiStrip'
import { MetricPanel } from '../../observe/MetricPanel'
import styles from './MetricPanels.module.css'

const DAY_MS = 86_400_000
const CPU_COLOR = 'var(--series-1)'
const MEMORY_COLOR = 'var(--series-2)'

interface PanelState {
  loading?: boolean
  error?: PanelErrorProps
}

interface MetricsProps {
  service: ServiceDTO
  /** Undefined while the metrics request is loading. */
  metrics?: ServiceMetricsDTO
}

/** Usage values are already percentages of the reserved size. */
const percentLabel = (value: number | null | undefined) => (value === null || value === undefined ? '–' : `${Math.round(value)}%`)
const nonNull = (values: (number | null)[]) => values.filter((value): value is number => value !== null)
const insightsOff = (metrics?: ServiceMetricsDTO) => metrics !== undefined && (metrics.insights === 'disabled' || metrics.series === null)
const hasLoadBalancer = (service: ServiceDTO) => service.networking.load_balancers.length > 0

const sub = (text: string) => <small className={styles.kpiSub}>{text}</small>

/** The Overview KPI strip: tasks, revision, CPU, memory and, behind a load balancer, requests, p95 and 5xx. */
export function ServiceKpis({ service, metrics }: MetricsProps) {
  const off = insightsOff(metrics)
  const cpuUnits = metrics?.now?.cpu_units ?? Number(service.task_definition.cpu)
  const memMib = metrics?.now?.mem_mib ?? Number(service.task_definition.memory)
  const pending = Math.max(0, service.desired - service.running)
  const alb = hasLoadBalancer(service) ? metrics?.alb : null

  const usage = (label: string, now: number | null | undefined, values: number[], color: string, unit: string): KpiItem => ({
    label,
    value: off ? (
      <>
        –{sub('Container Insights off')}
      </>
    ) : (
      <>
        {percentLabel(now)}
        {sub(unit)}
      </>
    ),
    spark: off ? undefined : values,
    sparkColor: color,
  })

  const items: KpiItem[] = [
    {
      label: 'Tasks',
      value: (
        <>
          {service.running}/{service.desired}
          {sub(pending > 0 ? `${pending} pending` : 'all running')}
        </>
      ),
      valueColor: pending > 0 ? 'var(--warning)' : undefined,
    },
    {
      label: 'Revision',
      value: (
        <>
          :{service.revision}
          {sub(service.image_tag)}
        </>
      ),
    },
    usage('CPU', metrics?.now?.cpu, nonNull(metrics?.series?.cpu ?? []), CPU_COLOR, `of ${cpuUnits} units`),
    usage('Memory', metrics?.now?.mem, nonNull(metrics?.series?.mem ?? []), MEMORY_COLOR, `of ${memMib} MiB`),
  ]
  if (alb) {
    items.push(
      {
        label: 'Req / min',
        value: alb.req_now === null ? '–' : fmtNum(alb.req_now),
        spark: nonNull(alb.req_per_min),
        sparkColor: 'var(--series-3)',
      },
      {
        label: 'p95',
        value: alb.p95_now === null ? '–' : <>{Math.round(alb.p95_now)}{sub('ms')}</>,
        spark: nonNull(alb.p95_ms),
        sparkColor: 'var(--series-4)',
      },
      {
        label: 'HTTP 5xx',
        value: <>{fmtNum(alb.err5xx_today)}{sub('today')}</>,
        valueColor: alb.err5xx_today > 0 ? 'var(--danger)' : undefined,
      },
    )
  }
  return <KpiStrip label="Service summary" items={items} />
}

/** CPU and memory over the last 24 h with deploy and restart markers. `now` is injectable for tests. */
export function UsageChart({ service, metrics, now: nowOverride, loading, error }: MetricsProps & PanelState & { now?: number }) {
  const clock = useNow(60_000)
  const now = nowOverride ?? clock
  const windowStart = now - DAY_MS
  const marks = (metrics?.markers ?? []).map((marker) => ({
    at: Math.min(1, Math.max(0, (Date.parse(marker.at) - windowStart) / DAY_MS)),
    label: marker.label,
  }))
  const cpu = metrics?.series?.cpu ?? []
  const mem = metrics?.series?.mem ?? []
  const off = insightsOff(metrics)

  return (
    <MetricPanel
      title="CPU and memory, 24 h"
      right={
        <Link className={styles.accentLink} to={`/services/${service.id}/metrics`}>
          All metrics →
        </Link>
      }
      stale={metrics?.stale}
      loading={loading}
      error={error}
      empty={!off && metrics !== undefined && allNull(cpu, mem)}
    >
      {off ? (
        <Banner kind="warn">Container Insights is off for {service.cluster.name}, so there are no CPU and memory graphs.</Banner>
      ) : (
        <>
          <Legend
            items={[
              { name: 'CPU', color: CPU_COLOR },
              { name: 'Memory', color: MEMORY_COLOR },
              { name: 'Deployment or restart', color: 'var(--info)' },
            ]}
          />
          <LineChart
            series={[
              { name: 'CPU', color: CPU_COLOR, values: cpu },
              { name: 'Memory', color: MEMORY_COLOR, values: mem },
            ]}
            xLabels={XL24}
            height={160}
            marks={marks}
          />
        </>
      )}
    </MetricPanel>
  )
}

/** Target health donut for a service behind a load balancer; `children` (the deployment card) sit under the donut. */
export function HealthCard({ service, children }: { service: ServiceDTO; children?: ReactNode }) {
  const targetHealthQuery = useTargetHealth(service.id)
  const targetHealth = targetHealthQuery.data
  const loadBalancer = service.networking.load_balancers[0]
  // Until the target-group snapshot arrives, fall back to the running tasks' own health.
  const known = targetHealth && targetHealth.groups.length > 0
  const unhealthyTasks = service.running_tasks.filter((task) => task.health === 'UNHEALTHY').length
  const healthy = known ? targetHealth.healthy : service.running_tasks.length - unhealthyTasks
  const unhealthy = known ? targetHealth.unhealthy : unhealthyTasks
  const missing = Math.max(0, service.desired - healthy - unhealthy)

  return (
    <Panel
      title="Target health"
      sub={<span className={styles.mono}>{`${healthy}/${service.desired} healthy`}</span>}
      actions={loadBalancer?.target_group_name}
      error={queryPanelError(targetHealthQuery, 'Target health could not be read.')}
    >
      <StackBar
        label="Target health"
        total={Math.max(service.desired, healthy + unhealthy)}
        legend
        parts={[
          { label: 'Healthy targets', n: healthy, color: 'var(--ok)' },
          { label: 'Unhealthy targets', n: unhealthy, color: 'var(--warn)' },
          { label: 'Missing tasks', n: missing, color: 'var(--bad)' },
        ]}
      />
      {children && <div className={styles.afterBar}>{children}</div>}
    </Panel>
  )
}

/** Request rate, p95 latency and 5xx count; nothing without a load balancer. */
export function ALBPanels({ service, metrics }: MetricsProps) {
  const alb = metrics?.alb
  if (!hasLoadBalancer(service) || !alb) return null

  const requestMax = niceMax(Math.max(0, ...nonNull(alb.req_per_min)))
  const p95Max = niceMax(Math.max(0, ...nonNull(alb.p95_ms)))
  const errors = alb.err5xx_per_hour.values
  const errorColumns = errors.map((value, index) => ({
    parts: [{ v: value ?? 0, color: 'var(--danger)' }],
    title: `${errors.length - index} h ago: ${value ?? 0} errors`,
  }))

  return (
    <div className={styles.g3}>
      <MetricPanel
        title="Requests per minute, 24 h"
        right={alb.req_now === null ? undefined : `now ${fmtNum(alb.req_now)}`}
        stale={metrics.stale}
        empty={allNull(alb.req_per_min)}
      >
        <LineChart series={[{ name: 'requests', color: 'var(--series-4)', values: alb.req_per_min }]} unit="" max={requestMax} xLabels={XL24} height={110} />
      </MetricPanel>
      <MetricPanel
        title="Response time p95, 24 h"
        right={alb.p95_now === null ? undefined : `now ${Math.round(alb.p95_now)} ms`}
        stale={metrics.stale}
        empty={allNull(alb.p95_ms)}
      >
        <LineChart series={[{ name: 'p95', color: 'var(--series-3)', values: alb.p95_ms }]} unit=" ms" max={p95Max} xLabels={XL24} height={110} />
      </MetricPanel>
      <MetricPanel title="HTTP 5xx per hour, 24 h" right={`${fmtNum(alb.err5xx_today)} today`} stale={metrics.stale}>
        <BarChart cols={errorColumns} height={110} xLabels={[[0, '24 h ago'], [12, '12 h'], [23, 'now']]} />
      </MetricPanel>
    </div>
  )
}

function UsageMeter({ value, color }: { value: number | null | undefined; color: string }) {
  return value === null || value === undefined ? <span className={styles.muted}>–</span> : <Meter pct={value} color={color} />
}

/** Running tasks panel: per-task CPU and memory meters when the metrics carry `tasks_now`, with Shell, Logs and Stop per task. */
export function RunningTasksMeters({ service, metrics }: MetricsProps) {
  const base = `/services/${service.id}`
  const [stopping, setStopping] = useState<RunningTaskDTO | null>(null)
  const usageByTask = new Map((metrics?.tasks_now ?? []).map((usage) => [usage.task_id, usage]))
  const withMeters = metrics?.tasks_now != null
  const canShell = service.can.terminal && service.enable_execute_command

  const columns: DataTableColumn<RunningTaskDTO>[] = [
    {
      key: 'task',
      header: 'Task',
      width: '120px',
      render: (task) => (
        <Link className={styles.mono} to={`${base}/tasks`} title={task.id}>
          {task.short_id}
        </Link>
      ),
    },
    {
      key: 'zone',
      header: 'Zone',
      width: '110px',
      render: (task) => (
        <span className={styles.muted}>
          {task.az}
          {task.spot ? ' · spot' : ''}
        </span>
      ),
    },
    ...(withMeters
      ? [
          { key: 'cpu', header: 'CPU', render: (task: RunningTaskDTO) => <UsageMeter value={usageByTask.get(task.short_id)?.cpu} color={CPU_COLOR} /> },
          {
            key: 'mem',
            header: 'Memory',
            render: (task: RunningTaskDTO) => (
              <UsageMeter value={usageByTask.get(task.short_id)?.mem} color={task.health === 'UNHEALTHY' ? 'var(--warning)' : MEMORY_COLOR} />
            ),
          },
        ]
      : []),
    {
      key: 'actions',
      header: '',
      width: '170px',
      align: 'right',
      render: (task) => (
        <span className={styles.rowActions}>
          {canShell && task.exec_agent_running && (
            <Link className={styles.rowBtn} to={`${base}/terminal?task=${encodeURIComponent(task.id)}`}>
              Shell
            </Link>
          )}
          {service.can.logs && (
            <Link className={styles.rowBtn} to={`${base}/logs`}>
              Logs
            </Link>
          )}
          {service.can.operate && (
            <button type="button" className={`${styles.rowBtn} ${styles.rowDanger}`} onClick={() => setStopping(task)}>
              Stop
            </button>
          )}
        </span>
      ),
    },
  ]

  return (
    <Panel title="Running tasks" actions={`${service.running_tasks.length} running`} flush>
      <DataTable
        label="Running tasks"
        columns={columns}
        rows={service.running_tasks}
        rowKey={(task) => task.id}
        rowStatus={(task) => (task.health === 'UNHEALTHY' ? 'warn' : undefined)}
        empty="No running tasks."
        minWidth={560}
        pageSize={25}
      />
      {stopping && (
        <StopTaskDialog
          taskId={stopping.id}
          shortId={stopping.short_id.slice(0, 12)}
          serviceName={service.name}
          serviceId={service.id}
          desiredCount={service.desired}
          onClose={() => setStopping(null)}
        />
      )}
    </Panel>
  )
}
