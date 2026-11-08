import type { ReactNode } from 'react'
import type { DeploySettingsResponse } from '../../../api/deploy'
import { LockNote } from '../../../components/LockNote'
import { Panel } from '../../../components/Panel'
import { Toggle } from '../../../components/Toggle'
import stepperStyles from '../HeaderActions.module.css'
import { isValidDesiredCount, type ServiceDraft } from './serviceDraft'
import styles from './editors.module.css'

interface ServicePanelProps {
  settings: DeploySettingsResponse
  draft: ServiceDraft
  onChange: (draft: ServiceDraft) => void
  /** Name of the service's tag, for the "You need Operate on ..." lock titles. */
  tagName: string
  /** The image scanning setting, which saves at once and so is not part of the draft. Absent when the panel is shown without it. */
  scanSetting?: ReactNode
}

const EXEC_ACTIONS = ['CreateControlChannel', 'CreateDataChannel', 'OpenControlChannel', 'OpenDataChannel'].map((action) => `ssmmessages:${action}`)

const parseNumber = (text: string): number | null => {
  if (text.trim() === '') return null
  const value = Number(text)
  return Number.isNaN(value) ? null : value
}

/** A toggle with a lock reason shown as its title; the title lives on a wrapper because disabled inputs swallow hover. */
function LockedToggle({ lock, ...props }: { lock?: string; checked: boolean; onChange: (checked: boolean) => void; disabled?: boolean; children: ReactNode }) {
  return (
    <span title={lock}>
      <Toggle {...props} disabled={props.disabled || lock !== undefined} />
    </span>
  )
}

