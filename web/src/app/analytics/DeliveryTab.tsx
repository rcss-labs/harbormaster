import type { AnalyticsDelivery, DeliveryServiceRow, FailedRolloutRow } from '../../api/analytics'
import { fmtNum } from '../../charts/format'
import { formatAgo, useNow } from '../../components/Ago'
import type { DataTableColumn } from '../../components/DataTable'
import { KpiStrip } from '../../components/KpiStrip'
import { BarCell, MonoCell, ServiceLink, TagCell } from './cells'
import { averageOf, COLOR, durationText, minutesText, percent, sourceFor, sumOf, whole } from './format'
import { FAILURES_EMPTY, KpiNote, rollupRows, SeriesPanel, SourcesPanel, TablePanel, type TabProps } from './parts'
import styles from './analytics.module.css'

const OUTCOME_COLORS = { completed: COLOR.accent, failed: COLOR.bad, superseded: COLOR.gray }
const REASON_COLORS = {
  circuit_breaker: COLOR.bad,
  health_check: COLOR.warn,
  failed_to_start: COLOR.orange,
  out_of_memory: COLOR.pink,
  stopped_by_user: COLOR.gray,
  other: COLOR.gray2,
}

/** The stored failure_reason group as words: "out_of_memory" -> "Out of memory". */
const reasonLabel = (reason: string) => {
  const words = reason.replaceAll('_', ' ')
  return words.charAt(0).toUpperCase() + words.slice(1)
}

const rateColor = (rate: number) => (rate > 0.2 ? COLOR.bad : rate > 0.1 ? COLOR.warn : COLOR.accent)
const rateStatus = (rate: number) => (rate > 0.2 ? 'bad' : rate > 0.1 ? 'warn' : undefined)

