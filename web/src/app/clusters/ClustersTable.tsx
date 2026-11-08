import { useState, type ReactNode } from 'react'
import { Link } from 'react-router'
import type { ClusterCardDTO } from '../../api/types'
import { fmtNum } from '../../charts/format'
import { Sparkline } from '../../charts/Sparkline'
import { Ago } from '../../components/Ago'
import type { ColumnPickerColumn } from '../../components/ColumnPicker'
import { DataTable, type DataTableColumn, type RowStatus, type SortState } from '../../components/DataTable'
import { StatusDot } from '../../components/StatusDot'
import { TagChip } from '../../components/TagChip'
import { STATUS_COLOR, clusterStatusKind, statusKind } from '../../components/status'
import { usePhone } from '../../components/usePhone'
import { EditTagDialog } from '../admin/tags/EditTagDialog'
import { type ClusterMetricsItem, formatPercent, presentPoints } from './clusterMetrics'
import { ClusterRowSheet, RowMoreButton } from './ClusterSheets'
import styles from './clusters.module.css'
import { clusterSeverity } from './filterClusters'
import { serviceSeverity } from './serviceSeverity'

export const CLUSTERS_PAGE_SIZE = 25

/** Every column of the clusters table for the column picker; the cluster name always shows. */
export const CLUSTER_COLUMN_OPTIONS: ColumnPickerColumn[] = [
  { key: 'name', label: 'Cluster', locked: true },
  { key: 'env', label: 'Env' },
  { key: 'status', label: 'Status' },
  { key: 'services', label: 'Services' },
  { key: 'tasks', label: 'Tasks' },
  { key: 'cpu', label: 'CPU 24h' },
  { key: 'mem', label: 'Memory 24h' },
  { key: 'vcpu', label: 'vCPU' },
  { key: 'spot', label: 'Spot' },
  { key: 'change', label: 'Last change' },
]
const SERVICE_PREVIEW = 3
const INSIGHTS_OFF = 'Container Insights is off'
/** Phone only: the account and region line that sits under the cluster name. */
const WHERE_KEY = 'where'

interface ClusterRow {
  cluster: ClusterCardDTO
  metrics?: ClusterMetricsItem
}

const ROW_STATUS: Partial<Record<ClusterCardDTO['status'], RowStatus>> = { needs_attention: 'bad', degraded: 'warn', deploying: 'info' }

/** The first services, problems first. */
function ServicePreview({ cluster }: { cluster: ClusterCardDTO }) {
  const ordered = [...cluster.services].sort((a, b) => serviceSeverity(b.status) - serviceSeverity(a.status))
  const rest = ordered.length - SERVICE_PREVIEW
  return (
    <span className={styles.svcPreview}>
      {ordered.slice(0, SERVICE_PREVIEW).map((service) => (
        <Link key={service.id} to={`/services/${service.id}/overview`} className={styles.svcLink}>
          <i className={styles.svcDot} style={{ background: STATUS_COLOR[statusKind(service.status)] }} aria-hidden="true" />
          {service.name}
        </Link>
      ))}
      {rest > 0 && <span className={styles.svcMore}>{`+${rest}`}</span>}
    </span>
  )
}

function UsageCell({ cluster, points, now, color }: { cluster: ClusterCardDTO; points: number[]; now: number | null | undefined; color: string }) {
  if (!cluster.container_insights) {
    return (
      <span className={styles.dim} title={INSIGHTS_OFF}>
        –
      </span>
    )
  }
  return (
    <span className={styles.usage}>
      {points.length > 0 ? <Sparkline values={points} color={color} height={18} fluid /> : <span className={styles.usageGap} />}
      <span className={styles.usageNow}>{formatPercent(now)}</span>
    </span>
  )
}

/** Phone line two for CPU or memory: "CPU 41%", the same dash as the desktop cell when Container Insights is off. */
function PhoneUsage({ cluster, label, now }: { cluster: ClusterCardDTO; label: string; now: number | null | undefined }) {
  if (!cluster.container_insights) {
    return (
      <span className={styles.dim} title={INSIGHTS_OFF}>
        –
      </span>
    )
  }
  return <span className={styles.mono}>{`${label} ${formatPercent(now)}`}</span>
}

