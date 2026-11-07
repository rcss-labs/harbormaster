import { useId } from 'react'
import type { CostRightsizing, CostService, CostServiceStatus } from '../../api/analytics'
import { DataTable, type DataTableColumn } from '../../components/DataTable'
import { EmptyState } from '../../components/EmptyState'
import { Pill } from '../../components/Pill'
import { AnalyticsPanel } from './AnalyticsPanel'
import { BarCell, ServiceLink } from './cells'
import { money, stepAt, type SuggestedService } from './cost'
import { COLOR } from './format'
import styles from './analytics.module.css'

const NO_SUGGESTION_REASON: Record<Exclude<CostServiceStatus, 'ok' | 'rollup_off' | 'short_history'>, string> = {
  not_fargate: 'Not on Fargate, so not priced',
  mixed_capacity: 'Some tasks are not on Fargate, so only Fargate tasks are priced',
  no_price: 'No price for this region',
  not_running: 'No running tasks',
  no_size: 'No task size in the task definition',
  no_data: 'No utilisation data. Is Container Insights on?',
  stale: 'Utilisation data is out of date',
}

function reasonFor(service: CostService, minHistoryDays: number): string {
  if (service.status === 'short_history') return `${service.history_days} of ${minHistoryDays} days of data`
  if (service.status === 'ok' || service.status === 'rollup_off') return ''
  return NO_SUGGESTION_REASON[service.status]
}

/** Shares above 1 (after a downsize) read as full. */
const shareBar = (share: number | null) => {
  if (share === null) return <span className={styles.dimMono}>—</span>
  const pct = Math.min(1, share) * 100
  return <BarCell pct={pct} label={`${Math.round(pct)}%`} color={COLOR.accent} />
}

interface RightsizingPanelProps {
  rightsizing: CostRightsizing
  headroomPct: number
  onHeadroomChange: (headroomPct: number) => void
  /** Service ids the user unticked; everything else is selected. */
  excluded: ReadonlySet<string>
  onToggle: (serviceId: string) => void
  onDraft: (rows: SuggestedService[]) => void
}

