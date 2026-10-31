import { useState } from 'react'
import { isApiError, useDesiredCountInfo, useRestart, useSetDesiredCount } from '../../api/change'
import type { ServiceDTO } from '../../api/types'
import { Banner } from '../../components/Banner'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Dialog } from '../../components/Dialog'
import { useToast } from '../../components/Toast'
import styles from './service.module.css'
import dialogStyles from './HeaderActions.module.css'

const ROLLOUT_RUNNING_MESSAGE = 'A rollout is already running'

interface HeaderActionsProps {
  serviceId: string
  service: ServiceDTO
}

/** Restart, desired count and AWS console buttons for the service header. Each button action opens its own dialog. */
export function HeaderActions({ serviceId, service }: HeaderActionsProps) {
  const [openDialog, setOpenDialog] = useState<'restart' | 'desired' | null>(null)
  const close = () => setOpenDialog(null)

  const rolloutRunning = service.deployments.some((deployment) => deployment.rollout_state === 'IN_PROGRESS')
  const operateTitle = service.can.operate ? undefined : `You need Operate on ${service.tag.name}`
  const restartBlockedTitle = !service.can.operate
    ? operateTitle
    : !service.controller.supported
      ? service.controller.banner || 'This controller does not support restarts'
      : rolloutRunning
        ? ROLLOUT_RUNNING_MESSAGE
        : undefined

  return (
    <>
      <button
        type="button"
        className={styles.btn}
        disabled={restartBlockedTitle !== undefined}
        title={restartBlockedTitle}
        onClick={() => setOpenDialog('restart')}
      >
        Restart (new deployment)
      </button>
      <button
        type="button"
        className={styles.btn}
        disabled={operateTitle !== undefined}
        title={operateTitle}
        onClick={() => setOpenDialog('desired')}
      >
        Desired count: {service.desired}
      </button>
      <a className={styles.btn} href={service.console_url} target="_blank" rel="noopener">
        AWS console ↗
      </a>
      {openDialog === 'restart' && <RestartDialog serviceId={serviceId} service={service} onClose={close} />}
      {openDialog === 'desired' && <DesiredCountDialog serviceId={serviceId} service={service} onClose={close} />}
    </>
  )
}

interface DialogProps {
  serviceId: string
  service: ServiceDTO
  onClose: () => void
}

function RestartDialog({ serviceId, service, onClose }: DialogProps) {
  const toast = useToast()
  const restart = useRestart(serviceId)

  const confirm = () =>
    restart.mutate(undefined, {
      onSuccess: () => {
        toast.show('Restart started.', 'ok')
        onClose()
      },
      onError: (error) => {
        toast.show(isApiError(error, 'conflict') ? ROLLOUT_RUNNING_MESSAGE : errorMessage(error, 'Restart failed.'), 'bad')
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

function DesiredCountDialog({ serviceId, service, onClose }: DialogProps) {
  const toast = useToast()
  const info = useDesiredCountInfo(serviceId)
  const setDesiredCount = useSetDesiredCount(serviceId)
  const [draft, setDraft] = useState(String(service.desired))

  const max = info.data?.max
  const autoScaling = info.data?.auto_scaling ?? null
  // An empty field parses to NaN, which fails the integer check and disables the submit button.
  const value = draft.trim() === '' ? Number.NaN : Number(draft)
  const valid = Number.isInteger(value) && value >= 0 && (max === undefined || value <= max)
  const outsideAutoScaling = valid && autoScaling !== null && (value < autoScaling.min || value > autoScaling.max)
  const taskWord = value === 1 ? 'task' : 'tasks'

  const submit = () => {
    if (!valid) return
    if (value === service.desired) {
      toast.show(`Desired count is already ${value}.`, 'info')
      onClose()
      return
    }
    setDesiredCount.mutate(
      { desired: value },
      {
        onSuccess: () => {
          toast.show(`Desired count set to ${value}.`, 'ok')
          onClose()
        },
        onError: (error) => {
          toast.show(errorMessage(error, 'Desired count could not be changed.'), 'bad')
          onClose()
        },
      },
    )
  }

  // Steppers start from the field's current number, or from the service's count when the field is empty or invalid.
  const stepFrom = valid ? value : service.desired

  return (
    <Dialog
      title={`Set ${service.name} to ${valid ? `${value} ${taskWord}` : 'a valid number of tasks'}?`}
      sub={`Now ${service.desired}.${valid && value === 0 ? ' Every task stops. The service and its definition stay.' : ''}`}
      onClose={onClose}
      actions={[
        { label: 'Cancel', onClick: onClose },
        { label: 'Update desired count', kind: 'primary', disabled: !valid || setDesiredCount.isPending, onClick: submit },
      ]}
    >
      <div className={dialogStyles.stepper}>
        <button
          type="button"
          className={styles.btn}
          aria-label="Decrease desired count"
          disabled={stepFrom <= 0}
          onClick={() => setDraft(String(Math.max(0, stepFrom - 1)))}
        >
          −
        </button>
        <input
          type="number"
          aria-label="Desired count"
          min={0}
          max={max}
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
        />
        <button
          type="button"
          className={styles.btn}
          aria-label="Increase desired count"
          disabled={max !== undefined && stepFrom >= max}
          onClick={() => setDraft(String(stepFrom + 1))}
        >
          +
        </button>
      </div>
      {outsideAutoScaling && autoScaling && (
        <Banner kind="warn">
          Auto scaling keeps this service between {autoScaling.min} and {autoScaling.max}. It will move {value} back into
          that range.
        </Banner>
      )}
    </Dialog>
  )
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback
}
