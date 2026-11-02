import { useState } from 'react'
import { Link } from 'react-router'
import { useDesiredCountInfo, useSetDesiredCount } from '../../api/change'
import type { ServiceDTO } from '../../api/types'
import { showWarning, useToast } from '../../components/Toast'
import styles from './service.module.css'
import { DesiredCountDialog } from './DesiredCountDialog'
import { RestartDialog, restartBlockedTitle } from './RestartDialog'
import stepperStyles from './HeaderActions.module.css'

interface HeaderActionsProps {
  serviceId: string
  service: ServiceDTO
}

/** Inline desired-count stepper (with Apply), Restart and AWS console for the sticky service header. Changes go through confirm dialogs. */
export function HeaderActions({ serviceId, service }: HeaderActionsProps) {
  const [restarting, setRestarting] = useState(false)

  const operateTitle = service.can.operate ? undefined : `You need Operate on ${service.tag.name}`
  const restartBlocked = restartBlockedTitle(service)

  return (
    <>
      <DesiredStepper serviceId={serviceId} service={service} disabledTitle={operateTitle} />
      <button
        type="button"
        className={styles.btn}
        aria-label="Restart (new deployment)"
        disabled={restartBlocked !== undefined}
        title={restartBlocked}
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
  // Follow the service's count when it changes underneath us (a poll, or an applied change), but never
  // overwrite an edit in progress: only a draft still equal to the previous server value is resynced.
  const [seenDesired, setSeenDesired] = useState(service.desired)
  if (seenDesired !== service.desired) {
    setSeenDesired(service.desired)
    if (!confirming && draft === String(seenDesired)) setDraft(String(service.desired))
  }

  const max = info.data?.max
  const autoScaling = info.data?.auto_scaling ?? null
  // An empty field parses to NaN, which fails the integer check and disables Apply.
  const value = draft.trim() === '' ? Number.NaN : Number(draft)
  const valid = Number.isInteger(value) && value >= 0 && (max === undefined || value <= max)
  const dirty = value !== service.desired
  const outsideAutoScaling = valid && autoScaling !== null && (value < autoScaling.min || value > autoScaling.max)
  // Steps start from the field's current number, or from the service's count when the field is empty or invalid.
  const stepFrom = valid ? value : service.desired
  const locked = disabledTitle !== undefined

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
        <DesiredCountDialog
          service={service}
          count={valid ? value : null}
          pending={setDesiredCount.isPending}
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