function columns(phone: boolean): DataTableColumn<ClusterRow>[] {
  const nameColumn: DataTableColumn<ClusterRow> = {
    key: 'name',
    header: 'Cluster',
    width: 'minmax(0,1.3fr)',
    sortValue: (r) => r.cluster.name,
    render: ({ cluster }): ReactNode =>
      phone ? (
        <Link to={`/clusters/${cluster.id}`} className={styles.clusterLink} data-hm-rl="">
          {cluster.name}
        </Link>
      ) : (
        <span className={styles.nameCell}>
          <Link to={`/clusters/${cluster.id}`} className={styles.clusterLink}>
            {cluster.name}
          </Link>
          <span className={styles.where}>
            {cluster.account_name} · {cluster.region}
          </span>
        </span>
      ),
  }
  const whereColumn: DataTableColumn<ClusterRow> = {
    key: WHERE_KEY,
    header: 'Account and region',
    phoneLabel: false,
    phone: 'meta',
    render: ({ cluster }) => (
      <span className={styles.whereLine}>
        {cluster.account_name} · {cluster.region}
      </span>
    ),
  }
  return [
    nameColumn,
    ...(phone ? [whereColumn] : []),
    { key: 'env', header: 'Env', width: '84px', phone: 'status', render: ({ cluster }) => <TagChip tag={cluster.tag} kind="cluster" /> },
    {
      key: 'status',
      header: 'Status',
      width: '120px',
      sortValue: (r) => clusterSeverity(r.cluster.status) * 1000 + (r.cluster.desired - r.cluster.running),
      render: ({ cluster }) => <StatusDot clusterStatus={cluster.status} />,
    },
    {
      key: 'services',
      header: 'Services',
      width: 'minmax(0,1.6fr)',
      phone: 'hidden',
      sortValue: (r) => r.cluster.services.length,
      render: ({ cluster }) => <ServicePreview cluster={cluster} />,
    },
    {
      key: 'tasks',
      header: 'Tasks',
      width: '110px',
      sortValue: (r) => r.cluster.desired,
      render: ({ cluster }) => {
        const share = cluster.desired > 0 ? Math.min(100, (cluster.running / cluster.desired) * 100) : 0
        return (
          <span className={styles.tasks}>
            <span className={styles.mono}>
              {cluster.running}/{cluster.desired}
            </span>
            <span className={styles.track} aria-hidden="true">
              <span style={{ width: `${share}%`, background: STATUS_COLOR[clusterStatusKind(cluster.status)] }} />
            </span>
          </span>
        )
      },
    },
    {
      key: 'cpu',
      header: 'CPU 24h',
      phoneLabel: false,
      width: 'minmax(0,1fr)',
      sortValue: (r) => (r.cluster.container_insights ? (r.metrics?.cpu_now ?? null) : null),
      render: ({ cluster, metrics }) =>
        phone ? (
          <PhoneUsage cluster={cluster} label="CPU" now={metrics?.cpu_now} />
        ) : (
          <UsageCell cluster={cluster} points={presentPoints(metrics?.cpu ?? [])} now={metrics?.cpu_now} color="var(--series-1)" />
        ),
    },
    {
      key: 'mem',
      header: 'Memory 24h',
      phoneLabel: false,
      width: 'minmax(0,1fr)',
      sortValue: (r) => (r.cluster.container_insights ? (r.metrics?.mem_now ?? null) : null),
      render: ({ cluster, metrics }) =>
        phone ? (
          <PhoneUsage cluster={cluster} label="Mem" now={metrics?.mem_now} />
        ) : (
          <UsageCell cluster={cluster} points={presentPoints(metrics?.mem ?? [])} now={metrics?.mem_now} color="var(--series-2)" />
        ),
    },
    { key: 'vcpu', header: 'vCPU', width: '56px', align: 'right', render: ({ cluster }) => <span className={styles.mono}>{fmtNum(cluster.reserved_vcpu)}</span> },
    { key: 'spot', header: 'Spot', width: '52px', align: 'right', render: ({ cluster }) => <span className={styles.mono}>{cluster.spot_tasks}</span> },
    {
      key: 'change',
      header: 'Last change',
      width: '92px',
      render: ({ cluster }) => <span className={styles.muted}>{cluster.last_change_at ? <Ago ts={cluster.last_change_at} /> : '–'}</span>,
    },
  ]
}

interface ClustersTableProps {
  clusters: ClusterCardDTO[]
  metricsById: Record<string, ClusterMetricsItem>
  sort: SortState
  onSortChange: (sort: SortState) => void
  page: number
  onPageChange: (page: number) => void
  empty: ReactNode
  /** Keys of the visible columns, from the column picker (on phones, from the sort and fields sheet). */
  visibleColumns: string[]
}

export function ClustersTable({ clusters, metricsById, sort, onSortChange, page, onPageChange, empty, visibleColumns }: ClustersTableProps) {
  const phone = usePhone()
  const [actingOn, setActingOn] = useState<ClusterCardDTO | null>(null)
  const [editing, setEditing] = useState<ClusterCardDTO | null>(null)
  // Name order first, so rows that tie on the sort key stay alphabetical.
  const rows = [...clusters].sort((a, b) => a.name.localeCompare(b.name)).map((cluster) => ({ cluster, metrics: metricsById[cluster.id] }))
  return (
    <div className={styles.tablePanel}>
      <DataTable
        label="Clusters"
        columns={columns(phone)}
        visibleColumns={phone ? [...visibleColumns, WHERE_KEY] : visibleColumns}
        rows={rows}
        rowKey={(row) => row.cluster.id}
        minWidth={1120}
        rowStatus={(row) => ROW_STATUS[row.cluster.status]}
        rowAction={phone ? (row) => <RowMoreButton name={row.cluster.name} onClick={() => setActingOn(row.cluster)} /> : undefined}
        sort={sort}
        onSortChange={onSortChange}
        pageSize={CLUSTERS_PAGE_SIZE}
        page={page}
        onPageChange={onPageChange}
        empty={empty}
      />
      {phone && (
        <ClusterRowSheet
          cluster={actingOn}
          onClose={() => setActingOn(null)}
          onEditTag={(cluster) => {
            setActingOn(null)
            setEditing(cluster)
          }}
        />
      )}
      {editing && <EditTagDialog kind="cluster" id={editing.id} name={editing.name} current={editing.tag.id} onClose={() => setEditing(null)} />}
    </div>
  )
}
