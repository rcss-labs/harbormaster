import { Link } from 'react-router'
import type { ClusterServiceDTO } from '../../api/types'
import { Sparkline } from '../../charts/Sparkline'
import { Ago } from '../../components/Ago'
import { DataTable, type DataTableColumn } from '../../components/DataTable'
import { StatusDot } from '../../components/StatusDot'
import { TagChip } from '../../components/TagChip'
import { STATUS_COLOR, statusKind } from '../../components/status'
import styles from './clusters.module.css'
import { serviceRowStatus, serviceSeverity } from './serviceSeverity'
import { type ClusterServiceMetrics, percentText, presentValues } from './ServiceCardMetrics'
import type { TagClickHandler } from './tagTarget'

export const SERVICES_PAGE_SIZE = 25

interface ServicesTableProps {
  services: ClusterServiceDTO[]
  clusterName: string
  /** Given only for admins; makes the tag chip open the tag dialog. */
  onTagClick?: TagClickHandler
  /** Per-service cluster metrics keyed by service id; undefined while loading. */
  metrics?: Record<string, ClusterServiceMetrics>
  insights?: boolean
}

/** Services of one cluster, problems first, 25 to a page. */
export function ServicesTable({ services, clusterName, onTagClick, metrics, insights = false }: ServicesTableProps) {
  const columns: DataTableColumn<ClusterServiceDTO>[] = [
    {
      key: 'name',
      header: 'Service',
      width: 'minmax(0,1.3fr)',
      sortValue: (service) => service.name,
      render: (service) => (
        <Link to={`/services/${service.id}/overview`} className={styles.clusterLink}>
          {service.name}
        </Link>
      ),
    },
    {
      key: 'tag',
      header: 'Label',
      width: '96px',
      render: (service) => (
        <TagChip
          tag={service.tag}
          kind="service"
          inheritedFrom={service.tag.inherited ? clusterName : undefined}
          onClick={onTagClick && (() => onTagClick({ kind: 'service', id: service.id, name: service.name, tag: service.tag }))}
        />
      ),
    },
    {
      key: 'status',
      header: 'Status',
      width: '130px',
      sortValue: (service) => serviceSeverity(service.status) * 1000 + (service.desired - service.running),
      render: (service) => <StatusDot status={service.status} />,
    },
    {
      key: 'revision',
      header: 'Task definition',
      width: 'minmax(0,1fr)',
      render: (service) => (
        <span className={styles.mono}>
          {service.family}:{service.revision}
        </span>
      ),
    },
    { key: 'image', header: 'Image tag', width: '110px', render: (service) => <span className={styles.mono}>{service.image_tag}</span> },
    {
      key: 'tasks',
      header: 'Tasks',
      width: '120px',
      sortValue: (service) => service.desired,
      render: (service) => {
        const share = service.desired > 0 ? Math.min(100, (service.running / service.desired) * 100) : 0
        return (
          <span className={styles.tasks}>
            <span className={styles.mono}>
              {service.running} / {service.desired}
            </span>
            <span className={styles.track} aria-hidden="true">
              <span style={{ width: `${share}%`, background: STATUS_COLOR[statusKind(service.status)] }} />
            </span>
          </span>
        )
      },
    },
    {
      key: 'cpu',
      header: 'CPU, 24 h',
      width: '110px',
      render: (service) => {
        const history = insights && metrics?.[service.id] ? presentValues(metrics[service.id].cpu) : []
        return history.length > 0 ? <Sparkline values={history} height={24} /> : <span className={styles.dim}>–</span>
      },
    },
    {
      key: 'mem',
      header: 'Memory',
      width: '70px',
      align: 'right',
      render: (service) => (
        <span className={styles.mono}>{insights && metrics?.[service.id] ? percentText(metrics[service.id].mem_now) : '–'}</span>
      ),
    },
    {
      key: 'change',
      header: 'Last change',
      width: '92px',
      render: (service) => <span className={styles.muted}>{service.last_change_at ? <Ago ts={service.last_change_at} /> : '–'}</span>,
    },
  ]

  return (
    <DataTable
      label="Services"
      columns={columns}
      rows={services}
      rowKey={(service) => service.id}
      minWidth={960}
      rowStatus={(service) => serviceRowStatus(service.status)}
      defaultSort={{ key: 'status', dir: 'desc' }}
      pageSize={SERVICES_PAGE_SIZE}
    />
  )
}
