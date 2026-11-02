import { useState } from 'react'
import { isApiError, useRestart } from '../../api/change'
import { useService } from '../../api/hooks'
import type { ServiceDTO } from '../../api/types'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { showWarning, useToast } from '../../components/Toast'
import styles from './overview.module.css'

const ROLLOUT_RUNNING_MESSAGE = 'A rollout is already running'

/** Why Restart is unavailable for this service; same rules and wording as the service header. */
function restartBlockedTitle(service: ServiceDTO | undefined): string | undefined {
  if (!service) return 'Loading the service'
  if (!service.can.operate) return `You need Operate on ${service.tag.name}`
  if (!service.controller.supported) return service.controller.banner || 'This controller does not support restarts'
  if (service.deployments.some((deployment) => deployment.rollout_state === 'IN_PROGRESS')) return ROLLOUT_RUNNING_MESSAGE
  return undefined
}

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

function RestartDialog({ serviceId, service, onClose }: { serviceId: string; service: ServiceDTO; onClose: () => void }) {
  const toast = useToast()
  const restart = useRestart(serviceId)

  const confirm = () =>
    restart.mutate(undefined, {
      onSuccess: (started) => {
        toast.show('Restart started.', 'ok')
        showWarning(toast, started.warning)
        onClose()
      },
      onError: (error) => {
        const fallback = error instanceof Error && error.message ? error.message : 'Restart failed.'
        toast.show(isApiError(error, 'conflict') ? ROLLOUT_RUNNING_MESSAGE : fallback, 'bad')
        onClose()
      },
    })

  return (
    <ConfirmDialog
      title={`Restart ${service.name}?`}
      sub={`Starts a new deployment of :${service.revision}. Each task is replaced, new ones first.`}
      confirmLabel="Restart"
      confirmDisabled={restart.isPending}
      onConfirm={confirm}
      onCancel={onClose}
    />
  )
}
