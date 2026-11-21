import type { DeploySettingsResponse } from '../../../api/deploy'
import { LockNote } from '../../../components/LockNote'
import { capacityChanged, capacityDraftFromSettings, type CapacityDraft } from './serviceDraft'
import { clusterNoSpotMessage } from './capacityParams'
import styles from './editors.module.css'

interface CapacityFieldsProps {
  settings: DeploySettingsResponse
  draft: CapacityDraft
  onChange: (draft: CapacityDraft) => void
  /** Name of the service's tag, for the "You need Deploy on ..." lock title. */
  tagName: string
  clusterName: string
}

export const SPOT_WARNING =
  "Spot tasks can be stopped with two minutes' notice, and ECS does not fall back to on-demand when Spot capacity runs out. Applying this starts a new deployment that replaces every task."

export const REPLACE_WARNING = 'Applying this starts a new deployment that replaces every task.'

const parseNumber = (text: string): number | null => {
  if (text.trim() === '') return null
  const value = Number(text)
  return Number.isNaN(value) ? null : value
}

/**
 * How the service spreads its tasks over on-demand Fargate and Fargate Spot: how many stay on demand and the weights that
 * split the rest. Spot weight 0 means on demand only. Nothing is sent from here; the draft goes through Review.
 */
export function CapacityFields({ settings, draft, onChange, tagName, clusterName }: CapacityFieldsProps) {
  const update = (patch: Partial<CapacityDraft>) => onChange({ ...draft, ...patch })
  const { capacity } = settings
  const deployLock = settings.can.deploy ? undefined : `You need Deploy on ${tagName}`
  const lock = deployLock ?? (capacity.editable ? undefined : capacity.lock_reason)
  // A cluster without FARGATE_SPOT cannot take Spot tasks. A service that already has some keeps its field, so it can leave Spot.
  const usesSpot = capacityDraftFromSettings(settings).spotWeight !== 0
  const clusterLock = capacity.cluster_providers.includes('FARGATE_SPOT') || usesSpot ? undefined : clusterNoSpotMessage(clusterName)
  const spotLock = lock ?? clusterLock
  const changed = capacityChanged(settings, draft)
  const putsTasksOnSpot = (draft.spotWeight ?? 0) > 0

  return (
    <div className={styles.group}>
      <h3 className={styles.groupTitle}>Capacity</h3>
      {/* The Deploy lock is stated once at the top of the Service panel; this group adds only what is its own. */}
      <LockNote>{deployLock === undefined ? lock : undefined}</LockNote>
      <div className={styles.fieldRow}>
        <label className={styles.field} title={lock}>
          On-demand tasks kept (base)
          <input
            className={styles.input}
            type="number"
            min={0}
            value={draft.onDemandBase ?? ''}
            disabled={lock !== undefined}
            onChange={(event) => update({ onDemandBase: parseNumber(event.target.value) })}
          />
        </label>
        <label className={styles.field} title={lock}>
          On-demand weight
          <input
            className={styles.input}
            type="number"
            min={0}
            value={draft.onDemandWeight ?? ''}
            disabled={lock !== undefined}
            onChange={(event) => update({ onDemandWeight: parseNumber(event.target.value) })}
          />
        </label>
        <label className={styles.field} title={spotLock}>
          Spot weight
          <input
            className={styles.input}
            type="number"
            min={0}
            value={draft.spotWeight ?? ''}
            disabled={spotLock !== undefined}
            onChange={(event) => update({ spotWeight: parseNumber(event.target.value) })}
          />
        </label>
      </div>
      <LockNote kind="why">{lock === undefined ? clusterLock : undefined}</LockNote>
      <p className={styles.hint}>The base stays on demand; the weights split the tasks above it. A Spot weight of 0 means on demand only.</p>
      {changed && (
        <p className={styles.warn} role="status">
          {putsTasksOnSpot ? SPOT_WARNING : REPLACE_WARNING}
        </p>
      )}
    </div>
  )
}
