import type { ServiceDTO } from '../../api/types'
import { ConfirmDialog } from '../../components/ConfirmDialog'

interface DesiredCountDialogProps {
  service: ServiceDTO
  /** The count being confirmed; null when the draft is not a valid count. */
  count: number | null
  pending: boolean
  onConfirm: () => void
  onCancel: () => void
}

/** Confirm dialog for a desired-count change, shared by the header stepper and the Scaling tab. */
export function DesiredCountDialog({ service, count, pending, onConfirm, onCancel }: DesiredCountDialogProps) {
  const taskWord = count === 1 ? 'task' : 'tasks'
  return (
    <ConfirmDialog
      title={`Set ${service.name} to ${count !== null ? `${count} ${taskWord}` : 'a valid number of tasks'}?`}
      sub={`Now ${service.desired}.${count === 0 ? ' Every task stops. The service and its definition stay.' : ''}`}
      confirmLabel="Update desired count"
      confirmDisabled={count === null || pending}
      onConfirm={onConfirm}
      onCancel={onCancel}
    />
  )
}
