/**
 * Used by both floci route passes: routes.spec.ts (desktop widths) and routes-phone.spec.ts (the 390 px phone pass,
 * which feeds this list to the generic phone assertions in e2e/fixtures/phone.ts). The entries are the same for both.
 */

/** An HTTP failure that floci cannot avoid; every other response with status >= 400 fails the route check. */
export interface ExpectedFailure {
  method: string
  path: RegExp
  /** Omit to allow any failing status. */
  status?: number
}

export const EXPECTED_FAILURES: ExpectedFailure[] = [
  // Floci has no StartLiveTail, so the Logs tab live-tail WebSocket upgrade fails ("Live tail disconnected." + Resume). The phone pass
  // sees the same failure and the same banner at 390 px.
  { method: 'GET', path: /^\/api\/services\/[^/]+\/logs\/tail$/ },
  // Floci has no SSM sessions, so the ECS Exec WebSocket (Terminal) cannot connect; only the layout is checked (the keys row and the
  // on-screen keyboard behaviour are covered on the fake backend by terminal.mobile.spec.ts).
  { method: 'GET', path: /^\/api\/tasks\/[^/]+\/exec$/ },
]

export function isExpectedFailure(method: string, url: string, status: number): boolean {
  const path = new URL(url).pathname
  return EXPECTED_FAILURES.some((entry) => entry.method === method && entry.path.test(path) && (entry.status === undefined || entry.status === status))
}
