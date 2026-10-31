import type { DeploymentDTO } from '../../../api/types'
import { Ago } from '../../../components/Ago'
import { Pill } from '../../../components/Pill'
import styles from '../service.module.css'

const plural = (count: number, noun: string) => `${count} ${noun}${count === 1 ? '' : 's'}`

interface RolloutCardProps {
  serviceName: string
  deployments: DeploymentDTO[]
  desiredCount: number
  strategyText: string
  circuitBreakerRollback: boolean
}

export function RolloutCard({ serviceName, deployments, desiredCount, strategyText, circuitBreakerRollback }: RolloutCardProps) {
  const primary = deployments.find((d) => d.status === 'PRIMARY') ?? deployments[0]
  const active = deployments.find((d) => d.status === 'ACTIVE')
  const circuitBreaker = `circuit breaker with rollback is ${circuitBreakerRollback ? 'on' : 'off'}`

  if (primary?.rollout_state === 'IN_PROGRESS') {
    const fromRevision = active?.revision ?? primary.revision
    const newCount = primary.running_count
    const oldCount = active?.running_count ?? 0
    const failed = primary.failed_tasks
    const desired = Math.max(1, desiredCount)
    return (
      <div className={styles.deployCard}>
        <div className={styles.titleRow}>
          <Pill kind="info">Rollout in progress</Pill>
          <span className={styles.mono}>
            :{fromRevision} → :{primary.revision}
          </span>
          <span className={`${styles.muted} ${styles.small} ${styles.pushRight}`}>
            started <Ago ts={primary.created_at} />
          </span>
        </div>
        <div className={styles.bar}>
          <span style={{ width: `${(newCount / desired) * 100}%` }} />
          <span className={styles.old} style={{ width: `${(oldCount / desired) * 100}%` }} />
        </div>
        <div className={`${styles.small} ${styles.muted}`}>
          {plural(newCount, 'new task')} on :{primary.revision} · {oldCount} old on :{fromRevision} ·{' '}
          {failed > 0 && (
            <>
              <span className={styles.danger}>{failed} failed to start</span> ·{' '}
            </>
          )}
          {circuitBreaker}
        </div>
      </div>
    )
  }

  if (primary?.rollout_state === 'FAILED') {
    return (
      <div className={styles.deployCard}>
        <div className={styles.titleRow}>
          <Pill kind="bad">Failed</Pill>
          <span className={styles.mono}>
            {serviceName}:{primary.revision}
          </span>
          <span className={`${styles.muted} ${styles.small} ${styles.pushRight}`}>
            <Ago ts={primary.updated_at} />
          </span>
        </div>
        <div className={styles.bar}>
          <span style={{ width: `${(primary.running_count / Math.max(1, desiredCount)) * 100}%` }} />
        </div>
        <div className={`${styles.small} ${styles.danger}`}>{primary.rollout_state_reason}</div>
      </div>
    )
  }

  return (
    <div className={styles.deployCard}>
      <div className={styles.titleRow}>
        <Pill kind="ok">Completed</Pill>
        <span className={styles.mono}>
          {serviceName}:{primary?.revision}
        </span>
        {primary && (
          <span className={`${styles.muted} ${styles.small} ${styles.pushRight}`}>
            <Ago ts={primary.updated_at} />
          </span>
        )}
      </div>
      <div className={styles.bar}>
        <span style={{ width: '100%' }} />
      </div>
      <div className={`${styles.small} ${styles.muted}`}>{strategyText}</div>
    </div>
  )
}
