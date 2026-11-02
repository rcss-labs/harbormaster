import { expect, test } from './fixtures/test'
import { login, serviceId } from './helpers'

const HOMELAB_ACCOUNT_ID = '482109375512'

test('admin walks the read-only product', async ({ page }) => {
  await login(page, 'pavel')
  await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible()
  const clustersTile = page.getByRole('main').getByText('Clusters', { exact: true }).locator('..')
  await expect(clustersTile).toHaveText(/^Clusters\s*6/)

  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Clusters' }).click()
  await page.getByRole('link', { name: /core-services/ }).first().click()
  const clusterHeading = page.getByRole('heading', { level: 1, name: 'core-services' })
  await expect(clusterHeading).toBeVisible()
  // The cluster's own status pill sits in the heading row, not in a service card below. The seeded notifications
  // rollout finishes a few fake-AWS steps after startup, so the pill may already read Healthy by the time we get here.
  await expect(clusterHeading.locator('..').getByText(/^(Deploying|Healthy)$/)).toBeVisible()

  await page.getByRole('link', { name: /payments-api/ }).first().click()
  await expect(page.getByRole('heading', { level: 1, name: 'payments-api' })).toBeVisible()
  const tasksTab = page.getByRole('main').getByRole('link', { name: /^Tasks/ })
  await expect(tasksTab).toContainText('3')
  await tasksTab.click()
  await expect(page.getByText('Running', { exact: true })).toHaveCount(3)

  await page.getByRole('link', { name: /^Operations log/ }).click()
  await expect(page.getByText('has reached a steady state.').first()).toBeVisible()

  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Settings' }).click()
  await page.getByRole('link', { name: 'Users & permissions' }).click()
  await expect(page.getByRole('row', { name: /^omer User V L O D E T/ })).toBeVisible()
  await page.getByRole('link', { name: /AWS accounts/ }).click()
  await expect(page.getByRole('main').getByText('homelab-admin').first()).toBeVisible()

  await page.getByRole('button', { name: 'Sign out' }).click()
  await expect(page).toHaveURL(/\/login(\?signed_out=1)?$/)
})

test('user sees only permitted clusters', async ({ page }) => {
  await login(page, 'omer')
  const nav = page.getByRole('navigation', { name: 'Main' })
  await expect(nav.getByText('My access')).toBeVisible()
  await expect(nav.getByText('Admin', { exact: true })).toHaveCount(0)

  await nav.getByRole('link', { name: 'Clusters' }).click()
  await expect(page.getByRole('article')).toHaveCount(5)
  await expect(page.getByRole('article', { name: 'ci-tools' })).toBeVisible()
  await expect(page.getByRole('article', { name: 'core-services' })).toHaveCount(0)

  await page.goto(`/services/${serviceId(HOMELAB_ACCOUNT_ID, 'us-east-1', 'core-services', 'payments-api')}`)
  await expect(page.getByText('No access')).toBeVisible()

  await page.goto('/tasks')
  await expect(page.getByText('release-build').first()).toBeVisible()
})

test('search jumps to a service', async ({ page }) => {
  await login(page, 'pavel')
  await page.getByRole('button', { name: /Search or run a command/ }).click()
  const palette = page.getByRole('dialog', { name: 'Command palette' })
  const search = palette.getByRole('combobox')
  await search.fill('catal')
  await expect(palette.getByRole('option', { name: /catalog-api/ }).first()).toBeVisible()
  // Enter opens the highlighted result; the first service match is catalog-api.
  await expect(async () => {
    await search.press('Enter')
    await expect(page.getByRole('heading', { level: 1, name: 'catalog-api' })).toBeVisible({ timeout: 2_000 })
  }).toPass({ timeout: 15_000 })
  await expect(page.getByText('Unhealthy task').first()).toBeVisible()
})

test('throttle and stale banners absent on healthy estate', async ({ page }) => {
  // Wait for the poller status to load first, so an empty page cannot pass vacuously.
  const pollerLoaded = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/poller' && response.ok())
  await login(page, 'pavel')
  await pollerLoaded
  await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible()
  // CSS-module class names are hashed in the production build, so match on the stable prefix.
  await expect(page.locator('[class*="_banner_"]')).toHaveCount(0)
  await expect(page.getByText(/slowed by AWS throttling|Data for .* is/)).toHaveCount(0)
})
