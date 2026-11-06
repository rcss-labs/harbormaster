import { expect, test } from '../e2e/fixtures/test'
import { saveScreenshot } from '../e2e/helpers'
import { isExpectedFailure } from './allowlist'
import { waivers } from './known-wave0'
import { ROUTES } from './routes'
import { resolveTargets } from './targets'

// Every route (see routes.ts) is loaded at each width in each theme; ids are resolved from the live API when a test runs.

const WIDTHS: [number, number][] = [
  [1440, 900],
  [2560, 1440],
  [3840, 2160],
  // 390 is the phone pass (routes-phone.spec.ts).
]
const THEMES = ['dark', 'light'] as const

for (const route of ROUTES) {
  test.describe(route.name, () => {
    for (const [width, height] of WIDTHS) {
      for (const theme of THEMES) {
        test(`${width}px ${theme}`, async ({ page, request }) => {
          const targets = await resolveTargets(request)
          const path = route.path(targets)
          test.skip(path === null, `${route.name} is not available on this branch or estate`)

          await page.setViewportSize({ width, height })
          await page.addInitScript((value) => localStorage.setItem('harbormaster-theme', value), theme)

          const pageErrors: string[] = []
          const consoleErrors: string[] = []
          const badResponses: string[] = []
          page.on('pageerror', (error) => pageErrors.push(error.message))
          page.on('console', (message) => {
            if (message.type() !== 'error') return
            const text = message.text()
            // A failing HTTP response is judged by the allowlist below, not as a console error as well.
            if (text.startsWith('Failed to load resource')) return
            // The same goes for a WebSocket that floci cannot serve (live tail, ECS Exec).
            if (/^WebSocket connection to .* failed/.test(text) && /\/(logs\/tail|exec)\b/.test(text)) return
            consoleErrors.push(text)
          })
          page.on('response', (response) => {
            if (response.status() >= 400 && !isExpectedFailure(response.request().method(), response.url(), response.status())) {
              badResponses.push(`${response.request().method()} ${new URL(response.url()).pathname} -> ${response.status()}`)
            }
          })

          await page.goto(path!)
          await expect(page.getByRole('main')).toBeVisible()
          await page.waitForLoadState('networkidle')

          const overflow = await page.evaluate(() => {
            const main = document.querySelector('main')
            return {
              document: document.documentElement.scrollWidth - window.innerWidth,
              main: main ? main.scrollWidth - main.clientWidth : 0,
            }
          })
          const banners = await page.evaluate(() => {
            // The innermost element whose text starts "Data for": covers the v4 DataBanner and the old poller banner.
            const matches = Array.from(document.querySelectorAll('main *, [data-hm-banner]')).filter((el) => (el.textContent ?? '').trim().startsWith('Data for'))
            return matches.filter((el) => !matches.some((other) => other !== el && el.contains(other))).length
          })

          await saveScreenshot(page, `${route.name}-${width}-${theme}.png`)

          // Hard checks first: these have no wave-0 waiver.
          expect(pageErrors, 'uncaught page errors').toEqual([])
          expect(consoleErrors, 'console errors').toEqual([])
          expect(badResponses, 'HTTP failures outside the allowlist').toEqual([])

          // Layout checks that the old UI legitimately fails are waived with test.fixme (see known-wave0.ts).
          const waived: string[] = []
          const overflowing = overflow.document > 0 || overflow.main > 0
          if (overflowing) {
            const reasons = waivers('overflow', route.name, width)
            if (reasons.length > 0) waived.push(...reasons)
            else {
              expect(overflow.document, 'document scrollWidth beyond the window (px)').toBeLessThanOrEqual(0)
              expect(overflow.main, 'main scrollWidth beyond its client width (px)').toBeLessThanOrEqual(0)
            }
          }
          if (banners > 1) {
            const reasons = waivers('stacked-banners', route.name, width)
            if (reasons.length > 0) waived.push(...reasons)
            else expect(banners, 'stacked "Data for" banners (at most one)').toBeLessThanOrEqual(1)
          }
          test.fixme(waived.length > 0, [...new Set(waived)].join('; '))
        })
      }
    }
  })
}
