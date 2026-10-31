import { useState } from 'react'
import { useParams } from 'react-router'
import { ApiError } from '../../api/client'
import { useCluster } from '../../api/hooks'
import { useClusterMetrics } from '../../api/observe'
import { BarChart } from '../../charts/BarChart'
import { Sparkline } from '../../charts/Sparkline'
import { fmtNum } from '../../charts/format'
import { Card } from '../../components/Card'
import { EmptyState } from '../../components/EmptyState'
import { HBars } from '../../charts/HBars'
import { Pill, type PillKind } from '../../components/Pill'
import { Seg } from '../../components/Seg'
import { Split } from '../../components/Split'
import { STATUS_COLOR, clusterStatusKind, clusterStatusLabel, statusKind } from '../../components/status'
import { TagChip } from '../../components/TagChip'
import { Tile } from '../../components/Tile'
import { EditTagDialog } from '../admin/tags/EditTagDialog'
import { useAuth } from '../auth/AuthProvider'
import { useCrumbs } from '../shell/crumbs'
import { ClusterUsageChart, UseNowTile } from './ClusterUsageChart'
import { ServiceCard } from './ServiceCard'
import { ServicesTable } from './ServicesTable'
import styles from './clusters.module.css'
import type { TagTarget } from './tagTarget'
import { LIST_VIEW_OPTIONS, useListView } from './useListView'

const STOP_NORMAL_COLOR = 'var(--border-strong)'
const STOP_ERROR_COLOR = 'var(--danger)'
const FARGATE_COLOR = 'var(--accent)'
const SPOT_COLOR = 'var(--series-3)'
const TASK_LINE_COLOR = 'var(--accent)'

/** Order in which service status counts stack in the Services tile. */
const SERVICE_SPLIT_ORDER: PillKind[] = ['ok', 'info', 'warn', 'bad', 'mute']

function NoAccess() {
  return (
    <div className={styles.noAccess}>
      <h2>No access</h2>
      <p>You have no permissions on any service in this cluster.</p>
    </div>
  )
}

export function Cluster() {
  const { clusterId = '' } = useParams()
  const clusterQuery = useCluster(clusterId)
  const clusterMetricsQuery = useClusterMetrics(clusterId, '24h')
  const { me } = useAuth()
  const [view, setView] = useListView('cards')
  const [editing, setEditing] = useState<TagTarget | null>(null)
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
  const serviceSplitParts = SERVICE_SPLIT_ORDER.map((kind) => ({
    n: services.filter((service) => statusKind(service.status) === kind).length,
    color: STATUS_COLOR[kind],
  }))

  const stopColumns = cluster.stops_per_hour.map((hour) => ({
    parts: [
      { v: hour.normal, color: STOP_NORMAL_COLOR },
      { v: hour.error, color: STOP_ERROR_COLOR },
    ],
    title: `${new Date(hour.hour).toLocaleString()}: ${hour.normal} normal, ${hour.error} with an error`,
  }))

  return (
    <>
      <div className={styles.pageHead}>
        <div className={styles.titles}>
          <div className={styles.titleRow}>
            <h1>{cluster.name}</h1>
            <TagChip
              tag={cluster.tag}
              kind="cluster"
              onClick={handleTagClick && (() => handleTagClick({ kind: 'cluster', id: cluster.id, name: cluster.name, tag: cluster.tag }))}
            />
            <Pill kind={clusterStatusKind(cluster.status)}>{clusterStatusLabel(cluster.status)}</Pill>
          </div>
          <div className={styles.meta}>
            <span>
              {cluster.account_name} ({cluster.account_id})
            </span>
            <span>{cluster.region}</span>
          </div>
        </div>
        <div className={styles.actions}>
          <a className={styles.btn} href={cluster.console_url} target="_blank" rel="noopener">
            Open in AWS console ↗
          </a>
        </div>
      </div>

      <div className={`${styles.grid} ${styles.g4}`}>
        <Tile k="Services" v={services.length} small={`${steadyCount} steady`}>
          <Split parts={serviceSplitParts} />
        </Tile>
        <Tile k="Tasks, 72 h" v={cluster.running} small={`of ${cluster.desired} desired`}>
          <Sparkline values={cluster.tasks_history_72h.map((point) => point.running)} color={TASK_LINE_COLOR} height={24} fluid />
        </Tile>
        <Tile
          k="Reserved by running tasks"
          v={
            <>
              {fmtNum(cluster.reserved_vcpu)}
              <small>vCPU</small>
              {fmtNum(cluster.reserved_gib)}
              <small>GiB</small>
            </>
          }
        >
          <Split
            parts={[
              { n: cluster.fargate_tasks, color: FARGATE_COLOR },
              { n: cluster.spot_tasks, color: SPOT_COLOR },
            ]}
          />
          <div className={styles.tileSub}>
            <span>
              {cluster.fargate_tasks} Fargate · {cluster.spot_tasks} Spot
            </span>
          </div>
        </Tile>
        <UseNowTile metrics={clusterMetrics} insights={cluster.container_insights} />
      </div>

      <div className={`${styles.grid} ${styles.gWide}`}>
        <ClusterUsageChart metrics={clusterMetrics} insights={cluster.container_insights} error={clusterMetricsQuery.error} />
        <Card title="Tasks by service" right="bar = running · line = desired">
          <HBars
            items={services.map((service) => ({
              name: service.name,
              href: `/services/${service.id}/tasks`,
              parts: [{ v: service.running, color: STATUS_COLOR[statusKind(service.status)] }],
              ghost: service.desired,
              label: `${service.running} / ${service.desired}`,
            }))}
          />
          <div className={styles.stopsBlock}>
            <h3 className={styles.stopsTitle}>Task stops per hour</h3>
            <BarChart cols={stopColumns} height={80} xLabels={[[0, '24 h ago'], [23, 'now']]} />
          </div>
        </Card>
      </div>

      <div className={styles.toolbar}>
        <h2>Services</h2>
        <span className={styles.toolbarEnd} />
        <Seg label="View" options={LIST_VIEW_OPTIONS} value={view} onChange={setView} />
      </div>
      {view === 'table' ? (
        <ServicesTable
          services={services}
          clusterName={cluster.name}
          onTagClick={handleTagClick}
          metrics={clusterMetrics?.services}
          insights={cluster.container_insights}
        />
      ) : (
        <div className={styles.cards}>
          {services.map((service) => (
            <ServiceCard
              key={service.id}
              service={service}
              clusterName={cluster.name}
              onTagClick={handleTagClick}
              metrics={clusterMetrics?.services[service.id]}
              insights={cluster.container_insights}
            />
          ))}
        </div>
      )}
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
    </>
  )
}
