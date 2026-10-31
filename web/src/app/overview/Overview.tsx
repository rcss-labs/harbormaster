import type { ReactNode } from 'react'
import { Link } from 'react-router'
import { useOverview } from '../../api/hooks'
import { Ago } from '../../components/Ago'
import { Card } from '../../components/Card'
import { EmptyState } from '../../components/EmptyState'
import { Feed, FeedRow } from '../../components/Feed'
import { Split } from '../../components/Split'
import { Tile } from '../../components/Tile'
import { STATUS_COLOR, REASON_COLOR, clusterStatusKind } from '../../components/status'
import { BarChart } from '../../charts/BarChart'
import { Donut } from '../../charts/Donut'
import { HBars } from '../../charts/HBars'
import { Legend } from '../../charts/Legend'
import { Sparkline } from '../../charts/Sparkline'
import { dayLabel } from '../../charts/format'
import { useCrumbs } from '../shell/crumbs'
import { useFilters } from '../shell/useFilters'
import styles from './overview.module.css'

const STOP_NORMAL_COLOR = 'var(--border-strong)'
const STOP_ERROR_COLOR = 'var(--danger)'
const DEPLOY_COLOR = 'var(--accent)'
const RESTART_COLOR = 'var(--series-3)'
const FAILED_COLOR = 'var(--danger)'
const MISSING_COLOR = 'var(--danger)'

const accountsText = (count: number) => `${count} ${count === 1 ? 'account' : 'accounts'}`

