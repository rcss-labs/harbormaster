import type { OffHoursService } from '../../../api/offhours'
import { ApiError } from '../../../api/client'
import { useSleepService, useWakeService } from '../../../api/offhours'
import { DataTable, type DataTableColumn } from '../../../components/DataTable'
import { Panel } from '../../../components/Panel'
import { Pill } from '../../../components/Pill'
import { useToast } from '../../../components/Toast'
import { money } from '../../analytics/cost'
import { NOT_PRICED, stateKind, stateLabel, wallClock } from './offhours'
import styles from './offhours.module.css'

interface ScheduleServicesProps {
  services: OffHoursService[]
  /** The schedule's zone: times in the table are read on its clock. */
  timezone: string
  /** The server clock. */
  now: string
}

const errorMessage = (error: unknown) => (error instanceof ApiError ? error.message : 'Something went wrong. Try again.')

function ServiceActions({ service, timezone }: { service: OffHoursService; timezone: string }) {
  const toast = useToast()
  const wake = useWakeService(service.service_id)
  const sleep = useSleepService(service.service_id)

  if (service.state !== 'asleep' && service.state !== 'woken') return null
  if (!service.can_operate) {
    return (
      <span className={`${styles.muted} ${styles.small}`}>
        {`You need Operate on ${service.tag.name} to ${service.state === 'asleep' ? 'wake it' : 'put it back to sleep'}.`}
      </span>
    )
  }
  if (service.state === 'asleep') {
    return (
      <button
        type="button"
        className={`${styles.btn} ${styles.sm}`}
        disabled={wake.isPending}
        onClick={() =>
          wake.mutate(undefined, {
            onSuccess: (response) => toast.show(`Woke ${service.service} until ${wallClock(response.woken_until, timezone).time}.`, 'ok'),
            onError: (error) => toast.show(errorMessage(error), 'bad'),
          })
        }
      >
        Wake for 2 hours
      </button>
    )
  }
  return (
    <button
      type="button"
      className={`${styles.btn} ${styles.sm}`}
      disabled={sleep.isPending}
      onClick={() =>
        sleep.mutate(undefined, {
          onSuccess: () => toast.show(`${service.service} is back to sleep.`, 'ok'),
          onError: (error) => toast.show(errorMessage(error), 'bad'),
        })
      }
    >
      Put back to sleep
    </button>
  )
}

export function ScheduleServices({ services, timezone, now }: ScheduleServicesProps) {
  const clock = wallClock(now, timezone)
  const columns: DataTableColumn<OffHoursService>[] = [
    {
      key: 'service',
      header: 'Service',
      width: 'minmax(140px, 1.2fr)',
      phone: 'title',
      sortValue: (row) => row.service,
      render: (row) => (
        <span>
          <strong className={styles.service}>{row.service}</strong>
          <span className={`${styles.muted} ${styles.small}`} style={{ display: 'block' }}>
            {row.cluster}
          </span>
        </span>
      ),
    },
    {
      key: 'tasks',
      header: 'Tasks',
      width: '80px',
      phoneLabel: 'Tasks',
      render: (row) => <span className={styles.mono}>{`${row.awake_tasks} → ${row.state === 'asleep' ? 0 : row.awake_tasks}`}</span>,
    },
    {
      key: 'status',
      header: 'State',
      width: 'minmax(180px, 1.6fr)',
      render: (row) => (
        <span className={styles.stateCell}>
          <Pill kind={stateKind(row.state)}>{stateLabel(row, now, timezone)}</Pill>
          {row.reason && <span className={styles.reason}>{row.reason}</span>}
        </span>
      ),
    },
    {
      key: 'cost',
      header: 'Cost a month, always on',
      width: '150px',
      align: 'right',
      phoneLabel: 'Always on',
      sortValue: (row) => row.monthly_always_on,
      render: (row) =>
        row.monthly_always_on === null ? (
          <span className={styles.muted} title={NOT_PRICED}>
            —
          </span>
        ) : (
          <span className={styles.cost}>{money(row.monthly_always_on)}</span>
        ),
    },
    {
      key: 'actions',
      header: <span className="hm-sr">Actions</span>,
      width: 'minmax(150px, max-content)',
      align: 'right',
      phone: 'meta',
      render: (row) => <ServiceActions service={row} timezone={timezone} />,
    },
  ]
  return (
    <Panel title="Services on this schedule" flush actions={<span className={styles.small}>{`Now: ${clock.weekday} ${clock.time}`}</span>}>
      <DataTable
        label="Services on this schedule"
        columns={columns}
        rows={services}
        rowKey={(row) => row.service_id}
        minWidth={720}
        phoneSort
        empty={<span className={styles.muted}>No services carry this tag.</span>}
      />
    </Panel>
  )
}
