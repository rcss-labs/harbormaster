import { expect, test } from './fixtures/test'
import { SVC, serviceUrl } from './fixtures/ids'
import { login } from './helpers'

// Runs in chromium-degraded: a second server whose scheduled-actions call and scaling tier always fail (HM_FAKE_FAIL),
// against its own database schema, so each test signs in here instead of using e2e/.auth.
test.beforeEach(async ({ page }) => {
  await login(page, 'pavel')
})

const REGIONS = 4

test('the freshness control keeps regions current while a failing scaling tier is named stale in every region', async ({ page }) => {
  await page.goto('/')
  // Core data stays current; the note appears once the first scaling poll fails (a few seconds).
  await expect(page.getByText(`auto scaling stale in ${REGIONS} regions`)).toBeVisible({ timeout: 30_000 })
  const control = page.getByRole('button', { name: `${REGIONS} of ${REGIONS} regions current` })
  await expect(control).toBeVisible()
  await control.click()
  const popover = page.getByRole('dialog', { name: 'Data freshness' })
  await expect(popover).toBeVisible()
  // Per region: Auto scaling is stale, Clusters and services is still current.
  await expect(popover.getByText(/^Auto scaling is (unavailable|\d+ min old)$/)).toHaveCount(REGIONS)
  await expect(popover.getByText(/^Current: .*Clusters and services/)).toHaveCount(REGIONS)
  await expect(popover.getByText(/^Current: .*Auto scaling/)).toHaveCount(0)
})

test('only the Scaling tab shows a page banner; Overview and Clusters show none', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByText(`auto scaling stale in ${REGIONS} regions`)).toBeVisible({ timeout: 30_000 })

  await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible()
  await expect(page.locator('main [data-hm-banner]')).toHaveCount(0)

  await page.goto('/clusters')
  await expect(page.getByRole('heading', { level: 1, name: 'Clusters' })).toBeVisible()
  await expect(page.locator('main [data-hm-banner]')).toHaveCount(0)

  await page.goto(serviceUrl(...SVC.paymentsApi, 'scaling'))
  const banner = page.locator('main [data-hm-banner]')
  await expect(banner).toHaveCount(1)
  await expect(banner).toHaveText(/Some data on this page is stale: Auto scaling/)
})
