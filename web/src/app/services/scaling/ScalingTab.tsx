import { useState, type ReactNode } from 'react'
import { useDesiredCountInfo, useSetDesiredCount, type DesiredCountInfo, type DesiredCountResult } from '../../../api/change'
import { useScaling } from '../../../api/observe'
import { useService } from '../../../api/hooks'
import type { ScalingDTO, ScalingPolicyDTO } from '../../../api/types'
import { Card } from '../../../components/Card'
import { EmptyState } from '../../../components/EmptyState'
import { Feed, FeedRow } from '../../../components/Feed'
import { KV } from '../../../components/KV'
import { useToast } from '../../../components/Toast'
import styles from './ScalingTab.module.css'

type SaveDesired = (desired: number) => void

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

function describeScheduled(scaling: ScalingDTO): string {
  if (scaling.scheduled_actions.length === 0) return 'None'
  return scaling.scheduled_actions.map((action) => `${action.name}: ${action.schedule} → min ${action.min_capacity ?? '–'}`).join('; ')
}

/** The service's Scaling tab: the desired-count stepper and the read-only auto scaling set in Terraform. */
export function ScalingTab({ serviceId }: { serviceId: string }) {
  const service = useService(serviceId)
  const info = useDesiredCountInfo(serviceId)
  const scaling = useScaling(serviceId)
  const setDesired = useSetDesiredCount(serviceId)
  const toast = useToast()

  const failed = [service, info, scaling].find((query) => query.isError)
  if (failed) {
    return <EmptyState>{failed.error instanceof Error ? failed.error.message : 'The scaling settings could not be loaded.'}</EmptyState>
  }
  if (!service.data || !info.data || !scaling.data) return null

  const saveDesired: SaveDesired = (desired) =>
    setDesired.mutate({ desired }, {
      onSuccess: (result: DesiredCountResult) =>
        toast.show(
          result.changed ? `Desired count set to ${result.desired}.` : `Desired count is already ${result.desired}.`,
          result.changed ? 'ok' : 'info',
        ),
      onError: (caught) => toast.show(caught.message, 'bad'),
    })

  return (
    <>
      <div className={styles.grid}>
        <DesiredCountCard
          // Remounts with the server's count after a save, so the draft always starts from what is running.
          key={info.data.desired}
          info={info.data}
          scaling={scaling.data}
          canOperate={service.data.can.operate}
          tagName={service.data.tag.name}
          saving={setDesired.isPending}
          onSave={saveDesired}
        />
        <AutoScalingCard scaling={scaling.data} />
      </div>
      <div className={styles.activity}>
        <Card title="Scaling activity">
          {scaling.data.activities.length === 0 ? (
            <EmptyState>No scaling activity in the last 7 days.</EmptyState>
          ) : (
            <Feed>
              {[...scaling.data.activities]
                .sort((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at))
                .map((activity) => (
                  <FeedRow key={activity.id} ts={activity.started_at} source="ecs" actor="ecs">
                    {activity.status_message || activity.description}
                  </FeedRow>
                ))}
            </Feed>
          )}
        </Card>
      </div>
    </>
  )
}

interface DesiredCountCardProps {
  info: DesiredCountInfo
  scaling: ScalingDTO
  canOperate: boolean
  tagName: string
  saving: boolean
  onSave: SaveDesired
}

function DesiredCountCard({ info, scaling, canOperate, tagName, saving, onSave }: DesiredCountCardProps) {
  const [draft, setDraft] = useState(String(info.desired))
  const draftCount = parseCount(draft)
  const outsideAutoScaling = draftCount !== null && scaling.has_target && (draftCount < scaling.min || draftCount > scaling.max)

  const step = (delta: number) => {
    const base = draftCount ?? info.desired
    setDraft(String(Math.min(info.max, Math.max(0, base + delta))))
  }

  const autoScalingNote = scaling.has_target
    ? `Auto scaling keeps this service between ${scaling.min} and ${scaling.max} tasks and may change a value outside that range back.`
    : 'No auto scaling: the count stays where you set it.'

  return (
    <Card title="Desired count">
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
          aria-label="Desired count"
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
          onClick={() => {
            if (draftCount !== null) onSave(draftCount)
          }}
        >
          Update desired count
        </button>
      </div>
      {outsideAutoScaling && <p className={styles.warning}>{`Outside the auto scaling range ${scaling.min}–${scaling.max}.`}</p>}
      <p className={styles.hint}>
        {`Running ${info.running} of ${info.desired}. ${autoScalingNote} Setting 0 stops every task without deleting the service.`}
      </p>
    </Card>
  )
}

function AutoScalingCard({ scaling }: { scaling: ScalingDTO }) {
  if (!scaling.has_target) {
    return (
      <Card title="Auto scaling" right="Application Auto Scaling · set in Terraform">
        <EmptyState>This service has no scalable target registered.</EmptyState>
      </Card>
    )
  }
  const rows: [ReactNode, ReactNode][] = [
    ['Minimum tasks', scaling.min],
    ['Maximum tasks', scaling.max],
    ...scaling.policies.map((policy): [ReactNode, ReactNode] => ['Policy', describePolicy(policy)]),
    ...scaling.policies.flatMap((policy): [ReactNode, ReactNode][] => [
      ['Scale-out cooldown', `${policy.scale_out_cooldown} s`],
      ['Scale-in cooldown', `${policy.scale_in_cooldown} s`],
    ]),
    ['Scheduled actions', describeScheduled(scaling)],
  ]
  return (
    <Card title="Auto scaling" right="Application Auto Scaling · set in Terraform">
      <KV rows={rows} />
    </Card>
  )
}
