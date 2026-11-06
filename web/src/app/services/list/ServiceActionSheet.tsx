import { useState } from 'react'
import type { ServiceListItemDTO } from '../../../api/types'
import { BottomSheet, SheetAction } from '../../../components/BottomSheet'
import { Icon } from '../../../components/Icon'
import { StatusDot } from '../../../components/StatusDot'
import { restartBlockedReason } from './filterServices'
import styles from './services.module.css'

interface ServiceActionSheetProps {
  service: ServiceListItemDTO
  pinned: boolean
  onTogglePin: () => void
  onDetails: () => void
  onRestart: () => void
}

/** Phone form of a row's action buttons: one "Actions for <name>" button and a sheet of seven rows. */
export function ServiceActionSheet({ service, pinned, onTogglePin, onDetails, onRestart }: ServiceActionSheetProps) {
  const [open, setOpen] = useState(false)
  const blocked = restartBlockedReason(service)
  const close = () => setOpen(false)
  const then = (action: () => void) => () => {
    close()
    action()
  }
  return (
    <>
      <button type="button" className={styles.moreButton} aria-label={`Actions for ${service.name}`} aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen(true)}>
        <Icon name="more" size={18} />
      </button>
      <BottomSheet
        open={open}
        onClose={close}
        role="menu"
        label={`Actions for ${service.name}`}
        title={<span className={styles.mono}>{service.name}</span>}
        sub={
          <>
            <StatusDot status={service.status} /> · {service.cluster.name} · {service.running} of {service.desired} tasks
          </>
        }
      >
        <SheetAction icon="services" label="Open service" href={`/services/${service.id}/overview`} onClick={close} />
        <SheetAction icon="panel" label="Details" onClick={then(onDetails)} />
        <SheetAction icon="logs" label="Logs" href={`/services/${service.id}/logs`} onClick={close} />
        <SheetAction icon="tasks" label="Tasks" href={`/services/${service.id}/tasks`} onClick={close} />
        <SheetAction icon="restart" label="Restart" disabled={blocked !== undefined} why={blocked} onClick={then(onRestart)} />
        <SheetAction icon="deploy" label="Deploy" href={`/services/${service.id}/deploy`} onClick={close} />
        <SheetAction icon="pin" label={pinned ? 'Unpin' : 'Pin'} why={pinned ? 'Remove from the sidebar' : 'Pin to the sidebar'} onClick={then(onTogglePin)} />
      </BottomSheet>
    </>
  )
}
