export type PillKind = 'ok' | 'bad' | 'warn' | 'mute'

const STATES: Record<string, { label: string; kind: PillKind }> = {
  requested: { label: 'Waiting', kind: 'mute' },
  running: { label: 'Running', kind: 'warn' },
  done: { label: 'Done', kind: 'ok' },
  failed: { label: 'Failed', kind: 'bad' },
  skipped: { label: 'Skipped', kind: 'mute' },
}

const REASONS: Record<string, string> = {
  budget: 'allowance used up',
  not_visible: 'agent cannot see the scope',
  not_configured: 'no model chosen',
  rate_limited: 'rate limit',
  missed: 'missed its time',
  disabled: 'reviews were off',
  instructions: 'instructions no longer pass the checks',
  agent_unavailable: 'agent unavailable',
  limit_reached: 'limit reached',
  no_result: 'no answer',
  interrupted: 'interrupted',
  error: 'error',
}

/** A state this version does not know is shown as it came, as text. */
export function runState(state: string): { label: string; kind: PillKind } {
  return STATES[state] ?? { label: state, kind: 'mute' }
}

/** Empty for no reason; a reason this version does not know is shown as it came. */
export function runReason(reason: string): string {
  return REASONS[reason] ?? reason
}