export function Overview() {
  useCrumbs([['Overview']])
  const { filters } = useFilters()
  const overview = useOverview(filters)
  const data = overview.data

  if (overview.isError) {
    const message = overview.error instanceof Error ? overview.error.message : 'The overview could not be loaded.'
    return (
      <>
        <PageHead />
        <EmptyState>{message}</EmptyState>
      </>
    )
  }
  if (!data) return <PageHead />

  const healthyClusters = data.clusters.filter((cluster) => cluster.status === 'healthy').length
  const statusCount = (status: keyof typeof data.services.by_status) => data.services.by_status[status] ?? 0
  const servicesNeedingAttention = statusCount('failing') + statusCount('degraded') + statusCount('unhealthy_task')
  const serviceSplitParts = [
    { n: statusCount('steady'), color: STATUS_COLOR.ok },
    { n: statusCount('deploying'), color: STATUS_COLOR.info },
    { n: statusCount('degraded') + statusCount('unhealthy_task'), color: STATUS_COLOR.warn },
    { n: statusCount('failing'), color: STATUS_COLOR.bad },
    { n: statusCount('scaled_to_zero'), color: STATUS_COLOR.mute },
  ]

  const { task_health: health, tasks } = data
  const donutSegments = [
    { name: 'Running, healthy', v: health.healthy, color: 'var(--success)' },
    { name: 'Running, unhealthy', v: health.unhealthy, color: 'var(--warning)' },
    { name: 'Rolling out', v: health.rolling_out, color: 'var(--info)' },
    { name: 'Missing (desired − running)', v: health.missing, color: MISSING_COLOR },
  ]

  const stopColumns = data.stops_per_hour.map((hour) => ({
    parts: [
      { v: hour.normal, color: STOP_NORMAL_COLOR },
      { v: hour.error, color: STOP_ERROR_COLOR },
    ],
    title: `${new Date(hour.hour).toLocaleString()}: ${hour.normal} normal, ${hour.error} with an error`,
  }))

  const deployColumns = data.deployments_per_day.map((day) => ({
    parts: [
      { v: day.deploy, color: DEPLOY_COLOR },
      { v: day.restart, color: RESTART_COLOR },
      { v: day.failed, color: FAILED_COLOR },
    ],
    title: `${dayLabel(Date.parse(`${day.day}T00:00:00`))}: ${day.deploy} deploys, ${day.restart} restarts, ${day.failed} failed`,
  }))
  const firstDay = data.deployments_per_day[0]

  return (
    <>
      <PageHead>
        {`${accountsText(data.accounts)} · ${data.regions} regions · polled from AWS every 5 s · last poll `}
        <Ago ts={data.polled_at} />
      </PageHead>

      <div className={`${styles.grid} ${styles.g4}`}>
        <Tile k="Clusters" v={data.clusters.length} small={`${healthyClusters} healthy`}>
          <div className={styles.dots}>
            {data.clusters.map((cluster) => (
              <Link
                key={cluster.id}
                to={`/clusters/${cluster.id}`}
                title={cluster.name}
                className={styles.dot}
                style={{ background: STATUS_COLOR[clusterStatusKind(cluster.status)] }}
              />
            ))}
          </div>
        </Tile>
        <Tile
          k="Services"
          v={data.services.total}
          small={servicesNeedingAttention > 0 ? <span className={styles.danger}>{servicesNeedingAttention} need attention</span> : 'all steady'}
        >
          <Split parts={serviceSplitParts} />
        </Tile>
        <Tile k="Tasks running" v={tasks.running} small={`of ${tasks.desired} desired · ${tasks.unhealthy} unhealthy`}>
          <Sparkline values={tasks.history_72h.map((point) => point.running)} color="var(--accent)" height={24} fluid />
        </Tile>
      </div>

      <Card title="Task health" right="bar = running · line = desired">
        <Donut segments={donutSegments} label={`${tasks.running}/${tasks.desired}`} sub="tasks running" />
        <div className={styles.afterDonut}>
          <HBars
            items={data.per_cluster.map((cluster) => ({
              name: cluster.name,
              href: `/clusters/${cluster.id}`,
              parts: [{ v: cluster.running, color: cluster.running < cluster.desired ? 'var(--warning)' : 'var(--success)' }],
              ghost: cluster.desired,
              label: `${cluster.running} / ${cluster.desired}`,
            }))}
          />
        </div>
      </Card>

      <div className={`${styles.grid} ${styles.g3}`}>
        <Card title="Task stops per hour, 24 h">
          <div className={styles.legend}>
            <Legend
              items={[
                { name: 'Normal (deploy, scale)', color: STOP_NORMAL_COLOR },
                { name: 'Error exit', color: STOP_ERROR_COLOR },
              ]}
            />
          </div>
          <BarChart cols={stopColumns} height={110} xLabels={[[0, '24 h ago'], [12, '12 h'], [23, 'now']]} />
        </Card>
        <Card title="Deployments per day, 14 days">
          <div className={styles.legend}>
            <Legend
              items={[
                { name: 'Deploy', color: DEPLOY_COLOR },
                { name: 'Restart', color: RESTART_COLOR },
                { name: 'Failed', color: FAILED_COLOR },
              ]}
            />
          </div>
          <BarChart
            cols={deployColumns}
            height={110}
            xLabels={firstDay ? [[0, dayLabel(Date.parse(`${firstDay.day}T00:00:00`))], [13, 'today']] : [[13, 'today']]}
          />
        </Card>
      </div>

      <Card title="Why tasks stopped, 3 days">
        <HBars items={data.stop_reasons.map((reason) => ({ name: reason.reason, v: reason.count, color: REASON_COLOR[reason.reason] }))} />
      </Card>

      <Card title="Live events" right="ECS service events and actions taken in Harbormaster">
        <Feed>
          {data.events.map((event, index) => (
            <FeedRow key={index} ts={event.ts} source={event.source} actor={event.actor}>
              <Link to={`/services/${event.service.id}/operations`}>
                <strong>{event.service.name}</strong>
              </Link>{' '}
              <span className={styles.muted}>{event.text}</span>
            </FeedRow>
          ))}
        </Feed>
      </Card>
    </>
  )
}

/** Title block; the meta line is passed as children once the overview data has loaded. */
function PageHead({ children }: { children?: ReactNode }) {
  return (
    <div className={styles.pageHead}>
      <div className={styles.titles}>
        <h1>Overview</h1>
        {children !== undefined && <div className={styles.meta}>{children}</div>}
      </div>
    </div>
  )
}
