import { useState, type ReactNode } from 'react'
import { Link } from 'react-router'
import { useDesiredCountInfo, useSetDesiredCount, type DesiredCountInfo, type DesiredCountResult } from '../../../api/change'
import { useScaling } from '../../../api/observe'
import { useService } from '../../../api/hooks'
import type { ScalingDTO, ScalingPolicyDTO } from '../../../api/types'
import { DataBanner } from '../../../components/DataBanner'
import { Panel } from '../../../components/Panel'
import type { PanelErrorProps } from '../../../components/PanelError'
import { queryPanelError } from '../panelError'
import { EmptyState } from '../../../components/EmptyState'
import { Feed, FeedRow } from '../../../components/Feed'
import { KV } from '../../../components/KV'
import { useToast } from '../../../components/Toast'
import type { ServiceDTO } from '../../../api/types'
import { DesiredCountDialog } from '../DesiredCountDialog'
import styles from './ScalingTab.module.css'

type SaveDesired = (desired: number, onSettled: () => void) => void

/** The desired count as a whole number of tasks, or null while the draft is blank or not a count. */
function parseCount(draft: string): number | null {
  return /^\d+$/.test(draft.trim()) ? Number(draft) : null
}

function describePolicy(policy: ScalingPolicyDTO): ReactNode {
  if (policy.metric.startsWith('ECSServiceAverage')) {
    return (
      <>
        Target tracking · <span className={styles.mono}>{policy.metric}</span> at {policy.target_value}%
      </>
    )
  }
  return `Step scaling · ${policy.name}`
}

const isUnreadable = (scaling: ScalingDTO, part: string) => scaling.unreadable_parts.includes(part)

function describeScheduled(scaling: ScalingDTO): string {
  const known = scaling.scheduled_actions.map((action) => `${action.name}: ${action.schedule} → min ${action.min_capacity ?? '–'}`).join('; ')
  if (isUnreadable(scaling, 'scheduled_actions')) {
    return known ? `Scheduled actions could not be read. Last known: ${known}` : 'Scheduled actions could not be read'
  }
  return known || 'None'
}

/** The service's Scaling tab: the desired-count stepper and the read-only auto scaling set in Terraform. */
export function ScalingTab({ serviceId }: { serviceId: string }) {
  const service = useService(serviceId)
  const info = useDesiredCountInfo(serviceId)
  const scaling = useScaling(serviceId)
  const setDesired = useSetDesiredCount(serviceId)
  const toast = useToast()

  // The service and its desired count are the page's core; the scaling snapshot may fail on its own and only blanks its panels.
  const failed = [service, info].find((query) => query.isError)
  if (failed) {
    return <EmptyState>{failed.error instanceof Error ? failed.error.message : 'The scaling settings could not be loaded.'}</EmptyState>
  }
  if (!service.data || !info.data) return null
  const scalingError = queryPanelError(scaling, 'Auto scaling could not be read.')
  const scalingLoading = scaling.data === undefined && !scalingError

  const saveDesired: SaveDesired = (desired, onSettled) =>
    setDesired.mutate({ desired }, {
      onSuccess: (result: DesiredCountResult) => {
        toast.show(
          result.changed ? `Desired count set to ${result.desired}.` : `Desired count is already ${result.desired}.`,
          result.changed ? 'ok' : 'info',
        )
        onSettled()
      },
      onError: (caught) => {
        toast.show(caught.message, 'bad')
        onSettled()
      },
    })

  const activities = scaling.data?.activities ?? []
  return (
    <>
      <DataBanner tiers={['Auto scaling']} />
      <div className={styles.grid}>
        <DesiredCountCard info={info.data} scaling={scaling.data} service={service.data} saving={setDesired.isPending} onSave={saveDesired} />
        <AutoScalingCard
          scaling={scaling.data}
          error={scalingError}
          loading={scalingLoading}
          serviceId={serviceId}
          canOperate={service.data.can.operate}
        />
      </div>
      <Panel title="Scaling activity" flush error={scalingError} loading={scalingLoading}>
        {activities.length === 0 ? (
          <EmptyState>No scaling activity in the last 7 days.</EmptyState>
        ) : (
          <Feed>
            {[...activities]
              .sort((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at))
              .map((activity) => (
                <FeedRow key={activity.id} ts={activity.started_at} source="ecs" actor="ecs">
                  {activity.status_message || activity.description}
                </FeedRow>
              ))}
          </Feed>
        )}
      </Panel>
    </>
  )
}

interface DesiredCountCardProps {
  info: DesiredCountInfo
  /** Undefined while the scaling snapshot is loading or failed: the range hints are then left out. */
  scaling?: ScalingDTO
  service: ServiceDTO
  saving: boolean
  onSave: SaveDesired
}

