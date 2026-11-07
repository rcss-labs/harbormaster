import { useState } from 'react'
import type { DeploySettingsResponse } from '../../../api/deploy'
import { DataTable, type DataTableColumn } from '../../../components/DataTable'
import { Panel } from '../../../components/Panel'
import { PanelError } from '../../../components/PanelError'
import { METRIC_LABELS, PolicyRowEditor } from './PolicyRowEditor'
import { ScheduledActionEditor } from './ScheduledActionEditor'
import { plural } from './format'
import { scalingDraftFromSettings, type PolicyDraft, type ScalingDraft, type ScheduledActionDraft } from './scalingDraft'
import styles from './editors.module.css'

interface ScalingPanelProps {
  settings: DeploySettingsResponse
  draft: ScalingDraft
  onChange: (draft: ScalingDraft) => void
  /** The service draft's desired count (null while the field is empty), to warn when auto scaling would move it. */
  desiredCount: number | null
  tagName: string
  /** Refetches the deploy settings; offered as Retry where a part of them could not be read. */
  onRetry: () => void
}

type Editing = { kind: 'new' } | { kind: 'edit'; name: string } | null

function policyMetricLabel(policy: PolicyDraft): string {
  if (policy.type === 'StepScaling') return 'Step scaling'
  if (policy.metric === 'other') return policy.metric_raw ? `Other (${policy.metric_raw})` : 'Other'
  return METRIC_LABELS[policy.metric]
}

const parseNumber = (text: string): number | null => {
  if (text.trim() === '') return null
  const value = Number(text)
  return Number.isNaN(value) ? null : value
}

