import type { Page } from '@playwright/test'
import { asUser, expect, test } from './fixtures/test'
import { clusterUrl } from './fixtures/ids'

// At 200 % zoom the top bar is full; a long unbroken crumb must wrap inside its own box instead of being clipped,
// squeezing the controls or growing the header without bound. Run with the Ask AI button (sam) and without it (dana).
const LONG_NAME = 'averylongclusternamewithoutanybreakpointsatall-and-more'
const CLUSTER = ['482109375512', 'us-east-1', 'core-services'] as const

async function openLongCrumb(page: Page) {
  await page.addInitScript(() => localStorage.setItem('harbormaster-scale', '200'))
  const url = clusterUrl(...CLUSTER)
  await page.route(`**/api${url}`, async (route) => {
    const response = await route.fetch()
    const body = (await response.json()) as { name: string }
    await route.fulfill({ response, json: { ...body, name: LONG_NAME } })
  })
  await page.goto(url)
  await expect(page.getByRole('navigation', { name: 'Breadcrumb' }).getByText(LONG_NAME)).toBeVisible()
}

for (const [role, hasAskAI] of [['sam', true], ['dana', false]] as const) {
  test.describe(`as ${role}`, () => {
    test.use({ ...asUser(role), viewport: { width: 1920, height: 1080 } }) // the window the 200 % checks in scale.spec use

    test(`a long crumb stays whole at 200 % ${hasAskAI ? 'with' : 'without'} the Ask AI button`, async ({ page }) => {
      await openLongCrumb(page)
      await expect(page.getByRole('button', { name: 'Ask AI' })).toHaveCount(hasAskAI ? 1 : 0)
      const read = await page.evaluate(() => {
        const nav = document.querySelector('nav[aria-label="Breadcrumb"]')!
        const last = nav.querySelector('span:last-child')!
        const header = document.querySelector('header')!
        const rect = (el: Element) => el.getBoundingClientRect().toJSON() as { x: number; right: number; y: number; bottom: number; width: number; height: number }
        return {
          nav: rect(nav),
          last: rect(last),
          navOverflow: nav.scrollWidth - nav.clientWidth,
          header: rect(header),
        }
      })
      expect(read.navOverflow, 'the crumbs do not overflow their box').toBeLessThanOrEqual(1)
      expect(read.last.right, 'the last crumb ends inside its box').toBeLessThanOrEqual(read.nav.right + 1)
      expect(read.nav.width, 'the crumbs keep a readable width').toBeGreaterThanOrEqual(96 * 2 - 1)
      expect(read.header.height, 'the header stays bounded').toBeLessThanOrEqual(300)
    })

    // No control may end up outside the window (a user at 200 % could not scale back): the bar wraps to a second row.
    test('every control stays in the window at 200 % with a long crumb', async ({ page }) => {
      await openLongCrumb(page)
      const read = await page.evaluate(() => ({
        innerWidth: window.innerWidth,
        rights: [...document.querySelectorAll('header button, header a')].map((el) => el.getBoundingClientRect().right),
        lefts: [...document.querySelectorAll('header button, header a')].map((el) => el.getBoundingClientRect().x),
      }))
      expect(Math.max(...read.rights)).toBeLessThanOrEqual(read.innerWidth + 1)
      expect(Math.min(...read.lefts)).toBeGreaterThanOrEqual(-1)
      await expect(page.getByRole('button', { name: 'Keyboard shortcuts' })).toBeVisible()
      await expect(page.getByRole('button', { name: 'UI scale' })).toBeVisible()
    })

    for (const width of [1280, 1440, 1920]) {
      test(`the bar stays one row at ${width}px with usual names`, async ({ page }) => {
        await page.setViewportSize({ width, height: 900 })
        await page.goto('/clusters')
        await expect(page.getByRole('navigation', { name: 'Breadcrumb' }).getByText('Clusters')).toBeVisible()
        const height = await page.evaluate(() => document.querySelector('header')!.getBoundingClientRect().height)
        expect(height).toBeLessThanOrEqual(57)
      })
    }

    test('a focused crumb link shows its outline', async ({ page }) => {
      await openLongCrumb(page)
      const link = page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: 'Clusters' })
      await link.focus()
      const outline = await link.evaluate((el) => {
        const style = getComputedStyle(el)
        return { style: style.outlineStyle, width: parseFloat(style.outlineWidth), clippedBy: getComputedStyle(el.closest('nav')!).overflowX }
      })
      expect(outline.style).not.toBe('none')
      expect(outline.width).toBeGreaterThan(0)
      // The ring reaches past the link's box, so the bar must not clip its crumbs.
      expect(outline.clippedBy).toBe('visible')
    })
  })
}
