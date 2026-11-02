import type { AnalyticsReliability, AvailabilityTrendRow, ReliabilityServiceRow } from '../../api/analytics'
import { fmtNum } from '../../charts/format'
import type { DataTableColumn } from '../../components/DataTable'
import { KpiStrip } from '../../components/KpiStrip'
import { BarCell, MonoCell, ServiceLink, TagCell } from './cells'
import { averageOf, COLOR, daysBefore, minutesText, percent, sourceFor, sumOf, whole, windowDays } from './format'
import { dailyEmpty, KpiNote, rollupRows, SeriesPanel, SourcesPanel, TablePanel, type TabProps } from './parts'
import styles from './analytics.module.css'

/** Keyed by the API's stop_reason_group strings (internal/status/stopreason.go); the expected groups are drawn grey by linesOf. */
const STOP_COLORS = {
  'Out of memory (exit 137)': COLOR.orange,
  'Failed load balancer health checks': COLOR.warn,
  'Failed to start': COLOR.pink,
  'Container exited with an error': COLOR.bad,
}
const EXIT_COLORS = { '1': COLOR.bad, '137': COLOR.orange, '143': COLOR.warn, '255': COLOR.pink, other: COLOR.gray }

const availabilityColor = (availability: number) => (availability < 0.98 ? COLOR.bad : availability < 0.995 ? COLOR.warn : COLOR.ok)
const exitLabel = (line: { key: string; label: string }) => (line.key === 'other' ? 'Other' : `Exit ${line.label}`)

