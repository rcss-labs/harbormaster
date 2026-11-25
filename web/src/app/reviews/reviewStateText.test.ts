import { describe, expect, it } from 'vitest'
import { reviewStateText, reviewTypeLabel } from './reviewStateText'

describe('reviewStateText', () => {
  it.each([
    ['requested', '', 'Waiting to run'],
    ['running', '', 'Running'],
    ['done', '', 'Review ready'],
    ['failed', 'error', 'The review did not finish.'],
    ['skipped', 'budget', "Not run: this month's AI allowance has too little left. Reviews keep room for chat and investigations."],
    ['skipped', 'not_visible', 'Not run: the agent cannot see any service in this scope.'],
    ['skipped', 'not_configured', 'Not run: no model is set up for reviews.'],
    ['skipped', 'rate_limited', 'Not run: too many reviews in the last day.'],
    ['skipped', 'missed', 'Not run: it could not start in time.'],
    ['skipped', 'disabled', 'Not run: the review was turned off.'],
    ['skipped', 'something_new', 'Not run.'],
    ['skipped', '', 'Not run.'],
    ['archived', '', 'Not run.'],
    ['toString', '', 'Not run.'],
  ])('%s / %s reads %s', (state, reason, text) => {
    expect(reviewStateText(state, reason)).toBe(text)
  })

  it('names the four review types and leaves an unknown one generic', () => {
    expect(['cost', 'security', 'reliability', 'capacity', 'custom'].map(reviewTypeLabel)).toEqual(['Cost', 'Security findings', 'Reliability', 'Capacity', 'Review'])
  })
})
