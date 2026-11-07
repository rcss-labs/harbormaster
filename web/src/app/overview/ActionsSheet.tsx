import { useState } from 'react'
import { useService } from '../../api/hooks'
import { BottomSheet, SheetAction } from '../../components/BottomSheet'
import { RestartDialog, restartBlockedTitle } from '../services/RestartDialog'

export interface ActionsTarget {
  /** Subject shown in the title and used in the accessible name, e.g. a service or account alert. */
  label: string
  /** Second line, e.g. the cluster. */
  sub?: string
  /** Empty for account alerts, which only offer Open accounts. */
  serviceId: string
}

interface ActionsSheetProps {
  target: ActionsTarget
  /** Whether the user may open the AWS accounts page; account rows offer it only to admins. */
  isAdmin: boolean
  onClose: () => void
}

/** Phone replacement for the inline Logs, Tasks and Restart buttons of an Overview row. The rules are the desktop ones. */
export function ActionsSheet({ target, isAdmin, onClose }: ActionsSheetProps) {
  if (target.serviceId === '') {
    return (
      <BottomSheet open onClose={onClose} label={`Actions for ${target.label}`} title={target.label} sub={target.sub}>
        {isAdmin && <SheetAction icon="settings" label="Open accounts" href="/settings/accounts" />}
      </BottomSheet>
    )
  }
  return <ServiceActions target={target} onClose={onClose} />
}

function ServiceActions({ target, onClose }: Omit<ActionsSheetProps, 'isAdmin'>) {
  const [restarting, setRestarting] = useState(false)
  const service = useService(target.serviceId).data
  const blocked = restartBlockedTitle(service)

  if (restarting && service) return <RestartDialog serviceId={target.serviceId} service={service} onClose={onClose} />

  return (
    <BottomSheet open onClose={onClose} label={`Actions for ${target.label}`} title={target.label} sub={target.sub}>
      <SheetAction icon="logs" label="Logs" href={`/services/${target.serviceId}/logs`} />
      <SheetAction icon="tasks" label="Tasks" href={`/services/${target.serviceId}/tasks`} />
      <SheetAction icon="restart" label="Restart" why={blocked} disabled={blocked !== undefined} onClick={() => setRestarting(true)} />
    </BottomSheet>
  )
}