export function ReliabilityTab({ data, showSources }: TabProps<AnalyticsReliability>) {
  const { kpis, window } = data
  const common = { window, showSources }
  const counts = sourceFor(data.sources, 'service_counts_history')
  const daily = sourceFor(data.sources, 'service_availability_daily')
  // The rollup panels look back 90 days (60 for the comparison of two 30-day periods), whatever the tab's range.
  const ninetyDays = { ...common, window: { from: daysBefore(window.to, 90) } }
  const sixtyDays = { ...common, window: { from: daysBefore(window.to, 60) } }
  const stoppedTasks = (column: string) => sourceFor(data.sources, 'tasks', `tasks.${column}`)
  const availability = kpis.availability
  const days = windowDays(window.used)
  const unexpectedShare = kpis.task_stops === 0 ? 0 : Math.round((kpis.unexpected_stops / kpis.task_stops) * 100)
  const averageAvailability = averageOf(data.availability.series[0]?.values ?? [])
  const peakBelow = Math.max(0, ...(data.services_below.series[0]?.values ?? []).map((value) => value ?? 0))

  const leastColumns: DataTableColumn<ReliabilityServiceRow>[] = [
    { key: 'service', header: 'Service', width: 'minmax(140px, 1.4fr)', sortValue: (row) => row.service, render: (row) => <ServiceLink id={row.service_id} name={row.service} /> },
    { key: 'cluster', header: 'Cluster', width: 'minmax(90px, 1fr)', sortValue: (row) => row.cluster, render: (row) => <MonoCell>{row.cluster}</MonoCell> },
    { key: 'tag', header: 'Tag', width: '90px', render: (row) => <TagCell tag={row.tag} /> },
    {
      key: 'availability',
      header: 'Availability',
      width: '140px',
      sortValue: (row) => row.availability,
      render: (row) => <BarCell pct={(row.availability * 100 - 95) * 20} label={percent(row.availability, 2)} color={availabilityColor(row.availability)} />,
    },
    { key: 'below', header: 'Below desired', width: '110px', align: 'right', sortValue: (row) => row.minutes_below, render: (row) => <MonoCell>{minutesText(row.minutes_below)}</MonoCell> },
    { key: 'stops', header: 'Unexp. stops', width: '100px', align: 'right', sortValue: (row) => row.unexpected_stops, render: (row) => <MonoCell>{row.unexpected_stops}</MonoCell> },
    { key: 'reason', header: 'Top reason', width: 'minmax(120px, 1fr)', render: (row) => row.top_reason ?? '—' },
  ]

  const worseColumns: DataTableColumn<AvailabilityTrendRow>[] = [
    { key: 'service', header: 'Service', width: 'minmax(140px, 1.4fr)', render: (row) => <ServiceLink id={row.service_id} name={row.service} /> },
    { key: 'cluster', header: 'Cluster', width: 'minmax(90px, 1fr)', render: (row) => <MonoCell>{row.cluster}</MonoCell> },
    { key: 'tag', header: 'Tag', width: '90px', render: (row) => <TagCell tag={row.tag} /> },
    { key: 'previous', header: 'Previous 30d', width: '110px', align: 'right', render: (row) => <MonoCell>{percent(row.previous, 2)}</MonoCell> },
    { key: 'current', header: 'Last 30d', width: '110px', align: 'right', render: (row) => <MonoCell>{percent(row.current, 2)}</MonoCell> },
    {
      key: 'change',
      header: 'Change',
      width: '100px',
      align: 'right',
      render: (row) => <span style={{ color: availabilityColor(row.current) }}><MonoCell>{`${row.change_pts > 0 ? '+' : '−'}${Math.abs(row.change_pts).toFixed(2)} pts`}</MonoCell></span>,
    },
  ]

  const worse = rollupRows(data.getting_worse)

  return (
    <>
      <KpiStrip
        label="Reliability key figures"
        items={[
          {
            label: 'Availability',
            value: <>{availability === null ? '—' : percent(availability, 2)}<KpiNote>5-min samples with running ≥ desired</KpiNote></>,
            valueColor: availability === null ? undefined : availability < 0.995 ? COLOR.warn : COLOR.ok,
          },
          { label: 'Time below desired', value: <>{minutesText(kpis.minutes_below_desired)}<KpiNote>{`summed across ${whole(kpis.services_below)} services`}</KpiNote></> },
          { label: 'Task stops', value: <>{whole(kpis.task_stops)}<KpiNote>{`${fmtNum(kpis.task_stops / days)} per day`}</KpiNote></> },
          { label: 'Unexpected stops', value: <>{whole(kpis.unexpected_stops)}<KpiNote>{`${unexpectedShare}% of stops`}</KpiNote></>, valueColor: kpis.unexpected_stops > 0 ? COLOR.warn : undefined },
          { label: 'Out of memory', value: <>{whole(kpis.out_of_memory)}<KpiNote>exit 137 / OutOfMemoryError</KpiNote></>, valueColor: kpis.out_of_memory > 20 ? COLOR.orange : undefined },
          { label: 'Placement failures', value: <>{whole(kpis.placement_failures)}<KpiNote>from ECS service events</KpiNote></> },
        ]}
      />
      <div className={styles.grid}>
        <SeriesPanel
          {...common}
          title="Availability"
          sub="running ≥ desired"
          span="half"
          series={data.availability}
          mode="area"
          colors={{ availability: COLOR.ok }}
          yMin={96}
          yMax={100}
          yFormat={(value) => `${value.toFixed(2)}%`}
          reference={{ value: 99.5, label: '99.5%' }}
          source={counts}
          legend={(lines) => lines.map((line) => ({ label: 'average', color: line.color, value: averageAvailability === null ? '—' : `${averageAvailability.toFixed(2)}%` }))}
        />
        <SeriesPanel
          {...common}
          title="Services below desired"
          span="half"
          series={data.services_below}
          mode="area"
          colors={{ services_below: COLOR.warn }}
          source={counts}
          legend={(lines) => lines.map((line) => ({ label: 'peak', color: line.color, value: fmtNum(peakBelow) }))}
        />
        <SeriesPanel
          {...common}
          title="Task stops by reason"
          sub="grey = expected"
          span="full"
          series={data.stops_by_reason}
          mode="stacked"
          colors={STOP_COLORS}
          source={stoppedTasks('stop_reason_group')}
          legend={(lines) => lines.map((line) => ({ label: line.label, color: line.color, value: fmtNum(sumOf(line.values)) }))}
        />
        <SeriesPanel
          {...common}
          title="Unexpected stops by zone"
          span="half"
          series={data.unexpected_by_zone}
          mode="line"
          source={stoppedTasks('availability_zone')}
          legend={(lines) => lines.map((line) => ({ label: line.label, color: line.color, value: fmtNum(sumOf(line.values)) }))}
        />
        <SeriesPanel
          {...common}
          title="Container exit codes"
          span="half"
          series={data.exit_codes}
          mode="stacked"
          colors={EXIT_COLORS}
          lineLabel={exitLabel}
          source={stoppedTasks('containers')}
          legend={(lines) => lines.slice(0, 3).map((line) => ({ label: line.label, color: line.color, value: fmtNum(sumOf(line.values)) }))}
        />
        <SeriesPanel
          {...ninetyDays}
          title="Availability, last 90 days"
          sub="daily"
          span="half"
          series={data.availability_90d.data}
          rollup={data.availability_90d}
          empty={dailyEmpty(data.availability_90d)}
          source={daily}
          mode="area"
          colors={{ availability: COLOR.ok }}
          yMin={96}
          yMax={100}
          yFormat={(value) => `${value.toFixed(2)}%`}
          reference={{ value: 99.5, label: '99.5%' }}
        />
        <SeriesPanel
          {...ninetyDays}
          title="Time below desired, last 90 days"
          sub="summed across services"
          span="half"
          series={data.below_desired_90d.data}
          rollup={data.below_desired_90d}
          empty={dailyEmpty(data.below_desired_90d)}
          source={daily}
          mode="area"
          colors={{ minutes_below: COLOR.warn }}
          yFormat={minutesText}
        />
        <TablePanel
          {...sixtyDays}
          title="Services getting less available"
          sub="last 30 days vs the 30 before"
          rollup={data.getting_worse}
          empty={dailyEmpty(data.getting_worse)}
          source={daily}
          rows={worse.rows}
          columns={worseColumns}
          rowKey={(row) => row.service_id}
          minWidth={700}
        />
        <TablePanel
          {...common}
          title="Least available services"
          source={counts}
          rows={data.least_available.rows}
          total={data.least_available.total}
          noun="services"
          columns={leastColumns}
          rowKey={(row) => row.service_id}
          rowStatus={(row) => (row.availability < 0.98 ? 'bad' : row.availability < 0.995 ? 'warn' : undefined)}
          minWidth={900}
          empty="No samples in this range."
        />
        <SourcesPanel sources={data.sources} {...common} />
      </div>
    </>
  )
}
