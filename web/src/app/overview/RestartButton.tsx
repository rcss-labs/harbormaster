import { useState } from 'react'
import { useService } from '../../api/hooks'
import { RestartDialog, restartBlockedTitle } from '../services/RestartDialog'
import styles from './overview.module.css'

/** Inline Restart for a list row; it loads the service to know whether the user may restart it. */
export function RestartButton({ serviceId }: { serviceId: string }) {
  const service = useService(serviceId).data
  const [open, setOpen] = useState(false)
  const blocked = restartBlockedTitle(service)
  return (
    <>
      <button type="button" className={styles.rowAction} disabled={blocked !== undefined} title={blocked} onClick={() => setOpen(true)}>
        Restart
      </button>
      {open && service && <RestartDialog serviceId={serviceId} service={service} onClose={() => setOpen(false)} />}
    </>
  )
}
