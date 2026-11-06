import type { Page } from '@playwright/test'
import { asUser, authFile, expect, test } from './fixtures/test'

// Read-only: runs in the observe project against the unmutated seed. Every figure the Services page shows is compared
// with a direct /api/services (or /api/alerts) response read in the same test, so a page that drifts from its API fails.

interface ServiceItem {
  id: string
  name: string
  status: string
  attention: string
  tag: { name: string }
}
interface AlertItem {
  service_id: string
}

const views = (page: Page) => page.getByRole('group', { name: 'Saved views' })
const serviceRows = (page: Page) => page.getByRole('table', { name: 'Services' }).getByRole('row')

async function readServices(page: Page): Promise<ServiceItem[]> {
  const response = await page.request.get('/api/services')
  expect(response.ok()).toBe(true)
  return ((await response.json()) as { items: ServiceItem[] }).items
}

test.describe('services page as sam', () => {
  test.use(asUser('sam'))

  test('the view counts, footer and rows agree with /api/services', async ({ page }) => {
    await page.goto('/services')
    const items = await readServices(page)
    expect(items.length).toBeGreaterThan(0)

    await expect(
      views(page).getByRole('button', {
        name: new RegExp(`^All ${items.length}$`),
      }),
    ).toBeVisible()
    await expect(serviceRows(page)).toHaveCount(items.length + 1) // header row
    await expect(page.getByText(`${items.length} of ${items.length} services`)).toBeVisible()
    for (const item of items) await expect(page.getByRole('link', { name: item.name, exact: true })).toBeVisible()

    // The failing status segment lists exactly the services the API reports as failing.
    const failing = items.filter((item) => item.status === 'failing')
    expect(failing.length).toBeGreaterThan(0)
    await page.goto('/services?status=bad')
    await expect(serviceRows(page)).toHaveCount(failing.length + 1)
    for (const item of failing) await expect(serviceRows(page).filter({ hasText: item.name })).toHaveCount(1)
  })

  test('the Problems view lists every service that needs attention, and its count matches', async ({ page }) => {
    await page.goto('/services')
    // Needs attention = status failing/degraded/unhealthy/deploying, or an open alert. Status is included because the
    // degraded alert waits for three polls, so an alerts-only view would hide a visibly Degraded service.
    const needsAttention = (item: ServiceItem) =>
      item.attention !== '' || ['failing', 'degraded', 'unhealthy_task', 'deploying'].includes(item.status)
    const problems = (await readServices(page)).filter(needsAttention)
    expect(problems.length).toBeGreaterThan(0)

    // Every service with an open alert is in Problems, whatever its status.
    const alerts = (await (await page.request.get('/api/alerts')).json()) as {
      items: AlertItem[]
    }
    const problemIds = new Set(problems.map((item) => item.id))
    for (const alert of alerts.items) if (alert.service_id !== '') expect(problemIds.has(alert.service_id)).toBe(true)

    await views(page)
      .getByRole('button', { name: /^Problems/ })
      .click()
    await expect(page).toHaveURL(/view=problems/)
    await expect(
      views(page).getByRole('button', {
        name: new RegExp(`^Problems ${problems.length}$`),
      }),
    ).toBeVisible()
    await expect(serviceRows(page)).toHaveCount(problems.length + 1)
    for (const item of problems) await expect(serviceRows(page).filter({ hasText: item.name })).toHaveCount(1)
  })
})

test.describe('services page as omer', () => {
  test.use(asUser('omer'))

  test('lists exactly the services /api/services returns for omer, which are fewer than the whole estate', async ({ page, browser }) => {
    await page.goto('/services')
    const items = await readServices(page)
    expect(items.length).toBeGreaterThan(0)
    expect(items.every((item) => item.tag.name !== 'Prod')).toBe(true)

    await expect(serviceRows(page)).toHaveCount(items.length + 1)
    await expect(
      views(page).getByRole('button', {
        name: new RegExp(`^All ${items.length}$`),
      }),
    ).toBeVisible()
    for (const item of items) await expect(page.getByRole('link', { name: item.name, exact: true })).toBeVisible()

    // Sam sees more services than omer, so omer's scope really narrows the list.
    const sam = await browser.newContext({ storageState: authFile('sam') })
    const samItems = (
      (await (await sam.request.get('/api/services')).json()) as {
        items: ServiceItem[]
      }
    ).items
    await sam.close()
    expect(samItems.length).toBeGreaterThan(items.length)
    for (const hidden of samItems.filter((item) => !items.some((own) => own.id === item.id))) {
      await expect(page.getByRole('link', { name: hidden.name, exact: true })).toHaveCount(0)
    }
  })
})
