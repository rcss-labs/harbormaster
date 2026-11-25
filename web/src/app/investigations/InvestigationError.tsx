import { ApiError } from '../../api/client'
import { PanelError } from '../../components/PanelError'

/** The reason a read failed, by status: not found has no Retry, forbidden is not retried, the rest can be. */
export function InvestigationError({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  const status = error instanceof ApiError ? error.status : 0
  if (status === 404) return <PanelError message="This investigation was not found." />
  if (status === 403) return <PanelError message="You do not have permission to read this investigation." />
  if (status === 503) return <PanelError message="The agent is not available right now." onRetry={onRetry} />
  return <PanelError message="The investigation could not be read." onRetry={onRetry} />
}
