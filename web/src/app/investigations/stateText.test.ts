import { describe, expect, it } from 'vitest'
import { stateText } from './stateText'

describe('stateText', () => {
  it.each([
    ['requested', '', 'Investigating'],
    ['running', '', 'Investigating'],
    ['done', '', 'Diagnosis ready'],
    ['failed', 'error', 'The investigation did not finish.'],
    ['failed', '', 'The investigation did not finish.'],
    ['skipped', 'budget', "Not investigated: this month's AI allowance is used up."],
    ['skipped', 'not_visible', 'Not investigated: the agent cannot see this service.'],
    ['skipped', 'not_configured', 'Not investigated: no model is set up for investigations.'],
    ['skipped', 'rate_limited', 'Not investigated: too many alerts in the last hour.'],
    ['skipped', 'expired', 'Not investigated: it waited too long.'],
    ['skipped', 'something_new', 'Not investigated.'],
    ['skipped', '', 'Not investigated.'],
    ['paused', '', 'Not investigated.'],
    ['toString', '', 'Not investigated.'],
    ['skipped', 'constructor', 'Not investigated.'],
  ])('%s / %s reads "%s"', (state, reason, text) => {
    expect(stateText(state, reason)).toBe(text)
  })
})
