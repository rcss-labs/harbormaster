import { useState } from 'react'
import type { DeploySettingsResponse } from '../../../api/deploy'
import { Toggle } from '../../../components/Toggle'
import type { PolicyDraft } from './scalingDraft'
import styles from './editors.module.css'

type EditableMetric = 'cpu' | 'memory' | 'alb'

export const METRIC_LABELS: Record<PolicyDraft['metric'], string> = {
  cpu: 'CPU %',
  memory: 'Memory %',
  alb: 'ALB requests per target',
  other: 'Other',
}

interface PolicyRowEditorProps {
  /** The policy being edited, or null for a new one. */
  initial: PolicyDraft | null
  existingNames: string[]
  targetGroups: DeploySettingsResponse['target_groups']
  onSave: (policy: PolicyDraft) => void
  onCancel: () => void
}

const NAME_PATTERN = /^[\w./-]{1,256}$/

const isWholeSeconds = (text: string) => text.trim() !== '' && Number.isInteger(Number(text)) && Number(text) >= 0

/** Inline form for one target-tracking policy (CPU, memory or ALB requests per target). */
export function PolicyRowEditor({ initial, existingNames, targetGroups, onSave, onCancel }: PolicyRowEditorProps) {
  const [name, setName] = useState(initial?.name ?? '')
  const [metric, setMetric] = useState<EditableMetric>(initial && initial.metric !== 'other' ? initial.metric : 'cpu')
  const [targetValue, setTargetValue] = useState(String(initial?.target_value ?? 60))
  const [scaleIn, setScaleIn] = useState(String(initial?.scale_in_cooldown ?? 300))
  const [scaleOut, setScaleOut] = useState(String(initial?.scale_out_cooldown ?? 60))
  const [disableScaleIn, setDisableScaleIn] = useState(initial?.disable_scale_in ?? false)
  const [targetGroupArn, setTargetGroupArn] = useState(initial?.target_group_arn ?? '')

  const target = Number(targetValue)
  const targetOk = targetValue.trim() !== '' && Number.isFinite(target) && target > 0 && (metric === 'alb' || target <= 100)
  const nameTaken = existingNames.includes(name) && name !== initial?.name
  const needsTargetGroup = metric === 'alb' && targetGroupArn === ''
  const valid = NAME_PATTERN.test(name) && !nameTaken && targetOk && isWholeSeconds(scaleIn) && isWholeSeconds(scaleOut) && !needsTargetGroup

  const save = () => {
    if (!valid) return
    const group = targetGroups.find((candidate) => candidate.arn === targetGroupArn)
    onSave({
      name,
      type: 'TargetTrackingScaling',
      metric,
      metric_raw: initial?.metric === metric ? initial.metric_raw : '',
      target_value: target,
      scale_in_cooldown: Number(scaleIn),
      scale_out_cooldown: Number(scaleOut),
      disable_scale_in: disableScaleIn,
      resource_label: metric === 'alb' ? (group?.resource_label ?? initial?.resource_label ?? '') : '',
      editable: true,
      target_group_arn: metric === 'alb' ? targetGroupArn : '',
    })
  }

  return (
    <form
      className={styles.editor}
      aria-label={initial ? `Edit ${initial.name}` : 'New scaling policy'}
      onSubmit={(event) => {
        event.preventDefault()
        save()
      }}
    >
      <div className={styles.fieldRow}>
        <label className={styles.field}>
          Policy name
          <input className={styles.input} value={name} disabled={initial !== null} onChange={(event) => setName(event.target.value)} />
        </label>
        <label className={styles.field}>
          Metric
          <select className={styles.select} value={metric} onChange={(event) => setMetric(event.target.value as EditableMetric)}>
            <option value="cpu">{METRIC_LABELS.cpu}</option>
            <option value="memory">{METRIC_LABELS.memory}</option>
            {targetGroups.length > 0 && <option value="alb">{METRIC_LABELS.alb}</option>}
          </select>
        </label>
        <label className={styles.field}>
          Target value
          <input className={styles.input} type="number" min={0} step="any" value={targetValue} onChange={(event) => setTargetValue(event.target.value)} />
        </label>
      </div>
      {nameTaken && <p className={styles.error}>A policy named {name} already exists.</p>}
      {metric !== 'alb' && !targetOk && targetValue.trim() !== '' && <p className={styles.error}>CPU and memory targets are percentages above 0 up to 100.</p>}

      {metric === 'alb' && (
        <>
          <label className={styles.field}>
            Target group
            <select className={styles.select} value={targetGroupArn} onChange={(event) => setTargetGroupArn(event.target.value)}>
              <option value="">Choose a target group</option>
              {targetGroups.map((group) => (
                <option key={group.arn} value={group.arn}>
                  {group.name}
                </option>
              ))}
            </select>
          </label>
          {needsTargetGroup && <p className={styles.hint}>Choose the target group that receives the requests.</p>}
        </>
      )}

      <div className={styles.fieldRow}>
        <label className={styles.field}>
          Scale-in cooldown (seconds)
          <input className={styles.input} type="number" min={0} value={scaleIn} onChange={(event) => setScaleIn(event.target.value)} />
        </label>
        <label className={styles.field}>
          Scale-out cooldown (seconds)
          <input className={styles.input} type="number" min={0} value={scaleOut} onChange={(event) => setScaleOut(event.target.value)} />
        </label>
      </div>
      <Toggle checked={disableScaleIn} onChange={setDisableScaleIn}>
        Disable scale-in
      </Toggle>

      <div className={styles.actions}>
        <button type="submit" className={`${styles.btn} ${styles.primary}`} disabled={!valid}>
          Save policy
        </button>
        <button type="button" className={styles.btn} onClick={onCancel}>
          Cancel
        </button>
      </div>
    </form>
  )
}
