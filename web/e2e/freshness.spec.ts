import { expect, test } from './fixtures/test'
import { SVC, serviceUrl } from './fixtures/ids'
import { login } from './helpers'

// Runs in chromium-degraded: a second server whose scheduled-actions call and scaling tier always fail (HM_FAKE_FAIL),
// against its own database schema, so each test signs in here instead of using e2e/.auth.
test.beforeEach(async ({ page }) => {
  await login(page, 'sam')
})

const REGIONS = 4

// The non-core note only shows where the top bar has room for it.
test.use({ viewport: { width: 1920, height: 1080 } })

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
  // Exactly one stale-data banner. Action banners, such as a registered revision waiting to deploy, are separate.
  const staleBanner = page.locator('main [data-hm-banner]').filter({ hasText: 'is stale' })
  await expect(staleBanner).toHaveCount(1)
  await expect(staleBanner).toHaveText(/Some data on this page is stale: Auto scaling/)
})

test('the stale note never squeezes the breadcrumb or grows the top bar at 1280', async ({ page }) => {
  await page.goto(serviceUrl(...SVC.paymentsApi, 'scaling'))
  const note = page.getByText(`auto scaling stale in ${REGIONS} regions`)
  await expect(note).toBeVisible({ timeout: 30_000 })

  await page.setViewportSize({ width: 1280, height: 720 })
  await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toContainText('payments-api')
  const topBarHeight = await page.evaluate(() => document.querySelector('nav[aria-label="Breadcrumb"]')!.parentElement!.getBoundingClientRect().height)
  expect(topBarHeight).toBeLessThanOrEqual(64)

  // The details stay one click away.
  await expect(note).toBeHidden()
  await page.getByRole('button', { name: /regions current$/ }).click()
  await expect(page.getByRole('dialog', { name: 'Data freshness' }).getByText(/^Auto scaling is /).first()).toBeVisible()
})
