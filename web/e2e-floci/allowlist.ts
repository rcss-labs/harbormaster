/** An HTTP failure that floci cannot avoid; every other response with status >= 400 fails the route check. */
export interface ExpectedFailure {
  method: string
  path: RegExp
  /** Omit to allow any failing status. */
  status?: number
  /** Entry is removed once this wave lands (lane 2C drops the `wave2` entries). */
  until?: 'wave2'
}

export const EXPECTED_FAILURES: ExpectedFailure[] = [
  // Floci has no DescribeScheduledActions, so the whole deploy-settings read fails (502) until lane 1B makes it partial.
  { method: 'GET', path: /^\/api\/services\/[^/]+\/deploy-settings$/, status: 502, until: 'wave2' },
  // Floci has no StartLiveTail, so the Logs tab live-tail WebSocket upgrade fails ("Live tail disconnected." + Resume).
  { method: 'GET', path: /^\/api\/services\/[^/]+\/logs\/tail$/ },
  // Floci has no SSM sessions, so the ECS Exec WebSocket (Terminal) cannot connect; only the layout is checked.
  { method: 'GET', path: /^\/api\/tasks\/[^/]+\/exec$/ },
]

export function isExpectedFailure(method: string, url: string, status: number): boolean {
  const path = new URL(url).pathname
  return EXPECTED_FAILURES.some((entry) => entry.method === method && entry.path.test(path) && (entry.status === undefined || entry.status === status))
}
