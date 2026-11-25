const STATE_TEXT: Record<string, string> = {
  requested: 'Waiting to run',
  running: 'Running',
  done: 'Review ready',
  failed: 'The review did not finish.',
}

const SKIPPED_TEXT: Record<string, string> = {
  budget: "Not run: this month's AI allowance has too little left. Reviews keep room for chat and investigations.",
  not_visible: 'Not run: the agent cannot see any service in this scope.',
  not_configured: 'Not run: no model is set up for reviews.',
  rate_limited: 'Not run: too many reviews in the last day.',
  missed: 'Not run: it could not start in time.',
  disabled: 'Not run: the review was turned off.',
}

const FALLBACK = 'Not run.'

const TYPE_LABEL: Record<string, string> = {
  cost: 'Cost',
  security: 'Security findings',
  reliability: 'Reliability',
  capacity: 'Capacity',
}

/** The one sentence for a run's state; any state or reason this app does not know reads as not run. */
export function reviewStateText(state: string, reason: string): string {
  if (state === 'skipped') return Object.hasOwn(SKIPPED_TEXT, reason) ? SKIPPED_TEXT[reason] : FALLBACK
  return Object.hasOwn(STATE_TEXT, state) ? STATE_TEXT[state] : FALLBACK
}

export const reviewTypeLabel = (type: string): string => (Object.hasOwn(TYPE_LABEL, type) ? TYPE_LABEL[type] : 'Review')

export type ReviewPillKind = 'ok' | 'bad' | 'info' | 'mute'

export const reviewPillKind = (state: string): ReviewPillKind => {
  if (state === 'done') return 'ok'
  if (state === 'failed') return 'bad'
  if (state === 'requested' || state === 'running') return 'info'
  return 'mute'
}
