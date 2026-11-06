import { Link } from 'react-router'
import type { ServiceListItemDTO, ServicesMetricsDTO } from '../../../api/types'
import { Ago } from '../../../components/Ago'
import { DataTable, type DataTableColumn, type SortState } from '../../../components/DataTable'
import { Icon } from '../../../components/Icon'
import { PinButton } from '../../../components/PinButton'
import { usePhone } from '../../../components/usePhone'
import { StatusDot } from '../../../components/StatusDot'
import { TagChip } from '../../../components/TagChip'
import { serviceRowStatus } from '../../clusters/serviceSeverity'
import { ServiceActionSheet } from './ServiceActionSheet'
import { metricFigure, type MetricKind } from './serviceMetrics'
import styles from './services.module.css'
import { restartBlockedReason, sortValue, taskTone, type ServiceSortKey } from './filterServices'

export const SERVICES_PAGE_SIZE = 25

/** Column keys in table order; `name` is always shown. */
export const COLUMN_KEYS = ['name', 'status', 'tag', 'cluster', 'account', 'tasks', 'revision', 'image', 'cpu_pct', 'mem_pct', 'last'] as const
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
  cpu_pct: 'CPU',
  mem_pct: 'Memory',
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
  /** Latest CPU and Memory of the rows on this page; undefined until the first answer. */
  metrics: ServicesMetricsDTO | undefined
  /** The batch request failed. */
  metricsFailed: boolean
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
  metrics,
  metricsFailed,
  empty,
}: ServicesTableProps) {
  const phone = usePhone()
  const sortBy = (key: ServiceSortKey) => (service: ServiceListItemDTO) => sortValue(service, key)
  // CPU and Memory are measured for the rows of this page only, so they are not sortable.
  const metricColumn = (key: 'cpu_pct' | 'mem_pct', kind: MetricKind, phoneLabel: string): DataTableColumn<ServiceListItemDTO> => ({
    key,
    header: COLUMN_LABELS[key],
    width: '90px',
    align: 'right',
    phoneLabel: false,
    render: (service) => {
      const figure = metricFigure(service, metrics, metricsFailed, kind)
      return (
        <span className={`${styles.mono} ${figure.tone === 'warn' ? styles.warnText : ''}`.trim()} title={figure.title}>
          {phone ? phoneLabel : ''}
          {figure.text}
          {phone && figure.short ? ` ${figure.short}` : ''}
        </span>
      )
    },
  })
  const columns: DataTableColumn<ServiceListItemDTO>[] = [
    {
      key: 'name',
      header: COLUMN_LABELS.name,
      width: 'minmax(180px,1.6fr)',
      sortValue: sortBy('name'),
      render: (service) =>
        phone ? (
          <Link to={`/services/${service.id}/overview`} className={styles.nameLink} data-hm-rl="">
            {service.name}
          </Link>
        ) : (
          <span className={styles.nameCell}>
            <Link to={`/services/${service.id}/overview`} className={styles.nameLink}>
              {service.name}
            </Link>
            <span className={styles.why}>{service.attention || service.cluster.name}</span>
          </span>
        ),
    },
    // Phone only: the open alert gets its own line under the name, as the desktop name cell does.
    ...(phone
      ? [
          {
            key: 'why',
            header: 'Attention',
            phone: 'meta' as const,
            phoneLabel: false as const,
            render: (service: ServiceListItemDTO) => (service.attention ? <span className={styles.phoneWhy}>{service.attention}</span> : null),
          },
        ]
      : []),
    { key: 'status', header: COLUMN_LABELS.status, width: '130px', sortValue: sortBy('status'), render: (service) => <StatusDot status={service.status} /> },
    {
      key: 'tag',
      header: COLUMN_LABELS.tag,
      width: '96px',
      sortValue: sortBy('tag'),
      phone: 'status',
      render: (service) => <TagChip tag={{ ...service.tag, inherited: service.tag_inherited }} inheritedFrom={service.tag_inherited ? service.cluster.name : undefined} />,
    },
    {
      key: 'cluster',
      header: COLUMN_LABELS.cluster,
      width: 'minmax(110px,1fr)',
      sortValue: sortBy('cluster'),
      phoneLabel: false,
      render: (service) =>
        phone ? (
          <span className={styles.mono}>{service.cluster.name}</span>
        ) : (
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
      phoneLabel: false,
      render: (service) =>
        phone ? (
          <span>
            {service.account_name} · {service.region}
          </span>
        ) : (
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
      phoneLabel: false,
      render: (service) => (
        <span className={`${styles.mono} ${taskTone(service) === 'bad' ? styles.bad : ''}`.trim()}>
          {service.running}/{service.desired}
          {phone ? ' tasks' : ''}
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
    metricColumn('cpu_pct', 'cpu', 'CPU '),
    metricColumn('mem_pct', 'mem', 'Mem '),
    {
      key: 'last',
      header: COLUMN_LABELS.last,
      width: '110px',
      sortValue: sortBy('last'),
      render: (service) => <span className={styles.nowrap}>{service.last_change_at ? <Ago ts={service.last_change_at} /> : '–'}</span>,
    },
  ]

  // Phones drop the tooltips, so the reason behind a dash is written under the list.
  const pageRows = services.slice((page - 1) * SERVICES_PAGE_SIZE, page * SERVICES_PAGE_SIZE)
  const dashReasons = phone ? dashReasonsOf(pageRows, visibleColumns, metrics, metricsFailed) : []
  return (
    <>
    <DataTable
      label="Services"
      columns={columns}
      rows={services}
      rowKey={(service) => service.id}
      rowStatus={(service) => serviceRowStatus(service.status)}
      visibleColumns={phone ? [...visibleColumns, 'status', 'why'] : visibleColumns}
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
        const pinned = isPinned(service.id)
        if (phone) {
          return (
            <ServiceActionSheet
              service={service}
              pinned={pinned}
              onTogglePin={() => onTogglePin(service)}
              onDetails={() => onDetails(service)}
              onRestart={() => onRestart(service)}
            />
          )
        }
        return (
          <span className={styles.actions}>
            <PinButton name={service.name} pinned={pinned} onToggle={() => onTogglePin(service)} />
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
    {dashReasons.map((reason) => (
      <p key={reason} className={styles.dashNote}>{`– ${reason}.`}</p>
    ))}
    </>
  )
}

/** The distinct reasons behind the CPU and Memory dashes of the rows on screen, so a phone can say why. */
function dashReasonsOf(rows: ServiceListItemDTO[], visibleColumns: string[], metrics: ServicesMetricsDTO | undefined, failed: boolean): string[] {
  const kinds: [string, MetricKind][] = [['cpu_pct', 'cpu'], ['mem_pct', 'mem']]
  const reasons = new Set<string>()
  for (const [column, kind] of kinds) {
    if (!visibleColumns.includes(column)) continue
    for (const row of rows) {
      const { title } = metricFigure(row, metrics, failed, kind)
      if (title) reasons.add(title.replace(/\.$/, ""))
    }
  }
  return [...reasons]
}
