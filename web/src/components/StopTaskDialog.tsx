import { isApiError, useStopTask } from '../api/change'
import { useService } from '../api/hooks'
import { Dialog } from './Dialog'
import { useToast } from './Toast'

interface StopTaskDialogProps {
  taskId: string
  shortId: string
  /** Null for a standalone task, which no service replaces. */
  serviceName: string | null
  /** The service id; lets the dialog look up the desired count and refresh the service's tasks. */
  serviceId?: string | null
  /** The service's desired count when the caller already has it; null means look it up by serviceId. */
  desiredCount: number | null
  onClose(): void
}

/** Confirms stopping one task. A service task is replaced by the service; a standalone task is not. */
export function StopTaskDialog(props: StopTaskDialogProps) {
  if (!props.serviceId) return <StopTaskConfirm {...props} />
  return <ServiceStopDialog {...props} serviceId={props.serviceId} />
}

/** Only mounted for service tasks, so useService never runs without an id. */
function ServiceStopDialog(props: StopTaskDialogProps & { serviceId: string }) {
  const service = useService(props.serviceId)
  return <StopTaskConfirm {...props} desiredCount={props.desiredCount ?? service.data?.desired ?? null} />
}

function StopTaskConfirm({ taskId, shortId, serviceName, serviceId, desiredCount, onClose }: StopTaskDialogProps) {
  const stopTask = useStopTask(serviceId ?? undefined)
  const toast = useToast()

  const sub =
    serviceName === null
      ? 'This task belongs to no service; nothing will replace it.'
      : desiredCount !== null
        ? `${serviceName} will start a replacement to keep ${desiredCount} running.`
        : undefined

  const confirm = () =>
    stopTask.mutate(taskId, {
      onSuccess: onClose,
      onError: (error) => toast.show(isApiError(error) ? error.message : 'The task could not be stopped.', 'bad'),
    })

  return (
    <Dialog
      title={`Stop task ${shortId}?`}
      sub={sub}
      onClose={onClose}
      actions={[
        { label: 'Cancel', onClick: onClose },
        { label: 'Stop task', kind: 'danger', onClick: confirm, disabled: stopTask.isPending },
      ]}
    />
  )
}
