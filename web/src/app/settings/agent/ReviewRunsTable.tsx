import { Link } from 'react-router'
import { useAgentReviewRuns, type ReviewRunRow } from '../../../api/agent'
import { Panel } from '../../../components/Panel'
import { Pill } from '../../../components/Pill'
import { Table, tableClass } from '../../../components/Table'
import styles from '../settings.module.css'
import { runReason, runState } from './runStates'

/** Empty means no run row to add up (a skipped period); the figure covers both attempts. */
const costText = (row: ReviewRunRow) => (row.cost_usd === '' ? 'n/a' : `$${row.cost_usd}`)

/** The results of one review, newest first. */
export function ReviewRunsTable({ reviewId, name }: { reviewId: number; name: string }) {
  const query = useAgentReviewRuns(reviewId)
  const rows = query.data?.pages.flatMap((page) => page.items) ?? []

  return (
    <Panel
      title={`Past results of ${name}`}
      loading={query.isPending}
      error={query.isError && !query.isFetchNextPageError ? { message: 'Could not load the past results.', detail: query.error.message, onRetry: () => void query.refetch() } : undefined}
    >
      {rows.length === 0 ? (
        <p>No results yet.</p>
      ) : (
        <Table>
          <thead>
            <tr>
              <th>State</th>
              <th>Period</th>
              <th>Requested</th>
              <th className={tableClass.num}>Cost</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => {
              const state = runState(row.state)
              const reason = row.reason ? runReason(row.reason) : ''
              return (
                <tr key={row.id}>
                  <td>
                    <Pill kind={state.kind}>{state.label}</Pill>
                    {reason && <span className={styles.hint}>{` ${reason}`}</span>}
                  </td>
                  <td>
                    <Link to={`/reviews/runs/${row.id}`}>{row.period_label || `Result ${row.id}`}</Link>
                    {row.manual && <span className={styles.hint}>{' run now'}</span>}
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
          Could not load more results.
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
