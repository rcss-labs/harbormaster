import { describe, expect, it } from 'vitest'
import { DEPLOY_SETTINGS_PAYMENTS } from '../../../test/fixtures'
import { serviceDraftErrors, serviceDraftFromSettings, serviceDraftToChange } from './serviceDraft'

const settings = DEPLOY_SETTINGS_PAYMENTS

describe('serviceDraft', () => {
  it('omits unchanged fields', () => {
    const draft = serviceDraftFromSettings(settings)
    expect(serviceDraftToChange(settings, draft)).toBeNull()

    expect(serviceDraftToChange(settings, { ...draft, desiredCount: 5 })).toEqual({ desired_count: 5 })
    expect(serviceDraftToChange(settings, { ...draft, enableExecuteCommand: false })).toEqual({ enable_execute_command: false })
    expect(serviceDraftToChange(settings, { ...draft, gracePeriodSeconds: 90 })).toEqual({ health_check_grace_period_seconds: 90 })
    expect(serviceDraftToChange(settings, { ...draft, forceNewDeployment: true })).toEqual({ force_new_deployment: true })
    expect(serviceDraftToChange(settings, { ...draft, minimumHealthyPercent: 50 })).toEqual({
      deployment_configuration: { circuit_breaker: { enable: true, rollback: true }, minimum_healthy_percent: 50, maximum_percent: 200 },
    })
  })

  it('ignores invalid (empty or out of range) numbers and reports them', () => {
    const draft = { ...serviceDraftFromSettings(settings), desiredCount: null, maximumPercent: 250 }
    expect(serviceDraftToChange(settings, draft)).toBeNull()
    expect(serviceDraftErrors(settings, draft)).toEqual([
      expect.stringContaining('Desired count'),
      expect.stringContaining('Maximum percent'),
    ])
  })
})
