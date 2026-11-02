import { asUser, expect, test } from './fixtures/test'

// Smoke test against the real backend: the sidebar leads to Analytics and every tab renders its first panel.
// The analytics endpoints land in the backend track, so this runs after both tracks are merged.
const TABS = [
  { link: 'Delivery', heading: 'Rollouts' },
  { link: 'Reliability', heading: 'Availability' },
  { link: 'Capacity', heading: 'Reserved vCPU, last 7 days' },
  { link: 'Operations', heading: 'Operations through Harbormaster' },
]

test.describe('analytics as pavel', () => {
  test.use(asUser('pavel'))

  test('sidebar leads to Analytics and the four tabs render', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/')
    await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: /Analytics/ }).click()
    await expect(page).toHaveURL(/\/analytics\/delivery/)
    await expect(page.getByRole('heading', { level: 1, name: 'Analytics' })).toBeVisible()

    for (const { link, heading } of TABS) {
      await page.getByRole('link', { name: new RegExp(`^${link}`) }).click()
      await expect(page).toHaveURL(new RegExp(`/analytics/${link.toLowerCase()}`))
      await expect(page.getByRole('heading', { name: heading, exact: true })).toBeVisible({ timeout: 15_000 })
    }
  })
})
