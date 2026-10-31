import { isApiError, useStopTask } from '../api/change'
import { Dialog } from './Dialog'
import { useToast } from './Toast'

interface StopTaskDialogProps {
  taskId: string
  shortId: string
  /** Null for a standalone task, which no service replaces. */
  serviceName: string | null
  /** The service's desired count; null when the caller does not know it. */
  desiredCount: number | null
  onClose(): void
}

/** Confirms stopping one task. A service task is replaced by the service; a standalone task is not. */
export function StopTaskDialog({ taskId, shortId, serviceName, desiredCount, onClose }: StopTaskDialogProps) {
  const stopTask = useStopTask()
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
