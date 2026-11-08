import type { ReactNode } from 'react'
import { Link } from 'react-router'
import type { TaskDTO } from '../../api/types'
import { Ago } from '../../components/Ago'
import { DataTable, type DataTableColumn } from '../../components/DataTable'
import {
  ACTIONS_WIDTH,
  HealthCell,
  IpCell,
  ReasonCell,
  ServiceCell,
  TaskActions,
  TaskIdButton,
  TaskStatusCell,
  cpuText,
  memoryText,
  taskStripe,
} from '../../components/TaskTable'
import rowStyles from '../../components/TaskTable.module.css'
import type { TaskTableRow } from './taskRows'
import styles from './TasksTable.module.css'

export const PAGE_SIZE = 50

/** One-line summary of a group: how many run where, or how long ago the first and last of the repeats stopped. */
function GroupSpread({ tasks }: { tasks: TaskDTO[] }) {
  if (tasks[0].status !== 'STOPPED') {
    const zones = new Set(tasks.map((task) => task.az)).size
    return <span className={styles.dim}>{`${tasks.length} running · ${zones} zone${zones === 1 ? '' : 's'}`}</span>
  }
  const times = tasks.flatMap((task) => (task.stopped_at ? [Date.parse(task.stopped_at)] : []))
  if (times.length === 0) return null
  return (
    <span className={styles.dim}>
      latest <Ago ts={Math.max(...times)} /> · oldest <Ago ts={Math.min(...times)} />
    </span>
  )
}

interface TasksTableProps {
  rows: TaskTableRow[]
  page: number
  onPageChange: (page: number) => void
  onToggleGroup: (groupKey: string) => void
  onOpen: (task: TaskDTO) => void
  onStop: (task: TaskDTO) => void
  /** Keys of the columns to show (see TASK_COLUMNS); the actions column is always shown and pinned. */
  visibleColumns: string[]
  empty: ReactNode
}

/** The redesigned Tasks list: 50 rows per page, repeat groups as expandable rows, actions pinned at the right. */
export function TasksTable({ rows, page, onPageChange, onToggleGroup, onOpen, onStop, visibleColumns, empty }: TasksTableProps) {
  const lead = (row: TaskTableRow) => (row.kind === 'group' ? row.tasks[0] : row.task)
  const columns: DataTableColumn<TaskTableRow>[] = [
    {
      key: 'task',
      header: 'Task',
      width: '120px',
      render: (row) =>
        row.kind === 'group' ? (
          <button type="button" className={styles.groupToggle} aria-expanded={row.open} onClick={() => onToggleGroup(row.groupKey)}>
            <span className={styles.chevron} aria-hidden="true">
              {row.open ? '▾' : '▸'}
            </span>
            {`×${row.tasks.length} tasks`}
          </button>
        ) : (
          <TaskIdButton task={row.task} nested={row.nested} onOpen={onOpen} />
        ),
    },
    { key: 'service', header: 'Service', width: 'minmax(0,1.1fr)', render: (row) => <ServiceCell task={lead(row)} /> },
    { key: 'status', header: 'Status', width: '230px', render: (row) => <TaskStatusCell task={lead(row)} /> },
    { key: 'health', header: 'Health', width: '84px', render: (row) => <HealthCell task={lead(row)} /> },
    {
      key: 'revision',
      header: 'Rev',
      width: '56px',
      render: (row) => <span className={styles.mono}>:{lead(row).revision}</span>,
    },
    { key: 'ip', header: 'IP · Subnet', width: 'minmax(150px,1fr)', render: (row) => (row.kind === 'group' ? null : <IpCell task={row.task} />) },
    {
      key: 'capacity',
      header: 'Capacity',
      width: '116px',
      render: (row) => (row.kind === 'group' ? null : <span className={styles.small}>{row.task.capacity}</span>),
    },
    {
      key: 'cpu',
      header: 'CPU',
      width: '100px',
      align: 'right',
      render: (row) => (row.kind === 'group' ? null : <span className={styles.mono}>{cpuText(row.task)}</span>),
    },
    {
      key: 'memory',
      header: 'Memory',
      width: '100px',
      align: 'right',
      render: (row) => (row.kind === 'group' ? null : <span className={styles.mono}>{memoryText(row.task)}</span>),
    },
    {
      key: 'started',
      header: 'Started',
      width: '84px',
      render: (row) => {
        if (row.kind === 'group') return null
        return row.task.started_at ? <Ago ts={row.task.started_at} /> : <span className={styles.dim}>–</span>
      },
    },
    {
      key: 'reason',
      header: 'Stopped reason',
      width: 'minmax(0,2fr)',
      render: (row) => {
        if (row.kind === 'group') {
          const reason = row.tasks[0].stopped_reason
          return (
            <span className={styles.reason} title={reason}>
              {reason} <GroupSpread tasks={row.tasks} />
            </span>
          )
        }
        return <ReasonCell task={row.task} />
      },
    },
    {
      key: 'actions',
      header: '',
      width: ACTIONS_WIDTH,
      align: 'right',
      render: (row) =>
        row.kind === 'group' ? (
          row.tasks[0].service ? (
            <span className={styles.actions}>
              <Link to={`/services/${row.tasks[0].service.id}/logs`} className={styles.btn}>
                Logs
              </Link>
            </span>
          ) : null
        ) : (
          <TaskActions task={row.task} withLogs onStop={onStop} onOpen={onOpen} />
        ),
    },
  ]

  return (
    <div className={rowStyles.rows}>
      <DataTable
        label="Tasks"
        columns={columns}
        rows={rows}
        rowKey={(row) => row.rowKey}
        rowStatus={(row) => taskStripe(lead(row))}
        visibleColumns={[...visibleColumns, 'actions']}
        stickyActions
        minWidth={1500}
        pageSize={PAGE_SIZE}
        page={page}
        onPageChange={onPageChange}
        empty={empty}
      />
    </div>
  )
}
