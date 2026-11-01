import { expect, test, type Page } from '@playwright/test'
import { login, serviceId } from './helpers'

const HOMELAB_ACCOUNT_ID = '482109375512'
const STAGING_ACCOUNT_ID = '731022849906'

// CSS-module class names are hashed in the production build, so match on the stable prefix.
const LOG_LINE = '[class*="_ln_"]'

/** A shared Card (div with an h2 title): the heading's grandparent. */
const card = (page: Page, title: string) => page.getByRole('heading', { level: 2, name: title, exact: true }).locator('xpath=../..')

const ALERTS_TIMEOUT = 30_000
// Scaling and ALB figures also wait for their poll tier's first pass after the server starts.
const POLL_TIMEOUT = 20_000

const nav = (page: Page) => page.getByRole('navigation', { name: 'Main' })

test('overview shows cluster usage and needs attention', async ({ page }) => {
  await login(page, 'pavel')
  const cpuCard = card(page, 'CPU by cluster, 24 h')
  await expect(cpuCard.getByText('core-services', { exact: true })).toBeVisible()

  const attention = card(page, 'Needs attention')
  const rows = attention.getByTestId('alert-row')
  // Alerts come from the poller's alerts tier, whose first pass finishes a few seconds after the server starts.
  await expect(rows.first()).toContainText('image-resizer: 0 of 2 tasks running', { timeout: ALERTS_TIMEOUT })
  await expect(rows.first()).toContainText('Failing')
  await expect(attention.getByText('catalog-api: 1 unhealthy task')).toBeVisible()

  // The badge counts danger alerts: the seed has exactly one (image-resizer), account alerts are warnings.
  await expect(rows.filter({ hasText: 'Failing' })).toHaveCount(1)
  await expect(nav(page).getByRole('link', { name: /^Overview/ })).toHaveText('Overview1')
})

test('cluster page draws the usage chart and service ALB stats', async ({ page }) => {
  await login(page, 'pavel')
  await nav(page).getByRole('link', { name: 'Clusters' }).click()
  await page.getByRole('link', { name: /core-services/ }).first().click()
  await expect(page.getByRole('heading', { level: 1, name: 'core-services' })).toBeVisible()

  const chart = card(page, 'Cluster CPU and memory, 24 h')
  await expect(chart).toBeVisible()

  const payments = page.getByRole('article', { name: 'payments-api' })
  await expect(payments.getByText('req/min')).toBeVisible({ timeout: POLL_TIMEOUT })
})

// The grid ends before now, so the fresh rollout's marker sits at the right edge (see deployMarks).
test('cluster chart shows a deployment marker label', async ({ page }) => {
  await login(page, 'pavel')
  await nav(page).getByRole('link', { name: 'Clusters' }).click()
  await page.getByRole('link', { name: /core-services/ }).first().click()
  const chart = card(page, 'Cluster CPU and memory, 24 h')
  await expect(chart.getByText(/:/).first()).toBeVisible({ timeout: ALERTS_TIMEOUT })
})

test('edge-proxy says Container Insights is off', async ({ page }) => {
  await login(page, 'pavel')
  await nav(page).getByRole('link', { name: 'Clusters' }).click()
  const card = page.getByRole('article', { name: 'edge-proxy' })
  await expect(card.getByText(/Container Insights is off/)).toBeVisible()

  await page.goto(`/services/${serviceId(HOMELAB_ACCOUNT_ID, 'eu-central-1', 'edge-proxy', 'edge-nginx')}/metrics`)
  await expect(page.getByText(/Container Insights is off for edge-proxy/)).toBeVisible()
  await expect(page.getByText(/containerInsights/)).toBeVisible()
  await expect(page.getByText('CPU utilization')).toHaveCount(0)
})

test('metrics tab range switch', async ({ page }) => {
  await login(page, 'pavel')
  await page.goto(`/services/${serviceId(HOMELAB_ACCOUNT_ID, 'eu-west-1', 'eu-apps', 'catalog-api')}/metrics`)
  await expect(card(page, 'CPU utilization')).toBeVisible()
  await page.getByRole('main').getByRole('button', { name: '6h', exact: true }).click()
  await expect(page).toHaveURL(/range=6h/)
  await expect(page.getByText('6 h ago').first()).toBeVisible()
})

test('target health shows the unhealthy target', async ({ page }) => {
  await login(page, 'pavel')
  const catalog = serviceId(HOMELAB_ACCOUNT_ID, 'eu-west-1', 'eu-apps', 'catalog-api')
  await page.goto(`/services/${catalog}/overview`)
  const health = card(page, 'Target health')
  await expect(health.getByText(/Unhealthy targets\s*1/)).toBeVisible()
})

test('scaling tab shows min, max and scheduled actions', async ({ page }) => {
  await login(page, 'pavel')
  const payments = serviceId(HOMELAB_ACCOUNT_ID, 'us-east-1', 'core-services', 'payments-api')
  await page.goto(`/services/${payments}/scaling`)
  const autoScaling = card(page, 'Auto scaling')
  await expect(autoScaling).toContainText(/Minimum tasks\s*2/, { timeout: POLL_TIMEOUT })
  await expect(autoScaling).toContainText(/Maximum tasks\s*6/)
  // Two scheduled actions: min 3 on weekday mornings, min 2 in the evening (shown as cron expressions, not "weekdays").
  await expect(autoScaling.getByText('weekdays')).toHaveCount(0)
  await expect(autoScaling.getByText(/→ min 3.*→ min 2/)).toBeVisible()
})

test('logs history, filter and live tail', async ({ page }) => {
  await login(page, 'oncall')
  const payments = serviceId(HOMELAB_ACCOUNT_ID, 'us-east-1', 'core-services', 'payments-api')
  await page.goto(`/services/${payments}/logs`)
  const lines = page.locator(LOG_LINE)
  await expect(lines.first()).toBeVisible()
  expect(await lines.count()).toBeGreaterThanOrEqual(1)
  await expect(page.getByText('live tail')).toBeVisible()

  await page.getByLabel('Level').selectOption('problems')
  await expect(async () => {
    const count = await lines.count()
    expect(count).toBeGreaterThan(0)
    for (const line of await lines.all()) {
      // The message span carries a level class (lvl-warn / lvl-error).
      await expect(line.locator('[class*="lvl-"]')).toHaveCount(1)
    }
  }).toPass({ timeout: 10_000 })

  await page.getByLabel('Level').selectOption('all')
  await page.getByLabel('Search logs').fill('refunds')
  await expect(async () => {
    const texts = await lines.allTextContents()
    expect(texts.length).toBeGreaterThan(0)
    for (const text of texts) expect(text).toContain('refunds')
  }).toPass({ timeout: 10_000 })

  await page.getByRole('button', { name: 'Pause' }).click()
  await expect(page.getByText('paused')).toBeVisible()
})

test('view-only user sees the logs lock', async ({ page }) => {
  await login(page, 'omer')
  const web = serviceId(STAGING_ACCOUNT_ID, 'us-east-1', 'staging-web', 'web')
  await page.goto(`/services/${web}/logs`)
  await expect(page.getByText(/You need\s*Logs\s*on/)).toBeVisible()
  await expect(page.getByLabel('Search logs')).toHaveCount(0)
  await expect(page.getByLabel('Level')).toHaveCount(0)
})