export function RightsizingPanel({ rightsizing, headroomPct, onHeadroomChange, excluded, onToggle, onDraft }: RightsizingPanelProps) {
  const sliderId = useId()
  const slider = (
    <div className={styles.headroom}>
      <label htmlFor={sliderId}>Headroom above p95</label>
      <input id={sliderId} type="range" min={10} max={100} step={5} value={headroomPct} onChange={(event) => onHeadroomChange(Number(event.target.value))} />
      <b>{`${headroomPct}%`}</b>
    </div>
  )
  const sub = 'highest daily p95, last 90 days'

  if (rightsizing.state === 'rollup_off') {
    return (
      <AnalyticsPanel title="Rightsizing" sub={sub} showSources={false}>
        <EmptyState>The daily utilisation rollup is off, so there are no suggestions. Set HM_ANALYTICS_UTIL=true to collect it.</EmptyState>
      </AnalyticsPanel>
    )
  }
  if (rightsizing.state === 'retention_short') {
    return (
      <AnalyticsPanel title="Rightsizing" sub={sub} showSources={false}>
        <EmptyState>{`Daily utilisation is kept for less than the ${rightsizing.min_history_days} days a suggestion needs. Raise HM_RETENTION_UTIL_DAILY to at least ${rightsizing.min_history_days} days.`}</EmptyState>
      </AnalyticsPanel>
    )
  }

  const suggested: SuggestedService[] = []
  for (const service of rightsizing.services) {
    if (service.status !== 'ok') continue
    const suggestion = stepAt(service.steps, headroomPct)
    if (suggestion !== null) suggested.push({ service, suggestion })
  }
  const unsuggested = rightsizing.services.filter((service) => service.status !== 'ok')
  const isSelected = ({ service }: SuggestedService) => service.can_edit && !excluded.has(service.service_id)
  const selected = suggested.filter(isSelected)
  const saving = selected.reduce((total, row) => total + row.suggestion.saving_monthly, 0)
  const upperBound = selected.some((row) => row.service.spot_tasks > 0)

  const columns: DataTableColumn<SuggestedService>[] = [
    {
      key: 'select',
      header: <span className="hm-sr">Select</span>,
      width: '36px',
      phone: 'action',
      render: ({ service }) => (
        <input
          type="checkbox"
          aria-label={`Select ${service.service}`}
          checked={service.can_edit && !excluded.has(service.service_id)}
          disabled={!service.can_edit}
          title={service.can_edit ? undefined : `You need Edit on ${service.tag.name}`}
          onChange={() => onToggle(service.service_id)}
        />
      ),
    },
    {
      key: 'service',
      header: 'Service',
      width: 'minmax(180px, 1.6fr)',
      sortValue: ({ service }) => service.service,
      render: ({ service }) => (
        <>
          <ServiceLink id={service.service_id} name={service.service} cluster={service.cluster} />
          <span className={styles.dimMono}>{`${service.tasks} ${service.tasks === 1 ? 'task' : 'tasks'}`}</span>
          <span className={styles.pills}>
            {service.auto_scaled && (
              <Pill kind="info" title="A smaller task may need more tasks, so the saving is an estimate.">
                Auto-scaled
              </Pill>
            )}
            {service.spot_tasks > 0 && <Pill kind="mute">Spot</Pill>}
          </span>
        </>
      ),
    },
    { key: 'now', header: 'Now', width: '120px', render: ({ service }) => <span className={styles.mono}>{`${service.cpu_units} / ${service.memory_mib}`}</span> },
    { key: 'cpu', header: 'p95 CPU', width: '120px', sortValue: ({ service }) => service.cpu_used_share, render: ({ service }) => shareBar(service.cpu_used_share) },
    { key: 'mem', header: 'p95 memory', width: '120px', sortValue: ({ service }) => service.mem_used_share, render: ({ service }) => shareBar(service.mem_used_share) },
    {
      key: 'suggested',
      header: 'Suggested',
      width: '150px',
      render: ({ suggestion }) => (
        <>
          <span className={styles.mono}>{`${suggestion.cpu_units} / ${suggestion.memory_mib}`}</span>
          {suggestion.limited_by_containers && <Pill kind="warn">Limited by containers</Pill>}
        </>
      ),
    },
    {
      key: 'saves',
      header: 'Saves / mo',
      width: '110px',
      align: 'right',
      sortValue: ({ suggestion }) => suggestion.saving_monthly,
      render: ({ service, suggestion }) => <span className={styles.mono}>{`${service.spot_tasks > 0 ? 'up to ' : ''}${money(suggestion.saving_monthly)}`}</span>,
    },
  ]

  return (
    <>
      <AnalyticsPanel title="Rightsizing" sub={sub} toolbar={slider} showSources={false} flush>
        <DataTable
          label="Rightsizing suggestions"
          columns={columns}
          rows={suggested}
          rowKey={({ service }) => service.service_id}
          minWidth={820}
          empty="At this headroom every service is already the right size."
          phoneSort
        />
        <div className={styles.more}>
          <span>{`${selected.length} selected · ${upperBound ? 'up to ' : ''}${money(saving)} per month`}</span>
          <button type="button" disabled={selected.length === 0} onClick={() => onDraft(selected)}>
            Draft change sets
          </button>
        </div>
      </AnalyticsPanel>
      {unsuggested.length > 0 && (
        <AnalyticsPanel title="No suggestion" showSources={false}>
          <ul className={styles.reasonList}>
            {unsuggested.map((service) => (
              <li key={service.service_id}>
                <ServiceLink id={service.service_id} name={service.service} cluster={service.cluster} />
                <span className={styles.dimMono}>{reasonFor(service, rightsizing.min_history_days)}</span>
              </li>
            ))}
          </ul>
        </AnalyticsPanel>
      )}
    </>
  )
}
