import { describe, expect, it } from 'vitest'
import { DEPLOY_SETTINGS_PAYMENTS } from '../../../test/fixtures'
import { rebaseScalingDraft, rebaseServiceDraft } from './rebase'
import { scalingDraftFromSettings } from './scalingDraft'
import { serviceDraftFromSettings } from './serviceDraft'

const fresh = { ...DEPLOY_SETTINGS_PAYMENTS, version: 'v2', desired_count: 4, enable_execute_command: false }

describe('rebase', () => {
  it('untouched service fields follow the fresh settings; edited ones stay', () => {
    const edited = { ...serviceDraftFromSettings(DEPLOY_SETTINGS_PAYMENTS), desiredCount: 9 }
    const rebased = rebaseServiceDraft(edited, DEPLOY_SETTINGS_PAYMENTS, fresh)
    expect(rebased.desiredCount).toBe(9)
    expect(rebased.enableExecuteCommand).toBe(false)
  })

  it('an unedited scaling draft is dropped, an edited one is kept', () => {
    const unedited = scalingDraftFromSettings(DEPLOY_SETTINGS_PAYMENTS)
    expect(rebaseScalingDraft(unedited, DEPLOY_SETTINGS_PAYMENTS)).toBeNull()
    const edited = { ...unedited, min: 1 }
    expect(rebaseScalingDraft(edited, DEPLOY_SETTINGS_PAYMENTS)).toBe(edited)
  })
})
