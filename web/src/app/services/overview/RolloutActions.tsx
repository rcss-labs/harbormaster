import { useState } from 'react'
import { Link } from 'react-router'
import type { ServiceDTO } from '../../../api/types'
import { Icon } from '../../../components/Icon'
import { LockNote } from '../../../components/LockNote'
import { DesiredCountSheet } from '../HeaderActions'
import { RestartDialog, restartBlockedTitle } from '../RestartDialog'
import styles from '../service.module.css'

/** Phone row under the rollout card: Restart, Desired count and a link to the Deploy tab. */
export function RolloutActions({ service }: { service: ServiceDTO }) {
  const [dialog, setDialog] = useState<'restart' | 'desired' | null>(null)
  const restartBlocked = restartBlockedTitle(service)
  const operateTitle = service.can.operate ? undefined : `You need Operate on ${service.tag.name}`
  // Restart's reason covers Operate too, so one line serves both buttons; hover does not exist on a phone.
  const reason = restartBlocked ?? operateTitle
  return (
    <>
      <div className={styles.rolloutActions}>
        <button type="button" className={styles.smallBtn} disabled={restartBlocked !== undefined} title={restartBlocked} onClick={() => setDialog('restart')}>
          <Icon name="restart" size={16} />
          Restart
        </button>
        <button type="button" className={styles.smallBtn} disabled={!service.can.operate} title={operateTitle} onClick={() => setDialog('desired')}>
          Desired count
        </button>
        <Link className={styles.smallBtn} to={`/services/${service.id}/deploy`}>
          Open Deploy
        </Link>
        {dialog === 'restart' && <RestartDialog serviceId={service.id} service={service} onClose={() => setDialog(null)} />}
        {dialog === 'desired' && <DesiredCountSheet serviceId={service.id} service={service} onClose={() => setDialog(null)} />}
      </div>
      <LockNote kind="why">{reason}</LockNote>
    </>
  )
}
