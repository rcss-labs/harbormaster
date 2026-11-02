import type { AnalyticsOperations, OperatorRow, RestartedServiceRow } from '../../api/analytics'
import { fmtNum } from '../../charts/format'
import { formatAgo, useNow } from '../../components/Ago'
import type { DataTableColumn } from '../../components/DataTable'
import { KpiStrip } from '../../components/KpiStrip'
import { MonoCell, ServiceLink } from './cells'
import { averageOf, COLOR, sourceFor, sumOf, whole } from './format'
import { KpiNote, SeriesPanel, SourcesPanel, TablePanel, type TabProps } from './parts'
import styles from './analytics.module.css'

const TYPE_COLORS = {
  deploy: COLOR.accent,
  rollback: COLOR.pink,
  task_definition: COLOR.ok,
  scale: COLOR.info,
  restart: COLOR.warn,
  stop_task: COLOR.bad,
  terminal: COLOR.orange,
}

export function OperationsTab({ data, showSources, showProposed }: TabProps<AnalyticsOperations>) {
  const { kpis, window } = data
  const common = { window, showSources, showProposed }
  const now = useNow(60_000)
  const log = sourceFor(data.sources, 'operation_log')
  const averageRestarts = averageOf(data.restarts_per_100_deploys.series[0]?.values ?? [])

  const operatorColumns: DataTableColumn<OperatorRow>[] = [
    { key: 'user', header: 'User', width: 'minmax(90px, 1fr)', sortValue: (row) => row.username, render: (row) => <MonoCell>{row.username}</MonoCell> },
    { key: 'total', header: 'Total', width: '60px', align: 'right', sortValue: (row) => row.total, render: (row) => <MonoCell>{whole(row.total)}</MonoCell> },
    { key: 'deploy', header: 'Deploy', width: '60px', align: 'right', sortValue: (row) => row.deploy, render: (row) => <MonoCell>{whole(row.deploy)}</MonoCell> },
    { key: 'restart', header: 'Restart', width: '60px', align: 'right', sortValue: (row) => row.restart, render: (row) => <MonoCell>{whole(row.restart)}</MonoCell> },
    { key: 'scale', header: 'Scale', width: '60px', align: 'right', sortValue: (row) => row.scale, render: (row) => <MonoCell>{whole(row.scale)}</MonoCell> },
    { key: 'terminal', header: 'Terminal', width: '66px', align: 'right', sortValue: (row) => row.terminal, render: (row) => <MonoCell>{whole(row.terminal)}</MonoCell> },
    { key: 'last', header: 'Last', width: '90px', align: 'right', sortValue: (row) => Date.parse(row.last_at), render: (row) => <MonoCell>{formatAgo(row.last_at, now)}</MonoCell> },
  ]

  const restartedColumns: DataTableColumn<RestartedServiceRow>[] = [
    { key: 'service', header: 'Service', width: 'minmax(120px, 1.3fr)', sortValue: (row) => row.service, render: (row) => <ServiceLink id={row.service_id} name={row.service} cluster={row.cluster} /> },
    { key: 'restarts', header: 'Restarts', width: '70px', align: 'right', sortValue: (row) => row.restarts, render: (row) => <MonoCell>{whole(row.restarts)}</MonoCell> },
    { key: 'people', header: 'People', width: '60px', align: 'right', sortValue: (row) => row.people, render: (row) => <MonoCell>{row.people}</MonoCell> },
    { key: 'last', header: 'Last', width: '90px', align: 'right', sortValue: (row) => Date.parse(row.last_at), render: (row) => <MonoCell>{formatAgo(row.last_at, now)}</MonoCell> },
  ]

  return (
    <>
      <KpiStrip
        label="Operations key figures"
        items={[
          { label: 'Operations', value: <>{whole(kpis.operations)}<KpiNote>{`by ${whole(kpis.people)} ${kpis.people === 1 ? 'person' : 'people'}`}</KpiNote></> },
          { label: 'Revisions registered', value: <>{whole(kpis.revisions_registered)}<KpiNote>from revision_records</KpiNote></> },
          { label: 'Scale changes', value: <>{whole(kpis.scale_changes)}<KpiNote>desired count updates</KpiNote></> },
          { label: 'Restarts', value: <>{whole(kpis.restarts)}<KpiNote>force new deployment</KpiNote></> },
          { label: 'Tasks stopped by hand', value: <>{whole(kpis.tasks_stopped)}<KpiNote>Stop task</KpiNote></> },
          { label: 'Terminal sessions', value: <>{whole(kpis.terminal_sessions)}<KpiNote>ECS Exec</KpiNote></> },
        ]}
      />
      <div className={styles.grid}>
        <SeriesPanel
          {...common}
          title="Operations through Harbormaster"
          span="full"
          series={data.by_type}
          mode="stacked"
          colors={TYPE_COLORS}
          source={log}
          legend={(lines) => lines.map((line) => ({ label: line.label, color: line.color, value: fmtNum(sumOf(line.values)) }))}
        />
        <SeriesPanel
          {...common}
          title="Hands-on operations"
          sub="restarts, stops and terminal sessions"
          span="half"
          series={data.hands_on}
          mode="line"
          colors={TYPE_COLORS}
          yFormat={(value) => value.toFixed(value < 10 ? 1 : 0)}
          source={log}
        />
        <SeriesPanel
          {...common}
          title="Restarts per 100 deploys"
          span="half"
          series={data.restarts_per_100_deploys}
          mode="area"
          colors={{ restarts_per_100_deploys: COLOR.warn }}
          yFormat={(value) => value.toFixed(0)}
          source={log}
          legend={(lines) => lines.map((line) => ({ label: 'average', color: line.color, value: averageRestarts === null ? '—' : averageRestarts.toFixed(0) }))}
        />
        <TablePanel
          {...common}
          title="Who operates"
          span="half"
          source={log ? { table: 'operation_log.actor_username', kept: log.kept } : undefined}
          rows={data.who.rows}
          total={data.who.total}
          noun="people"
          limit={7}
          columns={operatorColumns}
          rowKey={(row) => row.username}
          minWidth={520}
          empty="No operations in this range."
        />
        <TablePanel
          {...common}
          title="Most restarted services"
          span="half"
          source={log}
          rows={data.most_restarted.rows}
          total={data.most_restarted.total}
          noun="services"
          limit={7}
          columns={restartedColumns}
          rowKey={(row) => row.service_id}
          rowStatus={(row) => (kpis.restarts > 0 && row.restarts > kpis.restarts * 0.15 ? 'warn' : undefined)}
          minWidth={360}
          empty="No restarts in this range."
        />
        <SourcesPanel sources={data.sources} {...common} />
      </div>
    </>
  )
}
