import { useState, type ReactNode } from 'react'
import { Link, useParams } from 'react-router'
import { ApiError } from '../../api/client'
import { useCluster } from '../../api/hooks'
import { useClusterMetrics } from '../../api/observe'
import { BarChart } from '../../charts/BarChart'
import { fmtNum } from '../../charts/format'
import { ColumnPicker, useColumnPrefs } from '../../components/ColumnPicker'
import { DataBanner } from '../../components/DataBanner'
import { EmptyState } from '../../components/EmptyState'
import { KpiStrip } from '../../components/KpiStrip'
import { PinButton } from '../../components/PinButton'
import { Panel } from '../../components/Panel'
import { StackBar } from '../../components/StackBar'
import { StatusDot } from '../../components/StatusDot'
import { TagChip } from '../../components/TagChip'
import { STATUS_COLOR, statusKind } from '../../components/status'
import { EditTagDialog } from '../admin/tags/EditTagDialog'
import { useAuth } from '../auth/AuthProvider'
import { useCrumbs } from '../shell/crumbs'
import { usePins } from '../shell/pins'
import { ClusterUsageChart } from './ClusterUsageChart'
import { formatPercent } from './clusterMetrics'
import { SERVICE_COLUMN_OPTIONS, ServicesTable } from './ServicesTable'
import { presentValues } from './ServiceCardMetrics'
import styles from './clusters.module.css'
import { serviceSeverity } from './serviceSeverity'
import type { TagTarget } from './tagTarget'

const STOP_NORMAL_COLOR = 'var(--border-strong)'
const STOP_ERROR_COLOR = 'var(--danger)'
const TASK_LINE_COLOR = 'var(--accent)'
/** The Tasks by service panel lists this many services, problems first; the table below has them all. */
const BARS_SHOWN = 10

function NoAccess() {
  return (
    <div className={styles.noAccess}>
      <h2>No access</h2>
      <p>You have no permissions on any service in this cluster.</p>
    </div>
  )
}

const withSub = (value: ReactNode, sub: ReactNode) => (
  <>
    {value}
    <small className={styles.kpiSub}>{sub}</small>
  </>
)

