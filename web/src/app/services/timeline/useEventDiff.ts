import { useRevisionDefinition } from '../../../api/deploy'
import type { TimelineEvent } from '../../../api/timeline'
import { revisionDiff, type DiffRow } from './revisionDiff'

/** The server's diff rows plus, for a revision change, the two definitions compared here. */
export function useEventDiff(serviceId: string, event: TimelineEvent | null): { rows: DiffRow[]; pending: boolean; failed: boolean } {
  const compares = event !== null && event.from_revision !== null && event.to_revision !== null
  const before = useRevisionDefinition(serviceId, compares ? event.from_revision : null)
  const after = useRevisionDefinition(serviceId, compares ? event.to_revision : null)

  const failed = compares && (before.isError || after.isError)
  const pending = compares && !failed && (before.isPending || after.isPending)
  const rows = [...(event?.diff ?? []), ...(compares && before.data && after.data ? revisionDiff(before.data.definition, after.data.definition) : [])]
  return { rows, pending, failed }
}
