import { useParams } from 'react-router'
import { ApiError } from '../../api/client'
import { useReviewResultsEnabled, useReviewRun } from '../../api/reviews'
import { Panel } from '../../components/Panel'
import { useCrumbs } from '../shell/crumbs'
import { ReviewError } from './ReviewError'
import { ReviewRunView } from './ReviewRunView'

/** `/reviews/runs/:id`: one review result. Not found when the agent is off, the id is malformed or the API says 404. */
export function ReviewRunPage() {
  const { id = '' } = useParams()
  const enabled = useReviewResultsEnabled()
  const numericId = /^[0-9]{1,9}$/.test(id) ? Number(id) : null
  const query = useReviewRun(numericId)
  useCrumbs([['Reviews', '/reviews'], ['Result']])

  if (!enabled || numericId === null) return <p>Page not found.</p>
  if (query.error instanceof ApiError && query.error.status === 404) return <p>Page not found.</p>
  return (
    <Panel title="Review" loading={query.isPending}>
      {query.data ? <ReviewRunView run={query.data} /> : <ReviewError error={query.error} onRetry={() => void query.refetch()} />}
    </Panel>
  )
}