function DesiredCountCard({ info, scaling, service, saving, onSave }: DesiredCountCardProps) {
  const [draft, setDraft] = useState(String(info.desired))
  const [confirming, setConfirming] = useState(false)
  // Follow the running count when it changes (a poll, or a saved change) without dropping an edit in progress
  // or an open confirm dialog: only a draft still equal to the previous server value is resynced.
  const [seenDesired, setSeenDesired] = useState(info.desired)
  if (seenDesired !== info.desired) {
    setSeenDesired(info.desired)
    if (!confirming && draft === String(seenDesired)) setDraft(String(info.desired))
  }
  const canOperate = service.can.operate
  const tagName = service.tag.name
  const parsed = parseCount(draft)
  // A count above the service's maximum is not a valid draft: it cannot be applied or confirmed.
  const draftCount = parsed !== null && parsed <= info.max ? parsed : null
  const outOfRange = parsed !== null && draftCount === null
  const outsideAutoScaling = draftCount !== null && scaling?.has_target && (draftCount < scaling.min || draftCount > scaling.max)

  const step = (delta: number) => {
    const base = draftCount ?? info.desired
    setDraft(String(Math.min(info.max, Math.max(0, base + delta))))
  }

  const autoScalingNote = !scaling
    ? ''
    : scaling.has_target
      ? `Auto scaling keeps this service between ${scaling.min} and ${scaling.max} tasks and may change a value outside that range back.`
      : 'No auto scaling: the count stays where you set it.'

  return (
    <Panel title="Desired count">
      {scaling?.has_target && <RangeBar scaling={scaling} desired={draftCount ?? info.desired} />}
      <div className={styles.toolbar}>
        <button type="button" className={styles.icon} aria-label="Fewer tasks" onClick={() => step(-1)}>
          −
        </button>
        <input
          className={styles.count}
          type="number"
          min={0}
          max={info.max}
          value={draft}
          aria-label="Scale to"
          onChange={(event) => setDraft(event.target.value)}
        />
        <button type="button" className={styles.icon} aria-label="More tasks" onClick={() => step(1)}>
          +
        </button>
        <button
          type="button"
          className={styles.btn}
          disabled={!canOperate || draftCount === null || saving}
          title={canOperate ? undefined : `You need Operate on ${tagName}`}
          onClick={() => setConfirming(true)}
        >
          Update desired count
        </button>
      </div>
      {outsideAutoScaling && <p className={styles.warning}>{`Outside the auto scaling range ${scaling.min}–${scaling.max}.`}</p>}
      {outOfRange && <p className={styles.warning}>{`The most this service can run is ${info.max} tasks.`}</p>}
      {confirming && (
        <DesiredCountDialog
          service={service}
          count={draftCount}
          pending={saving}
          onConfirm={() => draftCount !== null && onSave(draftCount, () => setConfirming(false))}
          onCancel={() => setConfirming(false)}
        />
      )}
      <p className={styles.hint}>
        {`Running ${info.running} of ${info.desired}. ${autoScalingNote} Setting 0 stops every task without deleting the service.`}
      </p>
    </Panel>
  )
}

/** Track from 0 to the larger of max and desired, with the auto scaling min-max span shaded and the desired count marked. */
function RangeBar({ scaling, desired }: { scaling: ScalingDTO; desired: number }) {
  const top = Math.max(scaling.max, desired, 1)
  const pct = (count: number) => `${(count / top) * 100}%`
  return (
    <div
      className={styles.rangeBar}
      role="img"
      aria-label={`Auto scaling range ${scaling.min} to ${scaling.max} tasks, desired ${desired}`}
    >
      <span className={styles.rangeSpan} style={{ left: pct(scaling.min), width: `calc(${pct(scaling.max)} - ${pct(scaling.min)})` }} />
      <span className={styles.rangeMark} style={{ left: pct(desired) }} />
      <span className={styles.rangeScale}>
        <span>0</span>
        <span>{`min ${scaling.min}`}</span>
        <span>{`max ${scaling.max}`}</span>
      </span>
    </div>
  )
}

interface AutoScalingCardProps {
  scaling?: ScalingDTO
  error?: PanelErrorProps
  loading: boolean
  serviceId: string
  canOperate: boolean
}

function AutoScalingCard({ scaling, error, loading, serviceId, canOperate }: AutoScalingCardProps) {
  const actions = (
    <>
      {canOperate && (
        <Link className={styles.editLink} to={`/services/${serviceId}/deploy`}>
          Edit in Deploy →
        </Link>
      )}
      Application Auto Scaling · set in Terraform
    </>
  )
  if (!scaling) return <Panel title="Auto scaling" actions={actions} error={error} loading={loading}>{null}</Panel>
  if (!scaling.has_target && isUnreadable(scaling, 'scaling_target')) {
    return (
      <Panel title="Auto scaling" actions={actions}>
        <EmptyState>The scalable target could not be read.</EmptyState>
      </Panel>
    )
  }
  if (!scaling.has_target) {
    return (
      <Panel title="Auto scaling" actions={actions}>
        <EmptyState>This service has no scalable target registered.</EmptyState>
      </Panel>
    )
  }
  const rows: [ReactNode, ReactNode][] = [
    ['Minimum tasks', scaling.min],
    ['Maximum tasks', scaling.max],
    ...(isUnreadable(scaling, 'scaling_policies') ? [['Policy', 'Scaling policies could not be read'] as [ReactNode, ReactNode]] : []),
    ...scaling.policies.map((policy): [ReactNode, ReactNode] => ['Policy', describePolicy(policy)]),
    ...scaling.policies.flatMap((policy): [ReactNode, ReactNode][] => [
      ['Scale-out cooldown', `${policy.scale_out_cooldown} s`],
      ['Scale-in cooldown', `${policy.scale_in_cooldown} s`],
    ]),
    ['Scheduled actions', describeScheduled(scaling)],
  ]
  return (
    <Panel title="Auto scaling" actions={actions}>
      <KV rows={rows} />
    </Panel>
  )
}
