import type { APIRequestContext, Page } from '@playwright/test'
import { SVC, clusterUrl, serviceUrl } from './fixtures/ids'
import { asUser, expect, test } from './fixtures/test'

// Live page refresh: opening a cluster or service page reads that object from AWS at once and every 5 s while the tab is
// visible. Runs against `serve --fake-aws` in role=all with no shared store, so the registry and the bus are in memory.
// The fake feeds harbormaster_aws_api_calls_total, whose source label tells a focus read from a normal tier poll.
test.use(asUser('sam'))

const METRICS_TOKEN = 'e2e-metrics-token'

/** Sums the focus-labelled calls of one AWS operation (the fake names every service "ecs" or "elasticloadbalancing"). */
async function focusCalls(request: APIRequestContext, operation: string): Promise<number> {
  const response = await request.get('/metrics', { headers: { Authorization: `Bearer ${METRICS_TOKEN}` } })
  expect(response.status()).toBe(200)
  let total = 0
  for (const line of (await response.text()).split('\n')) {
    if (!line.startsWith('harbormaster_aws_api_calls_total{')) continue
    if (!line.includes(`operation="${operation}"`) || !line.includes('source="focus"')) continue
    total += Number(line.slice(line.lastIndexOf(' ') + 1))
  }
  return total
}

/** Makes the page report that its tab is hidden or visible, as switching tabs does. */
async function setVisibility(page: Page, state: 'visible' | 'hidden') {
  await page.evaluate((next) => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => next })
    Object.defineProperty(document, 'hidden', { configurable: true, get: () => next === 'hidden' })
    document.dispatchEvent(new Event('visibilitychange'))
  }, state)
}

test('opening a service page reads AWS at once and keeps it live', async ({ page, request }) => {
  const before = await focusCalls(request, 'DescribeServices')
  await page.goto(serviceUrl(...SVC.paymentsApi, 'overview'))
  await expect(page.getByRole('heading', { name: 'payments-api', level: 1 })).toBeVisible()
  // The stored data is on screen at once, with its age.
  await expect(page.getByText(/^Updated/)).toBeVisible()

  await expect.poll(() => focusCalls(request, 'DescribeServices'), { timeout: 4000 }).toBeGreaterThan(before)
  await expect(page.getByText('Live, every 5 s')).toBeVisible({ timeout: 8000 })

  const settled = await focusCalls(request, 'DescribeServices')
  await expect.poll(() => focusCalls(request, 'DescribeServices'), { timeout: 14_000 }).toBeGreaterThanOrEqual(settled + 2)
  // Release it, so the next test starts from a quiet registry (a closed page would only expire after the TTL).
  await setVisibility(page, 'hidden')
})

test('hiding the tab returns the service to normal pace', async ({ page, request }) => {
  const before = await focusCalls(request, 'DescribeServices')
  await page.goto(serviceUrl(...SVC.notifications, 'overview'))
  await expect(page.getByRole('heading', { name: 'notifications', level: 1 })).toBeVisible()
  await expect.poll(() => focusCalls(request, 'DescribeServices'), { timeout: 4000 }).toBeGreaterThan(before)

  await setVisibility(page, 'hidden')
  // The release shortens the registration to 12 s; a cycle may still be running after that.
  await page.waitForTimeout(16_000)
  const settled = await focusCalls(request, 'DescribeServices')
  await page.waitForTimeout(10_000)
  expect(await focusCalls(request, 'DescribeServices')).toBe(settled)

  // Showing the tab again opens it again.
  await setVisibility(page, 'visible')
  await expect.poll(() => focusCalls(request, 'DescribeServices'), { timeout: 4000 }).toBeGreaterThan(settled)
})

test('a cluster page is live too', async ({ page, request }) => {
  const before = await focusCalls(request, 'DescribeClusters')
  await page.goto(clusterUrl('482109375512', 'us-east-1', 'ci-tools'))
  await expect(page.getByRole('heading', { name: 'ci-tools', level: 1 })).toBeVisible()
  await expect(page.getByText(/^Updated/)).toBeVisible()
  await expect.poll(() => focusCalls(request, 'DescribeClusters'), { timeout: 4000 }).toBeGreaterThan(before)
  await expect(page.getByText('Live, every 5 s')).toBeVisible({ timeout: 8000 })
  await setVisibility(page, 'hidden')
})
