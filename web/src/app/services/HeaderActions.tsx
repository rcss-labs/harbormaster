import { useState } from 'react'
import { Link } from 'react-router'
import { isApiError, useDesiredCountInfo, useRestart, useSetDesiredCount } from '../../api/change'
import type { ServiceDTO } from '../../api/types'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { showWarning, useToast } from '../../components/Toast'
import styles from './service.module.css'
import stepperStyles from './HeaderActions.module.css'

const ROLLOUT_RUNNING_MESSAGE = 'A rollout is already running'

interface HeaderActionsProps {
  serviceId: string
  service: ServiceDTO
}

/** Inline desired-count stepper (with Apply), Restart and AWS console for the sticky service header. Changes go through confirm dialogs. */
export function HeaderActions({ serviceId, service }: HeaderActionsProps) {
  const [restarting, setRestarting] = useState(false)

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
      <DesiredStepper serviceId={serviceId} service={service} disabledTitle={operateTitle} />
      <button
        type="button"
        className={styles.btn}
        aria-label="Restart (new deployment)"
        disabled={restartBlockedTitle !== undefined}
        title={restartBlockedTitle}
        onClick={() => setRestarting(true)}
      >
        Restart
      </button>
      <a className={styles.btn} href={service.console_url} target="_blank" rel="noopener">
        AWS console ↗
      </a>
      {restarting && <RestartDialog serviceId={serviceId} service={service} onClose={() => setRestarting(false)} />}
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
      onSuccess: (started) => {
        toast.show('Restart started.', 'ok')
        showWarning(toast, started.warning)
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

interface StepperProps {
  serviceId: string
  service: ServiceDTO
  /** Set when the user lacks Operate: disables the stepper and explains why. */
  disabledTitle?: string
}

function DesiredStepper({ serviceId, service, disabledTitle }: StepperProps) {
  const toast = useToast()
  const info = useDesiredCountInfo(serviceId)
  const setDesiredCount = useSetDesiredCount(serviceId)
  const [draft, setDraft] = useState(String(service.desired))
  const [confirming, setConfirming] = useState(false)
  // Follow the service's count when it changes underneath us (a poll, or an applied change).
  const [seenDesired, setSeenDesired] = useState(service.desired)
  if (seenDesired !== service.desired) {
    setSeenDesired(service.desired)
    setDraft(String(service.desired))
  }

  const max = info.data?.max
  const autoScaling = info.data?.auto_scaling ?? null
  // An empty field parses to NaN, which fails the integer check and disables Apply.
  const value = draft.trim() === '' ? Number.NaN : Number(draft)
  const valid = Number.isInteger(value) && value >= 0 && (max === undefined || value <= max)
  const dirty = draft !== String(service.desired)
  const outsideAutoScaling = valid && autoScaling !== null && (value < autoScaling.min || value > autoScaling.max)
  // Steps start from the field's current number, or from the service's count when the field is empty or invalid.
  const stepFrom = valid ? value : service.desired
  const locked = disabledTitle !== undefined
  const taskWord = value === 1 ? 'task' : 'tasks'

  const apply = () =>
    setDesiredCount.mutate(
      { desired: value },
      {
        onSuccess: (result) => {
          toast.show(`Desired count set to ${value}.`, 'ok')
          showWarning(toast, result.warning)
          setConfirming(false)
        },
        onError: (error) => {
          toast.show(errorMessage(error, 'Desired count could not be changed.'), 'bad')
          setConfirming(false)
        },
      },
    )

  return (
    <>
      <div className={stepperStyles.stepper} role="group" aria-label="Desired count" title={disabledTitle}>
        <span className={stepperStyles.stepperLabel}>DESIRED</span>
        <button
          type="button"
          aria-label="Decrease desired count"
          disabled={locked || stepFrom <= 0}
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
          disabled={locked}
          onChange={(event) => setDraft(event.target.value)}
        />
        <button
          type="button"
          aria-label="Increase desired count"
          disabled={locked || (max !== undefined && stepFrom >= max)}
          onClick={() => setDraft(String(stepFrom + 1))}
        >
          +
        </button>
        {dirty && !locked && (
          <button type="button" className={stepperStyles.apply} disabled={!valid} onClick={() => setConfirming(true)}>
            Apply
          </button>
        )}
      </div>
      {outsideAutoScaling && autoScaling && (
        <p className={stepperStyles.scalingWarn} role="status">
          Auto scaling keeps this service between {autoScaling.min} and {autoScaling.max}. It will move {value} back into that range.{' '}
          <Link className={styles.accentLink} to={`/services/${serviceId}/scaling`}>
            Auto scaling settings →
          </Link>
        </p>
      )}
      {confirming && (
        <ConfirmDialog
          title={`Set ${service.name} to ${valid ? `${value} ${taskWord}` : 'a valid number of tasks'}?`}
          sub={`Now ${service.desired}.${valid && value === 0 ? ' Every task stops. The service and its definition stay.' : ''}`}
          confirmLabel="Update desired count"
          confirmDisabled={!valid || setDesiredCount.isPending}
          onConfirm={apply}
          onCancel={() => setConfirming(false)}
        />
      )}
    </>
  )
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback
}
