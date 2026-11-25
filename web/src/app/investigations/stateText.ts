const STATE_TEXT: Record<string, string> = {
  requested: 'Investigating',
  running: 'Investigating',
  done: 'Diagnosis ready',
  failed: 'The investigation did not finish.',
}

const SKIPPED_TEXT: Record<string, string> = {
  budget: "Not investigated: this month's AI allowance is used up.",
  not_visible: 'Not investigated: the agent cannot see this service.',
  not_configured: 'Not investigated: no model is set up for investigations.',
  rate_limited: 'Not investigated: too many alerts in the last hour.',
  expired: 'Not investigated: it waited too long.',
}

const FALLBACK = 'Not investigated.'

/** The one sentence for an investigation's state; any state or reason this app does not know reads as not investigated. */
export function stateText(state: string, reason: string): string {
  if (state === 'skipped') return Object.hasOwn(SKIPPED_TEXT, reason) ? SKIPPED_TEXT[reason] : FALLBACK
  return Object.hasOwn(STATE_TEXT, state) ? STATE_TEXT[state] : FALLBACK
}
