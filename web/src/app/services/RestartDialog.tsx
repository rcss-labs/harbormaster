import { isApiError, useRestart } from '../../api/change'
import type { ServiceDTO } from '../../api/types'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { showWarning, useToast } from '../../components/Toast'

const ROLLOUT_RUNNING_MESSAGE = 'A rollout is already running'

/** Why Restart is unavailable for this service, or undefined when it is allowed. Shared by the service header and list rows. */
export function restartBlockedTitle(service: ServiceDTO | undefined): string | undefined {
  if (!service) return 'Loading the service'
  if (!service.can.operate) return `You need Operate on ${service.tag.name}`
  if (!service.controller.supported) return service.controller.banner || 'This controller does not support restarts'
  if (service.deployments.some((deployment) => deployment.rollout_state === 'IN_PROGRESS')) return ROLLOUT_RUNNING_MESSAGE
  return undefined
}

interface RestartDialogProps {
  serviceId: string
  service: ServiceDTO
  onClose: () => void
}

/** Confirm dialog for a restart; runs it and toasts the outcome. */
export function RestartDialog({ serviceId, service, onClose }: RestartDialogProps) {
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