/** Desired count, ECS Exec, rollout safety and force-new-deployment, edited as a draft (nothing is sent from here). */
export function ServicePanel({ settings, draft, onChange, tagName, scanSetting }: ServicePanelProps) {
  const update = (patch: Partial<ServiceDraft>) => onChange({ ...draft, ...patch })
  const operateLock = settings.can.operate ? undefined : `You need Operate on ${tagName}`
  const deployLock = settings.can.deploy ? undefined : `You need Deploy on ${tagName}`
  const countValid = isValidDesiredCount(settings, draft.desiredCount)
  // Steps start from the field's number, or from the live count when the field is empty or out of range.
  const stepFrom = isValidDesiredCount(settings, draft.desiredCount) ? draft.desiredCount : settings.desired_count
  const graceLock = deployLock ?? (settings.has_load_balancer ? undefined : 'Only services behind a load balancer have a health check grace period')
  const breakerOn = draft.circuitBreakerEnable

  return (
    <Panel title="Service" sub={<span> · {settings.running_count} running</span>}>
      <div className={styles.stack}>
        {/* Phones have no hover: each lock is stated once at the top instead of in tooltips. */}
        <LockNote>{operateLock}</LockNote>
        <LockNote>{deployLock}</LockNote>
        <div className={styles.group}>
          <div className={stepperStyles.stepper} role="group" aria-label="Desired count" title={operateLock} style={{ alignSelf: 'flex-start' }}>
            <span className={stepperStyles.stepperLabel}>DESIRED</span>
            <button
              type="button"
              aria-label="Decrease desired count"
              disabled={operateLock !== undefined || stepFrom <= 0}
              onClick={() => update({ desiredCount: Math.max(0, stepFrom - 1) })}
            >
              −
            </button>
            <input
              type="number"
              aria-label="Desired count"
              min={0}
              max={settings.max_desired}
              value={draft.desiredCount ?? ''}
              disabled={operateLock !== undefined}
              onChange={(event) => update({ desiredCount: parseNumber(event.target.value) })}
            />
            <button
              type="button"
              aria-label="Increase desired count"
              disabled={operateLock !== undefined || stepFrom >= settings.max_desired}
              onClick={() => update({ desiredCount: stepFrom + 1 })}
            >
              +
            </button>
          </div>
          {!countValid && <p className={styles.error}>Desired count must be a whole number between 0 and {settings.max_desired}.</p>}
        </div>

        <div className={styles.group}>
          <h3 className={styles.groupTitle}>ECS Exec</h3>
          <LockedToggle lock={deployLock} checked={draft.enableExecuteCommand} onChange={(checked) => update({ enableExecuteCommand: checked })}>
            ECS Exec
          </LockedToggle>
          <p className={styles.hint}>Applies to new tasks only: turning it on or off restarts the service's tasks</p>
          {settings.task_role_arn === '' ? (
            <p className={styles.warn} role="status">
              This task definition has no task role, and ECS Exec needs one with {EXEC_ACTIONS.join(', ')}. Add a task role in the task definition before relying on Exec.
            </p>
          ) : (
            <p className={styles.hint}>The task role needs {EXEC_ACTIONS.join(', ')}.</p>
          )}
        </div>

        <div className={styles.group}>
          <h3 className={styles.groupTitle}>Rollout safety</h3>
          <LockedToggle
            lock={deployLock}
            checked={breakerOn}
            onChange={(checked) => update({ circuitBreakerEnable: checked, circuitBreakerRollback: checked && draft.circuitBreakerRollback })}
          >
            Circuit breaker
          </LockedToggle>
          <span title={deployLock ?? (breakerOn ? undefined : 'Turn on the circuit breaker to roll back automatically')}>
            <Toggle
              checked={breakerOn && draft.circuitBreakerRollback}
              disabled={deployLock !== undefined || !breakerOn}
              onChange={(checked) => update({ circuitBreakerRollback: checked })}
            >
              Roll back on failure
            </Toggle>
          </span>
          <LockNote kind="why">{deployLock === undefined && !breakerOn ? 'Turn on the circuit breaker to roll back automatically' : undefined}</LockNote>
          <div className={styles.fieldRow}>
            <label className={styles.field} title={deployLock}>
              Minimum healthy percent
              <input
                className={styles.input}
                type="number"
                min={0}
                max={100}
                value={draft.minimumHealthyPercent ?? ''}
                disabled={deployLock !== undefined}
                onChange={(event) => update({ minimumHealthyPercent: parseNumber(event.target.value) })}
              />
            </label>
            <label className={styles.field} title={deployLock}>
              Maximum percent
              <input
                className={styles.input}
                type="number"
                min={100}
                max={200}
                value={draft.maximumPercent ?? ''}
                disabled={deployLock !== undefined}
                onChange={(event) => update({ maximumPercent: parseNumber(event.target.value) })}
              />
            </label>
            <label className={styles.field} title={graceLock}>
              Health check grace period (seconds)
              <input
                className={styles.input}
                type="number"
                min={0}
                value={draft.gracePeriodSeconds ?? ''}
                disabled={graceLock !== undefined}
                onChange={(event) => update({ gracePeriodSeconds: parseNumber(event.target.value) })}
              />
            </label>
          </div>
          <LockNote kind="why">{deployLock === undefined ? graceLock : undefined}</LockNote>
          {draft.minimumHealthyPercent !== null && (draft.minimumHealthyPercent < 0 || draft.minimumHealthyPercent > 100) && (
            <p className={styles.error}>Minimum healthy percent must be between 0 and 100.</p>
          )}
          {draft.maximumPercent !== null && (draft.maximumPercent < 100 || draft.maximumPercent > 200) && (
            <p className={styles.error}>Maximum percent must be between 100 and 200.</p>
          )}
        </div>

        <div className={styles.group}>
          <LockedToggle lock={operateLock} checked={draft.forceNewDeployment} onChange={(checked) => update({ forceNewDeployment: checked })}>
            Force new deployment
          </LockedToggle>
          <p className={styles.hint}>Replaces every running task with a fresh one from the same task definition.</p>
        </div>
        {scanSetting}
      </div>
    </Panel>
  )
}
