import type { Status } from '../../api/types'
import type { RowStatus } from '../../components/DataTable'

/** Higher is worse; sorting descending puts problem services first. */
const SEVERITY: Record<Status, number> = { failing: 5, unhealthy_task: 4, degraded: 3, deploying: 2, scaled_to_zero: 1, steady: 0 }
export const serviceSeverity = (status: Status | ''): number => (status === '' ? 0 : SEVERITY[status])

/** The stripe colour of a non-steady service row. */
export const serviceRowStatus = (status: Status): RowStatus | undefined => {
  if (status === 'failing') return 'bad'
  if (status === 'degraded' || status === 'unhealthy_task') return 'warn'
  return status === 'deploying' ? 'info' : undefined
}
