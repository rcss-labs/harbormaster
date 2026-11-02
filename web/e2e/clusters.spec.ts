import { asUser, expect, test } from './fixtures/test'

// Read-only: runs in the observe project.

test.describe('clusters as pavel', () => {
  test.use(asUser('pavel'))

  test('table rows and the Insights-off dash', async ({ page }) => {
    await page.goto('/clusters')
    // Header row plus six clusters.
    await expect(page.getByRole('table', { name: 'Clusters' }).getByRole('row')).toHaveCount(7)
    await expect(page.getByRole('row', { name: /edge-proxy/ }).getByTitle('Container Insights is off').first()).toBeVisible()
    await expect(page.getByText(/A dash in CPU and memory means Container Insights is off/)).toBeVisible()
  })

  test('an admin opens the tag select from the core-services chip', async ({ page }) => {
    // The list shows the chip read-only; the editable chip is in the cluster page header.
    await page.goto('/clusters')
    await page.getByRole('link', { name: 'core-services' }).first().click()
    await expect(page.getByRole('heading', { level: 1, name: 'core-services' })).toBeVisible()
    await page.getByRole('heading', { level: 1, name: 'core-services' }).locator('..').getByRole('button', { name: 'Prod' }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByText('Tag for core-services')).toBeVisible()
    await expect(dialog.getByRole('radio')).toHaveCount(3)
    await dialog.getByRole('button', { name: 'Cancel' }).click()
    await expect(dialog).toHaveCount(0)
  })
})

test.describe('clusters as dana', () => {
  test.use(asUser('dana'))

  test('sees six clusters and a read-only Prod chip', async ({ page }) => {
    await page.goto('/clusters')
    await expect(page.getByRole('table', { name: 'Clusters' }).getByRole('row')).toHaveCount(7)
    const row = page.getByRole('row', { name: /core-services/ })
    await expect(row.getByText('Prod', { exact: true })).toBeVisible()
    await expect(row.getByRole('button', { name: 'Prod' })).toHaveCount(0)

    // Not an admin: the cluster page chip is not a button either.
    await row.getByRole('link', { name: 'core-services' }).click()
    const heading = page.getByRole('heading', { level: 1, name: 'core-services' })
    await expect(heading.locator('..').getByText('Prod', { exact: true })).toBeVisible()
    await expect(heading.locator('..').getByRole('button', { name: 'Prod' })).toHaveCount(0)
  })
})
