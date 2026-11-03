import { useState } from 'react'
import { isApiError, useDeploy } from '../../../api/change'
import type { DeploymentDTO, ServiceDTO } from '../../../api/types'
import { Ago } from '../../../components/Ago'
import { ConfirmDialog } from '../../../components/ConfirmDialog'
import { Pill } from '../../../components/Pill'
import { showWarning, useToast } from '../../../components/Toast'
import styles from './RolloutProgress.module.css'

const COMPLETED_VISIBLE_MS = 60_000

const primaryOf = (service: ServiceDTO): DeploymentDTO | undefined =>
  service.deployments.find((deployment) => deployment.status === 'PRIMARY') ?? service.deployments[0]

const ageOf = (deployment: DeploymentDTO) => deployment.updated_at || deployment.created_at

/** True while a rollout is running, and for a minute after it completes so the final state stays visible. */
export function shouldShowProgress(service: ServiceDTO, now: number): boolean {
  const primary = primaryOf(service)
  if (!primary) return false
  if (service.deployments.some((deployment) => deployment.rollout_state === 'IN_PROGRESS')) return true
  if (primary.rollout_state !== 'COMPLETED') return false
  const finishedAt = Date.parse(ageOf(primary))
  return Number.isFinite(finishedAt) && now - finishedAt <= COMPLETED_VISIBLE_MS
}

/** The older revision still serving tasks, to return to while the newest one is rolling out or has failed. */
function rollbackTarget(service: ServiceDTO, primary: DeploymentDTO): DeploymentDTO | undefined {
  if (primary.rollout_state !== 'IN_PROGRESS' && primary.rollout_state !== 'FAILED') return undefined
  return service.deployments.find((deployment) => deployment.id !== primary.id && deployment.revision < primary.revision)
}

const plural = (count: number, noun: string) => `${count} ${noun}${count === 1 ? '' : 's'}`

interface RolloutProgressProps {
  serviceId: string
  service: ServiceDTO
  /** Without Deploy the panel is read-only: the rollback button is not rendered. */
  canDeploy: boolean
  /** Effective tag name, used in the failure wording. */
  tagName: string
}

export function RolloutProgress({ serviceId, service, canDeploy, tagName }: RolloutProgressProps) {
  const deploy = useDeploy(serviceId)
  const toast = useToast()
  const [confirming, setConfirming] = useState(false)
  const primary = primaryOf(service)
  if (!primary) return null

  const desired = Math.max(1, service.desired)
  const target = rollbackTarget(service, primary)
  const running = primary.rollout_state === 'IN_PROGRESS'
  const failed = primary.rollout_state === 'FAILED'
  const newCount = primary.running_count
  const oldCount = target?.running_count ?? 0

  const rollBack = async () => {
    if (!target) return
    try {
      const started = await deploy.mutateAsync({ revision: target.revision, interrupt: true })
      showWarning(toast, started.warning)
    } catch (failure) {
      if (isApiError(failure, 'forbidden')) toast.show(`You need Deploy on ${tagName} to roll back.`, 'bad')
      else if (isApiError(failure, 'conflict')) toast.show('The service changed meanwhile; check the rollout and try again.', 'bad')
      else toast.show(failure instanceof Error ? failure.message : 'The rollback could not be started.', 'bad')
    } finally {
      setConfirming(false)
    }
  }

  return (
    <section className={styles.progress} aria-label="Rollout progress">
      <div className={styles.head}>
        {running ? <Pill kind="info">Rollout in progress</Pill> : failed ? <Pill kind="bad">Failed</Pill> : <Pill kind="ok">Completed</Pill>}
        <span className={styles.revisions}>{target && running ? `:${target.revision} → :${primary.revision}` : `${service.name}:${primary.revision}`}</span>
        <span className={styles.age}>
          {running ? 'started ' : ''}
          <Ago ts={running ? primary.created_at : ageOf(primary)} />
        </span>
      </div>
      <div className={styles.track} role="presentation">
        <span className={styles.fresh} style={{ width: `${Math.min(100, (newCount / desired) * 100)}%` }} />
        <span className={styles.stale} style={{ width: `${Math.min(100, (oldCount / desired) * 100)}%` }} />
      </div>
      <div className={styles.detail}>
        {running && (
          <span>
            {plural(newCount, 'new task')} on :{primary.revision}
            {target && <> · {oldCount} old on :{target.revision}</>}
          </span>
        )}
        {!running && !failed && (
          <span>
            {plural(newCount, 'task')} on :{primary.revision}
          </span>
        )}
        {primary.failed_tasks > 0 && <span className={styles.danger}>{primary.failed_tasks} failed to start</span>}
        {failed && primary.rollout_state_reason && <span className={styles.danger}>{primary.rollout_state_reason}</span>}
        {canDeploy && target && (
          <button type="button" className={styles.rollback} onClick={() => setConfirming(true)}>
            {`Roll back to :${target.revision}`}
          </button>
        )}
      </div>
      {confirming && target && (
        <ConfirmDialog
          title={`Roll back ${service.name} to :${target.revision}?`}
          sub={`Stops the rollout of :${primary.revision} and returns the service to :${target.revision}.`}
          confirmLabel={`Roll back to :${target.revision}`}
          danger
          confirmDisabled={deploy.isPending}
          onConfirm={() => void rollBack()}
          onCancel={() => setConfirming(false)}
        />
      )}
    </section>
  )
}
