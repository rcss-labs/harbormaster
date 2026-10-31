import { Banner } from '../../components/Banner'
import styles from './service.module.css'

interface PendingRevisionBannerProps {
  serviceName: string
  pendingRevision: number
  deployedRevision: number
  canDeploy: boolean
  /** Tooltip when Deploy is disabled, such as the lock explanation. */
  deployDisabledReason?: string
  onDeploy(): void
}

export function PendingRevisionBanner({ serviceName, pendingRevision, deployedRevision, canDeploy, deployDisabledReason, onDeploy }: PendingRevisionBannerProps) {
  return (
    <Banner kind="info">
      <span className={styles.pendingBanner}>
        <span>
          Revision <strong>{`${serviceName}:${pendingRevision}`}</strong> is registered but not deployed. The service still runs {`:${deployedRevision}`}.
        </span>
        <button type="button" className={styles.pendingDeploy} disabled={!canDeploy} title={canDeploy ? undefined : deployDisabledReason} onClick={onDeploy}>
          {`Deploy :${pendingRevision}`}
        </button>
      </span>
    </Banner>
  )
}
