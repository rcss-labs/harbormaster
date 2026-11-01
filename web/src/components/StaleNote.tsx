import type { StaleInfo } from '../api/types'
import { Ago } from './Ago'

/** Tells the user the numbers below came from an expired cache entry because the live fetch failed. */
export function StaleNote({ stale }: { stale: StaleInfo }) {
  return (
    <div role="status" style={{ color: 'var(--warning)', fontSize: 12, marginBottom: 8 }}>
      Showing data from <Ago ts={stale.fetched_at} />: {stale.reason}
    </div>
  )
}
