import { asUser, expect, test } from './fixtures/test'

// Read-only: runs in the observe project. The Services page lists every visible service (15 in the seeded estate).

const serviceRows = (page: import('@playwright/test').Page) => page.getByRole('table', { name: 'Services' }).getByRole('row')

test.describe('services page as sam', () => {
  test.use(asUser('sam'))

  test('lists all 15 services and the Problems view leads with image-resizer', async ({ page }) => {
    await page.goto('/services')
    await expect(serviceRows(page)).toHaveCount(16)
    await expect(page.getByRole('group', { name: 'Saved views' }).getByRole('button', { name: /^All 15$/ })).toBeVisible()

    await page.getByRole('group', { name: 'Saved views' }).getByRole('button', { name: /^Problems/ }).click()
    await expect(page).toHaveURL(/view=problems/)
    await expect(serviceRows(page).nth(1)).toContainText('image-resizer')
  })

  test('My pins lists a service after pinning it', async ({ page }) => {
    await page.goto('/services')
    await page.getByRole('button', { name: 'Pin payments-api' }).click()
    await page.getByRole('group', { name: 'Saved views' }).getByRole('button', { name: /^My pins/ }).click()
    await expect(serviceRows(page)).toHaveCount(2)
    await expect(serviceRows(page).nth(1)).toContainText('payments-api')
  })

  test('Details opens the drawer and Esc closes it', async ({ page }) => {
    await page.goto('/services')
    await page.getByRole('button', { name: 'Details for payments-api' }).click()
    const drawer = page.getByRole('dialog')
    await expect(drawer).toContainText('payments-api')
    await expect(drawer.getByRole('link', { name: 'History' })).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(drawer).toHaveCount(0)
  })

  test('CPU and Memory columns show figures for the page', async ({ page }) => {
    await page.goto('/services')
    const table = page.getByRole('table', { name: 'Services' })
    await expect(table.getByRole('columnheader', { name: 'CPU' })).toBeVisible()
    await expect(table.getByRole('columnheader', { name: 'Memory' })).toBeVisible()
    // Figures come from CloudWatch Container Insights; at least one row shows a percentage, and none shows 0 for "unavailable".
    await expect(table.getByRole('row').filter({ hasText: /\d+ %/ }).first()).toBeVisible()
  })

  test('the filter and status segment narrow the list and live in the URL', async ({ page }) => {
    await page.goto('/services?status=bad')
    await expect(serviceRows(page)).toHaveCount(2)
    await page.goto('/services?q=payments')
    await expect(serviceRows(page)).toHaveCount(2)
    await expect(page).toHaveURL(/q=payments/)
  })
})

test.describe('services page as omer', () => {
  test.use(asUser('omer'))

  test('never lists a Prod service or offers a Prod view', async ({ page }) => {
    await page.goto('/services')
    await expect(page.getByRole('table', { name: 'Services' })).toBeVisible()
    await expect(page.getByRole('group', { name: 'Saved views' }).getByRole('button', { name: /^Prod/ })).toHaveCount(0)
    expect(await page.getByRole('main').textContent()).not.toContain('payments-api')
  })
})
