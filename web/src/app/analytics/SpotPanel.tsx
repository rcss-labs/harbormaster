import type { CostSpot, CostSpotService } from '../../api/analytics'
import { DataTable, type DataTableColumn } from '../../components/DataTable'
import { EmptyState } from '../../components/EmptyState'
import { Pill } from '../../components/Pill'
import { AnalyticsPanel } from './AnalyticsPanel'
import { ServiceLink } from './cells'
import { isSpotTicked, money, priceDate, selectedSpot, spotCandidates, spotNotCandidateReason, spotReasons } from './cost'
import styles from './analytics.module.css'

interface SpotPanelProps {
  spot: CostSpot
  /** The day the Fargate Spot prices were read, YYYY-MM-DD. */
  pricesAsOf: string
  /** Ids the user flipped from the default selection. */
  toggled: ReadonlySet<string>
  onToggle: (serviceId: string) => void
  onDraft: () => void
}

const CAUTION_TITLE = 'A terminal session or a stop that was killed in the last days: check that the service tolerates Spot before applying.'

/** Services that could run on Fargate Spot, by monthly saving, with the reasons each qualifies, and why the others do not. */
export function SpotPanel({ spot, pricesAsOf, toggled, onToggle, onDraft }: SpotPanelProps) {
  const candidates = spotCandidates(spot)
  const others = spot.services.filter((service) => service.status !== 'candidate')
  const selected = selectedSpot(spot, toggled)
  const saving = selected.reduce((total, service) => total + (service.saving_monthly ?? 0), 0)
  const sub = `candidates by monthly saving · Spot prices as of ${priceDate(pricesAsOf)}`

  const columns: DataTableColumn<CostSpotService>[] = [
    {
      key: 'select',
      header: <span className="hm-sr">Select</span>,
      width: '36px',
      phone: 'action',
      render: (service) => (
        <input
          type="checkbox"
          aria-label={`Select ${service.service}`}
          checked={isSpotTicked(service, toggled)}
          disabled={!service.can_deploy}
          title={service.can_deploy ? undefined : `You need Deploy on ${service.tag.name}`}
          onChange={() => onToggle(service.service_id)}
        />
      ),
    },
    {
      key: 'service',
      header: 'Service',
      width: 'minmax(180px, 1.4fr)',
      sortValue: (service) => service.service,
      render: (service) => (
        <>
          <ServiceLink id={service.service_id} name={service.service} cluster={service.cluster} />
          <span className={styles.pills}>
            {service.auto_scaled && (
              <Pill kind="info" title="A different task count may change how many tasks run on Spot, so the saving is an estimate.">
                Auto-scaled
              </Pill>
            )}
            {service.caution && (
              <Pill kind="warn" title={CAUTION_TITLE}>
                Check first
              </Pill>
            )}
          </span>
        </>
      ),
    },
    {
      key: 'tasks',
      header: 'Tasks',
      width: '190px',
      render: (service) => <span className={styles.mono}>{`${service.desired_tasks} now · ${service.on_demand_base} on-demand, ${service.spot_tasks} on Spot`}</span>,
    },
    {
      key: 'why',
      header: 'Why it qualifies',
      width: 'minmax(260px, 2fr)',
      render: (service) => (
        <ul className={styles.whyList}>
          {spotReasons(service, spot.lookback_days).map((reason) => (
            <li key={reason.text} title={reason.title}>
              {reason.text}
            </li>
          ))}
        </ul>
      ),
    },
    {
      key: 'saves',
      header: 'Saves / mo',
      width: '110px',
      align: 'right',
      sortValue: (service) => service.saving_monthly,
      render: (service) => <span className={styles.mono}>{`about ${money(service.saving_monthly ?? 0)}`}</span>,
    },
  ]

  const nonProduction = [...new Set(spot.services.filter((service) => service.tag.non_production).map((service) => service.tag.name))].sort()
  const empty = (
    <EmptyState>
      <p>No service qualifies for Fargate Spot.</p>
      <p className={styles.dimMono}>
        {nonProduction.length === 0 ? 'Mark a tag as non-production in Admin, Tags to get suggestions.' : `Non-production tags: ${nonProduction.join(', ')}.`}
      </p>
    </EmptyState>
  )

  return (
    <>
      <AnalyticsPanel title="Fargate Spot" sub={sub} showSources={false} flush>
        {candidates.length === 0 ? (
          empty
        ) : (
          <>
            <DataTable
              label="Fargate Spot candidates"
              columns={columns}
              rows={candidates}
              rowKey={(service) => service.service_id}
              minWidth={900}
              phoneSort
            />
            <div className={styles.more}>
              <span>{`${selected.length} selected · about ${money(saving)} per month`}</span>
              <button type="button" disabled={selected.length === 0} onClick={onDraft}>
                Draft change sets
              </button>
            </div>
          </>
        )}
      </AnalyticsPanel>
      {others.length > 0 && (
        <AnalyticsPanel title="Not candidates" showSources={false}>
          <ul className={styles.reasonList}>
            {others.map((service) => (
              <li key={service.service_id}>
                <ServiceLink id={service.service_id} name={service.service} cluster={service.cluster} />
                <span className={styles.dimMono}>
                  {spotNotCandidateReason(service, spot.min_tasks)}
                  {service.status === 'no_cluster_spot' && service.saving_monthly !== null && ` It would save about ${money(service.saving_monthly)} per month.`}
                </span>
              </li>
            ))}
          </ul>
        </AnalyticsPanel>
      )}
    </>
  )
}