export function Cluster() {
  const { clusterId = '' } = useParams()
  const clusterQuery = useCluster(clusterId)
  const clusterMetricsQuery = useClusterMetrics(clusterId, '24h')
  const { me } = useAuth()
  const { isPinned, toggle } = usePins()
  const [editing, setEditing] = useState<TagTarget | null>(null)
  const [visibleColumns, setVisibleColumns] = useColumnPrefs(
    'harbormaster-columns:cluster-services',
    SERVICE_COLUMN_OPTIONS.map((column) => column.key),
  )
  const cluster = clusterQuery.data
  const clusterMetrics = clusterMetricsQuery.data
  useCrumbs([['Clusters', '/clusters'], [cluster?.name ?? '']])

  if (clusterQuery.error instanceof ApiError && clusterQuery.error.status === 404) return <NoAccess />
  if (clusterQuery.isError) {
    const message = clusterQuery.error instanceof Error ? clusterQuery.error.message : 'The cluster could not be loaded.'
    return <EmptyState>{message}</EmptyState>
  }
  if (!cluster) return null

  // Tag chips open the tag dialog for admins only.
  const handleTagClick = me?.admin ? setEditing : undefined
  const services = cluster.services_detail
  const steadyCount = services.filter((service) => service.status === 'steady').length
  const missing = cluster.desired - cluster.running
  const insights = cluster.container_insights
  const cpuHistory = presentValues(clusterMetrics?.cluster?.cpu ?? [])
  const memHistory = presentValues(clusterMetrics?.cluster?.mem ?? [])
  const bySeverity = [...services].sort((a, b) => serviceSeverity(b.status) - serviceSeverity(a.status) || a.name.localeCompare(b.name))

  const stopColumns = cluster.stops_per_hour.map((hour) => ({
    parts: [
      { v: hour.normal, color: STOP_NORMAL_COLOR },
      { v: hour.error, color: STOP_ERROR_COLOR },
    ],
    title: `${new Date(hour.hour).toLocaleString()}: ${hour.normal} normal, ${hour.error} with an error`,
  }))

  return (
    <div className={styles.page}>
      <header className={styles.pageHead}>
        <div className={styles.titles}>
          <div className={styles.titleRow}>
            <h1 className={styles.monoTitle}>{cluster.name}</h1>
            <TagChip
              tag={cluster.tag}
              kind="cluster"
              onClick={handleTagClick && (() => handleTagClick({ kind: 'cluster', id: cluster.id, name: cluster.name, tag: cluster.tag }))}
            />
            <StatusDot clusterStatus={cluster.status} />
            <PinButton
              name={cluster.name}
              pinned={isPinned('cluster', cluster.id)}
              onToggle={() =>
                toggle({ kind: 'cluster', id: cluster.id, name: cluster.name, account_name: cluster.account_name, account_id: cluster.account_id })
              }
            />
          </div>
          <div className={styles.metaLine}>
            <span>
              {cluster.account_name} ({cluster.account_id})
            </span>
            <span>·</span>
            <span>{cluster.region}</span>
            {cluster.vpc && (
              <>
                <span>·</span>
                <Link
                  className={styles.accentLink}
                  to={`/networks?${new URLSearchParams({ vpc: `${cluster.account_id}/${cluster.region}/${cluster.vpc.vpc_id}` })}`}
                >
                  {cluster.vpc.name}
                </Link>
              </>
            )}
            <span>·</span>
            <span>
              {cluster.fargate_tasks} Fargate · {cluster.spot_tasks} Spot
            </span>
          </div>
        </div>
        <div className={styles.actions}>
          <Link className={styles.btn} to={`/tasks?${new URLSearchParams({ filter: 'all', q: cluster.name })}`}>
            Tasks in this cluster
          </Link>
          <a className={styles.btn} href={cluster.console_url} target="_blank" rel="noopener">
            Open in AWS console ↗
          </a>
        </div>
      </header>
      <DataBanner tiers={['Clusters and services', 'Tasks']} />

      <KpiStrip
        label="Cluster summary"
        items={[
          {
            label: 'Services',
            value: withSub(services.length, `${steadyCount} steady`),
            href: `/services?${new URLSearchParams({ q: cluster.name })}`,
          },
          {
            label: 'Tasks',
            value: withSub(`${cluster.running}/${cluster.desired}`, missing > 0 ? `${missing} missing` : 'all running'),
            valueColor: missing > 0 ? 'var(--warning)' : undefined,
            href: `/tasks?${new URLSearchParams({ filter: 'all', q: cluster.name })}`,
            spark: cluster.tasks_history_72h.map((point) => point.running),
            sparkColor: TASK_LINE_COLOR,
          },
          { label: 'Reserved', value: withSub(fmtNum(cluster.reserved_vcpu), `vCPU · ${fmtNum(cluster.reserved_gib)} GiB`) },
          {
            label: 'CPU now',
            value: insights ? formatPercent(clusterMetrics?.now?.cpu) : withSub('–', 'Insights off'),
            spark: cpuHistory,
            sparkColor: 'var(--series-1)',
          },
          {
            label: 'Memory now',
            value: insights ? formatPercent(clusterMetrics?.now?.mem) : withSub('–', 'Insights off'),
            spark: memHistory,
            sparkColor: 'var(--series-2)',
          },
        ]}
      />

      <div className={styles.twoCol}>
        <ClusterUsageChart metrics={clusterMetrics} insights={insights} error={clusterMetricsQuery.error} />
        <Panel title="Tasks by service" actions={<span>bar = running · tick = desired</span>}>
          <div className={styles.barList}>
            {bySeverity.slice(0, BARS_SHOWN).map((service) => {
              // Each bar is scaled to the larger of running and desired, so the desired tick always fits.
              const scale = Math.max(service.running, service.desired)
              return (
                <div key={service.id} className={styles.barRow}>
                  <Link to={`/services/${service.id}/tasks`} className={styles.barName}>
                    {service.name}
                  </Link>
                  <div className={styles.barTrack}>
                    <StackBar parts={[{ n: service.running, color: STATUS_COLOR[statusKind(service.status)], label: 'Running' }]} total={scale} label="Tasks" />
                    <i className={styles.barTick} style={{ left: `${scale > 0 ? (service.desired / scale) * 100 : 0}%` }} aria-hidden="true" />
                  </div>
                  <span className={styles.barValue}>{`${service.running} / ${service.desired}`}</span>
                </div>
              )
            })}
          </div>
          {bySeverity.length > BARS_SHOWN && (
            <div className={styles.note}>{`${bySeverity.length - BARS_SHOWN} more in the table below`}</div>
          )}
          <div className={styles.stopsBlock}>
            <h3 className={styles.stopsTitle}>Task stops per hour</h3>
            <BarChart cols={stopColumns} height={80} xLabels={[[0, '24 h ago'], [23, 'now']]} />
          </div>
        </Panel>
      </div>

      <Panel
        title="Services"
        flush
        toolbar={
          <>
            <span className={styles.count}>{`${services.length} ${services.length === 1 ? 'service' : 'services'} · problems first`}</span>
            <ColumnPicker columns={SERVICE_COLUMN_OPTIONS} visible={visibleColumns} onChange={setVisibleColumns} />
          </>
        }
      >
        <ServicesTable
          services={services}
          clusterName={cluster.name}
          onTagClick={handleTagClick}
          metrics={clusterMetrics?.services}
          insights={insights}
          visibleColumns={visibleColumns}
        />
      </Panel>

      {editing?.kind === 'cluster' && (
        <EditTagDialog kind="cluster" id={editing.id} name={editing.name} current={editing.tag.id} onClose={() => setEditing(null)} />
      )}
      {editing?.kind === 'service' && (
        <EditTagDialog
          kind="service"
          id={editing.id}
          name={editing.name}
          current={editing.tag.inherited ? null : editing.tag.id}
          clusterName={cluster.name}
          clusterTag={cluster.tag}
          onClose={() => setEditing(null)}
        />
      )}
    </div>
  )
}
