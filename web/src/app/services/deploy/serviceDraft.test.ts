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

  it('emits a strategy only when it differs from live', () => {
    const draft = serviceDraftFromSettings(settings)
    // A launch type service reads as one on-demand provider with no base: that is not a change.
    expect(draft.capacity).toEqual({ onDemandBase: 0, onDemandWeight: 1, spotWeight: 0 })
    expect(serviceDraftToChange(settings, draft)).toBeNull()

    expect(serviceDraftToChange(settings, { ...draft, capacity: { onDemandBase: 1, onDemandWeight: 0, spotWeight: 1 } })).toEqual({
      capacity_provider_strategy: [
        { provider: 'FARGATE', base: 1, weight: 0 },
        { provider: 'FARGATE_SPOT', base: 0, weight: 1 },
      ],
    })
    // FARGATE is left out only when both its base and its weight are 0.
    expect(serviceDraftToChange(settings, { ...draft, capacity: { onDemandBase: 0, onDemandWeight: 0, spotWeight: 1 } })).toEqual({
      capacity_provider_strategy: [{ provider: 'FARGATE_SPOT', base: 0, weight: 1 }],
    })
    // Spot weight 0 means on demand only.
    expect(serviceDraftToChange(settings, { ...draft, capacity: { onDemandBase: 0, onDemandWeight: 3, spotWeight: 0 } })).toEqual({
      capacity_provider_strategy: [{ provider: 'FARGATE', base: 0, weight: 3 }],
    })

    const onSpot = { ...settings, capacity: { ...settings.capacity, launch_type: '', strategy: [{ provider: 'FARGATE', base: 1, weight: 0 }, { provider: 'FARGATE_SPOT', base: 0, weight: 1 }] } }
    const live = serviceDraftFromSettings(onSpot)
    expect(live.capacity).toEqual({ onDemandBase: 1, onDemandWeight: 0, spotWeight: 1 })
    expect(serviceDraftToChange(onSpot, live)).toBeNull()
    expect(serviceDraftToChange(onSpot, { ...live, capacity: { ...live.capacity, spotWeight: 2 } })?.capacity_provider_strategy).toHaveLength(2)
  })

  it('reports capacity numbers it cannot send', () => {
    const draft = serviceDraftFromSettings(settings)
    const bad = (capacity: Partial<typeof draft.capacity>) => serviceDraftErrors(settings, { ...draft, capacity: { ...draft.capacity, ...capacity } })
    expect(bad({ onDemandWeight: 0, spotWeight: 0 })).toEqual([expect.stringContaining('weight above 0')])
    expect(bad({ onDemandBase: -1 })).toEqual([expect.stringContaining('base')])
    expect(bad({ spotWeight: null })).toEqual([expect.stringContaining('Spot weight')])
    expect(bad({ onDemandWeight: 1001 })).toEqual([expect.stringContaining('weight')])
    expect(bad({})).toEqual([])
    expect(serviceDraftToChange(settings, { ...draft, capacity: { onDemandBase: 0, onDemandWeight: 0, spotWeight: 0 } })).toBeNull()
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
