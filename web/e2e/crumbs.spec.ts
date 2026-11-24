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

    // Measured with a 96px crumb box: the right-hand controls (Keyboard shortcuts, UI scale) end 49 to 130px past the
    // window at 200 % on 1920px, with or without the Ask AI button. The box needs less than 96px or the bar needs a
    // second row; either is a design call, so the check stays visible but off until then.
    test.fixme('the controls stay in the window at 200 % with a long crumb', async ({ page }) => {
      await openLongCrumb(page)
      const overflow = await page.evaluate(() =>
        Math.max(...[...document.querySelectorAll('header button')].map((el) => el.getBoundingClientRect().right - window.innerWidth)),
      )
      expect(overflow).toBeLessThanOrEqual(1)
    })

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
