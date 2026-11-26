import { Link } from 'react-router'
import { useAgentInvestigations, type AgentInvestigationDTO, type AgentInvestigatorLimits, type InvestigationState } from '../../../api/agent'
import { Panel } from '../../../components/Panel'
import { Pill } from '../../../components/Pill'
import { Table, tableClass } from '../../../components/Table'
import styles from '../settings.module.css'

const STATES: Record<InvestigationState, { label: string; kind: 'ok' | 'bad' | 'warn' | 'mute' }> = {
  requested: { label: 'Waiting', kind: 'mute' },
  running: { label: 'Running', kind: 'warn' },
  done: { label: 'Done', kind: 'ok' },
  failed: { label: 'Failed', kind: 'bad' },
  skipped: { label: 'Skipped', kind: 'mute' },
}

const REASONS: Record<string, string> = {
  budget: 'allowance used up',
  not_visible: 'agent cannot see the service',
  not_configured: 'no model chosen',
  rate_limited: 'hourly limit',
  limit_reached: 'hourly limit',
  expired: 'waited too long',
  agent_unavailable: 'agent unavailable',
  no_result: 'no answer',
  interrupted: 'interrupted',
  error: 'error',
}

const count = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`

function subjectName(row: AgentInvestigationDTO): string {
  const { subject } = row
  return subject.service || subject.cluster || subject.account_id || row.alert.title
}

/** Empty means no run row (skipped, or deleted by retention); after a retry the figure is the last attempt only. */
function costText(row: AgentInvestigationDTO): string {
  if (row.cost_usd === '') return 'n/a'
  return row.attempts > 1 ? `$${row.cost_usd} (last attempt)` : `$${row.cost_usd}`
}

/** The window and limits are set by the operator; this page only shows them. */
function limitsSentence(limits: AgentInvestigatorLimits): string {
  return `Alerts for the same service within ${count(limits.window_minutes, 'minute', 'minutes')} share one investigation. At most ${limits.per_hour} start an hour, ${limits.concurrent} run at a time, and none waits longer than ${count(limits.max_wait_minutes, 'minute', 'minutes')}.`
}

export function InvestigationsTable({ limits }: { limits: AgentInvestigatorLimits }) {
  const query = useAgentInvestigations()
  const rows = query.data?.pages.flatMap((page) => page.items) ?? []

  return (
    <Panel
      title="Investigations"
      loading={query.isPending}
      error={query.isError && !query.isFetchNextPageError ? { message: 'Could not load the investigations.', detail: query.error.message, onRetry: () => void query.refetch() } : undefined}
    >
      <p className={styles.hint}>{limits.per_hour === 0 ? 'Background investigations are off.' : limitsSentence(limits)}</p>
      {rows.length === 0 ? (
        <p>No investigations yet.</p>
      ) : (
        <Table>
          <thead>
            <tr>
              <th>State</th>
              <th>Subject</th>
              <th>Requested</th>
              <th className={tableClass.num}>Cost</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => {
              const state = STATES[row.state] ?? { label: 'Unknown', kind: 'mute' as const }
              const reason = REASONS[row.reason] && state.label !== 'Unknown' ? REASONS[row.reason] : undefined
              return (
                <tr key={row.id}>
                  <td>
                    <Pill kind={state.kind}>{state.label}</Pill>
                    {reason && <span className={styles.hint}>{` ${reason}`}</span>}
                  </td>
                  <td>
                    <Link to={`/investigations/${row.id}`}>{subjectName(row)}</Link>
                  </td>
                  <td>{new Date(row.requested_at).toLocaleString()}</td>
                  <td className={tableClass.num}>{costText(row)}</td>
                </tr>
              )
            })}
          </tbody>
        </Table>
      )}
      {query.isFetchNextPageError && (
        <p role="alert" className={styles.error}>
          Could not load more investigations.
        </p>
      )}
      {query.hasNextPage && (
        <div className={styles.windowRow}>
          <button type="button" className={styles.btn} disabled={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()}>
            {query.isFetchNextPageError ? 'Try again' : 'Load more'}
          </button>
        </div>
      )}
    </Panel>
  )
}
