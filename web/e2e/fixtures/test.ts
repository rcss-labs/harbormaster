import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test as base } from '@playwright/test'
import type { Role } from './users'

const here = path.dirname(fileURLToPath(import.meta.url))

/** Path of the storageState file auth.setup.ts writes for a role (independent of the cwd). */
export const authFile = (role: Role) => path.join(here, '..', '.auth', `${role}.json`)

/** Use as `test.use(asUser('omer'))` to run a file or describe block signed in as that role. */
export function asUser(role: Role) {
  return { storageState: authFile(role) }
}

// Drop-in replacement for Playwright's test. An automatic fixture records every CSP violation, from every
// document of every page in the test's context, on the Node side (so navigations, closed pages and evaluate
// failures cannot lose them), and fails the test with the list when any occurred.
export const test = base.extend<{ cspViolations: string[] }>({
  cspViolations: [
    async ({ context }, use) => {
      const violations: string[] = []
      await context.exposeBinding('__hmCsp', (_source, violation: string) => {
        violations.push(violation)
      })
      await context.addInitScript(() => {
        document.addEventListener('securitypolicyviolation', (e) => {
          const report = `${e.violatedDirective} blocked ${e.blockedURI || 'inline'} (${e.sourceFile ?? ''}:${e.lineNumber})`
          void (window as unknown as { __hmCsp: (v: string) => Promise<void> }).__hmCsp(report)
        })
      })
      await use(violations)
      // Violation reports are queued events; give in-flight binding calls a moment to land.
      await new Promise((resolve) => setTimeout(resolve, 100))
      expect(violations, 'CSP violations').toEqual([])
    },
    { auto: true },
  ],
})

export { expect }
export type { Page } from '@playwright/test'
