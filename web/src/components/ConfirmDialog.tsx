import type { ReactNode } from 'react'
import { Dialog } from './Dialog'

interface ConfirmDialogProps {
  title: ReactNode
  sub?: ReactNode
  children?: ReactNode
  confirmLabel: string
  danger?: boolean
  /** Disables only the confirm action, e.g. while its request is in flight. */
  confirmDisabled?: boolean
  onConfirm: () => void
  onCancel: () => void
}

export function ConfirmDialog({
  title,
  sub,
  children,
  confirmLabel,
  danger,
  confirmDisabled = false,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  return (
    <Dialog
      title={title}
      sub={sub}
      onClose={onCancel}
      actions={[
        { label: 'Cancel', onClick: onCancel },
        { label: confirmLabel, kind: danger ? 'danger' : 'primary', disabled: confirmDisabled, onClick: onConfirm },
      ]}
    >
      {children}
    </Dialog>
  )
}
