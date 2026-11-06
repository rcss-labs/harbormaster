import { asUser, expect, test } from './fixtures/test'

// Smoke test against the real backend: the sidebar leads to Analytics and every tab renders its first panel.
// The analytics endpoints land in the backend track, so this runs after both tracks are merged.
const TABS = [
  { link: 'Delivery', heading: 'Rollouts' },
  { link: 'Reliability', heading: 'Availability' },
  { link: 'Capacity', heading: 'Reserved vCPU, last 7 days' },
  { link: 'Operations', heading: 'Operations through Harbormaster' },
]

test.describe('analytics as sam', () => {
  test.use(asUser('sam'))

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
      await expect(page.getByText('PROPOSED')).toHaveCount(0)
    }
  })

  // The fake estate starts with recent daily history, so the 90-day panels are ordinary panels with data.
  test('the 90-day panels render as normal panels with data', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/analytics/reliability')
    const panel = page.getByRole('region', { name: 'Availability, last 90 days' })
    await expect(panel).toBeVisible({ timeout: 15_000 })
    await expect(panel).not.toHaveAttribute('data-variant', /.+/)
    await expect(panel.getByText('source service_availability_daily · kept')).toBeVisible()
    await expect(panel.getByRole('group', { name: /Availability/ })).toBeVisible()
    await expect(panel.getByText(/Daily data appears after the first rollup|Collecting daily data/)).toHaveCount(0)
    await expect(page.getByText('PROPOSED')).toHaveCount(0)

    await page.goto('/analytics/delivery')
    const reasons = page.getByRole('region', { name: 'Why rollouts failed' })
    await expect(reasons).toBeVisible({ timeout: 15_000 })
    await expect(reasons.getByText('No failed rollouts with a recorded reason in this range')).toHaveCount(0)
    await expect(page.getByText('PROPOSED')).toHaveCount(0)
  })
})
