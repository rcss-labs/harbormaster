import { ApiError } from '../../api/client'
import { PanelError } from '../../components/PanelError'

/** The reason a read failed, by status: forbidden is not retried, an unavailable agent and the rest can be. */
export function ReviewError({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  const status = error instanceof ApiError ? error.status : 0
  if (status === 403) return <PanelError message="You do not have permission to read this review." />
  if (status === 503) return <PanelError message="The agent is not available right now." onRetry={onRetry} />
  return <PanelError message="The review could not be read." onRetry={onRetry} />
}