/** Scalable target, target-tracking policies and scheduled actions, edited as a draft. Every edit needs Operate. */
export function ScalingPanel({ settings, draft, onChange, desiredCount, tagName, onRetry }: ScalingPanelProps) {
  const [editingPolicy, setEditingPolicy] = useState<Editing>(null)
  const [editingAction, setEditingAction] = useState<Editing>(null)
  const lock = settings.can.operate ? undefined : `You need Operate on ${tagName}`
  const locked = lock !== undefined
  // Parts the server could not read (settings.partial): only the editor of that part is blocked.
  const unreadable = (part: string) => settings.partial.find((entry) => entry.part === part)
  const scheduledUnreadable = unreadable('scheduled_actions')
  const policiesUnreadable = unreadable('scaling_policies')
  const scheduledLock = lock ?? (scheduledUnreadable ? 'Scheduled actions could not be read' : undefined)
  const policiesLock = lock ?? (policiesUnreadable ? 'Scaling policies could not be read' : undefined)
  // Removing the target also removes what could not be read, so it waits for a successful read.
  const removeLock = scheduledLock ?? policiesLock

  const addTarget = () => {
    const current = desiredCount ?? settings.desired_count
    const min = Math.min(1, current)
    onChange({ ...draft, hasTarget: true, min, max: Math.min(settings.max_desired, Math.max(current * 2, 1)) })
  }
  const removeTarget = () => {
    setEditingPolicy(null)
    setEditingAction(null)
    onChange({ hasTarget: false, min: null, max: null, policies: [], scheduledActions: [] })
  }
  const keepTarget = () => onChange(scalingDraftFromSettings(settings))

  const savePolicy = (policy: PolicyDraft) => {
    const exists = draft.policies.some((candidate) => candidate.name === policy.name)
    onChange({ ...draft, policies: exists ? draft.policies.map((candidate) => (candidate.name === policy.name ? policy : candidate)) : [...draft.policies, policy] })
    setEditingPolicy(null)
  }
  const saveAction = (action: ScheduledActionDraft) => {
    const exists = draft.scheduledActions.some((candidate) => candidate.name === action.name)
    onChange({
      ...draft,
      scheduledActions: exists ? draft.scheduledActions.map((candidate) => (candidate.name === action.name ? action : candidate)) : [...draft.scheduledActions, action],
    })
    setEditingAction(null)
  }

  const policyColumns: DataTableColumn<PolicyDraft>[] = [
    { key: 'name', header: 'Name', width: '1.4fr', render: (policy) => <span className={styles.mono}>{policy.name}</span> },
    { key: 'metric', header: 'Metric', width: '1.4fr', render: policyMetricLabel },
    { key: 'target', header: 'Target', width: '70px', align: 'right', render: (policy) => (policy.editable ? policy.target_value : '–') },
    {
      key: 'cooldowns',
      header: 'Cooldowns',
      width: '1.2fr',
      render: (policy) => (policy.editable ? `in ${policy.scale_in_cooldown} s · out ${policy.scale_out_cooldown} s` : '–'),
    },
    { key: 'scaleIn', header: 'Scale-in', width: '80px', render: (policy) => (policy.disable_scale_in ? 'Disabled' : 'On') },
    {
      key: 'actions',
      header: '',
      width: '130px',
      render: (policy) => (
        <span className={styles.rowActions}>
          {policy.editable && (
            <button type="button" className={styles.btn} aria-label={`Edit ${policy.name}`} disabled={policiesLock !== undefined} title={policiesLock} onClick={() => setEditingPolicy({ kind: 'edit', name: policy.name })}>
              Edit
            </button>
          )}
          <button
            type="button"
            className={`${styles.btn} ${styles.danger}`}
            aria-label={`Remove ${policy.name}`}
            disabled={policiesLock !== undefined}
            title={policiesLock}
            onClick={() => onChange({ ...draft, policies: draft.policies.filter((candidate) => candidate.name !== policy.name) })}
          >
            Remove
          </button>
        </span>
      ),
    },
  ]

  const actionColumns: DataTableColumn<ScheduledActionDraft>[] = [
    { key: 'name', header: 'Name', width: '1.2fr', render: (action) => <span className={styles.mono}>{action.name}</span> },
    { key: 'schedule', header: 'Schedule', width: '1.6fr', render: (action) => <span className={styles.mono}>{action.schedule}</span> },
    { key: 'timezone', header: 'Timezone', width: '1.2fr', render: (action) => action.timezone },
    { key: 'min', header: 'Min', width: '60px', align: 'right', render: (action) => action.min_capacity ?? '–' },
    { key: 'max', header: 'Max', width: '60px', align: 'right', render: (action) => action.max_capacity ?? '–' },
    {
      key: 'actions',
      header: '',
      width: '130px',
      render: (action) => (
        <span className={styles.rowActions}>
          <button type="button" className={styles.btn} aria-label={`Edit ${action.name}`} disabled={scheduledLock !== undefined} title={scheduledLock} onClick={() => setEditingAction({ kind: 'edit', name: action.name })}>
            Edit
          </button>
          <button
            type="button"
            className={`${styles.btn} ${styles.danger}`}
            aria-label={`Remove ${action.name}`}
            disabled={scheduledLock !== undefined}
            title={scheduledLock}
            onClick={() => onChange({ ...draft, scheduledActions: draft.scheduledActions.filter((candidate) => candidate.name !== action.name) })}
          >
            Remove
          </button>
        </span>
      ),
    },
  ]

  const rangeValid = draft.min !== null && draft.max !== null && Number.isInteger(draft.min) && Number.isInteger(draft.max) && draft.min <= draft.max
  const outsideRange = draft.hasTarget && rangeValid && desiredCount !== null && (desiredCount < (draft.min as number) || desiredCount > (draft.max as number))
  const removing = !draft.hasTarget && settings.scaling.has_target

  const policyBeingEdited = editingPolicy?.kind === 'edit' ? draft.policies.find((policy) => policy.name === editingPolicy.name) ?? null : null
  const actionBeingEdited = editingAction?.kind === 'edit' ? draft.scheduledActions.find((action) => action.name === editingAction.name) ?? null : null

  const targetUnreadable = unreadable('scaling_target')
  return (
    <Panel title="Auto scaling" error={targetUnreadable ? { message: targetUnreadable.message, onRetry } : undefined}>
      <div className={styles.stack}>
        {draft.hasTarget ? (
          <div className={styles.group}>
            <div className={styles.fieldRow}>
              <label className={styles.field} title={lock}>
                Minimum
                <input
                  className={styles.input}
                  type="number"
                  min={0}
                  value={draft.min ?? ''}
                  disabled={locked}
                  onChange={(event) => onChange({ ...draft, min: parseNumber(event.target.value) })}
                />
              </label>
              <label className={styles.field} title={lock}>
                Maximum
                <input
                  className={styles.input}
                  type="number"
                  min={0}
                  max={settings.max_desired}
                  value={draft.max ?? ''}
                  disabled={locked}
                  onChange={(event) => onChange({ ...draft, max: parseNumber(event.target.value) })}
                />
              </label>
            </div>
            {!rangeValid && <p className={styles.error}>Minimum and maximum must be whole numbers with minimum at most maximum.</p>}
            {outsideRange && (
              <p className={styles.warn} role="status">
                Auto scaling would move {desiredCount} back into {draft.min}–{draft.max}.
              </p>
            )}
            <div className={styles.actions}>
              <button type="button" className={`${styles.btn} ${styles.danger}`} disabled={removeLock !== undefined} title={removeLock} onClick={removeTarget}>
                Remove target
              </button>
            </div>
          </div>
        ) : removing ? (
          <div className={styles.group}>
            <p className={styles.warn} role="status">
              The scalable target will be removed. This also removes {plural(settings.scaling.policies.length, 'policy', 'policies')} and{' '}
              {plural(settings.scaling.scheduled_actions.length, 'scheduled action')}.
            </p>
            <div className={styles.actions}>
              <button type="button" className={styles.btn} onClick={keepTarget}>
                Keep target
              </button>
            </div>
          </div>
        ) : (
          <div className={styles.group}>
            <p className={styles.empty}>No auto scaling</p>
            <div className={styles.actions}>
              <span title={lock}>
                <button type="button" className={styles.btn} disabled={locked} onClick={addTarget}>
                  Add auto scaling target
                </button>
              </span>
            </div>
          </div>
        )}

        {draft.hasTarget && (
          <>
            <div className={styles.group}>
              <h3 className={styles.groupTitle}>Policies</h3>
              {policiesUnreadable ? (
                <PanelError message={policiesUnreadable.message} onRetry={onRetry} />
              ) : (
                <DataTable
                  label="Scaling policies"
                  columns={policyColumns}
                  rows={draft.policies}
                  rowKey={(policy) => policy.name}
                  minWidth={560}
                  empty="No scaling policies."
                />
              )}
              {editingPolicy ? (
                <PolicyRowEditor
                  key={policyBeingEdited?.name ?? 'new'}
                  initial={policyBeingEdited}
                  existingNames={draft.policies.map((policy) => policy.name)}
                  targetGroups={settings.target_groups}
                  onSave={savePolicy}
                  onCancel={() => setEditingPolicy(null)}
                />
              ) : (
                <div className={styles.actions}>
                  <button type="button" className={styles.btn} disabled={policiesLock !== undefined} title={policiesLock} onClick={() => setEditingPolicy({ kind: 'new' })}>
                    Add policy
                  </button>
                </div>
              )}
            </div>

            <div className={styles.group}>
              <h3 className={styles.groupTitle}>Scheduled actions</h3>
              {scheduledUnreadable ? (
                <PanelError message={scheduledUnreadable.message} onRetry={onRetry} />
              ) : (
                <DataTable
                  label="Scheduled actions"
                  columns={actionColumns}
                  rows={draft.scheduledActions}
                  rowKey={(action) => action.name}
                  minWidth={560}
                  empty="No scheduled actions."
                />
              )}
              {editingAction ? (
                <ScheduledActionEditor
                  key={actionBeingEdited?.name ?? 'new'}
                  initial={actionBeingEdited}
                  existingNames={draft.scheduledActions.map((action) => action.name)}
                  onSave={saveAction}
                  onCancel={() => setEditingAction(null)}
                />
              ) : (
                <div className={styles.actions}>
                  <button type="button" className={styles.btn} disabled={scheduledLock !== undefined} title={scheduledLock} onClick={() => setEditingAction({ kind: 'new' })}>
                    Add scheduled action
                  </button>
                </div>
              )}
            </div>
          </>
        )}
      </div>
    </Panel>
  )
}
