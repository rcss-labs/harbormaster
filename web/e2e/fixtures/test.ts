import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test as base } from '@playwright/test'
import type { Role } from './users'

declare global {
  interface Window {
    __csp?: string[]
  }
}

const here = path.dirname(fileURLToPath(import.meta.url))

/** Path of the storageState file auth.setup.ts writes for a role (independent of the cwd). */
export const authFile = (role: Role) => path.join(here, '..', '.auth', `${role}.json`)

/** Use as `test.use(asUser('omer'))` to run a file or describe block signed in as that role. */
export function asUser(role: Role) {
  return { storageState: authFile(role) }
}

// Drop-in replacement for Playwright's test: an automatic fixture collects CSP violations in every page
// and fails the test with the list when any occurred.
export const test = base.extend<{ cspViolations: string[] }>({
  cspViolations: [
    async ({ context }, use) => {
      await context.addInitScript(() => {
        window.__csp = []
        document.addEventListener('securitypolicyviolation', (e) => {
          window.__csp?.push(`${e.violatedDirective} blocked ${e.blockedURI || 'inline'} (${e.sourceFile ?? ''}:${e.lineNumber})`)
        })
      })
      await use([])
      const violations = await Promise.all(
        context.pages().map((p) => (p.isClosed() ? [] : p.evaluate(() => window.__csp ?? []).catch(() => []))),
      )
      expect(violations.flat(), 'CSP violations').toEqual([])
    },
    { auto: true },
  ],
})

export { expect }
