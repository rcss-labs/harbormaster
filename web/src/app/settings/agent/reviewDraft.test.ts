import { describe, expect, it } from 'vitest'
import type { AgentReviewsInfo, ReviewDTO } from '../../../api/agent'
import { bodyFromDraft, draftFromReview, validateDraft } from './reviewDraft'

const INFO: AgentReviewsInfo = {
  enabled: true,
  per_day: 24,
  max_late_minutes: 360,
  max_reviews: 20,
  types: [{ id: 'cost', label: 'Cost' }],
  supported_types: ['cost'],
  worst_case_run_usd: null,
  custom_enabled: false,
  max_instruction_chars: 2000,
}

function review(fields: Partial<ReviewDTO> = {}): ReviewDTO {
  return {
    id: 1,
    name: 'Weekly cost',
    type: 'cost',
    enabled: true,
    schedule: { cadence: 'weekly', at_minute: 480, weekday: 1, month_day: null, timezone: 'Europe/Berlin' },
    scope: [],
    routes: [
      { id: 10, name: 'Ops mail', destination: 'email', enabled: true },
      { id: 11, name: 'Pager', destination: 'pagerduty', enabled: true },
    ],
    version: 3,
    next_run_at: null,
    last_run: null,
    worst_case_month_usd: null,
    instructions: '',
    ...fields,
  }
}

describe('draftFromReview', () => {
  it('drops a route that is no longer allowed, so the editor can still save a switch off', () => {
    expect(draftFromReview(review()).routeIds).toEqual([10])
    expect(bodyFromDraft(draftFromReview(review()), 3).route_ids).toEqual([10])
  })
})

describe('validateDraft', () => {
  const saved = review({ routes: [{ id: 11, name: 'Pager', destination: 'pagerduty', enabled: true }] })

  it('lets an enabled review with no allowed route be turned off', () => {
    const draft = { ...draftFromReview(saved), enabled: false }
    expect(draft.routeIds).toEqual([])
    expect(validateDraft(draft, INFO, [], saved)).toBeNull()
  })

  it('still asks for a route when the review stays on, or was already off', () => {
    expect(validateDraft(draftFromReview(saved), INFO, [], saved)).toBe('Choose at least one route.')
    const alreadyOff = review({ enabled: false, routes: [] })
    expect(validateDraft(draftFromReview(alreadyOff), INFO, [], alreadyOff)).toBe('Choose at least one route.')
    expect(validateDraft({ ...draftFromReview(saved), enabled: false }, INFO, [])).toBe('Choose at least one route.')
  })
})
