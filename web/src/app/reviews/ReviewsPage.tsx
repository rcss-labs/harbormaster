import { Link } from 'react-router'
import { useReviewResultsEnabled, useReviewRuns } from '../../api/reviews'
import { Ago } from '../../components/Ago'
import { EmptyState } from '../../components/EmptyState'
import { Panel } from '../../components/Panel'
import { Pill } from '../../components/Pill'
import { useCrumbs } from '../shell/crumbs'
import { firstLine } from '../investigations/InvestigationList'
import { reviewPillKind, reviewStateText, reviewTypeLabel } from './reviewStateText'
import styles from './reviews.module.css'

/** `/reviews`: the results the reader may read, newest first. Names and summaries are plain text nodes. */
export function ReviewsPage() {
  const enabled = useReviewResultsEnabled()
  const query = useReviewRuns()
  useCrumbs([['Reviews']])
  if (!enabled) return <p>Page not found.</p>

  const rows = query.data?.pages.flatMap((page) => page.items) ?? []
  return (
    <Panel
      title="Reviews"
      flush
      loading={query.isPending}
      error={query.isError && !query.isFetchNextPageError ? { message: 'The reviews could not be read.', onRetry: () => void query.refetch() } : undefined}
    >
      {rows.length === 0 ? (
        <EmptyState>No review results yet.</EmptyState>
      ) : (
        <div className={styles.list}>
          {rows.map((run) => (
            <div key={run.id} className={styles.row} data-review-row>
              <Link to={`/reviews/runs/${run.id}`} className={styles.rowName}>
                {run.name}
              </Link>
              <span className={styles.cell}>{reviewTypeLabel(run.type)}</span>
              <span className={styles.muted}>{run.period_label}</span>
              <span className={styles.firstLine}>{firstLine(run.summary)}</span>
              <span className={styles.state}>
                <Pill kind={reviewPillKind(run.state)}>{reviewStateText(run.state, run.reason)}</Pill>
                <span className={styles.muted}>
                  <Ago ts={run.finished_at ?? run.requested_at} />
                </span>
              </span>
            </div>
          ))}
        </div>
      )}
      {query.isFetchNextPageError && (
        <p role="alert" className={styles.error}>
          Could not load more reviews.
        </p>
      )}
      {query.hasNextPage && (
        <div className={styles.more}>
          <button type="button" disabled={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()}>
            {query.isFetchNextPageError ? 'Try again' : 'Load more'}
          </button>
        </div>
      )}
    </Panel>
  )
}