export function DeliveryTab({ data, showSources }: TabProps<AnalyticsDelivery>) {
  const { kpis, window } = data
  const now = useNow(60_000)
  const common = { window, showSources }
  const source = sourceFor(data.sources, 'deployment_history')
  const failedRate = kpis.change_failure_rate

  const serviceColumns: DataTableColumn<DeliveryServiceRow>[] = [
    { key: 'service', header: 'Service', width: 'minmax(140px, 1.4fr)', sortValue: (row) => row.service, render: (row) => <ServiceLink id={row.service_id} name={row.service} /> },
    { key: 'cluster', header: 'Cluster', width: 'minmax(90px, 1fr)', sortValue: (row) => row.cluster, render: (row) => <MonoCell>{row.cluster}</MonoCell> },
    { key: 'tag', header: 'Tag', width: '90px', render: (row) => <TagCell tag={row.tag} /> },
    { key: 'rollouts', header: 'Rollouts', width: '80px', align: 'right', sortValue: (row) => row.rollouts, render: (row) => <MonoCell>{row.rollouts}</MonoCell> },
    { key: 'failed', header: 'Failed', width: '70px', align: 'right', sortValue: (row) => row.failed, render: (row) => <span style={{ color: COLOR.bad }}><MonoCell>{row.failed}</MonoCell></span> },
    {
      key: 'rate',
      header: 'Failure rate',
      width: '130px',
      sortValue: (row) => row.failure_rate,
      render: (row) => <BarCell pct={row.failure_rate * 200} label={percent(row.failure_rate)} color={rateColor(row.failure_rate)} />,
    },
    { key: 'median', header: 'Median', width: '90px', align: 'right', sortValue: (row) => row.median_s, render: (row) => <MonoCell>{row.median_s === null ? '—' : durationText(row.median_s)}</MonoCell> },
    { key: 'recover', header: 'Recover', width: '90px', align: 'right', sortValue: (row) => row.recover_s, render: (row) => <MonoCell>{row.recover_s === null ? '—' : minutesText(row.recover_s / 60)}</MonoCell> },
    {
      key: 'last',
      header: 'Last failed',
      width: '100px',
      align: 'right',
      sortValue: (row) => (row.last_failed_at === null ? null : Date.parse(row.last_failed_at)),
      render: (row) => <MonoCell>{row.last_failed_at === null ? '—' : formatAgo(row.last_failed_at, now)}</MonoCell>,
    },
  ]

  const failureColumns: DataTableColumn<FailedRolloutRow>[] = [
    { key: 'at', header: 'When', width: '90px', render: (row) => <MonoCell>{formatAgo(row.at, now)}</MonoCell> },
    { key: 'service', header: 'Service', width: 'minmax(130px, 1.2fr)', render: (row) => <ServiceLink id={row.service_id} name={row.service} cluster={row.cluster} /> },
    {
      key: 'revision',
      header: 'Revision',
      width: '110px',
      render: (row) => <MonoCell>{row.from_revision === null ? `:${row.to_revision}` : `:${row.from_revision} → :${row.to_revision}`}</MonoCell>,
    },
    { key: 'reason', header: 'Reason', width: '150px', render: (row) => <strong>{reasonLabel(row.reason)}</strong> },
    { key: 'detail', header: 'Detail', width: 'minmax(240px, 2.2fr)', render: (row) => <span className={styles.dimMono}>{row.detail}</span> },
    { key: 'ran', header: 'Ran for', width: '90px', align: 'right', render: (row) => <MonoCell>{row.ran_for_s === null ? '—' : durationText(row.ran_for_s)}</MonoCell> },
  ]

  const failures = rollupRows(data.recent_failures)
  const rollouts = data.rollouts.series
  const averageFailure = averageOf(data.failure_rate.series[0]?.values ?? [])

  return (
    <>
      <KpiStrip
        label="Delivery key figures"
        items={[
          { label: 'Rollouts', value: <>{whole(kpis.rollouts)}<KpiNote>{`${kpis.rollouts_per_day.toFixed(1)} per day`}</KpiNote></> },
          {
            label: 'Change failure rate',
            value: <>{failedRate === null ? '—' : percent(failedRate)}<KpiNote>failed ÷ (completed + failed)</KpiNote></>,
            valueColor: failedRate !== null && failedRate > 0.05 ? COLOR.warn : undefined,
          },
          {
            label: 'Median rollout',
            value: <>{kpis.median_rollout_s === null ? '—' : durationText(kpis.median_rollout_s)}<KpiNote>{kpis.p90_rollout_s === null ? 'p90 —' : `p90 ${durationText(kpis.p90_rollout_s)}`}</KpiNote></>,
          },
          {
            label: 'Time to recover',
            value: <>{kpis.time_to_recover_s === null ? '—' : minutesText(kpis.time_to_recover_s / 60)}<KpiNote>median, failed → next completed</KpiNote></>,
          },
          { label: 'Rollbacks', value: <>{whole(kpis.rollbacks)}<KpiNote>deploys to a lower revision</KpiNote></> },
          { label: 'Superseded', value: <>{whole(kpis.superseded)}<KpiNote>replaced before finishing</KpiNote></> },
        ]}
      />
      <div className={styles.grid}>
        <SeriesPanel
          {...common}
          title="Rollouts"
          span="full"
          series={data.rollouts}
          mode="stacked"
          colors={OUTCOME_COLORS}
          source={source}
          legend={(lines) => lines.map((line, i) => ({ label: line.label, color: line.color, value: whole(sumOf(rollouts[i].values)) }))}
        />
        <SeriesPanel
          {...common}
          title="Change failure rate"
          span="half"
          series={data.failure_rate}
          mode="area"
          colors={{ failure_rate: COLOR.bad }}
          yFormat={(value) => `${value.toFixed(1)}%`}
          reference={{ value: 5, label: '5%' }}
          source={source}
          legend={(lines) => lines.map((line) => ({ label: 'average', color: line.color, value: averageFailure === null ? '—' : `${averageFailure.toFixed(1)}%` }))}
        />
        <SeriesPanel
          {...common}
          title="Rollout duration"
          span="half"
          series={data.duration}
          mode="line"
          colors={{ median: COLOR.accent, p90: COLOR.orange }}
          yFormat={durationText}
          source={source}
          legend={(lines) => lines.map((line) => ({ label: line.label.toLowerCase(), color: line.color, value: durationText(averageOf(line.values) ?? 0) }))}
        />
        <SeriesPanel
          {...common}
          title="Why rollouts failed"
          span="full"
          series={data.failure_reasons.data}
          rollup={data.failure_reasons}
          empty={FAILURES_EMPTY}
          source={source}
          mode="stacked"
          colors={REASON_COLORS}
          legend={(lines) => lines.map((line) => ({ label: line.label, color: line.color, value: fmtNum(sumOf(line.values)) }))}
        />
        <TablePanel
          {...common}
          title="Recent failed rollouts, with reason"
          rollup={data.recent_failures}
          empty={FAILURES_EMPTY}
          source={source}
          rows={failures.rows}
          columns={failureColumns}
          rowKey={(row) => `${row.service_id}-${row.at}`}
          minWidth={880}
        />
        <TablePanel
          {...common}
          title="Services by change failure rate"
          sub="most failures first"
          source={source}
          rows={data.services.rows}
          total={data.services.total}
          noun="services with rollouts in this range"
          columns={serviceColumns}
          rowKey={(row) => row.service_id}
          rowStatus={(row) => rateStatus(row.failure_rate)}
          minWidth={960}
          empty="No rollouts in this range."
        />
        <SourcesPanel sources={data.sources} {...common} />
      </div>
    </>
  )
}
