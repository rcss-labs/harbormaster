import type { TaskDTO } from '../api/types'
import type { PillKind } from './Pill'
import styles from './stopStatus.module.css'

/**
 * Status of a STOPPED task from the API's stop_kind: error is red, expected (Spot, deployment, scale-in, user: a
 * non-zero exit that is no fault) is teal, anything else is a plain grey stop.
 */
export function stoppedStatus(task: TaskDTO): { kind: PillKind; label: string } {
  const exit = task.exit_code !== null && task.exit_code !== 0 ? ` (exit ${task.exit_code})` : ''
  if (task.stop_kind === 'error') return { kind: 'bad', label: exit ? `Stopped${exit}` : 'Stopped' }
  if (task.stop_kind === 'expected') return { kind: 'expected', label: `Stopped${exit}` }
  return { kind: 'mute', label: 'Stopped' }
}

/** Small neutral chip naming why a task was stopped (spot, deploy, ...); nothing when there is no cause. */
export function StopCauseChip({ cause }: { cause: string }) {
  if (!cause) return null
  return (
    <span className={styles.chip} title={`Stop cause: ${cause}`}>
      {cause}
    </span>
  )
}
