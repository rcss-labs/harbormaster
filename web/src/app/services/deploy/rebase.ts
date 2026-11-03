import type { DeploySettingsResponse } from '../../../api/deploy'
import { scalingDraftFromSettings, type ScalingDraft } from './scalingDraft'
import { serviceDraftFromSettings, type ServiceDraft } from './serviceDraft'

/**
 * Moves a service draft from one settings snapshot to the next: a field the user did not touch (still equal to the old
 * live value) follows the new live value, a field they edited keeps their value.
 */
export function rebaseServiceDraft(draft: ServiceDraft, before: DeploySettingsResponse, after: DeploySettingsResponse): ServiceDraft {
  const old = serviceDraftFromSettings(before)
  const next = serviceDraftFromSettings(after)
  const rebased: Record<string, unknown> = { ...draft }
  for (const key of Object.keys(old) as (keyof ServiceDraft)[]) {
    if (draft[key] === old[key]) rebased[key] = next[key]
  }
  return rebased as unknown as ServiceDraft
}

/** The scaling draft is one unit: untouched (null) when it equals the old live scaling, otherwise kept as edited. */
export function rebaseScalingDraft(draft: ScalingDraft, before: DeploySettingsResponse): ScalingDraft | null {
  return JSON.stringify(draft) === JSON.stringify(scalingDraftFromSettings(before)) ? null : draft
}
