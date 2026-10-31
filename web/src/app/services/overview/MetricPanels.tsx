import type { ReactNode } from 'react'
import { Link } from 'react-router'
import { allNull, useTargetHealth } from '../../../api/observe'
import type { ServiceDTO, ServiceMetricsDTO } from '../../../api/types'
import { BarChart } from '../../../charts/BarChart'
import { Donut } from '../../../charts/Donut'
import { Legend } from '../../../charts/Legend'
import { LineChart } from '../../../charts/LineChart'
import { Sparkline } from '../../../charts/Sparkline'
import { XL24, fmtNum, niceMax } from '../../../charts/format'
import { useNow } from '../../../components/Ago'
import { Banner } from '../../../components/Banner'
import { Card } from '../../../components/Card'
import { Meter } from '../../../components/Meter'
import { Tile } from '../../../components/Tile'
import { MetricPanel } from '../../observe/MetricPanel'
import styles from './MetricPanels.module.css'

const DAY_MS = 86_400_000
const CPU_COLOR = 'var(--series-1)'
const MEMORY_COLOR = 'var(--series-2)'

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

function UsageTile({ title, now, values, color, off }: { title: string; now?: number | null; values: number[]; color: string; off: boolean }) {
  return (
    <Tile k={title} v={off ? '–' : percentLabel(now)} small={off ? 'Container Insights off' : undefined}>
      {!off && values.length > 0 && <Sparkline values={values} color={color} height={24} fluid />}
    </Tile>
  )
}

/** The CPU and memory tiles for the Overview tiles row. */
export function UsageTiles({ service, metrics }: MetricsProps) {
  const off = insightsOff(metrics)
  const cpuUnits = metrics?.now?.cpu_units ?? Number(service.task_definition.cpu)
  const memMib = metrics?.now?.mem_mib ?? Number(service.task_definition.memory)
  return (
    <>
      <UsageTile
        title={`CPU (of ${cpuUnits} units)`}
        now={metrics?.now?.cpu}
        values={nonNull(metrics?.series?.cpu ?? [])}
        color={CPU_COLOR}
        off={off}
      />
      <UsageTile
        title={`Memory (of ${memMib} MiB)`}
        now={metrics?.now?.mem}
        values={nonNull(metrics?.series?.mem ?? [])}
        color={MEMORY_COLOR}
        off={off}
      />
    </>
  )
}

/** CPU and memory over the last 24 h with deploy and restart markers. `now` is injectable for tests. */
export function UsageChart({ service, metrics, now: nowOverride }: MetricsProps & { now?: number }) {
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
  const targetHealth = useTargetHealth(service.id).data
  const loadBalancer = service.networking.load_balancers[0]
  // Until the target-group snapshot arrives, fall back to the running tasks' own health.
  const known = targetHealth && targetHealth.groups.length > 0
  const unhealthyTasks = service.running_tasks.filter((task) => task.health === 'UNHEALTHY').length
  const healthy = known ? targetHealth.healthy : service.running_tasks.length - unhealthyTasks
  const unhealthy = known ? targetHealth.unhealthy : unhealthyTasks
  const missing = Math.max(0, service.desired - healthy - unhealthy)

  return (
    <Card title="Target health" right={loadBalancer?.target_group_name}>
      <Donut
        segments={[
          { name: 'Healthy targets', v: healthy, color: 'var(--success)' },
          { name: 'Unhealthy targets', v: unhealthy, color: 'var(--warning)' },
          { name: 'Missing tasks', v: missing, color: 'var(--danger)' },
        ]}
        label={`${healthy}/${service.desired}`}
        sub="healthy"
      />
      {children && <div className={styles.afterDonut}>{children}</div>}
    </Card>
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

/** Running tasks card: per-task CPU and memory meters when the metrics carry `tasks_now`, else the plain task rows. */
export function RunningTasksMeters({ service, metrics }: MetricsProps) {
  const base = `/services/${service.id}`
  const usageByTask = new Map((metrics?.tasks_now ?? []).map((usage) => [usage.task_id, usage]))
  const withMeters = metrics?.tasks_now != null

  return (
    <Card title="Running tasks" right={`${service.running_tasks.length} running`}>
      {service.running_tasks.length === 0 ? (
        <div className={styles.empty}>No running tasks.</div>
      ) : (
        <div className={withMeters ? styles.taskMeters : styles.taskRows}>
          <div className={`${withMeters ? styles.taskMeter : styles.taskRow} ${styles.muted} ${styles.small}`}>
            <span>Task</span>
            <span>Zone</span>
            {withMeters && (
              <>
                <span>CPU</span>
                <span>Memory</span>
              </>
            )}
          </div>
          {service.running_tasks.map((task) => {
            const usage = usageByTask.get(task.short_id)
            return (
              <div key={task.id} className={withMeters ? styles.taskMeter : styles.taskRow}>
                <Link className={styles.mono} to={`${base}/tasks`} title={task.id}>
                  {task.short_id}
                </Link>
                <span className={styles.muted}>
                  {task.az}
                  {task.spot ? ' · spot' : ''}
                </span>
                {withMeters && (
                  <>
                    <UsageMeter value={usage?.cpu} color={CPU_COLOR} />
                    <UsageMeter value={usage?.mem} color={task.health === 'UNHEALTHY' ? 'var(--warning)' : MEMORY_COLOR} />
                  </>
                )}
              </div>
            )
          })}
        </div>
      )}
    </Card>
  )
}
