import { Link } from 'react-router'
import type { ServiceListItemDTO } from '../../../api/types'
import { Ago } from '../../../components/Ago'
import { DataTable, type DataTableColumn, type SortState } from '../../../components/DataTable'
import { Icon } from '../../../components/Icon'
import { PinButton } from '../../../components/PinButton'
import { StatusDot } from '../../../components/StatusDot'
import { TagChip } from '../../../components/TagChip'
import { serviceRowStatus } from '../../clusters/serviceSeverity'
import styles from './services.module.css'
import { restartBlockedReason, sortValue, taskTone, type ServiceSortKey } from './filterServices'

export const SERVICES_PAGE_SIZE = 25

/** Column keys in table order; `name` is always shown. */
export const COLUMN_KEYS = ['name', 'status', 'tag', 'cluster', 'account', 'tasks', 'revision', 'image', 'last'] as const
export const DEFAULT_COLUMNS: string[] = [...COLUMN_KEYS]
export const COLUMN_LABELS: Record<(typeof COLUMN_KEYS)[number], string> = {
  name: 'Service',
  status: 'Status',
  tag: 'Tag',
  cluster: 'Cluster',
  account: 'Account / region',
  tasks: 'Tasks',
  revision: 'Revision',
  image: 'Image tag',
  last: 'Last change',
}

interface ServicesTableProps {
  /** Already filtered and sorted. */
  services: ServiceListItemDTO[]
  visibleColumns: string[]
  sort: SortState
  onSortChange: (sort: SortState) => void
  page: number
  onPageChange: (page: number) => void
  isPinned: (id: string) => boolean
  onTogglePin: (service: ServiceListItemDTO) => void
  onDetails: (service: ServiceListItemDTO) => void
  onRestart: (service: ServiceListItemDTO) => void
  empty: React.ReactNode
}

export function ServicesTable({
  services,
  visibleColumns,
  sort,
  onSortChange,
  page,
  onPageChange,
  isPinned,
  onTogglePin,
  onDetails,
  onRestart,
  empty,
}: ServicesTableProps) {
  const sortBy = (key: ServiceSortKey) => (service: ServiceListItemDTO) => sortValue(service, key)
  const columns: DataTableColumn<ServiceListItemDTO>[] = [
    {
      key: 'name',
      header: COLUMN_LABELS.name,
      width: 'minmax(180px,1.6fr)',
      sortValue: sortBy('name'),
      render: (service) => (
        <span className={styles.nameCell}>
          <Link to={`/services/${service.id}/overview`} className={styles.nameLink}>
            {service.name}
          </Link>
          <span className={styles.why}>{service.attention || service.cluster.name}</span>
        </span>
      ),
    },
    { key: 'status', header: COLUMN_LABELS.status, width: '130px', sortValue: sortBy('status'), render: (service) => <StatusDot status={service.status} /> },
    {
      key: 'tag',
      header: COLUMN_LABELS.tag,
      width: '96px',
      sortValue: sortBy('tag'),
      render: (service) => <TagChip tag={{ ...service.tag, inherited: service.tag_inherited }} inheritedFrom={service.tag_inherited ? service.cluster.name : undefined} />,
    },
    {
      key: 'cluster',
      header: COLUMN_LABELS.cluster,
      width: 'minmax(110px,1fr)',
      sortValue: sortBy('cluster'),
      render: (service) => (
        <Link to={`/clusters/${service.cluster.id}`} className={`${styles.mono} ${styles.link}`}>
          {service.cluster.name}
        </Link>
      ),
    },
    {
      key: 'account',
      header: COLUMN_LABELS.account,
      width: 'minmax(120px,1fr)',
      sortValue: sortBy('account'),
      render: (service) => (
        <span className={styles.two}>
          {service.account_name}
          <small>{service.region}</small>
        </span>
      ),
    },
    {
      key: 'tasks',
      header: COLUMN_LABELS.tasks,
      width: '80px',
      align: 'right',
      sortValue: sortBy('tasks'),
      render: (service) => (
        <span className={`${styles.mono} ${taskTone(service) === 'bad' ? styles.bad : ''}`.trim()}>
          {service.running}/{service.desired}
        </span>
      ),
    },
    {
      key: 'revision',
      header: COLUMN_LABELS.revision,
      width: '90px',
      align: 'right',
      sortValue: sortBy('revision'),
      render: (service) => <span className={styles.mono}>:{service.revision}</span>,
    },
    { key: 'image', header: COLUMN_LABELS.image, width: 'minmax(100px,1fr)', sortValue: sortBy('image'), render: (service) => <span className={styles.mono}>{service.image_tag}</span> },
    {
      key: 'last',
      header: COLUMN_LABELS.last,
      width: '110px',
      sortValue: sortBy('last'),
      render: (service) => <span className={styles.nowrap}>{service.last_change_at ? <Ago ts={service.last_change_at} /> : '–'}</span>,
    },
  ]

  return (
    <DataTable
      label="Services"
      columns={columns}
      rows={services}
      rowKey={(service) => service.id}
      rowStatus={(service) => serviceRowStatus(service.status)}
      visibleColumns={visibleColumns}
      minWidth={760}
      sort={sort}
      onSortChange={onSortChange}
      pageSize={SERVICES_PAGE_SIZE}
      page={page}
      onPageChange={onPageChange}
      stickyActions
      empty={empty}
      rowAction={(service) => {
        const blocked = restartBlockedReason(service)
        return (
          <span className={styles.actions}>
            <PinButton name={service.name} pinned={isPinned(service.id)} onToggle={() => onTogglePin(service)} />
            <button type="button" className={styles.btn} aria-label={`Details for ${service.name}`} onClick={() => onDetails(service)}>
              <Icon name="panel" />
              Details
            </button>
            <Link className={styles.btn} to={`/services/${service.id}/logs`}>
              Logs
            </Link>
            <Link className={styles.btn} to={`/services/${service.id}/tasks`}>
              Tasks
            </Link>
            <button
              type="button"
              className={styles.btn}
              aria-label={`Restart ${service.name}`}
              title={blocked ?? `Restart ${service.name}`}
              disabled={blocked !== undefined}
              onClick={() => onRestart(service)}
            >
              Restart
            </button>
          </span>
        )
      }}
    />
  )
}
